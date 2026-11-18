// Package budget turns memory history into a memory size that meets an OOM budget.
//
// For every container the recommender predicts the next hour's peak as
//
//	limit = base + scale * q
//
// where base is the container's highest hourly peak over the last BaseWindows
// (7 days), scale is base itself by default (so the margin is a percentage),
// and q is one threshold shared by the whole fleet. q is read from the pooled
// history of "surprises" (next-hour peak minus base, divided by scale) of every
// managed container, so that P(next-hour peak > limit) equals the per-hour
// share of the budget. This is split-conformal prediction with the calibration
// set pooled across containers; when the budget is smaller than the pool can
// resolve, the tail is extrapolated with an exponential fit (peaks over
// threshold, shape 0). Per-container scales (ScaleStep, ScaleSpread) are kept
// for the benchmark's ablations; they lost on the design set.
package budget

import (
	"math"
	"sort"
)

// Hour is one hour of a container's memory history.
type Hour struct {
	Peak     float64 // highest usage seen in the hour, NaN if the container did not run
	OOMLimit float64 // > 0 when the container was OOM-killed at this limit during the hour
}

// Config holds the recommender parameters.
type Config struct {
	BudgetPerDay   float64 // allowed OOM kills per container-day, e.g. 0.001
	WindowsPerDay  float64 // peak windows per day (24 for hourly windows)
	BaseWindows    int     // base = highest peak over this many windows
	MinWindows     int     // observed windows the base needs before it counts
	HistoryWindows int     // windows of history for scale and calibration scores
	OOMBump        float64 // an OOM at limit L counts as a peak of L*OOMBump (VPA uses 1.2)
	MinScaleFrac   float64 // scale is at least this share of base
	MinTailPoints  int     // scores used for the exponential tail fit
	// Scale picks how a container's variability is measured over the history:
	// ScaleStep, the median change between consecutive windows, which one level
	// shift barely moves; ScaleSpread, the 10th-90th percentile spread, which a
	// level shift inflates for the whole history window; or ScaleRelative, the
	// base itself, so the margin is one fleet-wide percentage.
	Scale string
}

// Scale definitions.
const (
	ScaleStep     = "step"
	ScaleSpread   = "spread"
	ScaleRelative = "relative"
)

// Default is the configuration used by the recommender and the benchmark.
var Default = Config{
	BudgetPerDay:   0.001,
	WindowsPerDay:  24,
	BaseWindows:    168,
	MinWindows:     12,
	HistoryWindows: 168,
	OOMBump:        1.2,
	MinScaleFrac:   0.02,
	MinTailPoints:  30,
	Scale:          ScaleRelative,
}

// value is the peak used for modelling: an OOM means the real peak was above
// the limit, so it is replaced by limit*bump, as VPA does.
func (h Hour) value(bump float64) float64 {
	if h.OOMLimit > 0 {
		return math.Max(nanZero(h.Peak), h.OOMLimit*bump)
	}
	return h.Peak
}

func nanZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

// baseScale returns base and scale computed from hours[:end] (end exclusive).
func (c Config) baseScale(hours []Hour, end int) (base, scale float64, ok bool) {
	// The base needs MinWindows observed windows: right after a container
	// starts its memory is still ramping up, and scoring against that partial
	// base would fill the pool with startup surprises that steady-state
	// containers never face.
	base = math.NaN()
	seen := 0
	for i := max(0, end-c.BaseWindows); i < end; i++ {
		if v := hours[i].value(c.OOMBump); !math.IsNaN(v) {
			seen++
			if math.IsNaN(base) || v > base {
				base = v
			}
		}
	}
	if seen < c.MinWindows || base <= 0 {
		return 0, 0, false
	}
	if c.Scale == ScaleRelative {
		return base, base, true
	}
	var vals []float64
	prev := math.NaN()
	for i := max(0, end-c.HistoryWindows); i < end; i++ {
		v := hours[i].value(c.OOMBump)
		switch {
		case math.IsNaN(v):
		case c.Scale == ScaleSpread:
			vals = append(vals, v)
		case !math.IsNaN(prev):
			vals = append(vals, math.Abs(v-prev))
		}
		prev = v
	}
	sort.Float64s(vals)
	s := quantile(vals, 0.5)
	if c.Scale == ScaleSpread {
		s = quantile(vals, 0.9) - quantile(vals, 0.1)
	}
	return base, math.Max(s, c.MinScaleFrac*base), true
}

func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(p*float64(len(sorted)-1))]
}

// Score is the surprise of hour end: (peak - base) / scale, using only hours
// before it for base and scale. ok is false when there is nothing to score.
func (c Config) Score(hours []Hour, end int) (float64, bool) {
	if end >= len(hours) {
		return 0, false
	}
	v := hours[end].value(c.OOMBump)
	base, scale, ok := c.baseScale(hours, end)
	if !ok || math.IsNaN(v) {
		return 0, false
	}
	return (v - base) / scale, true
}

// Scores returns every score of the series inside the history window.
func (c Config) Scores(hours []Hour) []float64 {
	var out []float64
	for end := max(1, len(hours)-c.HistoryWindows); end < len(hours); end++ {
		if s, ok := c.Score(hours, end); ok {
			out = append(out, s)
		}
	}
	return out
}

// Pool is a calibration set of scores sorted in descending order.
type Pool []float64

// NewPool sorts the scores (it takes ownership of the slice).
func NewPool(scores []float64) Pool {
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
	return Pool(scores)
}

// Threshold returns q with P(score > q) = alpha in the pool. It uses the
// empirical quantile while at least MinTailPoints scores lie above it, and an
// exponential tail fitted to the top MinTailPoints scores beyond that.
func (c Config) Threshold(p Pool, alpha float64) float64 {
	n := len(p)
	if n == 0 {
		return math.Inf(1)
	}
	above := alpha * float64(n) // expected number of scores above q
	if above >= float64(c.MinTailPoints) {
		return p[min(int(above), n-1)]
	}
	k := min(c.MinTailPoints, n-1)
	if k < 1 {
		return p[0]
	}
	u := p[k]
	var excess float64
	for _, s := range p[:k] {
		excess += s - u
	}
	return u + excess/float64(k)*math.Log(float64(k)/above)
}

// Rec is a memory recommendation in the units of the input.
type Rec struct {
	Target, Lower, Upper float64
}

// Recommend sizes the container for the window after hours. Lower carries ten
// times the budget's risk and Upper a tenth of it, so a running pod outside
// [Lower, Upper] is either too risky or wasteful enough to resize. There is
// no recommendation until the pool holds more than MinTailPoints scores.
func (c Config) Recommend(hours []Hour, p Pool) (Rec, bool) {
	base, scale, ok := c.baseScale(hours, len(hours))
	if !ok || len(p) <= c.MinTailPoints {
		return Rec{}, false
	}
	alpha := c.BudgetPerDay / c.WindowsPerDay // per-window share of the daily budget
	at := func(a float64) float64 { return math.Max(base+scale*c.Threshold(p, a), 0) }
	return Rec{Target: at(alpha), Lower: at(alpha * 10), Upper: at(alpha / 10)}, true
}
