// Command server is the Payment Recovery Agent.
//
// A merchant uploads a CSV of failed/pending payments (real Stripe/Razorpay
// export schema). The agent diagnoses each failure against real decline-code
// rules, scores the probability it can be recovered (logistic regression), and
// returns a prioritized recovery plan headlined by the number that matters:
// EXPECTED RECOVERABLE REVENUE = sum(P(recover) * amount).
//
// Later stages add: LLM-drafted recovery messages, a real send action
// (Twilio/email), and a live Stripe test-mode webhook. Standard library only.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type row struct {
	ChargeID    string    `json:"charge_id"`
	Amount      float64   `json:"amount"`
	Currency    string    `json:"currency"`
	Method      string    `json:"method"`
	FailureCode string    `json:"failure_code"`
	Email       string    `json:"customer_email"`
	Name        string    `json:"customer_name"`
	Diagnosis   Diagnosis `json:"diagnosis"`
	PRecover    float64   `json:"p_recover"`
	Expected    float64   `json:"expected_recovered"` // P(recover) * amount
	Attack      bool      `json:"attack"`             // part of a detected card-testing burst
	GraphFields                                         // NEW: graph_risk, fraud_signals, quarantined, ...
}

type analysis struct {
	Summary struct {
		Count           int     `json:"count"`
		Currency        string  `json:"currency"`
		AtRiskAmount    float64 `json:"at_risk_amount"`
		Recoverable     float64 `json:"recoverable_amount"`
		RecoverablePct  float64 `json:"recoverable_pct"`
		Trained         bool    `json:"trained_on_labels"`
		ThreatCount     int     `json:"threat_count"`
		ByBucket        map[string]int `json:"by_bucket"`
		// NEW (graph fraud signal)
		GraphStatus      string `json:"graph_status"`
		GraphReview      int    `json:"graph_review"`
		GraphQuarantined int    `json:"graph_quarantined"`
		QuarantinedTotal int    `json:"quarantined_total"`
	} `json:"summary"`
	Rows    []row    `json:"rows"`
	Threats []Threat `json:"threats"`
}

// colIndex builds a case-insensitive header -> column index map.
func colIndex(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}

func get(rec []string, idx map[string]int, keys ...string) string {
	for _, k := range keys {
		if i, ok := idx[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
	}
	return ""
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func truthy(s string) (val bool, present bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false, false
	}
	switch s {
	case "1", "true", "yes", "y", "recovered", "success":
		return true, true
	default:
		return false, true
	}
}

// parseNodeID reads an optional graph node id ("" or junk -> nil = not linked).
func parseNodeID(s string) *int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && v >= 0 {
		return &v
	}
	return nil
}

