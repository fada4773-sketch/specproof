package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/gen/record"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// blocking problems keep the spec unchanged; the others concern the
// instance and are reported only.
var blocking = []string{record.CodeSelectNone, record.CodeSeedMissing, record.CodeShared, record.CodeInvalid}

// recordCommand runs "apitest-gen record". Every request is printed when it
// is answered, so the log shows the order of the run.
func recordCommand(o *options, out io.Writer) error {
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
	section(out, st, fmt.Sprintf("REQUESTS to %s", o.baseURL))
	fmt.Fprintln(out, st.paint(dim, "  in the order they are sent; per tag: GET and POSTs that read, PUT/PATCH, DELETE, then POST"))
	tag := ""
	client := &record.Client{Opt: opt, Log: func(e record.Entry) { logEntry(out, st, e, &tag) }}
	res, err := record.Run(context.Background(), record.Input{Spec: s, Doc: doc, Config: cfg, Client: client,
		Prev: prev, Overwrite: o.overwrite, Writes: !o.readOnly && !o.dryRun, IgnoreLinting: o.ignoreLinting})
	if err != nil {
		return err
	}
	section(out, st, "SUMMARY")
	fmt.Fprintf(out, "  record: %d operations, %d with new or changed schemas, %d unchanged; %d complete; %d records from %q; requests: %s\n",
		res.Stats.Ops, res.Stats.Written, res.Stats.Unchanged, res.Stats.Done, res.Stats.Reused, record.RecordedKey, counts(client.Count))
	stop := findings(out, st, res, o.ignoreLinting)
	if stop > 0 {
		return fmt.Errorf("%d problems; nothing was written", stop)
	}
	tmp, _, err := stageSpec(doc, target)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	section(out, st, "FILES")
	fmt.Fprintf(out, "  spec %s: %d examples written, %d operations with moved ids\n", target, res.Stats.Examples, res.Stats.Remapped)
	if o.dryRun {
		fmt.Fprintln(out, "  dry run: nothing written, only GET was sent")
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
	fmt.Fprintf(out, "  %s: %q updated: %d operations complete, %d seed records the empty environment must hold\n",
		defs, record.RecordedKey, len(res.Recorded.Operations), len(res.Recorded.Seed))
	if err := record.SaveSuggestions(defs, res.Suggestions); err != nil {
		return err
	}
	if len(res.Suggestions) > 0 {
		fmt.Fprintf(out, "  %s: %d suggestions in %q: merge an entry into \"params\", \"select\", \"seed\" or \"$apitest\" and run again; an entry without one explains a cause the defaults cannot fix\n",
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
// the status colored, what was sent and answered under a failed one.
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
	fmt.Fprintf(out, "  %s %-6s %s %s  %s\n", st.paint(dim, fmt.Sprintf("#%03d", e.N)), e.Method, st.paint(color, fmt.Sprintf("%-3s", status)), e.URL, st.paint(dim, e.Why))
	if e.Err != nil {
		fmt.Fprintln(out, "         "+st.paint(red, "error:  "+e.Err.Error()))
		return
	}
	if e.Status/100 != 2 {
		if e.Body != nil {
			fmt.Fprintln(out, "         "+st.paint(dim, "sent:   "+record.Clip(e.Body)))
		}
		if e.Resp != nil {
			fmt.Fprintln(out, "         "+st.paint(dim, "answer: "+record.Clip(e.Resp)))
		}
	}
}

// findings writes the notes and problems as a table, the most serious
// first, then what each code means; it returns the number of problems
// that keep the spec unchanged.
func findings(out io.Writer, st style, res *record.Result, ignoreLinting bool) int {
	type row struct {
		label, code, where, msg string
		rank                    int
	}
	var rows []row
	stop := 0
	for _, p := range res.Problems {
		label, rank := "PROBLEM", 3
		if slices.Contains(blocking, p.Code) {
			label, rank = "FATAL", 4
			stop++
		}
		rows = append(rows, row{label, p.Code, p.Where, p.Message, rank})
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
		rows = append(rows, row{label, n.Code, n.Where, n.Message, rank})
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "  "+st.paint(green, "no findings"))
		return 0
	}
	slices.SortStableFunc(rows, func(a, b row) int { return b.rank - a.rank })
	colors := map[string]string{"FATAL": red, "PROBLEM": red, "WARN": yellow, "INFO": blue}
	count := map[string]int{}
	var cells [][]cell
	var codes []string
	for _, r := range rows {
		count[r.label]++
		if !slices.Contains(codes, r.code) {
			codes = append(codes, r.code)
		}
		cells = append(cells, []cell{{r.label, colors[r.label]}, {r.code, colors[r.label]}, {r.where, bold}, {r.msg, ""}})
	}
	var parts []string
	for _, l := range []string{"FATAL", "PROBLEM", "WARN", "INFO"} {
		if count[l] > 0 {
			parts = append(parts, st.paint(colors[l], fmt.Sprintf("%d %s", count[l], l)))
		}
	}
	fmt.Fprintln(out, "  findings: "+strings.Join(parts, ", "))
	section(out, st, "FINDINGS")
	table(out, st, []string{"", "CODE", "WHERE", "MESSAGE"}, cells)
	section(out, st, "WHAT THE CODES MEAN")
	var legend [][]cell
	for _, c := range codes {
		_, meaning, fix := record.Explain(c)
		if meaning == "" {
			continue
		}
		legend = append(legend, []cell{{c, bold}, {meaning + " → " + fix, ""}})
	}
	table(out, st, []string{"CODE", "MEANING → WHAT TO DO"}, legend)
	return stop
}
