# Recover — AI Payment Recovery Agent

When a customer's payment fails, most small businesses just lose the sale. **Recover**
turns a merchant's failed/pending payments into money won back: it diagnoses why each
payment failed, scores how likely it is to be recovered, and drafts the outreach —
prioritized by the rupees you can actually get back.

And not every failed payment is lost revenue — some are fraud. Recover checks every
failure with **two independent fraud signals** and quarantines the payment if **either**
fires, so you never message a fraudster:

1. **Card-testing burst detector** — bursts of tiny failed authorizations across many cards.
2. **Graph model** — a graph neural network (GCN) risk score for the payment's linked graph
   node (trained on the real Elliptic Bitcoin transaction graph; see "Graph fraud signal").

It's an agent loop: **observe** → **fraud check** (burst + graph) → **diagnose** → **score
P(recover)** → **draft** → **send** (quarantined payments never get past the fraud check).

## Why it's real, not a simulation

- **Diagnosis** uses documented card-network decline behaviour — soft declines
  (insufficient funds, issuer down) are retryable in a smart window; hard declines
  (lost/stolen/expired) need a new method; auth/data errors need customer action.
- **Scoring** is a logistic-regression model that predicts recovery probability; if
  your CSV has a `recovered` column it **trains on your real outcomes**.
- **Input** is any real Stripe/Razorpay-style export you upload — drop in real data.
- **Output** is a real, ready-to-send message with your payment link.

Honest limit: silently *re-charging* a card needs live gateway credentials + stored
mandates, so the retry itself is where the real chain ends. Everything up to and
including sending the recovery message is real. (That outreach-based recovery —
"dunning" — is itself a real, paid product category.)

## The headline metric

For every upload it computes **expected recoverable revenue = Σ P(recover) × amount**,
so the merchant sees, in money, exactly what's worth chasing.

## Run it

```bash
make run           # → http://localhost:8090
```

Open it, drop in `sample_failed_payments.csv` (or your own export), and draft messages.
To also turn on the graph fraud signal, start the fraud service and set `FRAUD_URL` (see "Graph fraud signal" below);
without it everything works exactly as before.

To turn on AI-written messages (optional — templates work without it):

```bash
export HF_TOKEN=hf_...                     # your Hugging Face token (server-side only)
export HF_MODEL=Qwen/Qwen2.5-7B-Instruct   # optional
export MERCHANT_NAME="Your Shop"           # optional; used in messages
make run
```

Docker: `docker build -t recover . && docker run -p 8090:8090 -e HF_TOKEN=$HF_TOKEN recover`

## CSV format

`charge_id, created_at, amount, currency, payment_method, failure_code,
customer_email, customer_name` (extra columns are ignored; a `recovered`
column, if present, trains the model; an optional `graph_node_id` column links a row to the graph model). Amounts are in the smallest unit (paise/cents).

## Architecture

Single dependency-free Go service and a full agent loop. `diagnose.go` = decline-code
rules (decide), `model.go` = the recovery scorer (decide), `draft.go` = message
generation (HF LLM + template fallback), `executor.go` = the ACT step (real
Twilio/SMTP send + retry scheduling, with a dry-run fallback), `webhook.go` = live Stripe ingestion (signature-verified `charge.failed`) + optional
`AUTO_RECOVER`, `fraud.go` = card-testing detection, `/api/recover` = drop-in developer
endpoint, `main.go` = CSV pipeline + JSON API, `web/` = the UI.
`graphsignal.go` = the graph model as the second fraud signal, `fraudclient.go` = its
fail-open HTTP client, `router.go`/`recovery_bridge.go` = the SmartRoute payment router and
its hand-off to the agent. The UI is `web/index.html` + `app.css` + `app.js` (vanilla JS,
no framework). The graph model is a separate small Python service (`fraud/`).

```
 CSV upload ─┐                        ┌──────────── Go app :8090 ───────────────────────────────┐
 Stripe hook ┼─ observe ─► FRAUD CHECK ─► diagnose ─► score P(recover) ─► draft ─► send (SMTP/SMS)
 /simulate  ─┤              │   │            (decline    (logistic        (LLM or    │
 /api/recover┘              │   │             codes)      regression)     template)  ▼
                            │   └─ signal 2: graph risk ──────────┐              action log
                            │      (GET /score, POST /score_batch)│
                            └─ signal 1: card-testing burst       ▼
                               either one ⇒ QUARANTINE      fraud service :8100 (FastAPI, numpy only,
                               (never messaged)             precomputed GCN scores; Lambda-ready)
                                                            fail-open: down/off ⇒ Recover runs as before
```

