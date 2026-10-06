package main

// graphsignal.go — the graph model as a SECOND fraud signal inside Recover.
//
// Pipeline: observe -> FRAUD CHECK -> diagnose -> score P(recover) -> draft -> send.
// The fraud check has two signals:
//   - card_testing : the existing burst detector (fraud.go)
//   - graph_risk   : the GCN's P(illicit) for the payment's linked graph node
// A payment is QUARANTINED (never messaged, excluded from recoverable revenue) if
// EITHER fires. graph_risk >= block threshold => quarantine; >= review threshold =>
// "review" (flagged and shown, still recoverable).
//
// Honest labeling: the GCN is trained on the Elliptic BITCOIN graph. A payment is
// linked to a graph node either explicitly (graph_node_id column / Stripe metadata /
// API field) or, for simulated and webhook traffic, by a labeled "demo link" to a
// random test-period node. It does not predict recovery; P(recover) stays the
// logistic regression in model.go.
//
// Fail-open: if FRAUD_URL is unset the graph signal is "disabled"; if the service is
// down it is "offline". In both cases Recover behaves exactly as before.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// graphClient is set by registerRouting; nil means the graph signal is disabled.
var graphClient *fraudClient

type FraudSignal struct {
	Kind   string `json:"kind"`   // card_testing | graph_risk
	Level  string `json:"level"`  // quarantine | review
	Detail string `json:"detail"` // why it fired
}

// GraphFields are the NEW fields added to rows, live items and API responses.
type GraphFields struct {
	GraphNodeID  *int          `json:"graph_node_id,omitempty"`
	GraphRisk    *float64      `json:"graph_risk"`           // null unless the graph model answered
	GraphStatus  string        `json:"graph_status"`         // ok | offline | not_linked | disabled
	GraphLink    string        `json:"graph_link,omitempty"` // explicit | demo
	FraudSignals []FraudSignal `json:"fraud_signals"`
	Quarantined  bool          `json:"quarantined"` // ANY quarantine-level signal fired
}

type graphInfo struct {
	NodeID  *int
	Risk    *float64
	Status  string
	Link    string
	Signals []FraudSignal
}

func (g *GraphFields) ensure() {
	if g.FraudSignals == nil {
		g.FraudSignals = []FraudSignal{}
	}
	if g.GraphStatus == "" {
		g.GraphStatus = "disabled"
	}
}

// setGraph copies a graph result in and recomputes Quarantined from all signals.
func (g *GraphFields) setGraph(gi graphInfo) {
	g.GraphNodeID, g.GraphRisk, g.GraphStatus, g.GraphLink = gi.NodeID, gi.Risk, gi.Status, gi.Link
	g.FraudSignals = append(g.ensureSignals(), gi.Signals...)
	g.recomputeQuarantine()
}

func (g *GraphFields) ensureSignals() []FraudSignal {
	g.ensure()
	return g.FraudSignals
}

func (g *GraphFields) addSignal(s FraudSignal) {
	g.ensure()
	g.FraudSignals = append(g.FraudSignals, s)
	g.recomputeQuarantine()
}

func (g *GraphFields) recomputeQuarantine() {
	g.Quarantined = false
	for _, s := range g.FraudSignals {
		if s.Level == "quarantine" {
			g.Quarantined = true
		}
	}
}

func (g *GraphFields) hasSignal(kind, level string) bool {
	for _, s := range g.FraudSignals {
		if s.Kind == kind && s.Level == level {
			return true
		}
	}
	return false
}

// quarantineReason explains, in words, why a payment was quarantined.
func (g *GraphFields) quarantineReason() string {
	for _, s := range g.FraudSignals {
		if s.Level == "quarantine" {
			return s.Detail
		}
	}
	return ""
}

func cardTestingSignal() FraudSignal {
	return FraudSignal{Kind: "card_testing", Level: "quarantine",
		Detail: "part of a detected card-testing burst (many small failed authorizations in a short window)"}
}

// signalsForRisk turns a graph risk into zero or one signal using the thresholds.
func signalsForRisk(risk float64, c *fraudClient) []FraudSignal {
	switch {
	case risk >= c.blockAt:
		return []FraudSignal{{Kind: "graph_risk", Level: "quarantine",
			Detail: fmt.Sprintf("graph model risk %.2f >= %.2f (quarantine threshold)", risk, c.blockAt)}}
	case risk >= c.reviewAt:
		return []FraudSignal{{Kind: "graph_risk", Level: "review",
			Detail: fmt.Sprintf("graph model risk %.2f is in the review band %.2f-%.2f", risk, c.reviewAt, c.blockAt)}}
	}
	return []FraudSignal{}
}

func infoFromRisk(c *fraudClient, id int, link string, risk float64) graphInfo {
	return graphInfo{NodeID: &id, Risk: &risk, Status: "ok", Link: link, Signals: signalsForRisk(risk, c)}
}

func graphUnavailable(status string, id *int, link string) graphInfo {
	return graphInfo{NodeID: id, Status: status, Link: link, Signals: []FraudSignal{}}
}

// demoIlliciPct is the share of demo-linked payments tied to an ILLICIT node, so the
// signal is visible in demos. It is NOT the real prevalence (see the UI note).
func demoIllicitPct() float64 { return envFloat("GRAPH_DEMO_ILLICIT_PCT", 25) }

