package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeGraph serves risks by node id: 1 -> 0.9 (quarantine), 2 -> 0.6 (review), others 0.1.
func fakeGraph(t *testing.T) *fraudClient {
	t.Helper()
	risk := func(id int) float64 {
		switch id {
		case 1:
			return 0.9
		case 2:
			return 0.6
		}
		return 0.1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /score/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		json.NewEncoder(w).Encode(map[string]any{"node_id": id, "risk": risk(id)})
	})
	mux.HandleFunc("POST /score_batch", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ IDs []int }
		json.NewDecoder(r.Body).Decode(&in)
		out := map[string]float64{}
		for _, id := range in.IDs {
			out[strconv.Itoa(id)] = risk(id)
		}
		json.NewEncoder(w).Encode(map[string]any{"scores": out})
	})
	mux.HandleFunc("GET /sample", func(w http.ResponseWriter, r *http.Request) {
		id := 3
		if r.URL.Query().Get("kind") == "illicit" {
			id = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"node_id": id})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fraudClient{base: srv.URL, http: &http.Client{Timeout: time.Second}, reviewAt: 0.5, blockAt: 0.8}
}

func useGraph(t *testing.T, c *fraudClient) {
	t.Helper()
	old := graphClient
	graphClient = c
	t.Cleanup(func() { graphClient = old })
}

func intp(i int) *int { return &i }

func TestEvalGraphDisabledWhenNoClient(t *testing.T) {
	useGraph(t, nil)
	gi := evalGraph(intp(1), false)
	if gi.Status != "disabled" || gi.Risk != nil || len(gi.Signals) != 0 {
		t.Fatalf("got %+v", gi)
	}
}

func TestEvalGraphSignalsAndLinks(t *testing.T) {
	useGraph(t, fakeGraph(t))
	if gi := evalGraph(intp(1), false); gi.Status != "ok" || gi.Link != "explicit" || gi.Signals[0].Level != "quarantine" {
		t.Fatalf("quarantine case: %+v", gi)
	}
	if gi := evalGraph(intp(2), false); len(gi.Signals) != 1 || gi.Signals[0].Level != "review" {
		t.Fatalf("review case: %+v", gi)
	}
	if gi := evalGraph(intp(3), false); len(gi.Signals) != 0 {
		t.Fatalf("clean case: %+v", gi)
	}
	if gi := evalGraph(nil, false); gi.Status != "not_linked" {
		t.Fatalf("no id, no demo: %+v", gi)
	}
	gi := evalGraph(nil, true) // demo link picks a node from /sample
	if gi.Status != "ok" || gi.Link != "demo" || gi.NodeID == nil {
		t.Fatalf("demo link: %+v", gi)
	}
}

const graphCSV = `charge_id,created_at,amount,currency,payment_method,failure_code,customer_email,customer_name,graph_node_id
c1,2026-08-01T09:00:00Z,50000,INR,card,insufficient_funds,a@example.com,A,1
c2,2026-08-01T10:00:00Z,50000,INR,card,insufficient_funds,b@example.com,B,2
c3,2026-08-01T11:00:00Z,50000,INR,card,insufficient_funds,c@example.com,C,3
c4,2026-08-01T12:00:00Z,50000,INR,card,insufficient_funds,d@example.com,D,
`

func analyzeCSV(t *testing.T) analysis {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(graphCSV)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var a analysis
	a.analyze(recs, newModel())
	return a
}

func rowByID(a analysis, id string) row {
	for _, r := range a.Rows {
		if r.ChargeID == id {
			return r
		}
	}
	return row{}
}

func TestAnalyzeQuarantinesOnGraphSignal(t *testing.T) {
	useGraph(t, fakeGraph(t))
	a := analyzeCSV(t)
	c1, c2, c3, c4 := rowByID(a, "c1"), rowByID(a, "c2"), rowByID(a, "c3"), rowByID(a, "c4")
	if !c1.Quarantined || c1.Attack || !c1.hasSignal("graph_risk", "quarantine") {
		t.Fatalf("c1 should be quarantined by the graph only: %+v", c1.GraphFields)
	}
	if c2.Quarantined || !c2.hasSignal("graph_risk", "review") {
		t.Fatalf("c2 should be flagged for review, not quarantined: %+v", c2.GraphFields)
	}
	if c3.Quarantined || len(c3.FraudSignals) != 0 || c3.GraphRisk == nil {
		t.Fatalf("c3 should be clean: %+v", c3.GraphFields)
	}
	if c4.GraphStatus != "not_linked" || c4.GraphRisk != nil {
		t.Fatalf("c4 has no node id: %+v", c4.GraphFields)
	}
	// c1 is excluded from recoverable revenue; the other three are included
	want := c2.Expected + c3.Expected + c4.Expected
	if d := a.Summary.Recoverable - want; d > 0.05 || d < -0.05 {
		t.Fatalf("recoverable %.2f want %.2f", a.Summary.Recoverable, want)
	}
	s := a.Summary
	if s.GraphStatus != "ok" || s.GraphReview != 1 || s.GraphQuarantined != 1 || s.QuarantinedTotal != 1 {
		t.Fatalf("summary %+v", s)
	}
}

// The graph model being down must not change a single recovery number (fail-open).
func TestAnalyzeFailsOpenWhenGraphOffline(t *testing.T) {
	useGraph(t, nil)
	base := analyzeCSV(t)
	useGraph(t, &fraudClient{base: "http://127.0.0.1:1", http: &http.Client{Timeout: 100 * time.Millisecond}, reviewAt: 0.5, blockAt: 0.8})
	off := analyzeCSV(t)
	if off.Summary.Recoverable != base.Summary.Recoverable || off.Summary.QuarantinedTotal != 0 {
		t.Fatalf("recoverable changed: %v vs %v", off.Summary.Recoverable, base.Summary.Recoverable)
	}
	if r := rowByID(off, "c1"); r.GraphStatus != "offline" || r.Quarantined || r.GraphRisk != nil {
		t.Fatalf("expected offline + not quarantined: %+v", r.GraphFields)
	}
	if off.Summary.GraphStatus != "offline" {
		t.Fatalf("summary status %q", off.Summary.GraphStatus)
	}
}

func TestExecuteSkipsGraphQuarantined(t *testing.T) {
	a := executeRow(execRow{ChargeID: "x", Name: "X", Email: "x@example.com", Amount: 1000, Currency: "INR",
		FailureCode: "insufficient_funds", Quarantined: true, QuarantineReason: "graph model risk 0.97"}, "")
	if a.Status != "skipped" || a.Message != "" || !strings.Contains(a.Detail, "graph model risk 0.97") {
		t.Fatalf("got %+v", a)
	}
}

func TestWebhookDemoLinkAndMetadataLink(t *testing.T) {
	useGraph(t, fakeGraph(t))
	// no metadata -> demo link
	it := withGraph(liveItem{}, nil, false)
	if it.GraphLink != "demo" || it.GraphStatus != "ok" {
		t.Fatalf("demo: %+v", it.GraphFields)
	}
	// explicit node id from Stripe metadata -> explicit link
	obj := []byte(`{"id":"ch_m","amount":1000,"failure_code":"insufficient_funds","metadata":{"graph_node_id":"1"}}`)
	p := parseFailure(obj)
	if p.GraphNodeID == nil || *p.GraphNodeID != 1 {
		t.Fatalf("metadata not parsed: %+v", p.GraphFields)
	}
	if it := withGraph(p, p.GraphNodeID, false); it.GraphLink != "explicit" || !it.Quarantined {
		t.Fatalf("explicit: %+v", it.GraphFields)
	}
}
