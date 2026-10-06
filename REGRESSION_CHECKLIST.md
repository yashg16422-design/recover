# Recover regression checklist

Every ORIGINAL Recover feature, how to check it, and what you should see. Run it on `main` first (baseline), and again on the
branch before merging. If something that passed on main fails on the branch, stop.

- **Backend (automated):** `scripts/regression.sh <server-binary> <label>` starts the server in a clean environment (no HF/SMTP/Stripe
  variables from your shell), runs every check below, and writes `regression/results_<label>.txt`. Section C starts a tiny fake SMTP server
  (`scripts/fakesmtp.py`) so real-SMTP sending can be tested without credentials.
- **UI (click-paths):** section B, checked in a browser. Baseline results: `regression/ui_main.txt`.

```bash
go build -o bin/server-main ./cmd/server           # on main
scripts/regression.sh bin/server-main main          # baseline
go build -o bin/server ./cmd/server                 # on the branch
scripts/regression.sh bin/server final              # final
diff regression/benchmark_main.json regression/benchmark_final.json && echo "benchmark byte-identical"
```

## A. Backend features (baseline on main: **52 PASS, 0 FAIL**; final branch: **76 PASS, 0 FAIL** incl. section D below)

| Feature | Check (curl) | Expected |
|---|---|---|
| Health | `curl :PORT/health` | `{"status":"ok"}` |
| App + static files | `curl -I :PORT/`, `/app.css`, `/sample_failed_payments.csv`, `/sample_with_attack.csv`, `/benchmark_labeled.csv` | 200; `/` title contains "Recover" |
| Dashboard overview | `curl :PORT/overview` | keys `payments_seen failures successes threats quarantined sent recovered series`; all 0 on a fresh server |
| CSV upload | `curl -X POST -H 'Content-Type: text/csv' --data-binary @web/sample_failed_payments.csv :PORT/analyze` | `summary.count` 12; rows sorted by `expected_recovered` desc; every row has `charge_id amount currency method failure_code customer_email customer_name diagnosis p_recover expected_recovered attack`; `summary` keeps `count currency at_risk_amount recoverable_amount recoverable_pct trained_on_labels threat_count by_bucket`; no threats; `trained_on_labels` false |
| Multipart upload | `curl -F file=@web/sample_failed_payments.csv :PORT/analyze` | same 12 rows |
| Bad CSV | `curl -X POST ... --data-binary $'a,b\n"x,y' :PORT/analyze` | HTTP 400 |
| Card-testing detection + quarantine | `/analyze` with `web/sample_with_attack.csv` | `threats[0].kind` = `card_testing`; at least 6 rows `attack:true`; those rows excluded from `recoverable_amount` |
| Diagnosis | (inside `/analyze`) | `diagnosis` has `bucket recoverable action retry_window reason` |
| Training on a `recovered` column | `/analyze` with `web/benchmark_labeled.csv` | `trained_on_labels` true |
| LLM drafting + template fallback + honest mode | `curl -X POST :PORT/draft -d '{"charge_id":"ch_1","customer_name":"Aarav","amount":49900,"currency":"INR","method":"card","failure_code":"insufficient_funds"}'` | `mode` "template", `note` "HF_TOKEN not set on server", message has name, amount and pay link; `failure_code":"fraudulent"` gives `mode` "skip" and an empty message |
| Act step (simulated) | `curl -X POST :PORT/execute -d '{"rows":[...],"demo_recipient":"me@demo.test"}'` | `summary.simulated`>0, attack rows `skipped` ("Quarantined"), `sent` 0, demo recipient appears in `detail` |
| Rate limit | 12 x `POST /execute` within a minute | at least one HTTP 429 |
| Drop-in API | `curl -X POST :PORT/api/recover -d '{"charge_id":"ch_9","customer_email":"a@b.com","amount":49900,"currency":"INR","failure_code":"insufficient_funds"}'` | `diagnosis p_recover expected_recovered message mode note`; no `sent`; with `"send":true` -> `sent` "simulated" |
| Live webhook (unsigned) | `curl -X POST :PORT/webhooks/stripe -d '<charge.failed event>'` then `curl :PORT/live` | 200; `/live` item has all original fields; `charge.succeeded` raises `successes` by 1 |
| Webhook signature (HMAC-SHA256) | with `STRIPE_WEBHOOK_SECRET` set: unsigned, wrong, correct `Stripe-Signature` | 400, 400, 200 |
| Simulate | 30 x `POST /simulate` | `payments_seen` rises by at least 30; `series` has points with `t failures threats sent` |
| Benchmark | `curl :PORT/benchmark` | strategies "Do nothing", "Blast everyone", "Recover (smart agent)"; the agent contacts 0 fraud; has `exceptions` and `stopping_rules`. Saved to `regression/benchmark_<label>.json`; **must be byte-identical on the branch** |
| Real SMTP + AUTO_RECOVER + TEST_RECIPIENT | fake SMTP on :2525; `AUTO_RECOVER=true TEST_RECIPIENT=me@demo.test`; POST a failed event | mail to `me@demo.test` carrying the drafted message; hard decline (`stolen_card`) is NOT mailed; `/api/recover` with `send:true` really sends (`sent`/`email`) |

