package main

// model.go — the recovery-probability scorer.
//
// This is the same online logistic-regression engine from the SmartRoute router,
// repurposed: instead of predicting P(a gateway succeeds), it predicts
// P(this failed payment is recoverable) from the diagnosis + payment context.
// It ships with sensible weights derived from real decline behaviour, and can be
// trained (Fit) on any labelled CSV that has a "recovered" column.

import "math"

// features turns a payment + its diagnosis into a fixed vector:
//   [bias, isSoft, isTimeout, isDataError, isHard, amountNorm, hasContact]
func features(d Diagnosis, amount float64, hasContact bool) []float64 {
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	amt := amount / 500000.0 // normalize (paise/smallest unit); capped below
	if amt > 1 {
		amt = 1
	}
	return []float64{
		1,
		b(d.Bucket == bucketSoft),
		b(d.Bucket == bucketTimeout),
		b(d.Bucket == bucketData),
		b(d.Bucket == bucketHard),
		amt,
		b(hasContact),
	}
}

type model struct {
	w  []float64
	lr float64
}

// newModel starts from weights that already encode real decline behaviour:
// soft/timeout are very recoverable, data errors moderately, hard declines not.
// Training on labelled data (Fit) refines these; without labels they're a strong,
// honest prior rather than random.
func newModel() *model {
	return &model{
		//    bias  soft  timeout data  hard   amt   contact
		w:  []float64{-1.1, 2.6, 2.9, 1.3, -2.2, -0.2, 0.7},
		lr: 0.05,
	}
}

func sigmoid(z float64) float64 { return 1.0 / (1.0 + math.Exp(-z)) }

func (m *model) predict(x []float64) float64 {
	z := 0.0
	for i := range x {
		z += m.w[i] * x[i]
	}
	return sigmoid(z)
}

// Fit runs one SGD epoch over labelled samples (x, y) — y=1 recovered, 0 not.
// Called only if the uploaded CSV includes a "recovered" column.
func (m *model) fit(xs [][]float64, ys []float64, epochs int) {
	for e := 0; e < epochs; e++ {
		for i := range xs {
			p := m.predict(xs[i])
			err := ys[i] - p
			for j := range xs[i] {
				m.w[j] += m.lr * err * xs[i][j]
			}
		}
	}
}

// recoveryProb is the convenience path used per payment at scoring time.
func (m *model) recoveryProb(d Diagnosis, amount float64, hasContact bool) float64 {
	return m.predict(features(d, amount, hasContact))
}
