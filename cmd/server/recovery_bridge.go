package main

// recovery_bridge.go — connects the payment router to the Recover agent.
//
// Whenever a routed payment does NOT end in a normal success, the router hands it
// to the same agent loop the dashboard uses (diagnose -> score -> draft -> act):
//
//   - every gateway failed  -> treated as failure_code "gateway_timeout" (the mock
//     PSPs return no decline code, so this is the router's own classification).
//     The payment appears in the dashboard's Live feed and the agent drafts and
//     sends a recovery message (real SMTP/Twilio if configured, else "simulated").
//   - fraud decision BLOCKED -> quarantined, NEVER messaged (you do not email a
//     suspected fraudster a payment link). Counted as a threat on the dashboard.
//
// Sending safety: executeRow honours demo_recipient / TEST_RECIPIENT, and emailSend
// never really sends to @example.com addresses.

import (
	"time"
)

const recoveryWait = 10 * time.Second // how long the HTTP response waits for the agent

// recoverFromRouter runs the agent for one payment the router could not complete.
// blocked=true means the fraud check stopped it before any gateway was tried.
func recoverFromRouter(pr payRequest, blocked bool) *action {
	row := execRow{
		ChargeID: pr.IdempotencyKey, Name: pr.CustomerName, Email: pr.CustomerEmail,
		Amount: pr.Amount, Currency: orDefault(pr.Currency, "INR"), Method: "card",
		FailureCode: "gateway_timeout", Attack: blocked,
	}

	if blocked {
		recordThreats(1, 1)
		a := executeRow(row, "") // Attack=true -> skipped, never messaged
		a.Detail = "Quarantined: blocked by the graph fraud check before any gateway was tried. Not messaged."
		return &a
	}

	d := diagnose(row.FailureCode)
	p := liveModel.recoveryProb(d, row.Amount, row.Email != "" || pr.DemoRecipient != "")
	pushLive(liveItem{
		Received: time.Now().Format("15:04:05"), ChargeID: row.ChargeID, Name: row.Name,
		Email: row.Email, Amount: row.Amount, Currency: row.Currency, Method: row.Method,
		FailureCode: row.FailureCode, Diagnosis: d, PRecover: round2(p), Expected: round2(p * row.Amount),
	})
	recordFailures(1)

	if !allow("route-recovery") { // same per-minute cap as /execute, so a loop can't mass-send
		return &action{ChargeID: row.ChargeID, Customer: row.Name, Type: "skip", Channel: "none",
			Status: "skipped", Detail: "recovery rate limit reached (10/min); not sent"}
	}

	done := make(chan action, 1)
	go func() {
		a := executeRow(row, pr.DemoRecipient)
		if a.Status == "sent" || a.Status == "simulated" {
			recordSent(1, round2(p*row.Amount))
		}
		done <- a
	}()
	select {
	case a := <-done:
		return &a
	case <-time.After(recoveryWait):
		return &action{ChargeID: row.ChargeID, Customer: row.Name, Status: "pending",
			Detail: "recovery is still running in the background"}
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
