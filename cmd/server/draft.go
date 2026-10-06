package main

// draft.go — turns one diagnosed failed payment into a ready-to-send recovery
// message. Uses a Hugging Face chat model when HF_TOKEN is set; otherwise falls
// back to a solid per-reason template so the app is always usable.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

var draftClient = &http.Client{Timeout: 30 * time.Second}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type draftIn struct {
	ChargeID    string  `json:"charge_id"`
	Name        string  `json:"customer_name"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	Method      string  `json:"method"`
	FailureCode string  `json:"failure_code"`
}

func money(amount float64, currency string) string {
	sym := map[string]string{"INR": "₹", "USD": "$", "EUR": "€", "GBP": "£"}[strings.ToUpper(currency)]
	if sym == "" {
		sym = currency + " "
	}
	return fmt.Sprintf("%s%.0f", sym, amount/100) // amounts stored in smallest unit
}

// templateMessage is the deterministic fallback (and a fine message in its own right).
func templateMessage(in draftIn, d Diagnosis, merchant, link string) string {
	name := in.Name
	if name == "" {
		name = "there"
	}
	amt := money(in.Amount, in.Currency)
	switch d.Action {
	case "retry_scheduled":
		return fmt.Sprintf("Hi %s, your %s payment to %s didn't go through — looks like a temporary issue. We'll retry it shortly, or you can complete it now: %s", name, amt, merchant, link)
	case "retry_immediate":
		return fmt.Sprintf("Hi %s, your %s payment to %s timed out before it finished. Please try again here: %s", name, amt, merchant, link)
	case "request_reauth":
		return fmt.Sprintf("Hi %s, your %s payment to %s needs a quick verification (OTP/3-D Secure). Complete it here: %s", name, amt, merchant, link)
	case "request_reenter":
		return fmt.Sprintf("Hi %s, your %s payment to %s didn't go through — a card detail looks off. Re-enter and retry here: %s", name, amt, merchant, link)
	case "request_new_method":
		return fmt.Sprintf("Hi %s, we couldn't charge your card for the %s payment to %s. Please use another payment method here: %s", name, amt, merchant, link)
	default:
		return ""
	}
}

// generateMessage produces the recovery message for one payment: LLM-written when
// HF_TOKEN is set and the call succeeds, otherwise the template. It returns the
// mode ("ai" | "template" | "skip") and, on LLM failure, a note explaining why it
// fell back — so the UI can show whether the AI actually ran.
func generateMessage(in draftIn) (message, mode, note string) {
	d := diagnose(in.FailureCode)
	merchant := getenv("MERCHANT_NAME", "Chai & Co")
	link := getenv("MERCHANT_PAY_LINK", "https://chaiandco.example/pay/") + in.ChargeID

	if d.Action == "review" {
		return "", "skip", "Flagged for manual review — no automated recovery message."
	}
	fallback := templateMessage(in, d, merchant, link)

	token := os.Getenv("HF_TOKEN")
	if token == "" {
		return fallback, "template", "HF_TOKEN not set on server"
	}

	sys := "You write short, warm payment-recovery messages for a small business. " +
		"One message, under 320 characters, ready to send over SMS/WhatsApp. Friendly, not pushy. " +
		"Always include the payment link exactly as given. No placeholders, no subject line, no quotes."
	user := fmt.Sprintf("Customer: %s\nAmount: %s\nMerchant: %s\nWhy it failed: %s\nWhat to do: %s\nPayment link: %s\nWrite the message.",
		in.Name, money(in.Amount, in.Currency), merchant, d.Reason, d.RetryWindow, link)

	body, _ := json.Marshal(map[string]any{
		"model": getenv("HF_MODEL", "Qwen/Qwen2.5-72B-Instruct"),
		"messages": []map[string]string{
			{"role": "system", "content": sys},
			{"role": "user", "content": user},
		},
		"max_tokens": 160, "temperature": 0.6, "stream": false,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://router.huggingface.co/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := draftClient.Do(req)
	if err != nil {
		return fallback, "template", "LLM unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	var ar struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return fallback, "template", "could not read LLM response"
	}
	if ar.Error != nil {
		return fallback, "template", fmt.Sprintf("LLM error: %v", ar.Error)
	}
	if len(ar.Choices) == 0 || ar.Choices[0].Message.Content == "" {
		return fallback, "template", "LLM returned no text"
	}
	return strings.TrimSpace(ar.Choices[0].Message.Content), "ai", ""
}

func handleDraft(w http.ResponseWriter, r *http.Request) {
	var in draftIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg, mode, note := generateMessage(in)
	writeJSON(w, map[string]string{"message": msg, "mode": mode, "note": note})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
