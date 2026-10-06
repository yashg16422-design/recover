package main

// router.go — the SmartRoute payment router, ported into Recover.
//
// Logic is the same as SmartRoute's cmd/router (EWMA health per gateway, a
// circuit breaker per gateway, failover across gateways under ONE idempotency
// key, adaptive vs static policy). What changed: it runs inside this server,
// exposes POST /api/route, and consults the graph fraud service first.
//
// Gateways are the mock PSPs in cmd/psp (simulated; not real acquirers).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- tunables (same defaults as SmartRoute) ----------
var (
	ewmaAlpha     = 0.25                   // weight of the newest sample in the EWMA
	wSuccess      = 1.0                    // score weight: prefer reliable gateways
	wLatency      = 0.3                    // score weight: penalize slow gateways
	wCost         = 0.2                    // score weight: penalize expensive gateways
	breakerFails  = 4                      // consecutive failures before a breaker opens
	breakerCooldn = 3 * time.Second        // wait before a half-open probe
	retryBudget   = 3                      // adaptive: max gateways tried per payment
	staticBudget  = 2                      // static baseline: fixed primary + ONE backup
	perTryTimeout = 800 * time.Millisecond // a gateway slower than this counts as failed
	windowSize    = 50                     // sliding window for "recent success rate"
)

type Backend struct {
	Name string
	URL  string
}

type breakerState int

const (
	brClosed   breakerState = iota // normal: send traffic
	brOpen                         // tripped: skip until cooldown elapses
	brHalfOpen                     // cooldown elapsed: allow ONE trial
)

func (b breakerState) String() string {
	switch b {
	case brClosed:
		return "closed"
	case brOpen:
		return "open"
	default:
		return "half_open"
	}
}

// Health is the router's learned view of one gateway. Guarded by mu because many
// payments update it concurrently.
type Health struct {
	mu sync.Mutex

	ewmaSuccess float64 // 0..1, exponentially-weighted recent success rate
	ewmaLatency float64 // ms
	avgCost     float64

	consecFail int
	breaker    breakerState
	openedAt   time.Time

	attempts int64
	success  int64
}

func newHealth() *Health {
	// start optimistic so a new gateway gets a fair first look
	return &Health{ewmaSuccess: 1.0, ewmaLatency: 100, avgCost: 1.0, breaker: brClosed}
}

// allowed reports whether the breaker permits traffic, moving open->half_open
// once the cooldown has passed.
func (h *Health) allowed(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.breaker == brOpen {
		if now.Sub(h.openedAt) >= breakerCooldn {
			h.breaker = brHalfOpen
			return true
		}
		return false
	}
	return true
}

// record folds one attempt into the learned health and drives the breaker.
func (h *Health) record(ok bool, latencyMS, cost float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts++
	outcome := 0.0
	if ok {
		outcome = 1.0
		h.success++
		h.consecFail = 0
		h.breaker = brClosed
		if cost > 0 {
			h.avgCost = ewmaAlpha*cost + (1-ewmaAlpha)*h.avgCost
		}
		h.ewmaLatency = ewmaAlpha*latencyMS + (1-ewmaAlpha)*h.ewmaLatency
	} else {
		h.consecFail++
		if h.consecFail >= breakerFails {
			if h.breaker != brOpen {
				h.openedAt = time.Now()
			}
			h.breaker = brOpen
		}
	}
	h.ewmaSuccess = ewmaAlpha*outcome + (1-ewmaAlpha)*h.ewmaSuccess
}

func (h *Health) snapshot() (sr, lat, cost float64, br string, att, suc int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ewmaSuccess, h.ewmaLatency, h.avgCost, h.breaker.String(), h.attempts, h.success
}

// outcomeWindow is a fixed-size ring of recent outcomes ("recent success rate").
type outcomeWindow struct {
	mu   sync.Mutex
	buf  []bool
	pos  int
	full bool
}

func newOutcomeWindow(n int) *outcomeWindow { return &outcomeWindow{buf: make([]bool, n)} }

func (w *outcomeWindow) add(ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf[w.pos] = ok
	w.pos = (w.pos + 1) % len(w.buf)
	if w.pos == 0 {
		w.full = true
	}
}

