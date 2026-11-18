// Command bench replays the Google 2011 memory trace (data/google2011-mem.bin.gz,
// built by cmd/tracegen) through three memory recommenders and reports OOM kills
// against memory allocated:
//
//   - vpa:    the real Vertical Pod Autoscaler 1.8.1 recommender code
//     (k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender), defaults
//     and a sweep of its target percentile and safety margin;
//   - krr:    Robusta KRR's "simple" strategy (max over 14 days plus a
//     buffer, OOM-kill data on), reimplemented;
//   - budget: this repository's recommender at several OOM budgets.
//
// Every recommender sets each container's memory limit once an hour. The
// replay then walks the 5-minute peaks: a peak above the limit is an OOM kill,
// and the recommender only ever sees usage capped at its own limit plus the
// OOM event, as it would in a cluster. The first warm-up days are observed
// without limits and not scored.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/harshit3355/vpa-recommender-oom-budget/internal/budget"
)

const windowsPerHour = 12

var epoch = time.Date(2011, 5, 1, 0, 0, 0, 0, time.UTC) // arbitrary; only differences matter

type dataset struct {
	ids     []string
	usage   [][]float32 // bytes per 5-minute window, NaN = not running
	windows int
}

func load(path string, bytesPerUnit float64) (*dataset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(zr)
	magic := make([]byte, 6)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "VPRB1\n" {
		return nil, fmt.Errorf("%s: not a VPRB1 file", path)
	}
	le := binary.LittleEndian
	var nTasks, nWin uint32
	binary.Read(r, le, &nTasks)
	binary.Read(r, le, &nWin)
	d := &dataset{windows: int(nWin)}
	q := make([]uint16, nWin)
	for i := uint32(0); i < nTasks; i++ {
		var job uint64
		var task uint32
		var scale float32
		binary.Read(r, le, &job)
		binary.Read(r, le, &task)
		binary.Read(r, le, &scale)
		if err := binary.Read(r, le, q); err != nil {
			return nil, fmt.Errorf("%s: task %d: %w", path, i, err)
		}
		u := make([]float32, nWin)
		// Zero readings count as missing: the trace reports 0 where the peak
		// was not measured, a running container's working set is never 0,
		// and VPA's model ignores every later peak of a 24-hour window that
		// opens with a 0 sample (ContainerState.addMemorySample in 1.8.1).
		seen := 0
		for w, v := range q {
			if v == math.MaxUint16 || v == 0 {
				u[w] = float32(math.NaN())
			} else {
				u[w] = float32(float64(v) / 65534 * float64(scale) * bytesPerUnit)
				seen++
			}
		}
		if float64(seen) < 0.9*float64(nWin) { // the tracegen coverage rule, on real readings
			continue
		}
		d.ids = append(d.ids, fmt.Sprintf("%d/%d", job, task))
		d.usage = append(d.usage, u)
	}
	return d, nil
}

type recommender interface {
	// observe feeds one 5-minute window: the peak the recommender can see
	// and, when the container was OOM-killed, the limit it was killed at.
	observe(c int, at time.Time, peak, oomLimit float64)
	// limits is called at the start of every hour and fills one limit per
	// container (NaN when it has no recommendation yet).
	limits(at time.Time, out []float64)
	// limit re-sizes one container right after an OOM kill, as the VPA
	// updater evicting the killed pod and the admission controller applying
	// the fresh recommendation would.
	limit(c int) float64
}

type variant struct {
	Name    string             `json:"name"`
	Family  string             `json:"family"`
	Default bool               `json:"default,omitempty"`
	Params  map[string]float64 `json:"params"`
	make    func(n int) recommender
}

// Result is one recommender's score on one split.
type Result struct {
	variant
	Containers     int     `json:"containers"`
	ContainerDays  float64 `json:"container_days"`
	OOMKills       int     `json:"oom_kills"`
	OOMPer1000     float64 `json:"oom_kills_per_1000_container_days"`
	OOMDaysPer1000 float64 `json:"container_days_with_oom_per_1000"`
	AllocOverPeak  float64 `json:"allocated_over_peak_usage"`
	ResizesPerDay  float64 `json:"resizes_per_container_day"`
	UnsizedWindows int     `json:"unsized_windows"`
	AllocGiBHours  float64 `json:"allocated_gib_hours"`
	PeakGiBHours   float64 `json:"peak_usage_gib_hours"`
}

