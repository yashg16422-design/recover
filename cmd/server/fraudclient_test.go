package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		risk float64
		want string
	}{
		{0.0, decisionRouted}, {0.49, decisionRouted},
		{0.5, decisionReview}, {0.79, decisionReview},
		{0.8, decisionBlocked}, {1.0, decisionBlocked},
	}
	for _, c := range cases {
		if got := decide(c.risk, 0.5, 0.8); got != c.want {
			t.Errorf("decide(%v) = %s, want %s", c.risk, got, c.want)
		}
	}
}

func newTestClient(url string, timeout time.Duration) *fraudClient {
	return &fraudClient{base: url, http: &http.Client{Timeout: timeout}, reviewAt: 0.5, blockAt: 0.8}
}

func TestCheckOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/score/7" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"node_id":7,"risk":0.9}`))
	}))
	defer srv.Close()
	got := newTestClient(srv.URL, time.Second).check(7)
	if got.check != "ok" || got.decision != decisionBlocked || got.risk != 0.9 {
		t.Fatalf("got %+v", got)
	}
}

// Fail-open: every kind of fraud-service failure must still ROUTE the payment.
func TestCheckFailsOpen(t *testing.T) {
	bad := map[string]http.HandlerFunc{
		"500":        func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) },
		"404":        func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"bad json":   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) },
		"no risk":    func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"node_id":1}`)) },
		"risk range": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"risk":1.7}`)) },
		"timeout": func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.Write([]byte(`{"risk":0.99}`))
		},
	}
	for name, h := range bad {
		srv := httptest.NewServer(h)
		got := newTestClient(srv.URL, 50*time.Millisecond).check(1)
		srv.Close()
		if got.check != "unavailable" || got.decision != decisionRouted {
			t.Errorf("%s: got %+v, want unavailable+ROUTED", name, got)
		}
	}
	// service completely down (connection refused)
	got := newTestClient("http://127.0.0.1:1", 50*time.Millisecond).check(1)
	if got.check != "unavailable" || got.decision != decisionRouted {
		t.Errorf("down: got %+v", got)
	}
}
