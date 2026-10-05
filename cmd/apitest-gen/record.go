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
	fmt.Fprintf(out, "requests to %s, in the order they are sent (per tag: GET and POSTs that read, PUT/PATCH, DELETE then POST):\n", o.baseURL)
	client := &record.Client{Opt: opt, Log: func(e record.Entry) { fmt.Fprintln(out, e) }}
	res, err := record.Run(context.Background(), record.Input{Spec: s, Doc: doc, Config: cfg, Client: client,
		Prev: prev, Overwrite: o.overwrite, Writes: !o.readOnly && !o.dryRun, IgnoreLinting: o.ignoreLinting})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nrecord: %d operations, %d with new or changed schemas, %d unchanged; %d complete; %d records from %q; requests: %s\n",
		res.Stats.Ops, res.Stats.Written, res.Stats.Unchanged, res.Stats.Done, res.Stats.Reused, record.RecordedKey, counts(client.Count))
	for _, n := range res.Notes {
		// -ignorelinting: the violations are written anyway and not shown
		if o.ignoreLinting && n.Code == record.CodeLintIgnored {
			continue
		}
		fmt.Fprintf(out, "  %-17s %s: %s\n", n.Code, n.Where, indent(n.Message))
	}
	stop := 0
	for _, p := range res.Problems {
		fmt.Fprintf(out, "\n  FATAL %s %s: %s\n", p.Code, p.Where, indent(p.Message))
		if slices.Contains(blocking, p.Code) {
			stop++
		}
	}
	if stop > 0 {
		return fmt.Errorf("%d problems; nothing was written", stop)
	}
	tmp, _, err := stageSpec(doc, target)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	fmt.Fprintf(out, "spec %s: %d examples written, %d operations with moved ids\n", target, res.Stats.Examples, res.Stats.Remapped)
	if o.dryRun {
		fmt.Fprintln(out, "dry run: nothing written, only GET was sent")
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
	fmt.Fprintf(out, "%s: %q updated: %d operations complete, %d seed records the empty environment must hold\n",
		defs, record.RecordedKey, len(res.Recorded.Operations), len(res.Recorded.Seed))
	if err := record.SaveSuggestions(defs, res.Suggestions); err != nil {
		return err
	}
	if len(res.Suggestions) > 0 {
		fmt.Fprintf(out, "%s: %d suggestions in %q: what to set in \"params\", \"select\", \"seed\" or \"$apitest\" for NO_DATA and NOT_IN_CONTAINER\n",
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
