package main

// benchmark.go — measured, reproducible proof of value.
//
// Runs the agent against a labeled dataset (each row has a ground-truth
// `recovered` outcome) and compares three strategies on money actually
// recovered, messages sent, fraud contacted, and precision. Anyone can hit
// GET /benchmark to reproduce the numbers, or download the labeled CSV to
// inspect the ground truth. This turns "expected recoverable" into measured.

import (
	"encoding/csv"
	"net/http"
	"os"
	"strings"
	"time"
)

const recoverThreshold = 0.35 // agent only pursues payments at/above this recovery probability

type strategyResult struct {
	Name          string  `json:"name"`
	Recovered     float64 `json:"recovered_amount"`
	RecoveryRate  float64 `json:"recovery_rate_pct"` // % of recoverable money captured
	MessagesSent  int     `json:"messages_sent"`
	FraudContacted int    `json:"fraud_contacted"`
	Precision     float64 `json:"precision_pct"` // % of messages that recovered money
}

func handleBenchmark(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile("web/benchmark_labeled.csv")
	if err != nil {
		http.Error(w, "benchmark data not found", http.StatusInternalServerError)
		return
	}
	recs, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(recs) < 2 {
		http.Error(w, "bad benchmark data", http.StatusInternalServerError)
		return
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(rec []string, names ...string) string {
		for _, n := range names {
			if i, ok := col[n]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
		}
		return ""
	}

	type brow struct {
		amt    float64
		label  bool
		attack bool
		d      Diagnosis
		p      float64
	}
	var rows []brow
	var frows []frow
	m := newModel()
	var recoverableTruth, totalAtRisk float64

	for _, rec := range recs[1:] {
		amt := parseFloat(get(rec, "amount"))
		code := get(rec, "failure_code", "failure", "decline_code")
		email := get(rec, "customer_email", "email")
		cid := get(rec, "charge_id", "id")
		lab := get(rec, "recovered", "label")
		label := lab == "1" || strings.EqualFold(lab, "true") || strings.EqualFold(lab, "yes")
		d := diagnose(code)
		p := m.recoveryProb(d, amt, email != "")
		rows = append(rows, brow{amt: amt, label: label, d: d, p: p})
		t, e := time.Parse(time.RFC3339, get(rec, "created_at", "created", "date"))
		frows = append(frows, frow{chargeID: cid, email: email, amount: amt, code: code, t: t, tOK: e == nil})
		totalAtRisk += amt
		if label {
			recoverableTruth += amt
		}
	}

	// fraud detection → mark attack rows
	_, attack := detectCardTesting(frows)
	i := 0
	for _, rec := range recs[1:] {
		if attack[get(rec, "charge_id", "id")] {
			rows[i].attack = true
		}
		i++
	}

	pct := func(n, d float64) float64 {
		if d == 0 {
			return 0
		}
		return round2(100 * n / d)
	}

	doNothing := strategyResult{Name: "Do nothing"}
	blast := strategyResult{Name: "Blast everyone"}
	agent := strategyResult{Name: "Recover (smart agent)"}
	var blastHits, agentHits int
	// exceptions the agent chose not to pursue
	exFraud, exHard, exLowProb := 0, 0, 0
	var exAmount float64

	for _, r := range rows {
		// blast: message every failed payment
		blast.MessagesSent++
		if r.attack {
			blast.FraudContacted++
		}
		if r.label {
			blast.Recovered += r.amt
			blastHits++
		}
		// agent: skip fraud, skip unrecoverable, require probability >= threshold
		pursue := !r.attack && r.d.Action != "review" && r.d.Recoverable != "none" && r.p >= recoverThreshold
		if pursue {
			agent.MessagesSent++
			if r.label {
				agent.Recovered += r.amt
				agentHits++
			}
		} else {
			exAmount += r.amt
			switch {
			case r.attack:
				exFraud++
			case r.d.Action == "review" || r.d.Recoverable == "none":
				exHard++
			default:
				exLowProb++
			}
		}
	}

	blast.Recovered = round2(blast.Recovered)
	agent.Recovered = round2(agent.Recovered)
	blast.RecoveryRate = pct(blast.Recovered, recoverableTruth)
	agent.RecoveryRate = pct(agent.Recovered, recoverableTruth)
	blast.Precision = pct(float64(blastHits), float64(blast.MessagesSent))
	agent.Precision = pct(float64(agentHits), float64(agent.MessagesSent))

	writeJSON(w, map[string]any{
		"dataset":            "web/benchmark_labeled.csv",
		"rows":               len(rows),
		"total_at_risk":      round2(totalAtRisk),
		"recoverable_truth":  round2(recoverableTruth),
		"strategies":         []strategyResult{doNothing, blast, agent},
		"exceptions": map[string]any{
			"count": exFraud + exHard + exLowProb, "amount": round2(exAmount),
			"fraud_quarantined": exFraud, "hard_declines": exHard, "below_threshold": exLowProb,
		},
		"stopping_rules": []string{
			"Never message payments flagged as card-testing fraud (quarantined).",
			"Never auto-retry hard declines (stolen/lost/fraudulent) — request a new method instead.",
			"Only pursue payments with recovery probability ≥ 0.35.",
			"Cap automated retries at 2 attempts, then escalate to manual review.",
		},
	})
}
