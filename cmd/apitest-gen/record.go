package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/record"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// recordCommand runs "apitest-gen record": with -analyse it adds the
// missing entries to the record file, else it records the missing answers
// and writes the file into the spec.
func recordCommand(o *options, out io.Writer) error {
	s, err := spec.Load(context.Background(), o.spec)
	if err != nil {
		return err
	}
	run, err := apitestRun(o)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(o.file)
	exists := statErr == nil
	f, err := record.Load(o.file)
	if err != nil {
		return err
	}
	st := newStyle(out)
	if o.analyse {
		return analyseCommand(o, out, st, s, f, run, exists)
	}
	if !exists {
		return fmt.Errorf("%s does not exist; create it with: apitest-gen record -spec %s -analyse", o.file, o.spec)
	}
	doc, err := yamldoc.Load(o.spec)
	if err != nil {
		return err
	}
	in := record.Input{Spec: s, Doc: doc, File: f, IgnoreFields: run.IgnoreFields}
	if o.refresh != "" {
		in.Refresh = strings.Split(o.refresh, ",")
	}
	if o.baseURL != "" {
		opt, err := discoverOptions(o)
		if err != nil {
			return err
		}
		in.Client = &record.Client{Opt: opt}
	}
	fmt.Fprintf(out, "apitest-gen record: %s → %s\n", o.file, o.spec)
	res, err := record.Run(context.Background(), in)
	if err != nil {
		return err
	}
	steps(out, st, o, res)
	target := o.spec
	if o.out != "" {
		target = o.out
	}
	var orderNotes []record.Note
	written := false
	if len(res.Problems) == 0 {
		tmp, staged, err := stageSpec(doc, target)
		if err != nil {
			return err
		}
		defer os.Remove(tmp)
		if orderNotes, err = record.CheckOrder(staged, f, run); err != nil {
			return err
		}
		if !o.dryRun && (res.SpecChanged || o.out != "") {
			if err := commitSpec(tmp, target); err != nil {
				return err
			}
			written = true
		}
	}
	notes := append(append([]record.Note{}, res.Notes...), orderNotes...)
	recordFindings(out, st, res.Problems, notes)
	section(out, st, "FILES")
	if res.FileChanged {
		if o.dryRun {
			fmt.Fprintf(out, "  %s: %d answers recorded, not saved (dry run)\n", o.file, res.Recorded)
		} else {
			if err := f.Save(o.file); err != nil {
				return err
			}
			fmt.Fprintf(out, "  %s: %d answers recorded and saved\n", o.file, res.Recorded)
		}
	} else {
		fmt.Fprintf(out, "  %s: unchanged\n", o.file)
	}
	switch {
	case len(res.Problems) > 0:
		fmt.Fprintf(out, "  %s: unchanged, the problems above come first\n", target)
		return fmt.Errorf("%d problems", len(res.Problems))
	case o.dryRun:
		fmt.Fprintf(out, "  %s: %d examples would change (dry run)\n", target, res.Examples)
	case written:
		fmt.Fprintf(out, "  %s: %d examples written\n", target, res.Examples)
	default:
		fmt.Fprintf(out, "  %s: unchanged, every example is up to date\n", target)
	}
	return nil
}

// apitestRun is "$apitest" of the defaults file: the order apitest runs
// the cases in. A missing file gives apitest's default order.
func apitestRun(o *options) (defaults.Run, error) {
	defs, err := defaults.LoadAll(o.defaults)
	if err != nil {
		return defaults.Run{}, err
	}
	if defs.Run == nil {
		return defaults.Run{}, nil
	}
	return *defs.Run, nil
}

