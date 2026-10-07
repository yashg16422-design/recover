#!/usr/bin/env bash
# Regression check for the ORIGINAL Recover features (backend). Usage:
#   scripts/regression.sh <server-binary> <label> [port]
# e.g. scripts/regression.sh bin/server main      -> regression/results_main.txt
# The server is started by this script in a CLEAN environment (no HF_TOKEN / SMTP /
# Stripe vars from your shell), from the repo root. UI click-paths are in
# REGRESSION_CHECKLIST.md (section B) and are checked in a browser.
set -u
BIN=${1:?server binary}; LABEL=${2:-run}; PORT=${3:-8290}
ROOT=$(cd "$(dirname "$0")/.." && pwd); cd "$ROOT"
OUT=regression/results_$LABEL.txt; : > "$OUT"
BASE=http://127.0.0.1:$PORT
PASS=0; FAIL=0; SRV=""; SMTP=""
say(){ echo "$*" | tee -a "$OUT"; }
ok(){ say "PASS  $1"; PASS=$((PASS+1)); }
no(){ say "FAIL  $1   -- $2"; FAIL=$((FAIL+1)); }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "got [$2] want [$3]"; }
py(){ python3 -c "import sys,json; d=json.load(sys.stdin); print($1)" 2>/dev/null; }
istrue(){ [ "$2" = "True" ] && ok "$1" || no "$1" "got [$2]"; }
start(){ stop; env -i PATH="$PATH" HOME="$HOME" "$@" "$BIN" -addr :$PORT >/tmp/recover_reg_server.log 2>&1 & SRV=$!
  for i in $(seq 1 50); do curl -fs $BASE/health >/dev/null 2>&1 && return; sleep 0.1; done; no "server starts" "see /tmp/recover_reg_server.log"; }
stop(){ [ -n "$SRV" ] && kill $SRV 2>/dev/null && wait $SRV 2>/dev/null; SRV=""; }
trap 'stop; [ -n "$SMTP" ] && kill $SMTP 2>/dev/null; [ -f /tmp/recover_reg_fraud.pid ] && kill $(cat /tmp/recover_reg_fraud.pid) 2>/dev/null' EXIT
post_csv(){ curl -s -X POST -H 'Content-Type: text/csv' --data-binary @"$1" $BASE/analyze; }
say "# Recover regression — label=$LABEL — $(date '+%Y-%m-%d %H:%M:%S') — binary=$BIN"

############ A. default environment (no LLM, no SMTP, no Stripe secret) ############
start FOO=1
say "## A. core endpoints"
eq "GET /health" "$(curl -s $BASE/health | py "d['status']")" ok
eq "GET / is 200" "$(curl -s -o /dev/null -w '%{http_code}' $BASE/)" 200
istrue "GET / is the Recover app" "$(curl -s $BASE/ | grep -qi '<title>[^<]*Recover' && echo True || echo False)"
for f in app.css sample_failed_payments.csv sample_with_attack.csv benchmark_labeled.csv; do
  eq "static /$f" "$(curl -s -o /dev/null -w '%{http_code}' $BASE/$f)" 200; done
O=$(curl -s $BASE/overview)
istrue "GET /overview has all keys" "$(echo "$O" | py "all(k in d for k in ['payments_seen','failures','successes','threats','quarantined','sent','recovered','series'])")"
eq "/overview starts at zero" "$(echo "$O" | py "d['payments_seen']+d['failures']+d['threats']+d['sent']")" 0

