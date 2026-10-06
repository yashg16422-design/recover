package main

// metrics.go — live session metrics powering the landing dashboard.
//
// These are REAL counters of what the agent has actually done this session
// (payments seen, revenue recovered, anomalies caught, messages sent). They rise
// as CSVs are analyzed, the plan is run, or live Stripe events arrive — no fake
// numbers. A short time-series drives the activity sparkline.

import (
	"net/http"
	"sync"
	"time"
)

type mPoint struct {
	T        string `json:"t"`
	Failures int    `json:"failures"`
	Threats  int    `json:"threats"`
	Sent     int    `json:"sent"`
}

var (
	mMu          sync.Mutex
	mFailures    int
	mSuccesses   int
	mThreats     int
	mQuarantined int
	mSent        int
	mRecovered   float64
	mSeries      = []mPoint{}
)

func snapshotLocked() {
	mSeries = append(mSeries, mPoint{time.Now().Format("15:04:05"), mFailures, mThreats, mSent})
	if len(mSeries) > 120 {
		mSeries = mSeries[len(mSeries)-120:]
	}
}

func recordFailures(n int)      { mMu.Lock(); mFailures += n; snapshotLocked(); mMu.Unlock() }
func recordSuccesses(n int)     { mMu.Lock(); mSuccesses += n; snapshotLocked(); mMu.Unlock() }
func recordThreats(t, q int)    { mMu.Lock(); mThreats += t; mQuarantined += q; snapshotLocked(); mMu.Unlock() }
func recordSent(n int, r float64) { mMu.Lock(); mSent += n; mRecovered += r; snapshotLocked(); mMu.Unlock() }

func handleOverview(w http.ResponseWriter, _ *http.Request) {
	mMu.Lock()
	defer mMu.Unlock()
	writeJSON(w, map[string]any{
		"payments_seen": mFailures + mSuccesses,
		"failures":      mFailures,
		"successes":     mSuccesses,
		"threats":       mThreats,
		"quarantined":   mQuarantined,
		"sent":          mSent,
		"recovered":     round2(mRecovered),
		"series":        mSeries,
	})
}