func analyseCommand(o *options, out io.Writer, st style, s *spec.Spec, f *record.File, run defaults.Run, exists bool) error {
	an, err := record.Analyse(s, f, run, o.seed)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "apitest-gen record -analyse: %s → %s\n", o.spec, o.file)
	if len(an.Added) == 0 {
		fmt.Fprintln(out, "  every case of the spec has an entry; nothing to add")
	} else {
		section(out, st, "ADDED (in the order apitest runs them)")
		tag := ""
		for _, step := range an.Added {
			if step.Tag != tag {
				tag = step.Tag
				fmt.Fprintln(out, st.paint(cyan, "  ── "+tag))
			}
			state := st.paint(yellow, "to record")
			if step.Response != nil {
				state = st.paint(green, "answer from the spec")
			}
			fmt.Fprintf(out, "  %-50s %s\n", step.String(), state)
		}
	}
	recordFindings(out, st, nil, an.Notes)
	if an.Run != nil {
		section(out, st, "ORDER")
		tags := `"` + strings.Join(an.Run.Tags, `", "`) + `"`
		fmt.Fprintln(out, "  Bodies refer to records of other tags (an id field named like a value of another tag), so apitest must run")
		fmt.Fprintln(out, "  the tags in this order and every DELETE last. Set it in both places:")
		fmt.Fprintf(out, "    %s:  \"$apitest\": {\"Tags\": [%s], \"DeleteLast\": true}\n", firstDefaults(o), tags)
		fmt.Fprintf(out, "    your test:      apitest.Config{Tags: []string{%s}, DeleteLast: true, …}\n", tags)
		fmt.Fprintln(out, "  Tags limits apitest to the tags it lists; it lists every tag of the spec.")
	}
	section(out, st, "FILES")
	if len(an.Added) == 0 && exists {
		fmt.Fprintf(out, "  %s: unchanged\n", o.file)
		return nil
	}
	if o.dryRun {
		fmt.Fprintf(out, "  %s: %d entries would be added (dry run)\n", o.file, len(an.Added))
		return nil
	}
	if err := f.Save(o.file); err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s: %d entries added (%d with the answer of the spec), %d saved values to link them\n", o.file, len(an.Added), an.Complete, an.Saves)
	fmt.Fprintln(out, "\nnext: check the order and the values in "+o.file+", start an EMPTY instance, then run")
	fmt.Fprintf(out, "  apitest-gen record -spec %s -file %s -base-url <url>\n", o.spec, o.file)
	return nil
}

// steps writes what the run did with each entry: the requests it sent and
// the entries it took from the file.
func steps(out io.Writer, st style, o *options, res *record.Result) {
	if len(res.Steps) == 0 {
		return
	}
	title := "ENTRIES"
	if res.Sent > 0 {
		title = fmt.Sprintf("ENTRIES (requests to %s)", o.baseURL)
	}
	section(out, st, title)
	tag := ""
	count := map[string]int{}
	for i, sr := range res.Steps {
		if sr.Step.Tag != tag {
			tag = sr.Step.Tag
			fmt.Fprintln(out, st.paint(cyan, "  ── "+tag))
		}
		count[sr.State]++
		status := "   "
		if sr.Status != 0 {
			status = fmt.Sprint(sr.Status)
		}
		color := map[string]string{record.StateRecorded: green, record.StateSent: dim, record.StateKept: dim, record.StateDiffers: yellow,
			record.StateFailed: red, record.StateStale: yellow, record.StateNotSent: dim}[sr.State]
		what := sr.Step.String()
		if sr.URL != "" {
			what = sr.Step.Op.Method + " " + sr.URL
			if sr.Step.Name != "" {
				what += " (" + sr.Step.Name + ")"
			}
		}
		line := fmt.Sprintf("  %s %-55s %s %s", st.paint(dim, fmt.Sprintf("#%02d", i+1)), what, status, st.paint(color, sr.State))
		if sr.Why != "" {
			line += st.paint(dim, "  ("+sr.Why+")")
		}
		fmt.Fprintln(out, line)
	}
	var parts []string
	for _, s := range []string{record.StateRecorded, record.StateSent, record.StateDiffers, record.StateKept, record.StateStale, record.StateFailed, record.StateNotSent} {
		if count[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count[s], s))
		}
	}
	fmt.Fprintf(out, "  %d entries: %s; %d requests sent\n", len(res.Steps), strings.Join(parts, ", "), res.Sent)
}

// recordFindings writes the problems and notes of a run as a table.
func recordFindings(out io.Writer, st style, problems, notes []record.Note) {
	if len(problems)+len(notes) == 0 {
		return
	}
	section(out, st, "FINDINGS")
	var rows [][]cell
	for _, p := range problems {
		rows = append(rows, []cell{{"PROBLEM", red}, {p.Code, red}, {p.Where, bold}, {p.Message, ""}})
	}
	for _, n := range notes {
		rows = append(rows, []cell{{"NOTE", yellow}, {n.Code, yellow}, {n.Where, bold}, {n.Message, ""}})
	}
	table(out, st, []string{"", "CODE", "WHERE", "MESSAGE"}, rows)
}
