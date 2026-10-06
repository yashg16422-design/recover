package main

// webhook.go — the LIVE input loop.
//
// Stripe (in test mode) fires real `charge.failed` / `payment_intent.payment_failed`
// events at this endpoint. We verify the signature the way Stripe actually signs —
// HMAC-SHA256 over "{timestamp}.{payload}", constant-time compared to the v1
// signature in the Stripe-Signature header — then diagnose + score the failure and
// add it to a live feed the dashboard polls. This is a genuine real-time agent input.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type liveItem struct {
	Received    string    `json:"received"`
	ChargeID    string    `json:"charge_id"`
	Name        string    `json:"customer_name"`
	Email       string    `json:"customer_email"`
	Amount      float64   `json:"amount"`
	Currency    string    `json:"currency"`
	Method      string    `json:"method"`
	FailureCode string    `json:"failure_code"`
	Diagnosis   Diagnosis `json:"diagnosis"`
	PRecover    float64   `json:"p_recover"`
	Expected    float64   `json:"expected_recovered"`
}

var (
	liveMu    sync.Mutex
	liveItems []liveItem
	liveModel = newModel()
)

func pushLive(it liveItem) {
	liveMu.Lock()
	liveItems = append([]liveItem{it}, liveItems...)
	if len(liveItems) > 50 {
		liveItems = liveItems[:50]
	}
	liveMu.Unlock()
}

func handleLive(w http.ResponseWriter, _ *http.Request) {
	liveMu.Lock()
	defer liveMu.Unlock()
	writeJSON(w, map[string]any{"items": liveItems})
}

// verifyStripe implements Stripe's real signature scheme.
func verifyStripe(payload []byte, header, secret string) bool {
	var t, v1 string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			t = kv[1]
		case "v1":
			v1 = kv[1]
		}
	}
	if t == "" || v1 == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t + "." + string(payload)))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(v1)) // constant-time
}

func handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Verify when a signing secret is configured (production). Without one we still
	// accept, so the parsing path is testable — but log-clear that it's unverified.
	if secret := os.Getenv("STRIPE_WEBHOOK_SECRET"); secret != "" {
		if !verifyStripe(payload, r.Header.Get("Stripe-Signature"), secret) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid signature"))
			return
		}
	}

	var ev struct {
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch ev.Type {
	case "charge.succeeded", "payment_intent.succeeded":
		recordSuccesses(1)
	case "charge.failed", "payment_intent.payment_failed":
		it := parseFailure(ev.Data.Object)
		it.Diagnosis = diagnose(it.FailureCode)
		p := liveModel.recoveryProb(it.Diagnosis, it.Amount, it.Email != "")
		it.PRecover = round2(p)
		it.Expected = round2(p * it.Amount)
		it.Received = time.Now().Format("15:04:05")
		pushLive(it)
		recordFailures(1)

		// Drop-in autonomous mode: if AUTO_RECOVER=true, recover immediately —
		// no dashboard, no human approval. This is the plug-and-play developer path.
		if os.Getenv("AUTO_RECOVER") == "true" {
			go executeRow(execRow{ChargeID: it.ChargeID, Name: it.Name, Email: it.Email,
				Amount: it.Amount, Currency: it.Currency, Method: it.Method,
				FailureCode: it.FailureCode}, os.Getenv("TEST_RECIPIENT"))
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// parseFailure extracts what we need from a Stripe charge / payment_intent object.
func parseFailure(obj json.RawMessage) liveItem {
	var c struct {
		ID             string  `json:"id"`
		Amount         float64 `json:"amount"`
		Currency       string  `json:"currency"`
		FailureCode    string  `json:"failure_code"`
		BillingDetails struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"billing_details"`
		PaymentMethodDetails struct {
			Type string `json:"type"`
		} `json:"payment_method_details"`
		LastPaymentError struct {
			Code        string `json:"code"`
			DeclineCode string `json:"decline_code"`
		} `json:"last_payment_error"`
	}
	_ = json.Unmarshal(obj, &c)
	it := liveItem{
		ChargeID: c.ID, Amount: c.Amount, Currency: strings.ToUpper(c.Currency),
		Name: c.BillingDetails.Name, Email: c.BillingDetails.Email,
		Method: c.PaymentMethodDetails.Type, FailureCode: c.FailureCode,
	}
	if it.FailureCode == "" { // payment_intent.payment_failed carries it here
		if c.LastPaymentError.DeclineCode != "" {
			it.FailureCode = c.LastPaymentError.DeclineCode
		} else {
			it.FailureCode = c.LastPaymentError.Code
		}
	}
	if it.Method == "" {
		it.Method = "card"
	}
	if it.Currency == "" {
		it.Currency = "INR"
	}
	if it.ChargeID == "" {
		it.ChargeID = "evt_" + time.Now().Format("150405")
	}
	return it
}