say "## A. CSV upload -> /analyze"
A=$(post_csv web/sample_failed_payments.csv)
eq "analyze sample: 12 rows" "$(echo "$A" | py "d['summary']['count']")" 12
istrue "analyze sample: every original row field present" "$(echo "$A" | py "all(k in d['rows'][0] for k in ['charge_id','amount','currency','method','failure_code','customer_email','customer_name','diagnosis','p_recover','expected_recovered','attack'])")"
istrue "analyze sample: summary fields present" "$(echo "$A" | py "all(k in d['summary'] for k in ['count','currency','at_risk_amount','recoverable_amount','recoverable_pct','trained_on_labels','threat_count','by_bucket'])")"
istrue "analyze sample: sorted by expected recovery (desc)" "$(echo "$A" | py "[r['expected_recovered'] for r in d['rows']]==sorted([r['expected_recovered'] for r in d['rows']],reverse=True)")"
istrue "analyze sample: diagnosis has bucket/recoverable/action/reason" "$(echo "$A" | py "all(k in d['rows'][0]['diagnosis'] for k in ['bucket','recoverable','action','retry_window','reason'])")"
eq "analyze sample: no threats" "$(echo "$A" | py "d['summary']['threat_count']")" 0
eq "analyze sample: not trained on labels" "$(echo "$A" | py "d['summary']['trained_on_labels']")" False
echo "$A" > regression/analyze_sample_$LABEL.json
AT=$(post_csv web/sample_with_attack.csv)
istrue "attack sample: card-testing burst detected" "$(echo "$AT" | py "d['summary']['threat_count']>=1 and d['threats'][0]['kind']=='card_testing'")"
istrue "attack sample: burst rows quarantined (attack=true)" "$(echo "$AT" | py "sum(1 for r in d['rows'] if r['attack'])>=6")"
istrue "attack sample: quarantined rows excluded from recoverable amount" "$(echo "$AT" | py "abs(d['summary']['recoverable_amount']-sum(r['expected_recovered'] for r in d['rows'] if not r['attack']))<0.05")"
echo "$AT" > regression/analyze_attack_$LABEL.json
L=$(post_csv web/benchmark_labeled.csv)
eq "CSV with 'recovered' column trains the model" "$(echo "$L" | py "d['summary']['trained_on_labels']")" True
M=$(curl -s -X POST -F "file=@web/sample_failed_payments.csv" $BASE/analyze)
eq "multipart file upload works" "$(echo "$M" | py "d['summary']['count']")" 12
eq "malformed CSV -> 400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: text/csv' --data-binary $'a,b\n"x,y' $BASE/analyze)" 400

say "## A. message drafting (no HF_TOKEN -> template fallback, honest mode)"
D=$(curl -s -X POST $BASE/draft -d '{"charge_id":"ch_1","customer_name":"Aarav","amount":49900,"currency":"INR","method":"card","failure_code":"insufficient_funds"}')
eq "draft mode is template" "$(echo "$D" | py "d['mode']")" template
eq "draft note explains fallback" "$(echo "$D" | py "d['note']")" "HF_TOKEN not set on server"
istrue "draft message has name, amount and pay link" "$(echo "$D" | py "'Aarav' in d['message'] and '₹499' in d['message'] and 'ch_1' in d['message']")"
eq "fraud code -> mode skip, no message" "$(curl -s -X POST $BASE/draft -d '{"charge_id":"c","failure_code":"fraudulent","amount":1000}' | py "d['mode']+'|'+d['message']")" "skip|"

say "## A. act step: /execute and /api/recover (simulated: no SMTP/Twilio)"
R=$(curl -s -X POST $BASE/execute -d "$(echo "$AT" | jq -c '{rows:[.rows[]|{charge_id,customer_name,customer_email,amount,currency,method,failure_code,attack}],demo_recipient:"me@demo.test"}')")
istrue "execute: actions simulated, attack rows skipped" "$(echo "$R" | py "d['summary']['simulated']>0 and d['summary']['skipped']>=6 and d['summary']['sent']==0")"
istrue "execute: quarantined rows never messaged" "$(echo "$R" | py "all(a['status']=='skipped' for a in d['actions'] if 'Quarantined' in a['detail'])")"
istrue "execute: honest mode field on actions" "$(echo "$R" | py "any(a['mode']=='template' for a in d['actions'])")"
istrue "execute: demo recipient honoured" "$(echo "$R" | py "any('me@demo.test' in a['detail'] for a in d['actions'])")"
AR=$(curl -s -X POST $BASE/api/recover -d '{"charge_id":"ch_9","customer_email":"a@b.com","amount":49900,"currency":"INR","failure_code":"insufficient_funds"}')
istrue "api/recover returns diagnosis, p_recover, expected, message, mode" "$(echo "$AR" | py "all(k in d for k in ['diagnosis','p_recover','expected_recovered','message','mode','note'])")"
eq "api/recover without send: no 'sent' field" "$(echo "$AR" | py "'sent' in d")" False
eq "api/recover send:true -> simulated" "$(curl -s -X POST $BASE/api/recover -d '{"charge_id":"ch_9","customer_email":"a@b.com","amount":49900,"currency":"INR","failure_code":"insufficient_funds","send":true}' | py "d['sent']")" simulated

