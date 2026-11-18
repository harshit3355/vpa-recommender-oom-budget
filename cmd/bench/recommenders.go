package main

import (
	"math"
	"time"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/logic"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"

	"github.com/harshit3355/vpa-recommender-oom-budget/internal/budget"
)

// vpa drives the real VPA recommender model: one ContainerState per
// container feeding its AggregateContainerState, exactly as the VPA
// recommender does with metrics-server samples and OOM events.
type vpa struct {
	rec   logic.PodResourceRecommender
	aggs  []*model.AggregateContainerState
	conts []*model.ContainerState
}

func newVPA(n int, percentile, margin, minMemoryMb float64) *vpa {
	cfg := logic.RecommendationConfig{ // VPA 1.8.1 flag defaults except the swept values
		SafetyMarginFraction:       margin,
		PodMinCPUMillicores:        25,
		PodMinMemoryMb:             minMemoryMb,
		TargetCPUPercentile:        0.9,
		LowerBoundCPUPercentile:    0.5,
		UpperBoundCPUPercentile:    0.95,
		ConfidenceIntervalCPU:      24 * time.Hour,
		TargetMemoryPercentile:     percentile,
		LowerBoundMemoryPercentile: 0.5,
		UpperBoundMemoryPercentile: 0.95,
		ConfidenceIntervalMemory:   24 * time.Hour,
	}
	v := &vpa{rec: logic.CreatePodResourceRecommender(cfg)}
	for range n {
		a := model.NewAggregateContainerState()
		v.aggs = append(v.aggs, a)
		v.conts = append(v.conts, model.NewContainerState(model.Resources{}, a))
	}
	return v
}

func (v *vpa) observe(c int, at time.Time, peak, oomLimit float64) {
	v.conts[c].AddSample(&model.ContainerUsageSample{
		MeasureStart: at, Usage: model.MemoryAmountFromBytes(peak), Resource: model.ResourceMemory,
	})
	if oomLimit > 0 { // the limit equals the request in this replay
		_ = v.conts[c].RecordOOM(at, model.MemoryAmountFromBytes(oomLimit))
	}
}

func (v *vpa) limit(c int) float64 {
	r := v.rec.GetRecommendedPodResources(model.ContainerNameToAggregateStateMap{"c": v.aggs[c]})
	if t := model.BytesFromMemoryAmount(r["c"].Target[model.ResourceMemory]); t > 0 {
		return t
	}
	return math.NaN()
}

func (v *vpa) limits(_ time.Time, out []float64) {
	for c := range out {
		out[c] = v.limit(c)
	}
}

// krr is Robusta KRR's "simple" memory strategy: the highest usage over the
// history window plus a buffer and, with OOM-kill data on, at least the
// highest OOM-killed limit plus a second buffer.
type krr struct {
	buf, oomBuf float64
	hist        int
	peaks, ooms [][]float64 // per container, one entry per finished hour
	cur, curOOM []float64   // the unfinished hour
	started     bool
}

func newKRR(n int, buf float64, histHours int, oomBuf float64) *krr {
	return &krr{buf: buf, oomBuf: oomBuf, hist: histHours,
		peaks: make([][]float64, n), ooms: make([][]float64, n), cur: make([]float64, n), curOOM: make([]float64, n)}
}

func (k *krr) observe(c int, _ time.Time, peak, oomLimit float64) {
	k.cur[c] = math.Max(k.cur[c], peak)
	k.curOOM[c] = math.Max(k.curOOM[c], oomLimit)
}

func (k *krr) limit(c int) float64 {
	maxPeak, maxOOM := k.cur[c], k.curOOM[c]
	for i, p := range k.peaks[c] {
		maxPeak = math.Max(maxPeak, p)
		maxOOM = math.Max(maxOOM, k.ooms[c][i])
	}
	if maxPeak <= 0 {
		return math.NaN()
	}
	return math.Max(maxPeak*(1+k.buf), maxOOM*(1+k.oomBuf))
}

func (k *krr) limits(_ time.Time, out []float64) {
	for c := range out {
		if k.started {
			k.peaks[c] = appendWindow(k.peaks[c], k.cur[c], k.hist)
			k.ooms[c] = appendWindow(k.ooms[c], k.curOOM[c], k.hist)
		}
		k.cur[c], k.curOOM[c] = 0, 0
		out[c] = k.limit(c)
	}
	k.started = true
}

func appendWindow(s []float64, v float64, n int) []float64 {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// budgetRec runs this repository's recommender. With pooled set, the
// calibration scores of all containers form one pool, as in the controller;
// otherwise each container is calibrated on its own scores (ablation).
type budgetRec struct {
	cfg    budget.Config
	pooled bool
	hours  [][]budget.Hour
	scores [][]float64
	cur    []budget.Hour // the unfinished hour
	pool   budget.Pool   // fleet pool as of the last hourly update
	start  bool
}

func newBudget(n int, cfg budget.Config, pooled bool) *budgetRec {
	b := &budgetRec{cfg: cfg, pooled: pooled, hours: make([][]budget.Hour, n), scores: make([][]float64, n), cur: make([]budget.Hour, n)}
	for c := range b.cur {
		b.cur[c] = budget.Hour{Peak: math.NaN()}
	}
	return b
}

func (b *budgetRec) observe(c int, _ time.Time, peak, oomLimit float64) {
	h := &b.cur[c]
	if math.IsNaN(h.Peak) || peak > h.Peak {
		h.Peak = peak
	}
	h.OOMLimit = math.Max(h.OOMLimit, oomLimit)
}

func (b *budgetRec) recommend(c int, hours []budget.Hour) float64 {
	p := b.pool
	if !b.pooled {
		p = budget.NewPool(append([]float64(nil), b.scores[c]...))
	}
	if r, ok := b.cfg.Recommend(hours, p); ok {
		return r.Target
	}
	return math.NaN()
}

// limit re-sizes one container mid-hour (after an OOM kill), counting the
// unfinished hour as the newest window, as the controller does.
func (b *budgetRec) limit(c int) float64 {
	return b.recommend(c, append(append([]budget.Hour(nil), b.hours[c]...), b.cur[c]))
}

func (b *budgetRec) limits(_ time.Time, out []float64) {
	keep := b.cfg.BaseWindows + b.cfg.HistoryWindows + 1
	var all []float64
	for c := range out {
		if b.start {
			hs := append(b.hours[c], b.cur[c])
			if len(hs) > keep {
				hs = hs[len(hs)-keep:]
			}
			b.hours[c] = hs
			if s, ok := b.cfg.Score(hs, len(hs)-1); ok {
				b.scores[c] = appendWindow(b.scores[c], s, b.cfg.HistoryWindows)
			}
		}
		b.cur[c] = budget.Hour{Peak: math.NaN()}
		all = append(all, b.scores[c]...)
	}
	b.start = true
	b.pool = budget.NewPool(all)
	for c := range out {
		out[c] = b.recommend(c, b.hours[c])
	}
}