func run(d *dataset, idx []int, v variant, warmupDays int) Result {
	n := len(idx)
	r := v.make(n)
	hours := d.windows / windowsPerHour
	warmH := warmupDays * 24
	lim := make([]float64, n)
	prev := make([]float64, n)
	oomDay := map[[2]int]bool{}
	res := Result{variant: v, Containers: n}
	var alloc, used float64
	var evalWindows, resizes int
	for h := 0; h < hours; h++ {
		at := epoch.Add(time.Duration(h) * time.Hour)
		copy(prev, lim)
		r.limits(at, lim)
		eval := h >= warmH
		if !eval {
			for c := range lim {
				lim[c] = math.Inf(1)
			}
		} else if h > warmH {
			for c := range lim {
				if prev[c] > 0 && math.Abs(lim[c]-prev[c]) > 0.05*prev[c] {
					resizes++
				}
			}
		}
		for w := 0; w < windowsPerHour; w++ {
			t := at.Add(time.Duration(w) * 5 * time.Minute)
			win := h*windowsPerHour + w
			for c, task := range idx {
				y := float64(d.usage[task][win])
				if math.IsNaN(y) {
					continue
				}
				l := lim[c]
				if math.IsNaN(l) {
					if eval {
						res.UnsizedWindows++
					}
					l = math.Inf(1)
				}
				if y > l {
					r.observe(c, t, l, l)
					if eval {
						res.OOMKills++
						oomDay[[2]int{c, win / 288}] = true
						if nl := r.limit(c); !math.IsNaN(nl) {
							lim[c] = nl // takes effect from the next window
						}
					}
				} else {
					r.observe(c, t, y, 0)
				}
				if eval {
					evalWindows++
					used += y
					if !math.IsInf(l, 1) {
						alloc += l
					}
				}
			}
		}
	}
	gibHour := float64(1<<30) * windowsPerHour
	res.ContainerDays = float64(evalWindows) / 288
	res.OOMPer1000 = float64(res.OOMKills) / res.ContainerDays * 1000
	res.OOMDaysPer1000 = float64(len(oomDay)) / res.ContainerDays * 1000
	res.AllocOverPeak = alloc / used
	res.ResizesPerDay = float64(resizes) / res.ContainerDays
	res.AllocGiBHours = alloc / gibHour
	res.PeakGiBHours = used / gibHour
	return res
}

func variants() []variant {
	var vs []variant
	for _, pct := range []float64{0.90, 0.95, 0.99} {
		for _, margin := range []float64{0.15, 0.30, 0.50, 1.00} {
			vs = append(vs, variant{
				Name: fmt.Sprintf("vpa p%.0f margin %.0f%%", pct*100, margin*100), Family: "vpa",
				Default: pct == 0.90 && margin == 0.15,
				Params:  map[string]float64{"target_memory_percentile": pct, "safety_margin_fraction": margin},
				make:    func(n int) recommender { return newVPA(n, pct, margin, 0) },
			})
		}
	}
	// The sweep turns VPA's 250 MiB per-pod floor off so that it compares
	// algorithms, not floors; this row shows VPA exactly as shipped.
	vs = append(vs, variant{
		Name: "vpa as shipped (250 MiB floor)", Family: "vpa-floor", Default: true,
		Params: map[string]float64{"target_memory_percentile": 0.9, "safety_margin_fraction": 0.15, "pod_min_memory_mb": 250},
		make:   func(n int) recommender { return newVPA(n, 0.9, 0.15, 250) },
	})
	for _, buf := range []float64{0.15, 0.30, 0.50, 1.00} {
		vs = append(vs, variant{
			Name: fmt.Sprintf("krr simple buffer %.0f%%", buf*100), Family: "krr",
			Default: buf == 0.15,
			Params:  map[string]float64{"memory_buffer_fraction": buf, "history_hours": 336, "oom_buffer_fraction": 0.25},
			make:    func(n int) recommender { return newKRR(n, buf, 336, 0.25) },
		})
	}
	for _, b := range []float64{0.1, 0.03, 0.01, 0.003, 0.001} {
		cfg := budget.Default
		cfg.BudgetPerDay = b
		vs = append(vs, variant{
			Name: fmt.Sprintf("budget %g/day", b), Family: "budget",
			Default: b == budget.Default.BudgetPerDay,
			Params:  map[string]float64{"budget_oom_per_container_day": b},
			make:    func(n int) recommender { return newBudget(n, cfg, true) },
		})
	}
	// Ablations at two budgets: the designs that lost on the design set, and
	// calibration per container instead of on the fleet's pool.
	ablations := []struct {
		name   string
		pooled bool
		set    func(*budget.Config)
	}{
		{"base 24h", true, func(c *budget.Config) { c.BaseWindows = 24 }},
		{"base 7 days (the design of held-out run 1)", true, func(c *budget.Config) { c.BaseWindows = 168 }},
		{"step scale", true, func(c *budget.Config) { c.Scale = budget.ScaleStep }},
		{"spread scale", true, func(c *budget.Config) { c.Scale = budget.ScaleSpread }},
		{"per-container calibration", false, func(*budget.Config) {}},
	}
	for _, a := range ablations {
		for _, b := range []float64{0.01, 0.001} {
			cfg := budget.Default
			cfg.BudgetPerDay = b
			a.set(&cfg)
			vs = append(vs, variant{
				Name: fmt.Sprintf("budget %g/day, %s", b, a.name), Family: "ablation",
				Params: map[string]float64{"budget_oom_per_container_day": b},
				make:   func(n int) recommender { return newBudget(n, cfg, a.pooled) },
			})
		}
	}
	return vs
}

