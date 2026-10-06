package main

// fraudclient.go — asks the graph fraud service (fraud/serve) for a risk score.
//
// Policy: FAIL-OPEN. The fraud check is advisory; if the service is down, slow or
// returns garbage, the payment is still routed and the response says
// fraud_check="unavailable". (Fail-closed would stop every payment whenever the
// fraud service blips.)
//
// Config (env):
//   FRAUD_URL         base URL, e.g. http://localhost:8100 or the API Gateway URL (empty = disabled)
//   FRAUD_TIMEOUT_MS  per-call timeout (default 300)
//   FRAUD_BLOCK_AT    risk >= this  -> BLOCKED   (default below, see README for how it was chosen)
//   FRAUD_REVIEW_AT   risk >= this  -> REVIEW    (routed, but flagged)

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	decisionRouted  = "ROUTED"
	decisionReview  = "REVIEW"
	decisionBlocked = "BLOCKED"

	// Chosen from the GCN threshold table in fraud/artifacts/results.json (test split, so
	// optimistic): 0.8 keeps precision high because a wrong BLOCK costs a real customer;
	// 0.5 is a wider net where a human looks before the payment is trusted.
	defaultBlockAt  = 0.8
	defaultReviewAt = 0.5
)

type fraudClient struct {
	base     string
	http     *http.Client
	blockAt  float64
	reviewAt float64
}

type fraudResult struct {
	risk     float64
	decision string
	check    string // "ok" | "unavailable"
	latency  time.Duration
}

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(getenv(key, ""), 64); err == nil {
		return v
	}
	return def
}

// newFraudClientFromEnv returns nil when FRAUD_URL is unset (fraud check disabled).
func newFraudClientFromEnv() *fraudClient {
	base := strings.TrimRight(getenv("FRAUD_URL", ""), "/")
	if base == "" {
		return nil
	}
	ms := int(envFloat("FRAUD_TIMEOUT_MS", 300))
	return &fraudClient{
		base:     base,
		http:     &http.Client{Timeout: time.Duration(ms) * time.Millisecond},
		blockAt:  envFloat("FRAUD_BLOCK_AT", defaultBlockAt),
		reviewAt: envFloat("FRAUD_REVIEW_AT", defaultReviewAt),
	}
}

// decide maps a risk score to a decision.
func decide(risk, reviewAt, blockAt float64) string {
	switch {
	case risk >= blockAt:
		return decisionBlocked
	case risk >= reviewAt:
		return decisionReview
	default:
		return decisionRouted
	}
}

// check fetches the risk for one node and applies the thresholds. Any failure
// (timeout, non-200, bad JSON, risk outside 0..1) fails open.
func (c *fraudClient) check(nodeID int) fraudResult {
	start := time.Now()
	fail := func() fraudResult {
		return fraudResult{decision: decisionRouted, check: "unavailable", latency: time.Since(start)}
	}
	resp, err := c.http.Get(fmt.Sprintf("%s/score/%d", c.base, nodeID))
	if err != nil {
		return fail()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail()
	}
	var s struct {
		Risk *float64 `json:"risk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil || s.Risk == nil || *s.Risk < 0 || *s.Risk > 1 {
		return fail()
	}
	return fraudResult{
		risk:     *s.Risk,
		decision: decide(*s.Risk, c.reviewAt, c.blockAt),
		check:    "ok",
		latency:  time.Since(start),
	}
}
