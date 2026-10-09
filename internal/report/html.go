package report

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed report.html.tmpl
var htmlTemplate string

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"ms":       ms,
	"lower":    strings.ToLower,
	"status":   statusClass,
	"words":    statusWord,
	"label":    caseLabel,
	"kind":     kindLabel,
	"inc":      func(i int) int { return i + 1 },
	"excerpt":  excerpt,
	"devstate": deviationState,
	"bodies": func(c Case) []*Body {
		var out []*Body
		for _, b := range []*Body{c.Request, c.Response, c.ResponseHeaders, c.Verify} {
			if b != nil {
				out = append(out, b)
			}
		}
		return out
	},
}).Parse(htmlTemplate))

// RenderHTML produces the HTML report: one self-contained page with the
// dashboard, the error case analysis and every case. Like Render, it depends
// only on r.
func RenderHTML(r *Report) ([]byte, error) {
	var b bytes.Buffer
	if err := page.Execute(&b, newView(r)); err != nil {
		return nil, fmt.Errorf("HTML report: %w", err)
	}
	return b.Bytes(), nil
}

// WriteHTML renders r as HTML and writes it atomically to path.
func WriteHTML(path string, r *Report) error {
	b, err := RenderHTML(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	return WriteFile(path, b)
}

// view is what the template shows; every figure is computed here.
type view struct {
	R        *Report
	S        Stats
	Title    string
	Started  string
	Duration string
	Run      int // cases that ran (not skipped, not buildable)
	PassRate string
	Coverage string
	CovPct   int
	ExPct    int
	Donut    template.HTML
	Statuses []slice
	Groups   []groupView
	Hist     []barView
	Codes    []barView
	Slowest  []barView
	Matrix   []matrixRow
	Cases    []caseView
	Filters  []slice // statuses present, for the filter chips
}

// slice is one status with its count and share.
type slice struct {
	Status string
	Count  int
	Pct    float64
}

type groupView struct {
	Name     string
	Total    int
	Passed   int
	Duration time.Duration
	Parts    []slice // share of the tag's cases per status
}

type barView struct {
	Label string
	Sub   string
	Value string
	Pct   float64
	Class string
}

type matrixRow struct {
	Expected string
	Cells    []matrixCell
}

type matrixCell struct {
	Count int
	Class string // "ok", "bad", "" (empty)
	Alpha float64
}

type caseView struct {
	Case
	ID       int
	Details  bool
	Excerpt  string
	BarPct   float64
	Received string
	Search   string
}

func newView(r *Report) *view {
	st := Compute(r)
	v := &view{R: r, S: st, Title: r.SpecTitle, Started: r.Started.UTC().Format("2006-01-02 15:04:05 UTC"),
		Duration: r.Duration.Round(time.Millisecond).String()}
	if r.SpecVersion != "" {
		v.Title += " " + r.SpecVersion
	}
	v.Run = st.Total - st.Counts[Skipped] - st.Counts[NotBuildable]
	v.PassRate = percent(st.Good, v.Run)
	v.Coverage = percent(r.Coverage.OperationsCovered, r.Coverage.Operations)
	v.CovPct = share(r.Coverage.OperationsCovered, r.Coverage.Operations)
	v.ExPct = share(r.Coverage.ExamplesCovered, r.Coverage.Examples)
	for _, s := range StatusOrder {
		if n := st.Counts[s]; n > 0 {
			sl := slice{s, n, pct(n, st.Total)}
			v.Statuses = append(v.Statuses, sl)
			v.Filters = append(v.Filters, sl)
		}
	}
	v.Donut = donut(v.Statuses, st.Total)
	for _, g := range st.Groups {
		gv := groupView{Name: g.Name, Total: g.Total, Passed: g.Counts[Passed], Duration: g.Duration}
		for _, s := range StatusOrder {
			if n := g.Counts[s]; n > 0 {
				gv.Parts = append(gv.Parts, slice{s, n, pct(n, g.Total)})
			}
		}
		v.Groups = append(v.Groups, gv)
	}
	maxBucket, maxCode := 0, 0
	for _, h := range st.Histogram {
		maxBucket = max(maxBucket, h.Count)
	}
	for _, h := range st.Histogram {
		v.Hist = append(v.Hist, barView{Label: h.Label, Value: fmt.Sprint(h.Count), Pct: pct(h.Count, maxBucket)})
	}
	for _, c := range st.Codes {
		maxCode = max(maxCode, c.Count)
	}
	for _, c := range st.Codes {
		v.Codes = append(v.Codes, barView{Label: fmt.Sprint(c.Code), Value: fmt.Sprint(c.Count), Pct: pct(c.Count, maxCode), Class: fmt.Sprintf("c%dxx", c.Code/100)})
	}
	for _, c := range st.Slowest {
		v.Slowest = append(v.Slowest, barView{Label: c.Method + " " + c.Target, Sub: caseLabel(c), Value: ms(c.Duration),
			Pct: pct(int(c.Duration), int(st.Max)), Class: statusClass(c.Status)})
	}
	maxCell := 0
	for _, row := range st.Errors.Matrix {
		for _, n := range row {
			maxCell = max(maxCell, n)
		}
	}
	for _, x := range st.Errors.Expected {
		row := matrixRow{Expected: x}
		for _, a := range st.Errors.Actual {
			n := st.Errors.Matrix[x][a]
			cell := matrixCell{Count: n}
			if n > 0 {
				cell.Class, cell.Alpha = "bad", 0.25+0.75*float64(n)/float64(maxCell)
				if a == x {
					cell.Class = "ok"
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		v.Matrix = append(v.Matrix, row)
	}
	for i, c := range r.Cases {
		cv := caseView{Case: c, ID: i + 1, Excerpt: excerpt(c), BarPct: pct(int(c.Duration), int(st.Max)), Received: "–"}
		if c.Code != 0 {
			cv.Received = fmt.Sprint(c.Code)
		}
		cv.Details = c.Message != "" || len(c.Diffs) > 0 || len(c.Problems) > 0 || c.Request != nil || c.Response != nil ||
			c.ResponseHeaders != nil || c.Verify != nil || c.Curl != ""
		cv.Search = strings.ToLower(strings.Join([]string{caseLabel(c), c.Method, c.Target, c.Status, c.Message, c.Kind, cv.Received}, " "))
		v.Cases = append(v.Cases, cv)
	}
	return v
}

// donut draws the statuses as a ring: one circle per status, its stroke as
// long as its share (the circumference of r = 15.9155 is 100).
func donut(parts []slice, total int) template.HTML {
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 42 42" class="donut" role="img" aria-label="cases by status">`)
	b.WriteString(`<circle class="ring" cx="21" cy="21" r="15.9155" fill="none" stroke-width="5"/>`)
	offset := 25.0 // start at 12 o'clock
	for _, p := range parts {
		gap := 0.0
		if len(parts) > 1 {
			gap = math.Min(0.6, p.Pct/4)
		}
		fmt.Fprintf(&b, `<circle class="seg s-%s" cx="21" cy="21" r="15.9155" fill="none" stroke-width="5" stroke-dasharray="%.3f %.3f" stroke-dashoffset="%.3f"><title>%s: %d (%.1f %%)</title></circle>`,
			statusClass(p.Status), p.Pct-gap, 100-p.Pct+gap, offset, html.EscapeString(p.Status), p.Count, p.Pct)
		offset -= p.Pct
	}
	fmt.Fprintf(&b, `<text x="21" y="21" class="donut-n">%d</text><text x="21" y="26.5" class="donut-l">cases</text></svg>`, total)
	return template.HTML(b.String())
}

// statusClass is the CSS class of a status: "passed", "schema-violation".
func statusClass(s string) string { return strings.ReplaceAll(strings.ToLower(s), "_", "-") }

func kindLabel(k string) string {
	if k == "" || k == "regular" {
		return ""
	}
	return k
}

func pct(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(n)*1000/float64(total)) / 10
}

func share(n, total int) int {
	if total <= 0 {
		return 0
	}
	return n * 100 / total
}
