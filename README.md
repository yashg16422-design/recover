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