say "## A. live Stripe webhook (unsigned path) + /live"
EV='{"type":"charge.failed","data":{"object":{"id":"ch_reg1","amount":49900,"currency":"inr","failure_code":"insufficient_funds","billing_details":{"email":"reg@example.com","name":"Reg"},"payment_method_details":{"type":"card"}}}}'
eq "webhook charge.failed -> 200" "$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/webhooks/stripe -d "$EV")" 200
LV=$(curl -s $BASE/live)
istrue "/live shows the event with diagnosis and p_recover" "$(echo "$LV" | py "d['items'][0]['charge_id']=='ch_reg1' and d['items'][0]['diagnosis']['recoverable']=='high' and 0<d['items'][0]['p_recover']<=1")"
istrue "/live item has every original field" "$(echo "$LV" | py "all(k in d['items'][0] for k in ['received','charge_id','customer_name','customer_email','amount','currency','method','failure_code','diagnosis','p_recover','expected_recovered'])")"
B0=$(curl -s $BASE/overview | py "d['successes']")
curl -s -o /dev/null -X POST $BASE/webhooks/stripe -d '{"type":"charge.succeeded","data":{"object":{}}}'
eq "webhook charge.succeeded counts a success" "$(curl -s $BASE/overview | py "d['successes']-$B0")" 1

say "## A. /simulate (traffic generator)"
P0=$(curl -s $BASE/overview | py "d['payments_seen']")
for i in $(seq 1 30); do curl -s -o /dev/null -X POST $BASE/simulate; done
istrue "simulate x30 raises payments_seen" "$(curl -s $BASE/overview | py "d['payments_seen']-$P0>=30")"
istrue "overview series has points" "$(curl -s $BASE/overview | py "len(d['series'])>5 and all(k in d['series'][0] for k in ['t','failures','threats','sent'])")"

say "## A. /benchmark (must be byte-identical across versions)"
curl -s $BASE/benchmark > regression/benchmark_$LABEL.json
BM=$(cat regression/benchmark_$LABEL.json)
istrue "benchmark has 3 strategies in order" "$(echo "$BM" | py "[s['name'] for s in d['strategies']][:2]==['Do nothing','Blast everyone'] and d['strategies'][2]['name'].startswith('Recover')")"
eq "benchmark: agent contacts zero fraud" "$(echo "$BM" | py "d['strategies'][2]['fraud_contacted']")" 0
istrue "benchmark has exceptions + stopping_rules" "$(echo "$BM" | py "'exceptions' in d and len(d['stopping_rules'])>0")"
say "benchmark numbers: $(echo "$BM" | py "[(s['name'],s['recovered_amount'],s['recovery_rate_pct'],s['messages_sent'],s['fraud_contacted'],s['precision_pct']) for s in d['strategies']]")"

say "## A. rate limit on /execute (10 per minute per IP)"
N429=0; for i in $(seq 1 12); do c=$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/execute -d '{"rows":[]}'); [ "$c" = 429 ] && N429=$((N429+1)); done
istrue "/execute eventually returns 429" "$([ $N429 -ge 1 ] && echo True || echo False)"

############ B. signed webhooks ############
say "## B. Stripe signature verification (STRIPE_WEBHOOK_SECRET set)"
SEC=whsec_regtest; start STRIPE_WEBHOOK_SECRET=$SEC
eq "unsigned webhook -> 400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/webhooks/stripe -d "$EV")" 400
eq "wrong signature -> 400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Stripe-Signature: t=1,v1=deadbeef' $BASE/webhooks/stripe -d "$EV")" 400
TS=$(date +%s); SIG=$(printf '%s.%s' "$TS" "$EV" | openssl dgst -sha256 -hmac "$SEC" -hex | sed 's/^.* //')
eq "correctly signed webhook -> 200" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Stripe-Signature: t=$TS,v1=$SIG" $BASE/webhooks/stripe -d "$EV")" 200
eq "signed event appears in /live" "$(curl -s $BASE/live | py "d['items'][0]['charge_id']")" ch_reg1

