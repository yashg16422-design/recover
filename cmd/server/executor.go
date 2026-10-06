package main

// executor.go — the ACT half of the agent loop.
//
// Given the diagnosed plan, the executor actually carries out the recovery:
//   - outreach: sends the recovery message (Twilio SMS/WhatsApp, or SMTP email)
//   - timed retry: schedules a retry for soft declines / timeouts
//   - hard declines / fraud: skips (records why)
// Real sends happen when credentials are configured; otherwise each action is
// recorded as "simulated" so the full loop still runs and is visible in a demo.
//
// A TEST_RECIPIENT env var routes every message to one address/number, which is
// the practical way to demo "a real SMS hits my phone" without real customer data.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type execRow struct {
	ChargeID    string  `json:"charge_id"`
	Name        string  `json:"customer_name"`
	Email       string  `json:"customer_email"`
	Phone       string  `json:"customer_phone"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	Method      string  `json:"method"`
	FailureCode string  `json:"failure_code"`
	Attack      bool    `json:"attack"`
}

type action struct {
	ChargeID  string `json:"charge_id"`
	Customer  string `json:"customer"`
	Type      string `json:"type"`      // outreach | retry | skip
	Channel   string `json:"channel"`   // sms | email | none
	Recipient string `json:"recipient"`
	Status    string `json:"status"`    // sent | simulated | scheduled | skipped | failed
	Mode      string `json:"mode"`      // ai | template (how the message was written)
	Detail    string `json:"detail"`
	When      string `json:"when"`
	Message   string `json:"message"`
}

var smsClient = &http.Client{Timeout: 20 * time.Second}

// twilioSend sends a real SMS/WhatsApp if Twilio creds are set; else simulates.
func twilioSend(to, body string) (status, detail string) {
	sid := os.Getenv("TWILIO_ACCOUNT_SID")
	tok := os.Getenv("TWILIO_AUTH_TOKEN")
	from := os.Getenv("TWILIO_FROM")
	if sid == "" || tok == "" || from == "" {
		return "simulated", "no Twilio creds set — would send SMS to " + to
	}
	form := url.Values{}
	form.Set("To", to)
	form.Set("From", from)
	form.Set("Body", body)
	req, _ := http.NewRequest(http.MethodPost,
		"https://api.twilio.com/2010-04-01/Accounts/"+sid+"/Messages.json",
		strings.NewReader(form.Encode()))
	req.SetBasicAuth(sid, tok)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := smsClient.Do(req)
	if err != nil {
		return "failed", err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return "sent", "SMS delivered to " + to
	}
	var e struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Message == "" {
		e.Message = "Twilio returned status " + resp.Status
	}
	return "failed", e.Message
}

// emailSend sends a real email if SMTP creds are set; else simulates.
func emailSend(to, subject, body string) (status, detail string) {
	host := os.Getenv("SMTP_HOST")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	from := getenv("SMTP_FROM", user)
	if host == "" || user == "" || pass == "" {
		return "simulated", "no SMTP creds set — would email " + to
	}
	if strings.HasSuffix(strings.ToLower(to), "@example.com") { // demo addresses are never really emailed
		return "simulated", "demo address (@example.com) — not sent: " + to
	}
	port := getenv("SMTP_PORT", "587")
	msg := "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\n\r\n" + body
	if err := smtp.SendMail(host+":"+port, smtp.PlainAuth("", user, pass, host), from, []string{to}, []byte(msg)); err != nil {
		return "failed", err.Error()
	}
	return "sent", "email delivered to " + to
}

// executeRow performs the agent's action for one payment. override, if non-empty,
// routes the message to that recipient (used by in-app Demo mode / TEST_RECIPIENT).
func executeRow(r execRow, override string) action {
	d := diagnose(r.FailureCode)
	a := action{ChargeID: r.ChargeID, Customer: r.Name, When: time.Now().Format("Jan 2, 15:04")}

	// suspected card-testing / fraud -> quarantine, never message
	if r.Attack {
		a.Type, a.Status, a.Channel = "skip", "skipped", "none"
		a.Detail = "Quarantined — part of a suspected card-testing attack. Not messaged."
		return a
	}

	// hard declines / fraud -> skip
	if d.Action == "review" || d.Recoverable == "none" {
		a.Type, a.Status, a.Channel = "skip", "skipped", "none"
		a.Detail = "Not recoverable (" + r.FailureCode + ") — routed to manual review, no message sent."
		return a
	}

	msg, mode, _ := generateMessage(draftIn{ChargeID: r.ChargeID, Name: r.Name, Amount: r.Amount,
		Currency: r.Currency, Method: r.Method, FailureCode: r.FailureCode})
	a.Message = msg
	a.Mode = mode
	a.Type = "outreach"

	// choose channel + recipient. Precedence: explicit override (in-app Demo mode)
	// > TEST_RECIPIENT env > the customer's real contact.
	recipient := override
	if recipient == "" {
		recipient = os.Getenv("TEST_RECIPIENT")
	}
	if recipient != "" {
		if strings.Contains(recipient, "@") {
			a.Channel = "email"
		} else {
			a.Channel = "sms"
		}
	} else if r.Phone != "" {
		a.Channel, recipient = "sms", r.Phone
	} else if r.Email != "" {
		a.Channel, recipient = "email", r.Email
	} else {
		a.Channel, a.Status, a.Detail = "none", "failed", "no contact info for customer"
		return a
	}
	a.Recipient = recipient

	if a.Channel == "sms" {
		a.Status, a.Detail = twilioSend(recipient, msg)
	} else {
		a.Status, a.Detail = emailSend(recipient, "Complete your payment to "+getenv("MERCHANT_NAME", "Chai & Co"), msg)
	}

	// soft declines / timeouts also get a scheduled retry (recorded; real re-charge needs gateway creds)
	if d.Action == "retry_scheduled" || d.Action == "retry_immediate" {
		delay := 48 * time.Hour
		if d.Action == "retry_immediate" {
			delay = 5 * time.Minute
		}
		a.Detail += fmt.Sprintf(" · retry scheduled for %s", time.Now().Add(delay).Format("Jan 2, 15:04"))
	}
	return a
}

var (
	rlMu   sync.Mutex
	rlHits = map[string][]time.Time{}
)

// allow is a simple per-IP limiter (10 runs/min) so a public demo link can't be abused.
func allow(ip string) bool {
	rlMu.Lock()
	defer rlMu.Unlock()
	cut := time.Now().Add(-time.Minute)
	kept := rlHits[ip][:0]
	for _, t := range rlHits[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 10 {
		rlHits[ip] = kept
		return false
	}
	rlHits[ip] = append(kept, time.Now())
	return true
}

// handleAPIRecover is the drop-in developer endpoint: POST one failed payment,
// get back the diagnosis, recovery probability, and AI-written message — and if
// "send": true, it sends it. This is the "few lines of code" integration.
func handleAPIRecover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		execRow
		Send          bool   `json:"send"`
		DemoRecipient string `json:"demo_recipient"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	d := diagnose(in.FailureCode)
	p := newModel().recoveryProb(d, in.Amount, in.Email != "" || in.Phone != "")
	msg, mode, note := generateMessage(draftIn{ChargeID: in.ChargeID, Name: in.Name,
		Amount: in.Amount, Currency: in.Currency, Method: in.Method, FailureCode: in.FailureCode})
	out := map[string]any{
		"charge_id":          in.ChargeID,
		"diagnosis":          d,
		"p_recover":          round2(p),
		"expected_recovered": round2(p * in.Amount),
		"message":            msg,
		"mode":               mode,
		"note":               note,
	}
	if in.Send {
		a := executeRow(in.execRow, in.DemoRecipient)
		out["sent"] = a.Status
		out["channel"] = a.Channel
		out["detail"] = a.Detail
	}
	writeJSON(w, out)
}