// demoNode picks a random labeled test-period node for a demo link.
func (c *fraudClient) demoNode() (int, bool) {
	kind := "licit"
	if rand.Float64()*100 < demoIllicitPct() {
		kind = "illicit"
	}
	resp, err := c.http.Get(c.base + "/sample?kind=" + kind)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	var s struct {
		NodeID *int `json:"node_id"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&s) != nil || s.NodeID == nil {
		return 0, false
	}
	return *s.NodeID, true
}

// scoreBatch asks for many risks in one call. ok=false means the service did not answer.
func (c *fraudClient) scoreBatch(ids []int) (map[int]float64, bool) {
	body, _ := json.Marshal(map[string]any{"ids": ids})
	resp, err := c.http.Post(c.base+"/score_batch", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	var out struct {
		Scores map[string]*float64 `json:"scores"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil, false
	}
	m := make(map[int]float64, len(out.Scores))
	for k, v := range out.Scores {
		if id, err := strconv.Atoi(k); err == nil && v != nil && *v >= 0 && *v <= 1 {
			m[id] = *v
		}
	}
	return m, true
}

// evalGraph runs the graph check for ONE payment. nodeID nil + demo=true => demo link;
// nodeID nil + demo=false => not_linked.
func evalGraph(nodeID *int, demo bool) graphInfo {
	c := graphClient
	if c == nil {
		return graphUnavailable("disabled", nodeID, "")
	}
	link := "explicit"
	if nodeID == nil {
		if !demo {
			return graphUnavailable("not_linked", nil, "")
		}
		id, ok := c.demoNode()
		if !ok {
			return graphUnavailable("offline", nil, "demo")
		}
		nodeID, link = &id, "demo"
	}
	fr := c.check(*nodeID)
	if fr.check != "ok" {
		return graphUnavailable("offline", nodeID, link)
	}
	return infoFromRisk(c, *nodeID, link, fr.risk)
}

// evalGraphBatch checks many explicitly linked payments with ONE service call.
func evalGraphBatch(ids []*int) []graphInfo {
	out := make([]graphInfo, len(ids))
	c := graphClient
	var want []int
	for i, id := range ids {
		switch {
		case c == nil:
			out[i] = graphUnavailable("disabled", id, "")
		case id == nil:
			out[i] = graphUnavailable("not_linked", nil, "")
		default:
			want = append(want, *id)
		}
	}
	if len(want) == 0 {
		return out
	}
	scores, ok := c.scoreBatch(want)
	for i, id := range ids {
		if c == nil || id == nil {
			continue
		}
		risk, found := scores[*id]
		if !ok || !found {
			out[i] = graphUnavailable("offline", id, "explicit")
			continue
		}
		out[i] = infoFromRisk(c, *id, "explicit", risk)
	}
	return out
}

// summarizeStatus rolls per-row graph statuses into one status for a whole upload.
func summarizeStatus(statuses []string) string {
	if graphClient == nil {
		return "disabled"
	}
	seen := map[string]bool{}
	for _, s := range statuses {
		seen[s] = true
	}
	switch {
	case seen["ok"]:
		return "ok"
	case seen["offline"]:
		return "offline"
	}
	return "not_linked"
}

// ---- metrics for the dashboard's graph counters (additive; see metrics.go) ----
var (
	gMu                             sync.Mutex
	gChecked, gReview, gQuarantined int
)

func recordGraph(gi graphInfo) {
	if gi.Status != "ok" {
		return
	}
	gMu.Lock()
	gChecked++
	for _, s := range gi.Signals {
		switch s.Level {
		case "review":
			gReview++
		case "quarantine":
			gQuarantined++
		}
	}
	gMu.Unlock()
}

func graphCounters() (checked, review, quarantined int) {
	gMu.Lock()
	defer gMu.Unlock()
	return gChecked, gReview, gQuarantined
}

// ---- status endpoint: lets the UI show "graph model offline" accurately ----
var (
	stMu     sync.Mutex
	stAt     time.Time
	stOnline bool
)

func graphOnline() bool {
	if graphClient == nil {
		return false
	}
	stMu.Lock()
	defer stMu.Unlock()
	if time.Since(stAt) < 2*time.Second {
		return stOnline
	}
	cl := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := cl.Get(graphClient.base + "/health")
	stOnline = err == nil && resp.StatusCode == http.StatusOK
	if resp != nil {
		resp.Body.Close()
	}
	stAt = time.Now()
	return stOnline
}

func handleGraphStatus(w http.ResponseWriter, _ *http.Request) {
	c := graphClient
	if c == nil {
		writeJSON(w, map[string]any{"enabled": false, "online": false, "status": "disabled",
			"note": "FRAUD_URL is not set; the graph signal is off and Recover runs as before"})
		return
	}
	online := graphOnline()
	st := "ok"
	if !online {
		st = "offline"
	}
	writeJSON(w, map[string]any{"enabled": true, "online": online, "status": st, "fail_mode": "open",
		"quarantine_at": c.blockAt, "review_at": c.reviewAt, "demo_illicit_pct": demoIllicitPct()})
}
