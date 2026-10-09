package report

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"
)

// Stats are the figures both reports show, computed from the cases only.
type Stats struct {
	Counts   map[string]int
	Total    int
	Executed int // cases that got an answer
	// Good are the cases that do not fail: passed, deviation, tolerated.
	Good int
	// Bad are the cases that fail the test.
	Bad int
	// Response times of the cases with an answer.
	Sum, Avg, P50, P90, P95, P99, Max time.Duration
	Slowest                           []Case // up to 10, slowest first
	Groups                            []GroupStats
	Codes                             []CodeCount // by code
	Histogram                         []Bucket
	Errors                            ErrorStats
}

// GroupStats are the figures of one tag.
type GroupStats struct {
	Name     string
	Total    int
	Counts   map[string]int
	Duration time.Duration // sum of the response times
}

// CodeCount is how often a status code was answered.
type CodeCount struct {
	Code  int
	Count int
}

// Bucket is one bar of the response time histogram.
type Bucket struct {
	Label string
	Count int
}

// ErrorStats analyse the error cases: what was documented, what came.
type ErrorStats struct {
	Cases []Case
	// Expected lists the expected statuses, Actual the received ones
	// ("–" without answer); Matrix[e][a] counts the cases.
	Expected, Actual []string
	Matrix           map[string]map[string]int
	AsDocumented     int // got the documented status and passed
	Other            int // tolerated or failed
}

// buckets are the upper bounds of the histogram.
var buckets = []struct {
	max   time.Duration
	label string
}{
	{10 * time.Millisecond, "< 10 ms"},
	{50 * time.Millisecond, "10–50 ms"},
	{100 * time.Millisecond, "50–100 ms"},
	{250 * time.Millisecond, "100–250 ms"},
	{500 * time.Millisecond, "250–500 ms"},
	{time.Second, "0.5–1 s"},
	{2500 * time.Millisecond, "1–2.5 s"},
	{0, "≥ 2.5 s"},
}

// Compute derives the figures of a report.
func Compute(r *Report) Stats {
	st := Stats{Counts: map[string]int{}, Total: len(r.Cases)}
	var times []time.Duration
	groups := map[string]*GroupStats{}
	var order []string
	codes := map[int]int{}
	st.Histogram = make([]Bucket, len(buckets))
	for i, b := range buckets {
		st.Histogram[i].Label = b.label
	}
	st.Errors.Matrix = map[string]map[string]int{}
	for _, c := range r.Cases {
		st.Counts[c.Status]++
		switch {
		case IsError(c.Status):
			st.Bad++
		case c.Status == Passed || c.Status == Deviation || c.Status == Tolerated:
			st.Good++
		}
		g := groups[c.Group]
		if g == nil {
			g = &GroupStats{Name: c.Group, Counts: map[string]int{}}
			groups[c.Group] = g
			order = append(order, c.Group)
		}
		g.Total++
		g.Counts[c.Status]++
		if c.Code != 0 {
			st.Executed++
			codes[c.Code]++
			times = append(times, c.Duration)
			g.Duration += c.Duration
			for i, b := range buckets {
				if b.max == 0 || c.Duration < b.max {
					st.Histogram[i].Count++
					break
				}
			}
		}
		if c.ErrorCase {
			st.Errors.add(c)
		}
	}
	for _, name := range order {
		st.Groups = append(st.Groups, *groups[name])
	}
	for code, n := range codes {
		st.Codes = append(st.Codes, CodeCount{code, n})
	}
	sort.Slice(st.Codes, func(i, j int) bool { return st.Codes[i].Code < st.Codes[j].Code })
	if len(times) > 0 {
		sorted := slices.Clone(times)
		slices.Sort(sorted)
		for _, d := range sorted {
			st.Sum += d
		}
		st.Avg = st.Sum / time.Duration(len(sorted))
		st.P50, st.P90, st.P95, st.P99 = percentile(sorted, 50), percentile(sorted, 90), percentile(sorted, 95), percentile(sorted, 99)
		st.Max = sorted[len(sorted)-1]
	}
	var answered []Case
	for _, c := range r.Cases {
		if c.Code != 0 {
			answered = append(answered, c)
		}
	}
	sort.SliceStable(answered, func(i, j int) bool { return answered[i].Duration > answered[j].Duration })
	st.Slowest = answered[:min(10, len(answered))]
	sortCodes(st.Errors.Expected)
	sortCodes(st.Errors.Actual)
	return st
}

func (e *ErrorStats) add(c Case) {
	e.Cases = append(e.Cases, c)
	exp, act := c.Expected, "–"
	if exp == "" {
		exp = "?"
	}
	if n := len(exp); n > 3 {
		exp = exp[:3] // "404, Schema" → "404"
	}
	if c.Code != 0 {
		act = strconv.Itoa(c.Code)
	}
	if !slices.Contains(e.Expected, exp) {
		e.Expected = append(e.Expected, exp)
	}
	if !slices.Contains(e.Actual, act) {
		e.Actual = append(e.Actual, act)
	}
	if e.Matrix[exp] == nil {
		e.Matrix[exp] = map[string]int{}
	}
	e.Matrix[exp][act]++
	if c.Status == Passed {
		e.AsDocumented++
	} else {
		e.Other++
	}
}

// sortCodes orders status codes numerically, "–" last.
func sortCodes(codes []string) {
	sort.SliceStable(codes, func(i, j int) bool {
		a, errA := strconv.Atoi(codes[i])
		b, errB := strconv.Atoi(codes[j])
		if errA != nil || errB != nil {
			return errA == nil
		}
		return a < b
	})
}

// percentile is the nearest-rank percentile of sorted durations.
func percentile(sorted []time.Duration, p int) time.Duration {
	i := (p*len(sorted) + 99) / 100
	return sorted[max(0, min(len(sorted)-1, i-1))]
}

// ms formats a duration for tables: "12 ms", "1.24 s".
func ms(d time.Duration) string {
	switch {
	case d == 0:
		return "–"
	case d < time.Millisecond:
		return fmt.Sprintf("%d µs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

// verdict says what an error case got, for the analysis.
func verdict(c Case) string {
	switch {
	case c.Status == Passed:
		return "as documented"
	case c.Status == Tolerated:
		return "tolerated (" + c.Tolerated + ")"
	case c.Code == 0:
		return c.Status + ", no answer"
	}
	return c.Status
}
