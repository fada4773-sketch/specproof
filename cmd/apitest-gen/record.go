package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/record"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// blocking problems keep the spec unchanged; the others concern the
// instance and are reported only.
var blocking = []string{record.CodeSelectNone, record.CodeSeedMissing, record.CodeShared, record.CodeInvalid}

// logFile is the file -show-bodies writes, in the current directory.
const logFile = "record-log.html"

// recordCommand runs "apitest-gen record". Every request is printed when it
// is answered, so the log shows the order of the run. With -show-bodies the
// requests, their bodies and answers, the findings and the summary go into
// logFile instead, and the console shows only a short report.
func recordCommand(o *options, out io.Writer) (err error) {
	s, err := spec.Load(context.Background(), o.spec)
	if err != nil {
		return err
	}
	defs := firstDefaults(o)
	cfg, err := record.Load(defs)
	if err != nil {
		return err
	}
	doc, err := yamldoc.Load(o.spec)
	if err != nil {
		return err
	}
	target := o.spec
	if o.out != "" {
		target = o.out
	}
	// the output of the last run: the examples of unchanged operations
	// come from it, none from the spec
	var prev *yamldoc.Doc
	if _, err := os.Stat(target); err == nil {
		if prev, err = yamldoc.Load(target); err != nil {
			return err
		}
	}
	opt, err := discoverOptions(o)
	if err != nil {
		return err
	}
	st := newStyle(out)
	full := out // the long report: requests, findings, codes
	lg := &runLog{Spec: o.spec, BaseURL: o.baseURL, Started: time.Now()}
	if o.showBodies {
		full = io.Discard
		defer func() {
			lg.Err = err
			path, werr := lg.write(logFile)
			if werr != nil {
				fmt.Fprintf(out, "  log: %v\n", werr)
				return
			}
			fmt.Fprintf(out, "  log: %s\n", path)
		}()
	}
	section(full, st, fmt.Sprintf("REQUESTS to %s", o.baseURL))
	fmt.Fprintln(full, st.paint(dim, "  in the order they are sent; per tag: GET and POSTs that read, PUT/PATCH, DELETE, then POST"))
	tag := ""
	client := &record.Client{Opt: opt, Log: func(e record.Entry) {
		lg.Entries = append(lg.Entries, e)
		logEntry(full, st, e, &tag)
	}}
	res, err := record.Run(context.Background(), record.Input{Spec: s, Doc: doc, Config: cfg, Client: client,
		Prev: prev, Overwrite: o.overwrite, Writes: !o.readOnly && !o.dryRun, IgnoreLinting: o.ignoreLinting,
		Token: strconv.FormatInt(time.Now().Unix(), 36)})
	if err != nil {
		return err
	}
	rows, stop := findingRows(res, o.ignoreLinting)
	lg.Findings, lg.Probes, lg.Coverage = rows, res.Probes, res.Coverage
	summary := fmt.Sprintf("%d operations, %d with new or changed schemas, %d unchanged; %d complete; %d records from %q; requests: %s",
		res.Stats.Ops, res.Stats.Written, res.Stats.Unchanged, res.Stats.Done, res.Stats.Reused, record.RecordedKey, counts(client.Count))
	lg.Summary = summary
	section(full, st, "SUMMARY")
	fmt.Fprintf(full, "  record: %s\n", summary)
	findings(full, st, rows)
	coverage(full, st, res.Coverage)
	if o.showBodies {
		fmt.Fprintf(out, "apitest-gen record: %s ← %s\n", o.spec, o.baseURL)
		fmt.Fprintf(out, "  %s\n", summary)
		fmt.Fprintf(out, "  findings: %s\n", findingCounts(st, rows))
		fmt.Fprintf(out, "  examples: %s\n", coverageLine(st, res.Coverage))
	}
	if stop > 0 {
		return fmt.Errorf("%d problems; nothing was written", stop)
	}
	tmp, _, err := stageSpec(doc, target)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	file := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		lg.Files = append(lg.Files, line)
		fmt.Fprintln(out, "  "+line)
	}
	if !o.showBodies {
		section(out, st, "FILES")
	}
	file("spec %s: %d examples written, %d operations with moved ids", target, res.Stats.Examples, res.Stats.Remapped)
	if o.dryRun {
		file("dry run: nothing written, only GET was sent")
		return nil
	}
	if res.Changed || o.out != "" {
		if err := commitSpec(tmp, target); err != nil {
			return err
		}
	}
	if err := record.SaveRecorded(defs, res.Recorded); err != nil {
		return err
	}
	file("%s: %q updated: %d operations complete, %d seed records the empty environment must hold",
		defs, record.RecordedKey, len(res.Recorded.Operations), len(res.Recorded.Seed))
	if err := record.SaveSuggestions(defs, res.Suggestions); err != nil {
		return err
	}
	if len(res.Suggestions) > 0 {
		file("%s: %d suggestions in %q: merge an entry into \"params\", \"select\", \"seed\" or \"$apitest\" and run again; an entry without one explains a cause the defaults cannot fix",
			defs, len(res.Suggestions), record.SuggestionsKey)
	}
	if len(res.Problems) > 0 {
		return fmt.Errorf("%d problems with the instance; the examples were written, the operations with problems are tried again next time", len(res.Problems))
	}
	return nil
}

