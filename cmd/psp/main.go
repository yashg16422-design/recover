// Command psp is a mock Payment Service Provider (acquiring bank / gateway).
//
// It stands in for a real gateway like a bank endpoint. Each instance has a
// "personality" set by flags: how often it succeeds, how slow it is, what it
// costs per transaction, and whether it's currently healthy. The router in
// ../router treats these exactly like real gateways it must choose between.
//
// Endpoints:
//   POST /charge          -> attempt a payment. Body: {"amount":.., "idempotency_key":".."}
//   POST /admin/down      -> chaos: force this PSP to fail every request (simulate outage)
//   POST /admin/up        -> chaos: restore normal behavior
//   GET  /healthz         -> 200 if up, 503 if forced down (for the router's health probe)
//   GET  /stats           -> this PSP's own counters (attempts / success / failed)
//
// Everything here is Go standard library only. No external dependencies.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// chargeRequest is what the router sends us for each payment attempt.
type chargeRequest struct {
	Amount         float64 `json:"amount"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// chargeResponse is what we send back. The router reads Status to learn whether
// this attempt worked, and LatencyMS to learn how slow we were.
type chargeResponse struct {
	Status    string  `json:"status"` // "success" or "failed"
	PSP       string  `json:"psp"`
	TxnID     string  `json:"txn_id"`
	LatencyMS int64   `json:"latency_ms"`
	Cost      float64 `json:"cost"`
}

// PSP holds the personality and live state of one mock gateway.
type PSP struct {
	name        string
	successRate float64 // probability a charge succeeds when healthy, 0..1
	meanLatency time.Duration
	cost        float64 // arbitrary cost units charged per successful txn

	down atomic.Bool // chaos switch: when true, every charge fails

	// counters (atomic so the /stats handler can read them without locks)
	attempts atomic.Int64
	success  atomic.Int64
	failed   atomic.Int64

	// idempotency: remember keys we've already charged so a retry of the SAME
	// payment does not charge twice. Real gateways do exactly this.
	mu   sync.Mutex
	seen map[string]chargeResponse
	rng  *rand.Rand
}

func newPSP(name string, sr float64, latency time.Duration, cost float64) *PSP {
	return &PSP{
		name:        name,
		successRate: sr,
		meanLatency: latency,
		cost:        cost,
		seen:        make(map[string]chargeResponse),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// simulateLatency sleeps for roughly meanLatency with +/-40% jitter, so the
// router sees realistic, noisy response times rather than a constant.
func (p *PSP) simulateLatency() time.Duration {
	jitter := 0.6 + p.rng.Float64()*0.8 // 0.6x .. 1.4x
	d := time.Duration(float64(p.meanLatency) * jitter)
	time.Sleep(d)
	return d
}

func (p *PSP) handleCharge(w http.ResponseWriter, r *http.Request) {
	var req chargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Idempotency check FIRST: if we've seen this key, return the stored result
	// without charging again. This is the single most important rule in payments.
	if req.IdempotencyKey != "" {
		p.mu.Lock()
		if prev, ok := p.seen[req.IdempotencyKey]; ok {
			p.mu.Unlock()
			writeJSON(w, http.StatusOK, prev)
			return
		}
		p.mu.Unlock()
	}

	p.attempts.Add(1)

	// A forced-down gateway fails fast (like a refused connection) rather than
	// hanging — so the router can fail over quickly. A healthy one takes its
	// normal (jittered) latency.
	down := p.down.Load()
	var latency time.Duration
	if down {
		latency = 5 * time.Millisecond
		time.Sleep(latency)
	} else {
		latency = p.simulateLatency()
	}

	// Fail when down (chaos) or, when healthy, with probability (1 - successRate).
	ok := !down && p.rng.Float64() < p.successRate

	resp := chargeResponse{
		PSP:       p.name,
		LatencyMS: latency.Milliseconds(),
	}
	if ok {
		p.success.Add(1)
		resp.Status = "success"
		resp.TxnID = p.name + "-" + randID(p.rng)
		resp.Cost = p.cost
	} else {
		p.failed.Add(1)
		resp.Status = "failed"
	}

	// Store successful results under the idempotency key so retries are safe.
	// (We only memoize successes here; a failed attempt is allowed to be retried.)
	if req.IdempotencyKey != "" && ok {
		p.mu.Lock()
		p.seen[req.IdempotencyKey] = resp
		p.mu.Unlock()
	}

	status := http.StatusOK
	if !ok {
		status = http.StatusPaymentRequired // 402: a clean "charge failed" signal
	}
	writeJSON(w, status, resp)
}

func (p *PSP) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if p.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (p *PSP) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"psp":      p.name,
		"attempts": p.attempts.Load(),
		"success":  p.success.Load(),
		"failed":   p.failed.Load(),
		"down":     p.down.Load(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randID(rng *rand.Rand) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rng.Intn(len(chars))]
	}
	return string(b)
}

func main() {
	addr := flag.String("addr", ":9001", "listen address")
	name := flag.String("name", "psp-a", "PSP name")
	sr := flag.Float64("success", 0.95, "success rate 0..1 when healthy")
	latencyMS := flag.Int("latency", 120, "mean latency in milliseconds")
	cost := flag.Float64("cost", 1.0, "cost units per successful txn")
	flag.Parse()

	p := newPSP(*name, *sr, time.Duration(*latencyMS)*time.Millisecond, *cost)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /charge", p.handleCharge)
	mux.HandleFunc("GET /healthz", p.handleHealthz)
	mux.HandleFunc("GET /stats", p.handleStats)
	mux.HandleFunc("POST /admin/down", func(w http.ResponseWriter, r *http.Request) {
		p.down.Store(true)
		log.Printf("[%s] CHAOS: forced DOWN", p.name)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /admin/up", func(w http.ResponseWriter, r *http.Request) {
		p.down.Store(false)
		log.Printf("[%s] restored UP", p.name)
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("PSP %q listening on %s (success=%.2f latency=%dms cost=%.2f)",
		*name, *addr, *sr, *latencyMS, *cost)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
