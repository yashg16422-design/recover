package main

// diagnose.go — the deterministic "diagnosis" layer of the recovery agent.
//
// It maps a real payment failure_code (Stripe/Razorpay-style) to a recovery
// strategy, using the way card networks actually classify declines:
//   - soft declines (insufficient funds, issuer down)  -> retry in a smart window
//   - timeouts (gateway/UPI)                            -> retry almost immediately
//   - data errors (wrong CVC/number)                    -> ask customer to re-enter
//   - hard declines (lost/stolen/expired/fraud)         -> request a new method
// This is documented, real logic — not a simulation.

type bucket string

const (
	bucketSoft    bucket = "soft_decline"   // retryable after a wait
	bucketTimeout bucket = "timeout"        // retry quickly on an alternate rail
	bucketData    bucket = "data_error"     // customer must fix input
	bucketHard    bucket = "hard_decline"   // needs a new payment method
	bucketUnknown bucket = "unknown"
)

// Diagnosis is the agent's read on a single failed payment.
type Diagnosis struct {
	Bucket      bucket `json:"bucket"`
	Recoverable string `json:"recoverable"`  // high | medium | low | none
	Action      string `json:"action"`       // machine-friendly next step
	RetryWindow string `json:"retry_window"` // human-friendly timing
	Reason      string `json:"reason"`       // short explanation for the merchant
}

// declineTable maps common failure codes to their recovery strategy. Codes cover
// the Stripe/Razorpay vocabulary a real export would contain.
var declineTable = map[string]Diagnosis{
	"insufficient_funds": {bucketSoft, "high", "retry_scheduled", "in 24–72h (near payday)",
		"Buyer had no balance at the time — very commonly succeeds on a later retry."},
	"processing_error": {bucketSoft, "high", "retry_scheduled", "in 1–6h",
		"Temporary processor error — a retry usually clears it."},
	"issuer_unavailable": {bucketSoft, "high", "retry_scheduled", "in 1–6h",
		"The customer's bank was unreachable — retry when it's back."},
	"try_again_later": {bucketSoft, "high", "retry_scheduled", "in 6–24h",
		"Issuer asked to try again later."},
	"do_not_honor": {bucketSoft, "medium", "retry_scheduled", "in 24–72h",
		"Generic issuer refusal — a later retry or a nudge to the customer often works."},
	"gateway_timeout": {bucketTimeout, "high", "retry_immediate", "within minutes",
		"Network timed out mid-transaction — retry quickly, often on another rail."},
	"upi_timeout": {bucketTimeout, "high", "retry_immediate", "within minutes",
		"UPI request timed out — retry promptly on an alternate rail."},
	"authentication_required": {bucketData, "medium", "request_reauth", "customer action",
		"Needs the customer to complete 3-D Secure / OTP verification."},
	"incorrect_cvc": {bucketData, "medium", "request_reenter", "customer action",
		"Wrong CVC — ask the customer to re-enter their card details."},
	"incorrect_number": {bucketData, "medium", "request_reenter", "customer action",
		"Wrong card number — ask the customer to re-enter their card details."},
	"expired_card": {bucketHard, "low", "request_new_method", "customer action",
		"Card has expired — ask the customer for an updated card."},
	"lost_card": {bucketHard, "none", "request_new_method", "customer action",
		"Card reported lost — do not retry; request a new payment method."},
	"stolen_card": {bucketHard, "none", "request_new_method", "customer action",
		"Card reported stolen — do not retry; request a new payment method."},
	"fraudulent": {bucketHard, "none", "review", "manual review",
		"Flagged as fraud — route to manual review, do not auto-retry."},
	"pickup_card": {bucketHard, "none", "request_new_method", "customer action",
		"Issuer flagged the card — request a new payment method."},
	"card_declined": {bucketHard, "low", "request_new_method", "customer action",
		"Generic hard decline — a different payment method is usually needed."},
	"generic_decline": {bucketHard, "low", "request_new_method", "customer action",
		"Generic decline from the issuer — usually needs a different payment method."},
}

func diagnose(failureCode string) Diagnosis {
	if d, ok := declineTable[failureCode]; ok {
		return d
	}
	return Diagnosis{bucketUnknown, "low", "review", "manual review",
		"Unrecognized failure code — send to manual review."}
}