func counts(m map[string]int) string {
	var parts []string
	for _, k := range []string{"GET", "PUT", "PATCH", "DELETE", "POST"} {
		if m[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// logEntry writes one request as a line: a heading when the tag changes,
// the status colored, what was sent and answered under a failed one and
// where its values come from. A request that only checked a candidate for
// "select" is no failure: its status stays dim.
func logEntry(out io.Writer, st style, e record.Entry, tag *string) {
	if e.Tag != *tag {
		*tag = e.Tag
		fmt.Fprintln(out, st.paint(cyan, "  ── "+e.Tag+" "))
	}
	status, color := fmt.Sprint(e.Status), green
	switch {
	case e.Err != nil:
		status, color = "ERR", red
	case e.Status/100 == 4:
		color = yellow
	case e.Status/100 != 2:
		color = red
	}
	if e.Probe && color != green {
		color = dim
	}
	fmt.Fprintf(out, "  %s %-6s %s %s  %s\n", st.paint(dim, fmt.Sprintf("#%03d", e.N)), e.Method, st.paint(color, fmt.Sprintf("%-3s", status)), e.URL, st.paint(dim, e.Why))
	if !e.Failed() {
		return
	}
	if e.Err != nil {
		fmt.Fprintln(out, "         "+st.paint(red, "error:  "+e.Err.Error()))
	}
	if e.Body != nil {
		fmt.Fprintln(out, "         "+st.paint(dim, "sent:   "+record.Clip(e.Body)))
	}
	if e.Resp != nil {
		fmt.Fprintln(out, "         "+st.paint(dim, "answer: "+record.Clip(e.Resp)))
	}
	for _, o := range e.Origin {
		fmt.Fprintln(out, "         "+st.paint(dim, "from:   "+o))
	}
}

// finding is one row of the findings: a note or a problem of the run.
type finding struct {
	Label, Code, Where, Message string
	rank                        int
}

// labels are the severities of the findings, the most serious first.
var labels = []string{"FATAL", "PROBLEM", "WARN", "INFO"}

var labelColors = map[string]string{"FATAL": red, "PROBLEM": red, "WARN": yellow, "INFO": blue}

// findingRows are the notes and problems, the most serious first; stop
// is the number of problems that keep the spec unchanged.
func findingRows(res *record.Result, ignoreLinting bool) (rows []finding, stop int) {
	for _, p := range res.Problems {
		label, rank := "PROBLEM", 3
		if slices.Contains(blocking, p.Code) {
			label, rank = "FATAL", 4
			stop++
		}
		rows = append(rows, finding{label, p.Code, p.Where, p.Message, rank})
	}
	for _, n := range res.Notes {
		// -ignorelinting: the violations are written anyway and not shown
		if ignoreLinting && n.Code == record.CodeLintIgnored {
			continue
		}
		sev, _, _ := record.Explain(n.Code)
		label, rank := "INFO", 0
		switch sev {
		case record.Warning:
			label, rank = "WARN", 1
		case record.Problem:
			label, rank = "PROBLEM", 2
		}
		rows = append(rows, finding{label, n.Code, n.Where, n.Message, rank})
	}
	slices.SortStableFunc(rows, func(a, b finding) int { return b.rank - a.rank })
	return rows, stop
}

// findingCounts is "2 PROBLEM, 3 WARN", or "none".
func findingCounts(st style, rows []finding) string {
	count := map[string]int{}
	for _, r := range rows {
		count[r.Label]++
	}
	var parts []string
	for _, l := range labels {
		if count[l] > 0 {
			parts = append(parts, st.paint(labelColors[l], fmt.Sprintf("%d %s", count[l], l)))
		}
	}
	if len(parts) == 0 {
		return st.paint(green, "none")
	}
	return strings.Join(parts, ", ")
}

// findings writes the findings as a table, then what each code means.
func findings(out io.Writer, st style, rows []finding) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "  "+st.paint(green, "no findings"))
		return
	}
	var cells [][]cell
	for _, r := range rows {
		c := labelColors[r.Label]
		cells = append(cells, []cell{{r.Label, c}, {r.Code, c}, {r.Where, bold}, {r.Message, ""}})
	}
	fmt.Fprintln(out, "  findings: "+findingCounts(st, rows))
	section(out, st, "FINDINGS")
	table(out, st, []string{"", "CODE", "WHERE", "MESSAGE"}, cells)
	section(out, st, "WHAT THE CODES MEAN")
	var legend [][]cell
	for _, c := range codesOf(rows) {
		_, meaning, fix := record.Explain(c)
		if meaning == "" {
			continue
		}
		legend = append(legend, []cell{{c, bold}, {meaning + " → " + fix, ""}})
	}
	table(out, st, []string{"CODE", "MEANING → WHAT TO DO"}, legend)
}