############ C. real SMTP + AUTO_RECOVER (against a local fake SMTP server) ############
say "## C. real SMTP path, AUTO_RECOVER, TEST_RECIPIENT (fake local SMTP server)"
MAIL=/tmp/recover_reg_mail.txt; rm -f $MAIL
python3 scripts/fakesmtp.py $MAIL >/dev/null 2>&1 & SMTP=$!; sleep 0.5
start AUTO_RECOVER=true TEST_RECIPIENT=me@demo.test SMTP_HOST=127.0.0.1 SMTP_PORT=2525 SMTP_USER=u SMTP_PASS=p SMTP_FROM=shop@demo.test
curl -s -o /dev/null -X POST $BASE/webhooks/stripe -d "$EV"; sleep 1.5
istrue "AUTO_RECOVER: webhook failure is emailed to TEST_RECIPIENT" "$(grep -q 'To: me@demo.test' $MAIL 2>/dev/null && echo True || echo False)"
istrue "AUTO_RECOVER: email carries the drafted message" "$(grep -q 'Reg' $MAIL 2>/dev/null && grep -q 'payment' $MAIL && echo True || echo False)"
SR=$(curl -s -X POST $BASE/api/recover -d '{"charge_id":"ch_s","customer_email":"c@d.com","customer_name":"Cee","amount":19900,"currency":"INR","failure_code":"gateway_timeout","send":true,"demo_recipient":"you@demo.test"}')
eq "api/recover send:true really sends via SMTP" "$(echo "$SR" | py "d['sent']+'|'+d['channel']")" "sent|email"
istrue "mail server received it" "$(grep -q 'To: you@demo.test' $MAIL && echo True || echo False)"
N0=$(grep -c '^=====' $MAIL)
curl -s -o /dev/null -X POST $BASE/webhooks/stripe -d '{"type":"charge.failed","data":{"object":{"id":"ch_hard","amount":9900,"failure_code":"stolen_card","billing_details":{"email":"x@y.com","name":"X"}}}}'; sleep 1
eq "AUTO_RECOVER: hard decline is NOT messaged" "$(( $(grep -c '^=====' $MAIL) - N0 ))" 0


############ D. graph fraud signal (only when the binary has it AND the fraud service can start) ############
start FOO=1
if [ "$(curl -s -o /dev/null -w '%{http_code}' $BASE/api/graph/status)" = 200 ] && [ -x fraud/.venv-serve/bin/uvicorn ] && [ -f fraud/artifacts/scores.npy ]; then
  say "## D. graph fraud signal (new): disabled mode must equal the baseline"
  eq "no FRAUD_URL -> graph_status disabled" "$(curl -s $BASE/api/graph/status | py "d['status']")" disabled
  eq "no FRAUD_URL -> /analyze has graph_status disabled" "$(post_csv web/sample_failed_payments.csv | py "d['summary']['graph_status']")" disabled
  GP=8102; (cd fraud/serve; exec ../.venv-serve/bin/uvicorn app:app --port $GP --log-level warning >/dev/null 2>&1) & echo $! > /tmp/recover_reg_fraud.pid
  for i in $(seq 1 60); do curl -fs http://127.0.0.1:$GP/health >/dev/null 2>&1 && break; sleep 0.2; done
  start FRAUD_URL=http://127.0.0.1:$GP
  say "## D. graph signal ON"
  eq "graph status online" "$(curl -s $BASE/api/graph/status | py "d['status']+'|'+str(d['online'])")" "ok|True"
  G=$(post_csv web/sample_failed_payments.csv); echo "$G" > regression/analyze_sample_graph_$LABEL.json
  eq "analyze: graph_status ok" "$(echo "$G" | py "d['summary']['graph_status']")" ok
  istrue "analyze: every linked row has a numeric graph_risk in [0,1]" "$(echo "$G" | py "all(isinstance(r['graph_risk'],float) and 0<=r['graph_risk']<=1 for r in d['rows'])")"
  istrue "analyze: every row has fraud_signals (list), quarantined (bool), graph_link explicit" "$(echo "$G" | py "all(isinstance(r['fraud_signals'],list) and isinstance(r['quarantined'],bool) and r['graph_link']=='explicit' for r in d['rows'])")"
  istrue "analyze: review band flags at least one row (flag only, not quarantined)" "$(echo "$G" | py "d['summary']['graph_review']>=1 and any(any(x['level']=='review' for x in r['fraud_signals']) and not r['quarantined'] for r in d['rows'])")"
  istrue "analyze: ORIGINAL row/summary fields unchanged vs main (graph on)" "$(python3 scripts/regcmp.py old_fields_same regression/analyze_sample_main.json regression/analyze_sample_graph_$LABEL.json)"
  GA=$(post_csv web/sample_with_attack.csv)
  istrue "attack sample: burst rows carry a card_testing signal" "$(echo "$GA" | py "sum(1 for r in d['rows'] if any(x['kind']=='card_testing' for x in r['fraud_signals']))>=6")"
  istrue "attack sample: graph quarantines a non-burst row on its own (second signal)" "$(echo "$GA" | py "any(r['quarantined'] and not r['attack'] and r['fraud_signals'][0]['kind']=='graph_risk' for r in d['rows'])")"
  istrue "attack sample: every quarantined row says why" "$(echo "$GA" | py "all(len(r['fraud_signals'])>=1 and r['fraud_signals'][0]['detail'] for r in d['rows'] if r['quarantined'])")"
  istrue "attack sample: recoverable excludes ALL quarantined rows" "$(echo "$GA" | py "abs(d['summary']['recoverable_amount']-sum(r['expected_recovered'] for r in d['rows'] if not r['quarantined']))<0.05")"
  GX=$(curl -s -X POST $BASE/execute -d "$(echo "$GA" | jq -c '{rows:[.rows[]|{charge_id,customer_name,customer_email,amount,currency,method,failure_code,attack,quarantined,quarantine_reason:(.fraud_signals[0].detail // "")}],demo_recipient:"me@demo.test"}')")
  istrue "execute: graph-quarantined row is skipped, not messaged" "$(echo "$GX" | py "any('graph model' in a['detail'] and a['status']=='skipped' and a['message']=='' for a in d['actions'])")"
  for i in $(seq 1 60); do curl -s -X POST $BASE/simulate; echo; done > /tmp/recover_reg_sim.txt
  istrue "simulate: response describes events; failure events carry the graph check (demo link)" "$(python3 -c "
