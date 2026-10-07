package main

// sim.go — an HONEST demo-traffic generator.
//
// Locally there's no live payment stream, so the dashboard sits still. This
// endpoint generates ONE synthetic payment event per call — mostly successes,
// some genuine failures, the occasional card-testing burst — and runs each
// through the REAL pipeline (real diagnosis, scoring, fraud counting). The UI
// calls it on a timer while "Simulate live traffic" is on, and labels it
// SIMULATED so it's never mistaken for real production volume.

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

var simRng = rand.New(rand.NewSource(time.Now().UnixNano()))
var simNames = []string{"Aarav", "Diya", "Kabir", "Meera", "Rohan", "Ananya", "Vivaan", "Isha", "Arjun", "Saanvi", "Tara", "Dev"}
var simSoft = []string{"insufficient_funds", "issuer_unavailable", "do_not_honor", "processing_error", "gateway_timeout"}

func synthFailure(code string, amount float64) liveItem {
	name := simNames[simRng.Intn(len(simNames))]
	it := liveItem{
		ChargeID: fmt.Sprintf("sim_%06d", simRng.Intn(1000000)),
		Name:     name,
		Email:    strings.ToLower(name) + "@example.com",
		Amount:   amount, Currency: "INR", Method: "card", FailureCode: code,
	}
	it.Diagnosis = diagnose(code)
	p := liveModel.recoveryProb(it.Diagnosis, amount, true)
	it.PRecover = round2(p)
	it.Expected = round2(p * amount)
	it.Received = time.Now().Format("15:04:05")
	return it
}

func handleSimulate(w http.ResponseWriter, _ *http.Request) {
	kind := "success"
	events := []liveItem{} // NEW: the response now describes what was generated
	switch roll := simRng.Float64(); {
	case roll < 0.65: // a payment succeeded
		recordSuccesses(1)
	case roll < 0.90: // a normal failure — recover it
		code := simSoft[simRng.Intn(len(simSoft))]
		amt := float64((simRng.Intn(491) + 10) * 1000) // ₹100–₹5000
		kind = "failure"
		it := withGraph(synthFailure(code, amt), nil, false) // NEW: graph check (demo link)
		pushLive(it)
		events = append(events, it)
		recordFailures(1)
		if it.hasSignal("graph_risk", "quarantine") {
			recordGraphQuarantined(1)
		}
		if !it.Quarantined && (it.Diagnosis.Recoverable == "high" || it.Diagnosis.Recoverable == "medium") {
			recordSent(1, it.Expected)
		}
	default: // a card-testing burst
		n := 6 + simRng.Intn(5)
		kind = "burst"
		for i := 0; i < n; i++ {
			it := withGraph(synthFailure("card_declined", float64((simRng.Intn(3)+1)*100)), nil, true) // ₹1–₹3
			pushLive(it)
			events = append(events, it)
		}
		recordFailures(n)
		recordThreats(1, n)
	}
	writeJSON(w, map[string]any{"kind": kind, "simulated": true, "events": events})
}