func (a *analysis) analyze(records [][]string, m *model) {
	if len(records) < 2 {
		return
	}
	idx := colIndex(records[0])
	a.Summary.ByBucket = map[string]int{}

	// First pass: if a "recovered" label column exists, train the model on it.
	var trainX [][]float64
	var trainY []float64
	_, hasLabel := idx["recovered"]
	if hasLabel {
		for _, rec := range records[1:] {
			d := diagnose(get(rec, idx, "failure_code", "failure", "decline_code"))
			amt := parseFloat(get(rec, idx, "amount"))
			contact := get(rec, idx, "customer_email", "email") != "" || get(rec, idx, "customer_phone", "phone") != ""
			y, present := truthy(get(rec, idx, "recovered"))
			if !present {
				continue
			}
			trainX = append(trainX, features(d, amt, contact))
			trainY = append(trainY, boolF(y))
		}
		if len(trainX) > 0 {
			m.fit(trainX, trainY, 300)
			a.Summary.Trained = true
		}
	}

	// Second pass: diagnose + score every row, and collect rows for fraud detection.
	var frows []frow
	var gids []*int // optional graph node id per row (aligned with a.Rows before sorting)
	for _, rec := range records[1:] {
		gids = append(gids, parseNodeID(get(rec, idx, "graph_node_id")))
		fc := get(rec, idx, "failure_code", "failure", "decline_code")
		d := diagnose(fc)
		amt := parseFloat(get(rec, idx, "amount"))
		email := get(rec, idx, "customer_email", "email")
		contact := email != "" || get(rec, idx, "customer_phone", "phone") != ""
		p := m.recoveryProb(d, amt, contact)
		cid := get(rec, idx, "charge_id", "id", "payment_id")
		r := row{
			ChargeID:    cid,
			Amount:      amt,
			Currency:    get(rec, idx, "currency"),
			Method:      get(rec, idx, "payment_method", "method"),
			FailureCode: fc,
			Email:       email,
			Name:        get(rec, idx, "customer_name", "name"),
			Diagnosis:   d,
			PRecover:    round2(p),
			Expected:    round2(p * amt),
		}
		a.Rows = append(a.Rows, r)
		a.Summary.Count++
		a.Summary.AtRiskAmount += amt
		a.Summary.ByBucket[string(d.Bucket)]++
		if a.Summary.Currency == "" {
			a.Summary.Currency = r.Currency
		}
		t, err := time.Parse(time.RFC3339, get(rec, idx, "created_at", "created", "date"))
		frows = append(frows, frow{chargeID: cid, email: email, amount: amt, code: fc, t: t, tOK: err == nil})
	}

	// Fraud pass: detect card-testing bursts and quarantine those rows.
	threats, attack := detectCardTesting(frows)
	a.Threats = threats
	a.Summary.ThreatCount = len(threats)
	// Graph pass: the second fraud signal (one batch call; fail-open if the service is down).
	infos := evalGraphBatch(gids)
	statuses := make([]string, 0, len(a.Rows))
	graphOnly := 0 // quarantined by the graph model but not part of a card-testing burst
	for i := range a.Rows {
		r := &a.Rows[i]
		r.ensure()
		if attack[r.ChargeID] {
			r.Attack = true
			r.addSignal(cardTestingSignal())
		}
		r.setGraph(infos[i])
		recordGraph(infos[i])
		statuses = append(statuses, infos[i].Status)
		if r.hasSignal("graph_risk", "review") {
			a.Summary.GraphReview++
		}
		if r.hasSignal("graph_risk", "quarantine") {
			a.Summary.GraphQuarantined++
			if !r.Attack {
				graphOnly++
			}
		}
		if r.Quarantined {
			a.Summary.QuarantinedTotal++
			continue // either fraud signal => not recoverable revenue
		}
		a.Summary.Recoverable += r.Expected
	}
	a.Summary.GraphStatus = summarizeStatus(statuses)

	a.Summary.Recoverable = round2(a.Summary.Recoverable)
	a.Summary.AtRiskAmount = round2(a.Summary.AtRiskAmount)
	if a.Summary.AtRiskAmount > 0 {
		a.Summary.RecoverablePct = round2(100 * a.Summary.Recoverable / a.Summary.AtRiskAmount)
	}

	// Prioritize by expected recovered revenue (biggest wins first).
	sort.SliceStable(a.Rows, func(i, j int) bool { return a.Rows[i].Expected > a.Rows[j].Expected })

	// feed the live dashboard
	q := 0
	for _, v := range attack {
		if v {
			q++
		}
	}
	recordFailures(a.Summary.Count)
	if len(threats) > 0 {
		recordThreats(len(threats), q)
	}
	if graphOnly > 0 {
		recordGraphQuarantined(graphOnly)
	}
}

func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func handleAnalyze(w http.ResponseWriter, r *http.Request) {
	var reader io.Reader = r.Body
	// support multipart file uploads from the browser (field name "file")
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if f, _, err := r.FormFile("file"); err == nil {
			defer f.Close()
			reader = f
		}
	}
	records, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		http.Error(w, "could not parse CSV: "+err.Error(), http.StatusBadRequest)
		return
	}
	var a analysis
	a.analyze(records, newModel())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()
	// Most hosting platforms (Render, HF Spaces, etc.) inject the port to bind via $PORT.
	if p := os.Getenv("PORT"); p != "" {
		*addr = ":" + p
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /analyze", handleAnalyze)
	mux.HandleFunc("POST /draft", handleDraft)
	mux.HandleFunc("POST /execute", handleExecute)
	mux.HandleFunc("POST /api/recover", handleAPIRecover)
	mux.HandleFunc("POST /webhooks/stripe", handleStripeWebhook)
	mux.HandleFunc("GET /live", handleLive)
	mux.HandleFunc("GET /overview", handleOverview)
	mux.HandleFunc("POST /simulate", handleSimulate)
	mux.HandleFunc("GET /benchmark", handleBenchmark)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	registerRouting(mux) // SmartRoute router + graph fraud check (router.go, fraudclient.go)
	mux.Handle("GET /", http.FileServer(http.Dir("web")))

	// withRecovery: one bad request can never crash the server.
	handler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("recovered from panic on %s: %v", r.URL.Path, rec)
					http.Error(w, "internal error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}(mux)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("recovery agent on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