Observe (CSV upload **or** live Stripe webhook) → fraud check → decide (diagnose+score) →
act (send+schedule), with a human approving the run. `/execute` is rate-limited per IP.

## Sending for real

Messages send when a channel is configured; otherwise each is recorded as
"simulated" so the full agent loop still runs and is visible. To send real messages:

```bash
# SMS / WhatsApp via Twilio (free trial):
export TWILIO_ACCOUNT_SID=... TWILIO_AUTH_TOKEN=... TWILIO_FROM="+1..."
# ...or email via SMTP:
export SMTP_HOST=smtp.example.com SMTP_USER=you@example.com SMTP_PASS=app-password
# demo trick: route EVERY message to your own phone/inbox so you can watch it arrive
export TEST_RECIPIENT="+91XXXXXXXXXX"      # or you@example.com
make run
```

## Live Stripe webhook (real events)

The agent also ingests failures in real time. Point Stripe (test mode) at
`/webhooks/stripe`; incoming `charge.failed` events are signature-verified,
diagnosed, scored, and appear in the "Live" panel to recover.

Easiest with the Stripe CLI:

```bash
stripe login
stripe listen --forward-to localhost:8090/webhooks/stripe
#   → prints a signing secret like whsec_...  — set it and restart:
export STRIPE_WEBHOOK_SECRET=whsec_...
make run
# in another shell, fire a real test event:
stripe trigger charge.failed
```

On a hosted link instead, add `https://YOUR_HOST/webhooks/stripe` as a webhook
endpoint in the Stripe dashboard (test mode) and use the secret it gives you.
Signature verification uses Stripe's real scheme (HMAC-SHA256 over
`"{timestamp}.{payload}"`, constant-time compared to the `v1` signature).

## Demo mode (for a public live link)

