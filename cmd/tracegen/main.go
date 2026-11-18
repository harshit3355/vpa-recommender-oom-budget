// Command tracegen builds the benchmark dataset from the public Google 2011
// cluster trace (clusterdata-2011-2, CC-BY 4.0): the peak memory of each task
// in every 5-minute window, for a deterministic sample of tasks that were
// already running when the trace starts.
//
// Every part is downloaded, checked against the bucket's SHA256SUM, and only
// the sampled tasks are kept, so the ~33 GB of raw parts never touch disk.
// The output is byte-reproducible for the same flags.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	base       = "https://storage.googleapis.com/clusterdata-2011-2"
	totalParts = 500
	traceStart = int64(600_000_000) // µs: the first measurement window starts 600 s in
	windowUS   = int64(300_000_000) // 5-minute measurement windows
	traceDays  = 29.0
	missing    = math.MaxUint16
)

var cacheDir = flag.String("cache", "", "keep verified raw parts here and reuse them on a rerun (~92 MB each)")

type key struct {
	job  uint64
	task uint32
}

type update struct {
	k   key
	w   int
	val float32
}

func main() {
	days := flag.Int("days", 21, "days of trace to extract")
	mod := flag.Uint("candidate-mod", 16, "keep tasks whose FNV-1a hash of job/task is -candidate-residue modulo this")
	residue := flag.Uint("candidate-residue", 0, "residue to keep; different residues give disjoint task samples")
	minCov := flag.Float64("min-coverage", 0.9, "keep tasks with data in at least this share of windows")
	workers := flag.Int("workers", 4, "parallel downloads")
	out := flag.String("out", "data/google2011-mem.bin.gz", "output file")
	flag.Parse()

	sums := fetchSums()
	nWin := *days * 288
	endUS := traceStart + int64(nWin)*windowUS
	nParts := int(math.Ceil(float64(*days)/traceDays*totalParts)) + 2
	if nParts > totalParts {
		nParts = totalParts
	}

	// Part 0 defines the candidate set: sampled tasks running in the first window.
	series := map[key][]float32{}
	ups := fetchPart(0, sums, nil, endUS)
	for _, u := range ups {
		if u.w != 0 || series[u.k] != nil {
			continue
		}
		h := fnv.New64a()
		fmt.Fprintf(h, "%d/%d", u.k.job, u.k.task)
		if h.Sum64()%uint64(*mod) == uint64(*residue) {
			s := make([]float32, nWin)
			for i := range s {
				s[i] = -1
			}
			series[u.k] = s
		}
	}
	apply(series, ups)
	log.Printf("part 0: %d candidate tasks, %d parts to read", len(series), nParts)

	var mu sync.Mutex
	parts := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range parts {
				u := fetchPart(p, sums, series, endUS) // only reads map membership, never written after part 0
				mu.Lock()
				apply(series, u)
				mu.Unlock()
				log.Printf("part %d: %d samples", p, len(u))
			}
		}()
	}
	for p := 1; p < nParts; p++ {
		parts <- p
	}
	close(parts)
	wg.Wait()

	write(*out, series, nWin, *minCov)
}

func apply(series map[key][]float32, ups []update) {
	for _, u := range ups {
		if s := series[u.k]; s != nil && u.val > s[u.w] {
			s[u.w] = u.val
		}
	}
}

