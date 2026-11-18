package budget

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestThresholdEmpiricalAndTail(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	s := make([]float64, 100_000)
	for i := range s {
		s[i] = rng.ExpFloat64()
	}
	p := NewPool(s)
	c := Default
	// 1,000 scores lie above the 1% quantile: read off the data.
	if q := c.Threshold(p, 0.01); math.Abs(q-math.Log(100)) > 0.15 {
		t.Errorf("empirical 1%% threshold = %.3f, want ~%.3f", q, math.Log(100))
	}
	// 0.1 scores lie above the 1e-6 quantile: extrapolated by the tail fit,
	// whose 30-point mean excess carries roughly 10% error at this distance.
	if q := c.Threshold(p, 1e-6); math.Abs(q/math.Log(1e6)-1) > 0.15 {
		t.Errorf("tail 1e-6 threshold = %.3f, want ~%.3f", q, math.Log(1e6))
	}
	if q := c.Threshold(nil, 0.01); !math.IsInf(q, 1) {
		t.Errorf("empty pool threshold = %v, want +Inf", q)
	}
}

// A fleet of containers with different levels and noise: the pooled
// recommender should exceed in about alpha of the container-hours.
func TestCalibrationOnStationaryFleet(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	const containers, hours = 150, 500
	c := Default
	c.BudgetPerDay = 0.24 // alpha = 1% per hour, enough exceedances to measure
	level := make([]float64, containers)
	noise := make([]float64, containers)
	hist := make([][]Hour, containers)
	for i := range level {
		level[i] = 100 * math.Exp(rng.NormFloat64())
		noise[i] = level[i] * (0.01 + 0.2*rng.Float64())
	}
	peak := func(i int) float64 { return level[i] + noise[i]*rng.NormFloat64() }
	for i := range hist {
		for range c.BaseWindows + c.HistoryWindows {
			hist[i] = append(hist[i], Hour{Peak: peak(i)})
		}
	}
	scores := make([][]float64, containers)
	for i := range hist {
		scores[i] = c.Scores(hist[i])
	}
	var exceed, total int
	for h := 0; h < hours; h++ {
		var all []float64
		for i := range scores {
			all = append(all, scores[i]...)
		}
		pool := NewPool(all)
		for i := range hist {
			r, ok := c.Recommend(hist[i], pool)
			if !ok {
				t.Fatal("no recommendation")
			}
			y := peak(i)
			if y > r.Target {
				exceed++
			}
			total++
			hist[i] = append(hist[i][1:], Hour{Peak: y})
			if s, ok := c.Score(hist[i], len(hist[i])-1); ok {
				scores[i] = append(scores[i][1:], s)
			}
		}
	}
	got := float64(exceed) / float64(total)
	if got < 0.006 || got > 0.016 {
		t.Errorf("exceedance rate %.4f, want ~0.01 (budget %.2f/day)", got, c.BudgetPerDay)
	}
}

func flat(n int, v float64) []Hour {
	h := make([]Hour, n)
	for i := range h {
		h[i] = Hour{Peak: v}
	}
	return h
}

func TestOOMRaisesTarget(t *testing.T) {
	c := Default
	pool := NewPool(append(make([]float64, 40), 0.1, -0.5))
	h := flat(48, 100)
	before, _ := c.Recommend(h, pool)
	h[len(h)-1] = Hour{Peak: 120, OOMLimit: 120}
	after, _ := c.Recommend(h, pool)
	if after.Target < 120*c.OOMBump || after.Target <= before.Target {
		t.Errorf("target after OOM at 120 = %.1f (before %.1f), want >= %.1f", after.Target, before.Target, 120*c.OOMBump)
	}
}

func TestSmallerBudgetLargerTarget(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	var s []float64
	for range 5000 {
		s = append(s, rng.NormFloat64())
	}
	pool := NewPool(s)
	h := flat(200, 100)
	for i := range h {
		h[i].Peak += 5 * rng.NormFloat64()
	}
	prev := 0.0
	for _, b := range []float64{0.1, 0.01, 0.001, 0.0001} {
		c := Default
		c.BudgetPerDay = b
		r, ok := c.Recommend(h, pool)
		if !ok || r.Target <= prev || r.Lower > r.Target || r.Upper < r.Target {
			t.Fatalf("budget %g: %+v (previous target %.2f)", b, r, prev)
		}
		prev = r.Target
	}
}

func TestNoDataNoRecommendation(t *testing.T) {
	h := []Hour{{Peak: math.NaN()}, {Peak: math.NaN()}}
	if _, ok := Default.Recommend(h, NewPool(make([]float64, 100))); ok {
		t.Error("recommendation from an empty history")
	}
	// A container that started 3 windows ago: its base has too little history.
	young := append(make([]Hour, 45), flat(3, 100)...)
	for i := range 45 {
		young[i].Peak = math.NaN()
	}
	if _, ok := Default.Recommend(young, NewPool(make([]float64, 100))); ok {
		t.Error("recommendation from 3 windows of history")
	}
	if s := Default.Scores(append(young, Hour{Peak: 400})); len(s) != 0 {
		t.Errorf("startup windows were scored: %v", s)
	}
	// Too few calibration scores to fit a tail: wait instead of guessing.
	if _, ok := Default.Recommend(flat(48, 100), NewPool(make([]float64, Default.MinTailPoints))); ok {
		t.Error("recommendation from a pool of only MinTailPoints scores")
	}
}