## B. UI click-paths (baseline on main: **all pass except U8b**; new UI: see `regression/ui_final.txt`, **all pass, U8b fixed**)

On the redesigned UI the same checks apply, with these path changes: the overview/benchmark/live/upload panels are now **tabs** (Overview, Recovery Plan, Live, Fraud Model, Benchmark, Drop-in API); "Take a tour" is in the top bar; Demo mode is a bar under the header. Element ids are unchanged.

| # | Click path | Expected |
|---|---|---|
| U1 | Open `/` with empty localStorage | Intro modal (4 slides); last "Next" = "Take a tour"; opens the 8-step spotlight tour; final step closes it |
| U2 | Overview card: click "Simulate live traffic" | SIMULATED badge; 4 KPIs (payments seen, revenue recovered, anomalies caught, messages sent) rise; sparkline canvas draws; click again stops |
| U3 | "try the sample" | Results appear; KPIs At risk / Recoverable / Actionable; 12-row plan; "Draft" shows a message tagged `template` |
| U4 | "Upload another file" then "try attack sample" | Threats card (card-testing, high risk); burst rows greyed, tagged fraud, "quarantined" |
| U5 | Demo mode input = `me@demo.test`, "Approve & run recovery" | Agent action log; summary "N sent / N simulated / N skipped / revenue in flight"; demo recipient appears; mode tags |
| U6 | "Run benchmark" | 3-strategy table, exceptions line, 4 stopping rules, dataset download link |
| U7 | (part of U5) | Demo recipient overrides each customer's contact |
| U8 | POST a webhook event, wait 3 s | Live panel lists it with its recoverability % |
| U8b | Live panel: "Recover these live failures" button | **FAILS ON MAIN (pre-existing bug):** `runLive` is never defined, so the button is never created. **Fixed on this branch** (`runLive` defined; quarantined events are skipped) |
| U9 | Drop-in card | Shows the `curl /api/recover` example |
| U10 | "For merchants" card; "Take the tour" links | Present and working |

## D. New: graph fraud signal (automated, section D of `scripts/regression.sh`; skipped on main)

| Feature | Expected |
|---|---|
| `GET /api/graph/status` | `disabled` without `FRAUD_URL`; `ok`/online with the service; `offline` when it is killed |
| `/analyze` with graph on | every linked row has `graph_risk` in [0,1], `fraud_signals` (list), `quarantined`, `graph_link`; summary gains `graph_status graph_review graph_quarantined quarantined_total`; ORIGINAL row and summary fields identical to main |
| Two signals | burst rows carry a `card_testing` signal; a non-burst row can be quarantined by `graph_risk` alone; review band flags without quarantining; recoverable amount excludes every quarantined row |
| Quarantine is honoured | `/execute` skips graph-quarantined rows (no message); `/api/recover` with a quarantining `graph_node_id` and `send:true` returns `sent:"skipped"` |
| `/simulate`, `/live`, webhooks | simulate returns its events; failure events carry graph fields with a demo link; Stripe `metadata.graph_node_id` -> explicit link |
| `/overview` | additive `graph_checked graph_review graph_quarantined`, series points gain `graph` |
| Fail-open | service killed mid-run: status `offline`, `/analyze` still works, nothing quarantined, recoverable amount equals the no-graph baseline, draft and webhook still work |
| Benchmark | `regression/benchmark_final.json` is byte-identical to `regression/benchmark_main.json` |

## C. Known pre-existing issues on main (not caused by this work)

1. **U8b:** `web/index.html` sets `b.onclick=runLive` but `runLive` is not defined anywhere, so the "Recover these live failures" button never appears. (**Fixed on `feat/recover-gnn`.**)
2. **Docs nit:** README's example table calls the third strategy "Recover (agent)"; the API returns "Recover (smart agent)" and the numbers are 29,878 / 84.7% / 29 / 0 / 75.86%.
