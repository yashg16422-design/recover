package main

// fraud.go — the second side of the agent.
//
// Not every failed payment is lost revenue. Fraudsters run "card testing":
// firing bursts of tiny authorizations to check stolen card numbers. Those show
// up in the SAME failed-payment stream. This detector clusters failures that
// match the card-testing signature — many small-value failures in a short window
// across many cards — so the agent can flag the attack AND quarantine those rows
// from recovery (you don't email a fraudster a payment link).

import (
	"sort"
	"time"
)

// Threat is one detected attack cluster.
type Threat struct {
	Kind          string   `json:"kind"`
	WindowStart   string   `json:"window_start"`
	WindowEnd     string   `json:"window_end"`
	Count         int      `json:"count"`
	DistinctCards int      `json:"distinct_customers"`
	TotalAmount   float64  `json:"total_amount"`
	SampleCodes   []string `json:"sample_codes"`
	Risk          string   `json:"risk"`
	Explanation   string   `json:"explanation"`
}

type frow struct {
	chargeID string
	email    string
	amount   float64
	code     string
	t        time.Time
	tOK      bool
}

// detectCardTesting returns detected threats and the set of charge_ids that are
// part of an attack (so recovery can skip them).
func detectCardTesting(rows []frow) ([]Threat, map[string]bool) {
	attack := map[string]bool{}
	var threats []Threat

	var ts []frow
	for _, r := range rows {
		if r.tOK {
			ts = append(ts, r)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].t.Before(ts[j].t) })

	const window = 10 * time.Minute
	const minBurst = 6
	const smallAmt = 20000.0 // ₹200 (paise) — testing charges are tiny

	i := 0
	for i < len(ts) {
		j := i
		for j < len(ts) && ts[j].t.Sub(ts[i].t) <= window {
			j++
		}
		cluster := ts[i:j]
		small := 0
		emails := map[string]bool{}
		codes := map[string]bool{}
		var total float64
		for _, c := range cluster {
			if c.amount <= smallAmt {
				small++
			}
			emails[c.email] = true
			codes[c.code] = true
			total += c.amount
		}
		// Signature: a burst, mostly tiny amounts, spread across several cards.
		if len(cluster) >= minBurst && small >= minBurst && len(emails) >= minBurst-2 {
			scodes := make([]string, 0, 4)
			for code := range codes {
				if len(scodes) < 4 {
					scodes = append(scodes, code)
				}
			}
			for _, c := range cluster {
				attack[c.chargeID] = true
			}
			threats = append(threats, Threat{
				Kind:          "card_testing",
				WindowStart:   cluster[0].t.Format("Jan 2, 15:04"),
				WindowEnd:     cluster[len(cluster)-1].t.Format("15:04"),
				Count:         len(cluster),
				DistinctCards: len(emails),
				TotalAmount:   total,
				SampleCodes:   scodes,
				Risk:          "high",
				Explanation:   "Burst of small failed authorizations in a short window across multiple cards — a classic card-testing signature. Quarantined from recovery.",
			})
			i = j
		} else {
			i++
		}
	}
	return threats, attack
}
