package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// writeSynthetic writes a VPRB1 file (the cmd/tracegen format) of containers
// with a daily cycle and noise, in normalized units.
func writeSynthetic(t *testing.T, containers, days int) string {
	rng := rand.New(rand.NewPCG(7, 8))
	nWin := days * 288
	var raw bytes.Buffer
	le := binary.LittleEndian
	raw.WriteString("VPRB1\n")
	binary.Write(&raw, le, uint32(containers))
	binary.Write(&raw, le, uint32(nWin))
	for c := range containers {
		level := 0.003 * (1 + rng.Float64())
		v := make([]float64, nWin)
		var scale float64
		for w := range v {
			v[w] = level * (1 + 0.1*math.Sin(2*math.Pi*float64(w)/288) + 0.03*rng.NormFloat64())
			scale = math.Max(scale, v[w])
		}
		binary.Write(&raw, le, uint64(c))
		binary.Write(&raw, le, uint32(0))
		binary.Write(&raw, le, float32(scale))
		for w, x := range v {
			q := uint16(math.Round(x / scale * 65534))
			if w%97 == 0 {
				q = math.MaxUint16 // a few windows without data
			}
			binary.Write(&raw, le, q)
		}
	}
	path := filepath.Join(t.TempDir(), "synthetic.bin.gz")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(raw.Bytes())
	zw.Close()
	if err := os.WriteFile(path, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplayDefaults(t *testing.T) {
	d, err := load(writeSynthetic(t, 30, 9), 64*float64(1<<30))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ids) != 30 || d.windows != 9*288 || !math.IsNaN(float64(d.usage[0][0])) {
		t.Fatalf("loaded %d containers, %d windows, first value %v", len(d.ids), d.windows, d.usage[0][0])
	}
	idx := make([]int, len(d.ids))
	for i := range idx {
		idx[i] = i
	}
	for _, v := range variants() {
		if !v.Default {
			continue
		}
		r := run(d, idx, v, 7)
		t.Logf("%-24s oom/1000d=%.1f alloc/peak=%.3f", v.Name, r.OOMPer1000, r.AllocOverPeak)
		if r.UnsizedWindows != 0 {
			t.Errorf("%s: %d scored windows without a recommendation", v.Name, r.UnsizedWindows)
		}
		if math.Abs(r.ContainerDays-30*2*(1-1.0/97)) > 1 {
			t.Errorf("%s: %.1f container-days scored, want about 59.4", v.Name, r.ContainerDays)
		}
		if r.AllocOverPeak < 1 || r.AllocOverPeak > 3 {
			t.Errorf("%s: allocated/peak %.3f out of range", v.Name, r.AllocOverPeak)
		}
	}
}