// gridVariants is the design search, run on the design set only: the base
// window and scale definition of the budget recommender, next to the
// baselines' shipped defaults and KRR's buffers.
func gridVariants() []variant {
	var vs []variant
	for _, v := range variants() {
		if v.Family == "krr" || (v.Default && v.Family != "budget") {
			vs = append(vs, v)
		}
	}
	for _, baseW := range []int{24, 168, 336} {
		for _, scale := range []string{budget.ScaleStep, budget.ScaleSpread, budget.ScaleRelative} {
			if baseW == 336 && scale != budget.ScaleRelative {
				continue // added after the first grid: KRR's 14-day window, best scale only
			}
			for _, b := range []float64{0.03, 0.01, 0.003, 0.001} {
				cfg := budget.Default
				cfg.BudgetPerDay, cfg.BaseWindows, cfg.Scale = b, baseW, scale
				vs = append(vs, variant{
					Name: fmt.Sprintf("budget %g/day, base %dh, %s scale", b, baseW, scale), Family: "grid",
					Params: map[string]float64{"budget_oom_per_container_day": b, "base_windows": float64(baseW)},
					make:   func(n int) recommender { return newBudget(n, cfg, true) },
				})
			}
		}
	}
	return vs
}

func main() {
	data := flag.String("data", "data/google2011-mem-test.bin.gz", "dataset from cmd/tracegen")
	set := flag.String("set", "held-out test set", "name of the dataset in the report")
	gib := flag.Float64("gib-per-unit", 64, "GiB that one normalized trace unit (the largest machine) stands for")
	warm := flag.Int("warmup-days", 7, "unscored days at the start")
	out := flag.String("out", "reports", "report directory")
	grid := flag.Bool("grid", false, "run the design grid instead of the report variants (design set only)")
	flag.Parse()

	d, err := load(*data, *gib*float64(1<<30))
	if err != nil {
		log.Fatal(err)
	}
	report := map[string][]Result{}
	for _, s := range []string{*set} {
		idx := make([]int, len(d.ids))
		for i := range idx {
			idx[i] = i
		}
		vs := variants()
		if *grid {
			vs = gridVariants()
		}
		results := make([]Result, len(vs))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i, v := range vs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i] = run(d, idx, v, *warm)
				log.Printf("%s %-28s oom/1000d=%7.2f alloc/peak=%.3f", s, v.Name, results[i].OOMPer1000, results[i].AllocOverPeak)
			}()
		}
		wg.Wait()
		report[s] = results
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	js, _ := json.MarshalIndent(map[string]any{
		"dataset": filepath.Base(*data), "gib_per_unit": *gib, "warmup_days": *warm,
		"windows": d.windows, "results": report,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "benchmark.json"), append(js, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "benchmark.md"), []byte(markdown(report)), 0o644); err != nil {
		log.Fatal(err)
	}
}