func fetchSums() map[string]string {
	sums := map[string]string{}
	for _, line := range strings.Split(string(get(base+"/SHA256SUM")), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			sums[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	return sums
}

// A part is ~92 MB; a connection that stalls longer than this is retried.
var client = &http.Client{Timeout: 5 * time.Minute}

func get(url string) []byte {
	for attempt := 1; ; attempt++ {
		resp, err := client.Get(url)
		if err == nil {
			var b []byte
			b, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil && resp.StatusCode == http.StatusOK {
				return b
			}
			if err == nil {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		if attempt == 5 {
			log.Fatalf("%s: %v", url, err)
		}
		log.Printf("%s: %v; retrying", url, err)
		time.Sleep(time.Duration(attempt) * 5 * time.Second)
	}
}

// fetchPart downloads one task_usage part, verifies its checksum and returns
// the peak-memory samples of the known tasks (of every task when known is nil).
func fetchPart(p int, sums map[string]string, known map[key][]float32, endUS int64) []update {
	name := fmt.Sprintf("task_usage/part-%05d-of-%05d.csv.gz", p, totalParts)
	want, ok := sums[name]
	if !ok {
		log.Fatalf("%s missing from SHA256SUM", name)
	}
	verified := func(b []byte) bool { got := sha256.Sum256(b); return hex.EncodeToString(got[:]) == want }
	cached := filepath.Join(*cacheDir, filepath.Base(name))
	raw, err := os.ReadFile(cached)
	if *cacheDir == "" || err != nil || !verified(raw) {
		raw = get(base + "/" + name)
		if !verified(raw) {
			log.Fatalf("%s: checksum mismatch", name)
		}
		if *cacheDir != "" { // write then rename, so a crash never leaves a bad part behind
			if err := os.WriteFile(cached+".tmp", raw, 0o644); err != nil {
				log.Fatal(err)
			}
			if err := os.Rename(cached+".tmp", cached); err != nil {
				log.Fatal(err)
			}
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	var ups []update
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		// Columns: 0 start time, 2 job ID, 3 task index, 10 maximum memory usage.
		f := strings.Split(sc.Text(), ",")
		if len(f) < 11 || f[10] == "" {
			continue
		}
		start, err1 := strconv.ParseInt(f[0], 10, 64)
		job, err2 := strconv.ParseUint(f[2], 10, 64)
		task, err3 := strconv.ParseUint(f[3], 10, 32)
		val, err4 := strconv.ParseFloat(f[10], 32)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || start < traceStart || start >= endUS {
			continue
		}
		k := key{job, uint32(task)}
		if known != nil && known[k] == nil {
			continue
		}
		ups = append(ups, update{k, int((start - traceStart) / windowUS), float32(val)})
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return ups
}

// write stores the kept tasks sorted by (job, task) as gzip:
// "VPRB1\n", uint32 tasks, uint32 windows, then per task uint64 job,
// uint32 task, float32 scale and one uint16 per window
// (round(value/scale*65534); 65535 means no data).
func write(path string, series map[key][]float32, nWin int, minCov float64) {
	var keys []key
	for k, s := range series {
		n := 0
		for _, v := range s {
			if v >= 0 {
				n++
			}
		}
		if float64(n) >= minCov*float64(nWin) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].job != keys[j].job {
			return keys[i].job < keys[j].job
		}
		return keys[i].task < keys[j].task
	})

	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression) // zero ModTime keeps the bytes reproducible
	bw := bufio.NewWriter(zw)
	le := binary.LittleEndian
	bw.WriteString("VPRB1\n")
	binary.Write(bw, le, uint32(len(keys)))
	binary.Write(bw, le, uint32(nWin))
	for _, k := range keys {
		s := series[k]
		var scale float32
		for _, v := range s {
			scale = max(scale, v)
		}
		q := make([]uint16, nWin)
		for i, v := range s {
			switch {
			case v < 0:
				q[i] = missing
			case scale > 0:
				q[i] = uint16(math.Round(float64(v / scale * 65534)))
			}
		}
		binary.Write(bw, le, k.job)
		binary.Write(bw, le, k.task)
		binary.Write(bw, le, scale)
		binary.Write(bw, le, q)
	}
	for _, err := range []error{bw.Flush(), zw.Close(), f.Close()} {
		if err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("wrote %s: %d of %d candidates with >= %.0f%% coverage, %d windows", path, len(keys), len(series), minCov*100, nWin)
}
