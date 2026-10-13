package contract

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// ReviewInput is what the review shows besides the generated scenario.
type ReviewInput struct {
	Spec, SpecVersion, BaseURL string
	Now                        time.Time
	Out                        *Output
	// Result is the run against the instance; nil if it did not run.
	Result *apitest.Result
	// Golden is the written golden snapshot; nil if none was written.
	Golden *scenario.Snapshot
	// Learn lists what could not be learned.
	Learn []string
	// Examples is what was written into the spec.
	Examples string
}

// Review renders the review list: the one file the user reads; the rest
// is checked as a git diff.
func Review(in ReviewInput) string {
	var b strings.Builder
	sc := in.Out.Scenario
	b.WriteString("# Review: contract scenario\n\n")
	fmt.Fprintf(&b, "Generated %s from %s", in.Now.Format("2006-01-02 15:04"), in.Spec)
	if in.SpecVersion != "" {
		fmt.Fprintf(&b, " %s", in.SpecVersion)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "%d steps, %d records", len(sc.Steps), len(sc.Records))
	switch {
	case in.Result == nil:
		b.WriteString("; not run.\n")
	case in.Result.Failed:
		fmt.Fprintf(&b, "; the run against %s failed, the golden snapshot was not written.\n", in.BaseURL)
	default:
		fmt.Fprintf(&b, "; every step answered as expected (%s).\n", in.BaseURL)
	}
	if in.Examples != "" {
		fmt.Fprintf(&b, "Spec: %s\n", in.Examples)
	}

	if in.Result != nil {
		var failed []apitest.CaseResult
		for _, c := range in.Result.Cases {
			switch c.Status {
			case apitest.StatusPassed, apitest.StatusDeviation, apitest.StatusTolerated:
			default:
				failed = append(failed, c)
			}
		}
		if len(failed) > 0 {
			fmt.Fprintf(&b, "\n## Steps that did not answer as expected (%d)\n\n", len(failed))
			b.WriteString("Fix the API, or tell the generator what is right in contract-overrides.yaml.\n\n")
			b.WriteString("| Step | Result | Message | Override |\n|---|---|---|---|\n")
			for _, c := range failed {
				id := c.Name[strings.Index(c.Name, "/")+1:]
				fix := fmt.Sprintf("`steps: {%s: {…}}` or `skip: [%s]`", id, id)
				if c.Status == apitest.StatusFailed && c.StatusCode != 0 {
					fix = fmt.Sprintf("`steps: {%s: {expect: %d}}`", id, c.StatusCode)
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", id, c.Status, cell(c.Message), fix)
			}
		}
	}

	out := in.Out
	fmt.Fprintf(&b, "\n## Guesses (%d)\n\n", len(out.Guesses))
	if len(out.Guesses) == 0 {
		b.WriteString("None: every decision follows from the spec.\n")
	} else {
		b.WriteString("| Step | Decision | Why | If wrong |\n|---|---|---|---|\n")
		for _, n := range out.Guesses {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cell(n.Step), cell(n.What), cell(n.Why), code(n.Fix))
		}
	}
	if len(out.NotGenerated) > 0 {
		fmt.Fprintf(&b, "\n## Not generated (%d)\n\n", len(out.NotGenerated))
		b.WriteString("| Request | Why | Add it with |\n|---|---|---|\n")
		for _, n := range out.NotGenerated {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(n.What), cell(n.Why), cell(n.Fix))
		}
	}
	if len(out.Uncovered) > 0 {
		fmt.Fprintf(&b, "\n## Operations without a step (%d)\n\n", len(out.Uncovered))
		b.WriteString("| Operation | Why | Add it with |\n|---|---|---|\n")
		for _, n := range out.Uncovered {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(n.What), cell(n.Why), cell(n.Fix))
		}
	}
	if in.Golden != nil {
		var vol []string
		for _, sh := range in.Golden.Steps {
			for _, v := range sh.Volatile {
				if !slices.Contains(vol, v) {
					vol = append(vol, v)
				}
			}
		}
		slices.Sort(vol)
		b.WriteString("\n## Volatile fields (read twice, compared only by schema)\n\n")
		if len(vol) == 0 {
			b.WriteString("None.\n")
		} else {
			b.WriteString(strings.Join(vol, ", ") + "\n")
		}
	}
	if len(in.Learn) > 0 {
		b.WriteString("\n## Not learned\n\n")
		for _, l := range in.Learn {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	return b.String()
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

func code(s string) string {
	if s == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}