// coverageLine is "41 of 44 places have an example (30 written, 11 kept),
// 3 without; 2 values generated".
func coverageLine(st style, c record.Coverage) string {
	without := st.paint(green, "0 without")
	if c.Without() > 0 {
		without = st.paint(yellow, fmt.Sprintf("%d without", c.Without()))
	}
	return fmt.Sprintf("%d of %d places have an example (%d written by this run, %d kept from the last output), %s; %d values generated (required, no data read)",
		c.Run+c.Kept, c.Places, c.Run, c.Kept, without, c.Generated)
}

// coverage writes how many places have an example, then the places
// without one and why.
func coverage(out io.Writer, st style, c record.Coverage) {
	section(out, st, "EXAMPLES")
	fmt.Fprintln(out, "  "+coverageLine(st, c))
	fmt.Fprintln(out, st.paint(dim, "  places: path and required query parameters, the request body, the first 2xx response with JSON content"))
	if c.Without() == 0 {
		return
	}
	var cells [][]cell
	for _, g := range c.Missing {
		cells = append(cells, []cell{{g.Op, bold}, {g.Place, yellow}, {g.Why, ""}})
	}
	table(out, st, []string{"OPERATION", "PLACE", "WHY IT HAS NO EXAMPLE"}, cells)
}

// codesOf are the codes of the findings, each once, in their order.
func codesOf(rows []finding) []string {
	var codes []string
	for _, r := range rows {
		if !slices.Contains(codes, r.Code) {
			codes = append(codes, r.Code)
		}
	}
	return codes
}

// absolute is a path as the console shows it: absolute where possible.
func absolute(path string) string {
	if a, err := filepath.Abs(path); err == nil {
		return a
	}
	return path
}