import json
ev=[e for l in open('/tmp/recover_reg_sim.txt') if l.strip() for e in json.loads(l)['events']]
print(len(ev)>0 and all('graph_status' in e and 'fraud_signals' in e and e['graph_link']=='demo' and e['graph_status']=='ok' for e in ev))")"
  LV2=$(curl -s $BASE/live)
  istrue "live: items carry graph fields (original fields untouched)" "$(echo "$LV2" | py "all(k in d['items'][0] for k in ['received','charge_id','customer_name','diagnosis','p_recover','graph_status','fraud_signals','quarantined'])")"
  curl -s -o /dev/null -X POST $BASE/webhooks/stripe -d '{"type":"charge.failed","data":{"object":{"id":"ch_meta","amount":9900,"failure_code":"insufficient_funds","billing_details":{"email":"m@example.com","name":"Meta"},"metadata":{"graph_node_id":"136279"}}}}'
  istrue "webhook: Stripe metadata graph_node_id -> explicit link + quarantine-level signal" "$(curl -s $BASE/live | py "[i for i in d['items'] if i['charge_id']=='ch_meta'][0]['graph_link']=='explicit' and [i for i in d['items'] if i['charge_id']=='ch_meta'][0]['quarantined']")"
  AG=$(curl -s -X POST $BASE/api/recover -d '{"charge_id":"ch_g","customer_email":"a@b.com","amount":49900,"currency":"INR","failure_code":"insufficient_funds","graph_node_id":136279,"send":true}')
  istrue "api/recover: graph fields present; quarantined payment is NOT sent" "$(echo "$AG" | py "d['graph_risk']>0.8 and d['quarantined'] and d['sent']=='skipped' and d['fraud_signals'][0]['kind']=='graph_risk'")"
  eq "api/recover without node id -> not_linked" "$(curl -s -X POST $BASE/api/recover -d '{"charge_id":"ch_h","amount":1000,"failure_code":"insufficient_funds"}' | py "d['graph_status']")" not_linked
  istrue "overview: graph counters rise" "$(curl -s $BASE/overview | py "d['graph_checked']>0 and 'graph_review' in d and 'graph_quarantined' in d and 'graph' in d['series'][-1]")"
  say "## D. fail-open: kill the graph service mid-run"
  kill $(cat /tmp/recover_reg_fraud.pid) 2>/dev/null; sleep 2.5
  eq "status says offline" "$(curl -s $BASE/api/graph/status | py "d['status']")" offline
  post_csv web/sample_failed_payments.csv > regression/analyze_sample_offline_$LABEL.json
  eq "analyze still works; graph_status offline" "$(py "d['summary']['graph_status']" < regression/analyze_sample_offline_$LABEL.json)" offline
  istrue "offline: nothing quarantined; numbers equal the no-graph baseline" "$(python3 scripts/regcmp.py offline_equal regression/analyze_sample_main.json regression/analyze_sample_offline_$LABEL.json)"
  eq "offline: draft still works" "$(curl -s -X POST $BASE/draft -d '{"charge_id":"c","customer_name":"A","amount":1000,"currency":"INR","failure_code":"insufficient_funds"}' | py "d['mode']")" template
  eq "offline: webhook still accepted" "$(curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/webhooks/stripe -d "$EV")" 200
else
  say "## D. skipped (this binary has no graph signal, or the fraud service/artifacts are missing)"
fi
stop
say ""; say "RESULT label=$LABEL  PASS=$PASS  FAIL=$FAIL"
[ $FAIL -eq 0 ]