// frontier returns the results of a family that no other result of the
// family beats on both OOM rate and memory, by increasing OOM rate.
func frontier(rs []Result, family string) []Result {
	var f []Result
	for _, r := range rs {
		if r.Family == family && r.OOMPer1000 > 0 {
			f = append(f, r)
		}
	}
	sort.Slice(f, func(i, j int) bool { return f[i].OOMPer1000 < f[j].OOMPer1000 })
	var out []Result
	for _, r := range f {
		if len(out) == 0 || r.AllocOverPeak < out[len(out)-1].AllocOverPeak {
			out = append(out, r)
		}
	}
	return out
}

// at returns the frontier's memory (allocated/peak) at an OOM rate, linear in
// log(OOM rate) between neighbouring settings; ok is false outside the range
// the family's settings cover, where any number would be an extrapolation.
func at(f []Result, oom float64) (float64, bool) {
	for i := 0; i+1 < len(f); i++ {
		lo, hi := f[i], f[i+1]
		if oom >= lo.OOMPer1000 && oom <= hi.OOMPer1000 {
			t := math.Log(oom/lo.OOMPer1000) / math.Log(hi.OOMPer1000/lo.OOMPer1000)
			return lo.AllocOverPeak + t*(hi.AllocOverPeak-lo.AllocOverPeak), true
		}
	}
	return 0, false
}

// matched compares memory at the same OOM rate, read off each family's own
// sweep, and the budget asked for against the OOM rate that came out.
func matched(b *strings.Builder, rs []Result) {
	vs := func(mem float64, f []Result, oom float64) string {
		theirs, ok := at(f, oom)
		if !ok {
			return "outside its swept range"
		}
		return fmt.Sprintf("%+.1f%%", (mem/theirs-1)*100)
	}
	vpaF, krrF, ours := frontier(rs, "vpa"), frontier(rs, "krr"), frontier(rs, "budget")
	b.WriteString("Memory at the same OOM rate (the baseline's sweep, interpolated in log OOM rate between its two nearest settings; negative means the budget recommender needs less):\n\n| budget setting | OOM kills per 1,000 container-days | vs VPA (floor off, best of 12 settings) | vs KRR (best of 4 buffers) |\n|---|---:|---:|---:|\n")
	for _, r := range rs {
		if r.Family == "budget" {
			fmt.Fprintf(b, "| %s | %.2f | %s | %s |\n", r.Name, r.OOMPer1000, vs(r.AllocOverPeak, vpaF, r.OOMPer1000), vs(r.AllocOverPeak, krrF, r.OOMPer1000))
		}
	}
	b.WriteString("\n| shipped default | OOM kills per 1,000 container-days | memory / peak | budget recommender at the same OOM rate |\n|---|---:|---:|---:|\n")
	for _, r := range rs {
		if !r.Default || r.Family == "budget" {
			continue
		}
		cell := "outside its swept range"
		if m, ok := at(ours, r.OOMPer1000); ok {
			cell = fmt.Sprintf("%.3f (%+.1f%%)", m, (m/r.AllocOverPeak-1)*100)
		}
		fmt.Fprintf(b, "| %s | %.2f | %.3f | %s |\n", r.Name, r.OOMPer1000, r.AllocOverPeak, cell)
	}
	b.WriteString("\nCalibration (budget asked for vs OOM kills that happened):\n\n| budget, OOM kills per container-day | realized |\n|---:|---:|\n")
	for _, r := range rs {
		if r.Family == "budget" {
			fmt.Fprintf(b, "| %g | %.4f |\n", r.Params["budget_oom_per_container_day"], r.OOMPer1000/1000)
		}
	}
	b.WriteString("\n")
}

func markdown(report map[string][]Result) string {
	var b strings.Builder
	names := make([]string, 0, len(report))
	for s := range report {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		rs := report[s]
		fmt.Fprintf(&b, "## %s: %d containers, %.0f scored container-days\n\n", s, rs[0].Containers, rs[0].ContainerDays)
		b.WriteString("| recommender | OOM kills per 1,000 container-days | container-days with an OOM per 1,000 | memory allocated / peak usage | resizes per container-day |\n|---|---:|---:|---:|---:|\n")
		for _, r := range rs {
			name := r.Name
			if r.Default {
				name += " (default)"
			}
			fmt.Fprintf(&b, "| %s | %.2f | %.2f | %.3f | %.2f |\n", name, r.OOMPer1000, r.OOMDaysPer1000, r.AllocOverPeak, r.ResizesPerDay)
		}
		b.WriteString("\n")
		matched(&b, rs)
	}
	return b.String()
}
