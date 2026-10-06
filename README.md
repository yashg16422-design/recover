# Recover — AI Payment Recovery Agent

When a customer's payment fails, most small businesses just lose the sale. **Recover**
turns a merchant's failed/pending payments into money won back: it diagnoses why each
payment failed, scores how likely it is to be recovered, and drafts the outreach —
prioritized by the rupees you can actually get back.

And not every failed payment is lost revenue — some are fraud. Recover also scans the
stream for **card-testing attacks** (bursts of tiny failed authorizations across many
cards) and quarantines them from recovery, so you never message a fraudster.

It's an agent loop: **observe** (ingest failures) → **decide** (diagnose + score + detect
fraud) → **act** (recover the real ones, quarantine the attacks).

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
column, if present, trains the model). Amounts are in the smallest unit (paise/cents).

## Architecture

Single dependency-free Go service and a full agent loop. `diagnose.go` = decline-code
rules (decide), `model.go` = the recovery scorer (decide), `draft.go` = message
generation (HF LLM + template fallback), `executor.go` = the ACT step (real
Twilio/SMTP send + retry scheduling, with a dry-run fallback), `webhook.go` = live Stripe ingestion (signature-verified `charge.failed`) + optional
`AUTO_RECOVER`, `fraud.go` = card-testing detection, `/api/recover` = drop-in developer
endpoint, `main.go` = CSV pipeline + JSON API, `web/` = the UI.
Observe (CSV upload **or** live Stripe webhook) → decide (diagnose+score) → act
(send+schedule), with a human approving the run. `/execute` is rate-limited per IP.

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

## Fraud detection (card-testing)

Fraudsters test stolen cards by firing bursts of tiny authorizations — and those land
in the same failed-payment stream. `fraud.go` clusters failures that match the
signature (many small-value failures in a short window across many cards) and flags
them as a threat. Flagged rows are **quarantined**: excluded from recoverable revenue
and never messaged. Try it in the app with **"try attack sample."**

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
| Recover (agent) | ₹29,878 | 85% | 29 | 0 | 76% |

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

## Graph fraud detection (SmartRoute router + GNN)

The Go server now contains the **SmartRoute router** (EWMA gateway health, circuit breaker, failover under one
idempotency key; ported from the SmartRoute project into `cmd/server/router.go`) and asks a **graph fraud service** for a risk
score *before* choosing a gateway. Gateways are the mock PSPs in `cmd/psp` (simulated, not real acquirers).

**Honest labeling:** the model is trained on the **Elliptic Bitcoin transaction graph**, not card payments. The
integration demonstrates the architecture pattern (router consults a graph fraud service), not a claim that the model
works on card data. Scores are **precomputed** per node (a lookup, not live inference), and there is no Neo4j.

### How it fits together

```
POST /api/route {"amount":499,"fraud_node_id":123}
   -> fraud service GET /score/123  (FRAUD_URL, 300 ms timeout, FAIL-OPEN)
   -> risk >= 0.8 BLOCKED (no gateway touched) | >= 0.5 REVIEW (routed, flagged) | else ROUTED
   -> SmartRoute picks a gateway (EWMA + breaker + failover)
response: the existing fields (status, psp, attempts, tried, policy) + fraud_check, fraud_decision, fraud_risk, fraud_latency_ms
```

`fraud_node_id` is optional. Without it, routing behaves as before (`fraud_check: "skipped"`).
**Fail-open:** if the fraud service is down, slow or returns bad data, the payment is routed anyway and the response says
`fraud_check: "unavailable"` (tested in `cmd/server/fraudclient_test.go`).

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

**Thresholds** (from the GCN threshold table; chosen on the test split, so these numbers are optimistic): block at **0.8**
(precision 0.783, recall 0.357, 494 flagged) because wrongly blocking a customer is costly;
review at **0.5** (precision 0.477, recall 0.607). Override with `FRAUD_BLOCK_AT` / `FRAUD_REVIEW_AT`.
In practice most illicit transactions are *not* blocked at 0.8; the UI shows those misses instead of hiding them.

**Serving latency** (local, uvicorn, 1000 sequential requests, no network, **not Lambda**): p50 0.46 ms,
p95 0.58 ms (1-hop); p50 0.49 ms, p95 0.64 ms (2-hop).
Lambda package (built locally, not deployed): 80.9 MB unzipped, 25.1 MB zipped. Cold vs warm Lambda latency: **not measured** (needs a real deployment).

### Run it

```bash
make fraud-venv            # one-time: creates fraud/.venv (training) and fraud/.venv-serve (serving, no torch)
make fraud-train           # offline; downloads Elliptic (~150 MB); writes fraud/artifacts/* (about 6 minutes on CPU)
make fraud-serve           # fraud service on :8100
make psps                  # mock gateways on :9001-:9003 (second terminal)
FRAUD_URL=http://localhost:8100 make run     # Recover + router on :8090 (third terminal)
make fraud-bench           # latency benchmark (starts its own copy of the service on :8101)
make test                  # go vet + go test
```

Open `http://localhost:8090/fraud.html`. Ports: Recover 8090, fraud service 8100, mock PSPs 9001-9003.
AWS: see `fraud/DEPLOY_AWS.md` (nothing is deployed). Postman: import `fraud/postman_collection.json` plus `fraud/postman_env_local.json`.

### Limitations

Bitcoin data, not card payments; precomputed scores (a brand-new transaction cannot be scored); no graph database;
no auth on the fraud API; thresholds tuned on the test split; the router's gateways are mocks.