func (w *outcomeWindow) rate() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.buf)
	if !w.full {
		n = w.pos
	}
	if n == 0 {
		return 1.0
	}
	c := 0
	for i := 0; i < n; i++ {
		if w.buf[i] {
			c++
		}
	}
	return float64(c) / float64(n)
}

// Router ties it together.
type Router struct {
	backends []Backend
	health   map[string]*Health
	client   *http.Client

	winAdaptive *outcomeWindow
	winStatic   *outcomeWindow

	mu            sync.Mutex
	adaptiveTotal int64
	adaptiveOK    int64
	staticTotal   int64
	staticOK      int64
	routeCount    map[string]int64
}

func NewRouter(backends []Backend) *Router {
	h := make(map[string]*Health, len(backends))
	for _, b := range backends {
		h[b.Name] = newHealth()
	}
	return &Router{
		backends:    backends,
		health:      h,
		client:      &http.Client{Timeout: perTryTimeout},
		winAdaptive: newOutcomeWindow(windowSize),
		winStatic:   newOutcomeWindow(windowSize),
		routeCount:  make(map[string]int64),
	}
}

// scoreOrder is the adaptive try-order, best first:
// score = wSuccess*successRate - wLatency*normLatency - wCost*normCost, over the
// gateways whose breaker allows traffic (all of them if every breaker is open).
func (r *Router) scoreOrder(now time.Time) []Backend {
	type scored struct {
		b     Backend
		score float64
	}
	var avail []Backend
	for _, b := range r.backends {
		if r.health[b.Name].allowed(now) {
			avail = append(avail, b)
		}
	}
	if len(avail) == 0 { // every breaker open -> probe everyone rather than black out
		avail = append(avail, r.backends...)
	}
	maxLat, maxCost := 1.0, 1.0
	for _, b := range avail {
		_, lat, cost, _, _, _ := r.health[b.Name].snapshot()
		if lat > maxLat {
			maxLat = lat
		}
		if cost > maxCost {
			maxCost = cost
		}
	}
	out := make([]scored, 0, len(avail))
	for _, b := range avail {
		sr, lat, cost, _, _, _ := r.health[b.Name].snapshot()
		out = append(out, scored{b, wSuccess*sr - wLatency*(lat/maxLat) - wCost*(cost/maxCost)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	res := make([]Backend, len(out))
	for i := range out {
		res[i] = out[i].b
	}
	return res
}

func (r *Router) tryOrder(policy string, now time.Time) []Backend {
	if policy == "static" {
		return r.backends // fixed order, no learning
	}
	return r.scoreOrder(now)
}

type payRequest struct {
	Amount         float64 `json:"amount"`
	IdempotencyKey string  `json:"idempotency_key"`
	FraudNodeID    *int    `json:"fraud_node_id,omitempty"` // optional: which graph node to risk-score

	// optional customer context so a failed payment can be handed to the recovery agent
	CustomerName  string `json:"customer_name,omitempty"`
	CustomerEmail string `json:"customer_email,omitempty"`
	Currency      string `json:"currency,omitempty"`
	DemoRecipient string `json:"demo_recipient,omitempty"` // send recovery messages here instead (demo mode)
}

type payResult struct {
	Status   string   `json:"status"` // "success" | "failed"
	PSP      string   `json:"psp,omitempty"`
	Attempts int      `json:"attempts"`
	Tried    []string `json:"tried"`
	Policy   string   `json:"policy"`
}

// chargeOne sends the payment to one gateway. A timeout or transport error counts
// as failure: a hung gateway must not block the customer.
func (r *Router) chargeOne(b Backend, body []byte) (ok bool, latMS float64, cost float64) {
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, b.URL+"/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	latMS = float64(time.Since(start).Milliseconds())
	if err != nil {
		return false, latMS, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, latMS, 0
	}
	var cr struct {
		Status string  `json:"status"`
		Cost   float64 `json:"cost"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return false, latMS, 0
	}
	return cr.Status == "success", latMS, cr.Cost
}

// route runs decision + failover for one payment. The SAME idempotency key is
// reused for every attempt, so failover can never double-charge.
func (r *Router) route(policy string, pr payRequest) payResult {
	if pr.IdempotencyKey == "" {
		pr.IdempotencyKey = fmt.Sprintf("idem-%d-%d", time.Now().UnixNano(), rand.Int63())
	}
	body, _ := json.Marshal(map[string]any{"amount": pr.Amount, "idempotency_key": pr.IdempotencyKey})

	order := r.tryOrder(policy, time.Now())
	budget := retryBudget
	if policy == "static" {
		budget = staticBudget
	}
	res := payResult{Status: "failed", Policy: policy}
	for i, b := range order {
		if i >= budget {
			break
		}
		res.Attempts++
		res.Tried = append(res.Tried, b.Name)
		ok, lat, cost := r.chargeOne(b, body)
		if policy == "adaptive" { // static stays dumb on purpose
			r.health[b.Name].record(ok, lat, cost)
		}
		if ok {
			res.Status = "success"
			res.PSP = b.Name
			if policy == "adaptive" {
				r.mu.Lock()
				r.routeCount[b.Name]++
				r.mu.Unlock()
			}
			break
		}
	}

	success := res.Status == "success"
	r.mu.Lock()
	if policy == "adaptive" {
		r.adaptiveTotal++
		if success {
			r.adaptiveOK++
		}
	} else {
		r.staticTotal++
		if success {
			r.staticOK++
		}
	}
	r.mu.Unlock()
	if policy == "adaptive" {
		r.winAdaptive.add(success)
	} else {
		r.winStatic.add(success)
	}
	return res
}

// routeResponse = the router's result + the fraud decision (additive fields).
type routeResponse struct {
	payResult
	FraudCheck     string   `json:"fraud_check"`                // "ok" | "unavailable" | "skipped"
	FraudDecision  string   `json:"fraud_decision"`             // ROUTED | REVIEW | BLOCKED
	FraudRisk      *float64 `json:"fraud_risk,omitempty"`       // 0..1, graph model's P(illicit)
	FraudLatencyMS *int64   `json:"fraud_latency_ms,omitempty"` // round trip to the fraud service
	Recovery       *action  `json:"recovery,omitempty"`         // what the Recover agent did about a failed/blocked payment
}

func (r *Router) handleRoute(fc *fraudClient) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		policy := req.URL.Query().Get("policy")
		if policy != "static" {
			policy = "adaptive"
		}
		var pr payRequest
		if err := json.NewDecoder(req.Body).Decode(&pr); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if pr.IdempotencyKey == "" { // also used as the charge id if the recovery agent is called
			pr.IdempotencyKey = fmt.Sprintf("idem-%d-%d", time.Now().UnixNano(), rand.Int63())
		}
		out := routeResponse{FraudCheck: "skipped", FraudDecision: decisionRouted}
		if fc != nil && pr.FraudNodeID != nil {
			fr := fc.check(*pr.FraudNodeID)
			out.FraudCheck, out.FraudDecision = fr.check, fr.decision
			if fr.check == "ok" {
				out.FraudRisk = &fr.risk
			}
			ms := fr.latency.Milliseconds()
			out.FraudLatencyMS = &ms
			log.Printf("fraud node=%d check=%s risk=%.3f decision=%s latency=%dms",
				*pr.FraudNodeID, fr.check, fr.risk, fr.decision, ms)
		}

		status := http.StatusOK
		if out.FraudDecision == decisionBlocked {
			// blocked before any gateway is touched
			out.payResult = payResult{Status: "blocked", Tried: []string{}, Policy: policy}
			status = http.StatusForbidden
			out.Recovery = recoverFromRouter(pr, true) // quarantined, never messaged
		} else {
			out.payResult = r.route(policy, pr)
			if out.Status != "success" {
				status = http.StatusBadGateway
				out.Recovery = recoverFromRouter(pr, false) // the Recover agent takes over
			} else {
				recordSuccesses(1)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	}
}

// handleChaos takes mock gateways offline/online so failover and recovery can be demoed:
// POST /api/route/chaos?psp=psp-a|all&down=true|false (forwards to the PSP's admin API).
func (r *Router) handleChaos(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get("psp")
	action := "up"
	if req.URL.Query().Get("down") == "true" {
		action = "down"
	}
	hit := 0
	for _, b := range r.backends {
		if name != "all" && b.Name != name {
			continue
		}
		hit++
		hr, _ := http.NewRequest(http.MethodPost, b.URL+"/admin/"+action, nil)
		if resp, err := r.client.Do(hr); err == nil {
			resp.Body.Close()
		}
	}
	if hit == 0 {
		http.Error(w, "unknown psp", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"psp": name, "action": action, "gateways": hit})
}

func (r *Router) handleStats(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	adT, adOK, stT, stOK := r.adaptiveTotal, r.adaptiveOK, r.staticTotal, r.staticOK
	rc := make(map[string]int64, len(r.routeCount))
	for k, v := range r.routeCount {
		rc[k] = v
	}
	r.mu.Unlock()

	psps := make([]map[string]any, 0, len(r.backends))
	for _, b := range r.backends {
		sr, lat, cost, br, att, suc := r.health[b.Name].snapshot()
		psps = append(psps, map[string]any{
			"name": b.Name, "ewma_success": roundTo(sr, 3), "ewma_latency": roundTo(lat, 1),
			"avg_cost": roundTo(cost, 3), "breaker": br, "attempts": att, "success": suc, "routed": rc[b.Name],
		})
	}
	writeJSON(w, map[string]any{
		"adaptive": map[string]any{"total": adT, "success": adOK, "cum_rate": safeRate(adOK, adT), "recent_rate": roundTo(r.winAdaptive.rate(), 3)},
		"static":   map[string]any{"total": stT, "success": stOK, "cum_rate": safeRate(stOK, stT), "recent_rate": roundTo(r.winStatic.rate(), 3)},
		"psps":     psps,
	})
}

func safeRate(ok, total int64) float64 {
	if total == 0 {
		return 1.0
	}
	return roundTo(float64(ok)/float64(total), 3)
}

func roundTo(f float64, d int) float64 {
	p := 1.0
	for i := 0; i < d; i++ {
		p *= 10
	}
	return float64(int(f*p+0.5)) / p
}

// parseBackends turns "psp-a@http://host:9001,psp-b@http://host:9002" into Backends.
func parseBackends(s string) []Backend {
	var out []Backend
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		nv := strings.SplitN(part, "@", 2)
		if len(nv) != 2 {
			log.Fatalf("bad PSPS entry %q (want name@url)", part)
		}
		out = append(out, Backend{Name: nv[0], URL: nv[1]})
	}
	return out
}

// registerRouting wires the routing + fraud endpoints into Recover's mux.
// Gateways come from $PSPS (default: the three mock PSPs on :9001-:9003).
func registerRouting(mux *http.ServeMux) {
	backends := parseBackends(getenv("PSPS",
		"psp-a@http://localhost:9001,psp-b@http://localhost:9002,psp-c@http://localhost:9003"))
	rt := NewRouter(backends)
	fc := newFraudClientFromEnv()

	mux.HandleFunc("POST /api/route", rt.handleRoute(fc))
	mux.HandleFunc("GET /api/route/stats", rt.handleStats)
	mux.HandleFunc("POST /api/route/chaos", rt.handleChaos)
	mux.HandleFunc("GET /api/route/config", func(w http.ResponseWriter, _ *http.Request) {
		if fc == nil {
			writeJSON(w, map[string]any{"fraud_enabled": false})
			return
		}
		writeJSON(w, map[string]any{"fraud_enabled": true, "block_at": fc.blockAt, "review_at": fc.reviewAt, "fail_mode": "open"})
	})

	// Read-only proxy so the browser UI can reach the fraud service without CORS.
	mux.HandleFunc("GET /api/fraud/", func(w http.ResponseWriter, req *http.Request) {
		if fc == nil {
			http.Error(w, `{"error":"FRAUD_URL not set"}`, http.StatusServiceUnavailable)
			return
		}
		target, _ := url.Parse(fc.base)
		p := httputil.NewSingleHostReverseProxy(target)
		p.Transport = &http.Transport{ResponseHeaderTimeout: 5 * time.Second}
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api/fraud")
		p.ServeHTTP(w, req)
	})
	if fc != nil {
		log.Printf("routing: %d gateways, fraud service %s (block>=%.2f review>=%.2f, fail-open)", len(backends), fc.base, fc.blockAt, fc.reviewAt)
	} else {
		log.Printf("routing: %d gateways, fraud check disabled (set FRAUD_URL to enable)", len(backends))
	}
}
