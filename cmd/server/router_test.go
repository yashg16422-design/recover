package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testRoute runs one POST /api/route against a router whose gateways always fail
// (or always succeed) and a fake fraud service that returns the given risk.
func testRoute(t *testing.T, gatewaysOK bool, risk float64, body string) (int, routeResponse) {
	t.Helper()
	for _, k := range []string{"SMTP_HOST", "SMTP_USER", "SMTP_PASS", "TWILIO_ACCOUNT_SID", "TEST_RECIPIENT", "HF_TOKEN"} {
		t.Setenv(k, "") // no real sends, no LLM calls in tests
	}
	psp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gatewaysOK {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"status":"success","cost":1}`))
	}))
	t.Cleanup(psp.Close)
	fraud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]float64{"risk": risk})
	}))
	t.Cleanup(fraud.Close)

	rt := NewRouter([]Backend{{"a", psp.URL}, {"b", psp.URL}})
	fc := &fraudClient{base: fraud.URL, http: &http.Client{Timeout: time.Second}, reviewAt: 0.5, blockAt: 0.8}
	rec := httptest.NewRecorder()
	rt.handleRoute(fc)(rec, httptest.NewRequest("POST", "/api/route", strings.NewReader(body)))
	var out routeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v: %s", err, rec.Body.String())
	}
	return rec.Code, out
}

const payBody = `{"amount":49900,"fraud_node_id":7,"customer_name":"Aarav","customer_email":"aarav@example.com"}`

func TestRouteSuccessHasNoRecovery(t *testing.T) {
	code, out := testRoute(t, true, 0.1, payBody)
	if code != 200 || out.Status != "success" || out.Recovery != nil || out.FraudDecision != decisionRouted {
		t.Fatalf("got %d %+v", code, out)
	}
}

func TestFailedPaymentGoesToRecoveryAgent(t *testing.T) {
	code, out := testRoute(t, false, 0.1, payBody)
	if code != http.StatusBadGateway || out.Status != "failed" {
		t.Fatalf("got %d %+v", code, out)
	}
	if out.Recovery == nil || out.Recovery.Type != "outreach" || out.Recovery.Message == "" {
		t.Fatalf("expected a drafted outreach, got %+v", out.Recovery)
	}
	// @example.com is never really emailed; with no SMTP creds it is simulated
	if out.Recovery.Status != "simulated" || out.Recovery.Channel != "email" {
		t.Fatalf("expected simulated email, got %+v", out.Recovery)
	}
}

func TestBlockedPaymentIsQuarantinedNotMessaged(t *testing.T) {
	code, out := testRoute(t, true, 0.95, payBody)
	if code != http.StatusForbidden || out.FraudDecision != decisionBlocked || out.Attempts != 0 {
		t.Fatalf("got %d %+v", code, out)
	}
	if out.Recovery == nil || out.Recovery.Status != "skipped" || out.Recovery.Message != "" {
		t.Fatalf("blocked payment must not be messaged, got %+v", out.Recovery)
	}
}

func TestFailedPaymentWithoutContactIsReportedNotCrashed(t *testing.T) {
	_, out := testRoute(t, false, 0.1, `{"amount":49900,"fraud_node_id":7}`)
	if out.Recovery == nil || out.Recovery.Status != "failed" {
		t.Fatalf("expected 'no contact info' failure, got %+v", out.Recovery)
	}
}

// The proxy must present the UPSTREAM's Host header (API Gateway rejects any other) and strip /api/fraud.
func TestFraudProxyRewritesHostAndPath(t *testing.T) {
	var gotHost, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/fraud/", fraudProxy(&fraudClient{base: up.URL}))
	req := httptest.NewRequest("GET", "/api/fraud/score/7?hops=2", nil)
	req.Host = "recover.13-211-0-1.sslip.io" // what the browser/Caddy would send
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || gotPath != "/score/7" {
		t.Fatalf("code %d path %q", rec.Code, gotPath)
	}
	if want := strings.TrimPrefix(up.URL, "http://"); gotHost != want {
		t.Fatalf("upstream saw Host %q, want %q", gotHost, want)
	}
}

func TestFraudProxyDisabledWithoutFraudURL(t *testing.T) {
	rec := httptest.NewRecorder()
	fraudProxy(nil)(rec, httptest.NewRequest("GET", "/api/fraud/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d", rec.Code)
	}
}
