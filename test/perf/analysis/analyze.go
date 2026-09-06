// Command analyze stream-parses a k6 JSON output file and computes steady-phase
// latency percentiles, achieved rate, and error rate from the RAW data (not the
// k6 summary). It also emits per-second time series for charting. Test asset for
// TASK-029.
//
// Usage: analyze <k6.json> <phase> <out-prefix>
//
// It writes:
//
//	<out-prefix>.stats.json     — percentiles, achieved rate, error rate
//	<out-prefix>.latency_ts.csv — t_sec,p50,p95,p99 per steady second
//	<out-prefix>.rate_ts.csv    — t_sec,reqs,errs per second
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

type point struct {
	Metric string `json:"metric"`
	Type   string `json:"type"`
	Data   struct {
		Time  string  `json:"time"`
		Value float64 `json:"value"`
		Tags  struct {
			Phase  string `json:"phase"`
			Status string `json:"status"`
		} `json:"tags"`
	} `json:"data"`
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: analyze <k6.json> <phase> <out-prefix>")
		os.Exit(2)
	}
	path, phase, out := os.Args[1], os.Args[2], os.Args[3]
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var durs []float64                 // steady http_req_duration values (ms)
	perSecLat := map[int64][]float64{} // rounded epoch sec -> latencies
	reqPerSec := map[int64]int64{}
	errPerSec := map[int64]int64{}
	var totalReqs, totalErrs int64
	var minT, maxT int64 = math.MaxInt64, 0

	parseSec := func(ts string) int64 {
		// RFC3339 with offset; seconds field is chars 17-18. Cheap: use time-free
		// bucketing by truncating the string to the second.
		// 2026-09-04T21:47:53.507579+03:00
		if len(ts) < 19 {
			return 0
		}
		// hh*3600+mm*60+ss is enough for relative bucketing within a run.
		h := int64(ts[11]-'0')*10 + int64(ts[12]-'0')
		m := int64(ts[14]-'0')*10 + int64(ts[15]-'0')
		s := int64(ts[17]-'0')*10 + int64(ts[18]-'0')
		return h*3600 + m*60 + s
	}

	for sc.Scan() {
		line := sc.Bytes()
		// fast reject: only Point lines for the two metrics we need
		if len(line) < 20 || line[2] != 't' { // "{"type":"Metric"...} starts with {"type -> [2]='t'; Points start with {"metric -> [2]='m'
			// Points begin with {"metric":... so line[2]=='m'
		}
		var p point
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.Type != "Point" {
			continue
		}
		if p.Data.Tags.Phase != phase {
			continue
		}
		sec := parseSec(p.Data.Time)
		switch p.Metric {
		case "http_req_duration":
			durs = append(durs, p.Data.Value)
			perSecLat[sec] = append(perSecLat[sec], p.Data.Value)
		case "http_reqs":
			reqPerSec[sec] += int64(p.Data.Value)
			totalReqs += int64(p.Data.Value)
			if sec < minT {
				minT = sec
			}
			if sec > maxT {
				maxT = sec
			}
		case "http_req_failed":
			if p.Data.Value != 0 {
				errPerSec[sec] += int64(p.Data.Value)
				totalErrs += int64(p.Data.Value)
			}
		}
	}
	if err := sc.Err(); err != nil {
		panic(err)
	}

	sort.Float64s(durs)
	spanSec := maxT - minT + 1
	if spanSec <= 0 {
		spanSec = 1
	}
	achievedRate := float64(totalReqs) / float64(spanSec)
	errRate := 0.0
	if totalReqs > 0 {
		errRate = float64(totalErrs) / float64(totalReqs)
	}

	stats := map[string]any{
		"phase":         phase,
		"samples":       len(durs),
		"total_reqs":    totalReqs,
		"total_errs":    totalErrs,
		"span_sec":      spanSec,
		"achieved_rate": achievedRate,
		"error_rate":    errRate,
		"p50_ms":        pct(durs, 50),
		"p90_ms":        pct(durs, 90),
		"p95_ms":        pct(durs, 95),
		"p99_ms":        pct(durs, 99),
		"p999_ms":       pct(durs, 99.9),
		"max_ms":        pct(durs, 100),
	}
	sj, _ := json.MarshalIndent(stats, "", "  ")
	_ = os.WriteFile(out+".stats.json", sj, 0o644)
	fmt.Println(string(sj))

	// per-second latency ts
	secs := make([]int64, 0, len(perSecLat))
	for s := range perSecLat {
		secs = append(secs, s)
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
	lf, _ := os.Create(out + ".latency_ts.csv")
	fmt.Fprintln(lf, "t_rel,p50,p95,p99,n")
	for _, s := range secs {
		v := perSecLat[s]
		sort.Float64s(v)
		fmt.Fprintf(lf, "%d,%.3f,%.3f,%.3f,%d\n", s-minT, pct(v, 50), pct(v, 95), pct(v, 99), len(v))
	}
	lf.Close()

	rf, _ := os.Create(out + ".rate_ts.csv")
	fmt.Fprintln(rf, "t_rel,reqs,errs")
	for s := minT; s <= maxT; s++ {
		fmt.Fprintf(rf, "%d,%d,%d\n", s-minT, reqPerSec[s], errPerSec[s])
	}
	rf.Close()
}