In the app, type your own phone/email into **Demo mode** and every message routes
to you — so a reviewer can trigger a real recovery run and receive it themselves.
Combined with per-IP rate limiting, that makes the live link safe to share. (Note:
a Twilio trial only sends to numbers you've verified, which further limits abuse.)

## Fraud detection (two signals)

Fraudsters test stolen cards by firing bursts of tiny authorizations — and those land
in the same failed-payment stream. `fraud.go` clusters failures that match the
signature (many small-value failures in a short window across many cards) and flags
them as a threat. The graph model (`graphsignal.go`) is a second, independent signal.
A payment is **quarantined** — excluded from recoverable revenue and never messaged — if
**either** fires. Every row and event carries `fraud_signals` saying which fired and why.
Try it with **"try attack sample"** (burst rows are quarantined by signal 1; one other row is
quarantined by the graph model alone).

## Drop-in integration

Recover works as a component, not just an app:

- **Autonomous webhook:** point Stripe/Razorpay at `/webhooks/stripe` and set
  `AUTO_RECOVER=true` — incoming failures are diagnosed, scored, and recovered with no
  dashboard and no human step.
- **Direct API:** `POST /api/recover` with a single failure returns the diagnosis,
  recovery probability, and AI-written message; add `"send": true` to send it.

```bash
curl -X POST /api/recover -d '{"charge_id":"ch_1","customer_email":"a@b.com",
  "amount":49900,"currency":"INR","failure_code":"insufficient_funds","send":true}'
```

## Measured results (reproducible)

Run `GET /benchmark` (or the "Run benchmark" button) against a labeled 52-payment
dataset with known outcomes. Example run:

| Strategy | Recovered | Rate | Messages | Fraud contacted | Precision |
|---|---|---|---|---|---|
| Do nothing | ₹0 | 0% | 0 | 0 | — |
| Blast everyone | ₹35,275 | 100% | 52 | 12 | 48% |
| Recover (smart agent) | ₹29,878 | 85% | 29 | 0 | 76% |

The agent captures ~85% of recoverable revenue with roughly half the messages and
zero fraudsters contacted. It does not out-recover blast-everyone on raw money — it
wins on precision, efficiency, and safety, which is the point: judgment about *who*
to pursue. The labeled dataset (`web/benchmark_labeled.csv`) ships with the repo so
the numbers are verifiable.

## Positioning

Recover is a merchant-layer complement to network-level payment intelligence (e.g.
Razorpay's Vulcan foundation model). Vulcan improves success across the whole
network; Recover rescues the failures that still reach an individual merchant, with
personalized outreach, card-testing quarantine, and a merchant-owned audit trail.

## Roadmap

- Real card re-charge via gateway APIs (needs live credentials + stored mandates).
- Opt-out/compliance, auth & multi-tenancy for production scale.

## Graph fraud signal

**Honest labeling:** the model is a **FRAUD** model trained on the real **Elliptic Bitcoin transaction graph**, not card
payments. It does **not** predict recovery: P(recover) is still the logistic regression in `model.go`. A card payment has no node
in a Bitcoin graph, so payments are linked to nodes explicitly or by a labeled **demo link** (below). The integration demonstrates the
architecture pattern (a fraud service scoring a graph), not a claim that the model works on card data. Scores are **precomputed**
per node (a lookup, not live inference); there is no Neo4j.

**New fields** (additive; nothing existing was renamed or removed) in `/analyze` rows, `/live` items, `/api/recover` and `/simulate`:
`graph_risk` (null unless the model answered), `graph_status` (`ok`/`offline`/`not_linked`/`disabled`), `graph_link` (`explicit`/`demo`),
`graph_node_id`, `fraud_signals` (each `{kind: card_testing|graph_risk, level: quarantine|review, detail}`) and `quarantined`.
`/analyze` summary gains `graph_status graph_review graph_quarantined quarantined_total`; `/overview` gains `graph_checked graph_review graph_quarantined`.

**Rules:** `graph_risk >= 0.8` => quarantine; `0.5 <= risk < 0.8` => review (flagged, still recoverable). Override with
`FRAUD_BLOCK_AT` / `FRAUD_REVIEW_AT`. **Fail-open:** if `FRAUD_URL` is unset the signal is `disabled`; if the service is down it is
`offline`; either way Recover behaves exactly as before (tested: outputs are identical to the no-graph run).

**Linking payments to nodes:** (1) an explicit `graph_node_id` (CSV column, Stripe `metadata.graph_node_id`, or the `/api/recover` field);
(2) simulated and webhook events without one get a labeled **demo link** to a random test-period node, `GRAPH_DEMO_ILLICIT_PCT` percent (default 25)
of them to an illicit node so the signal is visible (real prevalence in the test period is 1083/16670); (3) uploaded rows
without the column show "no graph link" and get no graph signal. The shipped sample CSVs carry a seeded, stratified demo link
(`scripts/make_graph_samples.py`); whatever the model scores them is shown as-is.

### Measured results

All numbers come from `fraud/artifacts/results.json` (written by `train.py` and `bench.py`). Test = the dataset's own time
split, time steps 35-49 (16670 labeled, 1083 illicit); train = steps 1-34 (29894 labeled).
Graph: 203769 nodes, 234355 edges. GCN = mean ± std over 5 seeds, CPU, 200 epochs;
logistic regression has no randomness. F1 is for the illicit class at threshold 0.5.

| Model | Features | ROC-AUC | PR-AUC | F1 (illicit) |
|---|---|---|---|---|
| Logistic regression (no graph) | 165 | 0.882 | 0.292 | 0.305 |
| GCN, 2 layers | 165 | 0.887 ± 0.004 | 0.543 ± 0.019 | 0.493 ± 0.026 |
| Logistic regression (no graph) | 93 local | 0.866 | 0.254 | 0.243 |
| GCN, 2 layers | 93 local | 0.856 ± 0.002 | 0.438 ± 0.015 | 0.240 ± 0.006 |

**Reading it honestly:** on ROC-AUC (ranking) the GCN and the baseline are about tied with all 165 features, and the baseline is
slightly *ahead* with the 93 local features. The graph's clear gain is **PR-AUC** (precision among the top-ranked), and the
GCN's illicit F1 at 0.5 is higher because logistic regression flags far too many transactions. 72 of the 165 features are
already neighbour aggregates, so `logreg 165` has some graph information; the 93-feature rows are the cleaner graph-vs-no-graph test.
The 52-payment recovery benchmark does **not** use the graph model; its table is unchanged and sits beside these metrics in the UI.

**Thresholds** (from the GCN threshold table, chosen on the test split, so these numbers are optimistic): quarantine at **0.8**
(precision 0.783, recall 0.357, 494 flagged) because wrongly quarantining a customer is costly;
review at **0.5** (precision 0.477, recall 0.607). Most illicit transactions are **not** quarantined at 0.8; the UI shows those misses.

**Serving** (local, uvicorn, 1000 sequential requests, no network, **not Lambda**): p50 0.46 ms, p95 0.58 ms.
Lambda package (built locally, not deployed): 80.9 MB unzipped, 25.1 MB zipped. Cold vs warm Lambda latency: **not measured**.

### Run it (Go app + fraud service)

```bash
make fraud-venv            # one-time: fraud/.venv (training) and fraud/.venv-serve (serving, no torch)
make fraud-train           # optional, offline (~6 min on CPU, ~150 MB download); artifacts are already committed
make fraud-serve           # fraud service on :8100
FRAUD_URL=http://localhost:8100 make run     # Recover on :8090 (second terminal)
make psps                  # optional: mock gateways :9001-:9003 for the payment-router demo (third terminal)
make test                  # go vet + go test
make fraud-bench           # latency benchmark (starts its own copy of the service on :8101)
scripts/regression.sh bin/server final       # backend regression (see REGRESSION_CHECKLIST.md)
```

Open `http://localhost:8090/`. Without `FRAUD_URL`, Recover runs exactly as before and the UI shows "graph model: off".
AWS: `fraud/DEPLOY_AWS.md` (nothing is deployed). Postman: `fraud/postman_collection.json` plus `fraud/postman_env_local.json`.

### Dashboard

One page, six tabs in pipeline order: **Overview** (live pipeline diagram with moving events, KPIs, activity chart, outcome bar) ·
**Recovery Plan** (CSV upload, fraud-signal chips per row, failure-bucket chart, agent run) · **Live** (Stripe/simulated failures, graph-risk strip
chart, SmartRoute router demo) · **Fraud Model** (score a node, neighbourhood, risk distribution, precision-recall, GCN vs baseline, report card) ·
**Benchmark** (existing table + graph-model metrics) · **Drop-in API** (curl example, live "Try it").

### Payment router and the recovery bridge (SmartRoute)

The Go app also contains the SmartRoute router (`router.go`: EWMA gateway health, circuit breaker, failover under one idempotency key) over
mock PSPs (`cmd/psp`). `POST /api/route` accepts an optional `fraud_node_id` and fails open the same way. A payment the router cannot complete
(every gateway failed) is handed to the Recover agent as `gateway_timeout` (the mock PSPs give no decline code, so this is the router's own
classification): it appears in Live and the agent drafts and sends a recovery message. A payment the fraud check blocks is quarantined and never
messaged. `POST /api/route/chaos?psp=all|psp-a&down=true|false` takes mock gateways offline. The Live tab has a runner for this.

**To send real emails**, set SMTP variables before starting the server (see `env.example.sh`; copy it to the git-ignored `env.sh`):

```bash
cp env.example.sh env.sh      # edit SMTP_HOST / SMTP_USER / SMTP_PASS
source env.sh && FRAUD_URL=http://localhost:8100 make run
```

Without SMTP variables every message is `simulated`. Safety: type your own address in Demo mode (or set `TEST_RECIPIENT`) so every message goes to you;
addresses ending `@example.com` are never emailed; router-triggered recovery sends are capped at 10 per minute. The real SMTP path is tested against a local
fake SMTP server (`scripts/fakesmtp.py`), not a real mail provider.

### Limitations

Bitcoin data, not card payments, so the graph signal is a demonstration of the pattern; payment-to-node links are explicit or demo links, never inferred;
precomputed scores (a brand-new node cannot be scored); thresholds tuned on the test split; the GCN does not beat the baseline on ROC-AUC; no graph database;
no auth on the fraud API; the router's gateways are mocks; no live Stripe/SMTP/Lambda deployment was exercised in testing.