func handleExecute(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if h, _, ok := strings.Cut(ip, ":"); ok {
		ip = h
	}
	if !allow(ip) {
		http.Error(w, "rate limit — try again shortly", http.StatusTooManyRequests)
		return
	}
	var in struct {
		Rows          []execRow `json:"rows"`
		DemoRecipient string    `json:"demo_recipient"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(in.Rows) > 100 { // safety cap
		in.Rows = in.Rows[:100]
	}
	// Run rows concurrently (bounded) — each may make an LLM call, so serial would be slow.
	actions := make([]action, len(in.Rows))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, row := range in.Rows {
		wg.Add(1)
		go func(i int, row execRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			actions[i] = executeRow(row, in.DemoRecipient)
		}(i, row)
	}
	wg.Wait()

	sum := map[string]any{"sent": 0, "simulated": 0, "scheduled": 0, "skipped": 0, "failed": 0}
	var inFlight float64
	for i := range actions {
		a := actions[i]
		if c, ok := sum[a.Status].(int); ok {
			sum[a.Status] = c + 1
		}
		if a.Status == "sent" || a.Status == "simulated" {
			inFlight += in.Rows[i].Amount
		}
	}
	sum["revenue_in_flight"] = inFlight
	if s, ok := sum["sent"].(int); ok {
		sim, _ := sum["simulated"].(int)
		recordSent(s+sim, inFlight)
	}
	writeJSON(w, map[string]any{"actions": actions, "summary": sum})
}
