package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Render produces the Markdown report. The output depends only on r, so equal
// results give identical reports apart from time values (FR-REP-05).
func Render(r *Report) []byte {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	title := r.SpecTitle
	if r.SpecVersion != "" {
		title += " " + r.SpecVersion
	}
	w("# apitest-Report: %s\n\n", inline(title))
	w("| | |\n|---|---|\n")
	w("| Spec | %s (OpenAPI %s) |\n", code(r.SpecFile), inline(r.OpenAPI))
	w("| Base URL | %s |\n", code(r.BaseURL))
	w("| Start | %s |\n", r.Started.UTC().Format(time.RFC3339))
	w("| Duration | %s |\n", r.Duration.Round(time.Millisecond))
	w("| Go / apitest | %s / %s |\n", inline(r.GoVersion), inline(r.Version))
	w("| Strict | %s |\n", yesNo(r.Strict))
	if r.Passed {
		w("| Result | ✅ passed |\n\n")
	} else {
		w("| Result | ❌ failed |\n\n")
	}
	if r.Aborted != "" {
		w("> ❗ **Run aborted:** %s\n\n", inline(r.Aborted))
	}
	// section renders a collapsible block; open ones need attention.
	section := func(title string, open bool, body func()) {
		attr := ""
		if open {
			attr = " open"
		}
		w("<details%s><summary><b>%s</b></summary>\n\n", attr, escapeHTML(title))
		body()
		w("</details>\n\n")
	}

	if len(r.Warnings) > 0 {
		section(fmt.Sprintf("⚠️ Warnings (%d)", len(r.Warnings)), true, func() {
			for _, warn := range r.Warnings {
				w("- %s\n", inline(warn))
			}
			w("\n")
		})
	}

	// Summary
	counts := map[string]int{}
	for _, c := range r.Cases {
		counts[c.Status]++
	}
	w("## Summary\n\n| Status | Count |\n|---|---:|\n")
	for _, s := range StatusOrder {
		if counts[s] > 0 {
			w("| `%s` | %d |\n", s, counts[s])
		}
	}
	w("| **Total** | %d |\n\n", len(r.Cases))
	if r.NotSelected > 0 {
		w("%d more cases were not selected by `go test -run`.\n\n", r.NotSelected)
	}

	// Coverage
	cov := r.Coverage
	section(fmt.Sprintf("📊 Coverage: %d of %d operations (%s)", cov.OperationsCovered, cov.Operations, percent(cov.OperationsCovered, cov.Operations)), false, func() {
		w("- Operations with an executed case: %d of %d (%s)\n", cov.OperationsCovered, cov.Operations, percent(cov.OperationsCovered, cov.Operations))
		w("- Named examples executed: %d of %d (%s)\n\n", cov.ExamplesCovered, cov.Examples, percent(cov.ExamplesCovered, cov.Examples))
		if len(cov.Uncovered) > 0 {
			w("| Uncovered operation | Reason |\n|---|---|\n")
			for _, u := range cov.Uncovered {
				w("| %s | %s |\n", code(u.Operation), inline(u.Reason))
			}
			w("\n")
		}
	})

	if len(r.Findings) > 0 {
		section(fmt.Sprintf("🔎 Spec findings (%d)", len(r.Findings)), false, func() {
			w("| Location | Finding |\n|---|---|\n")
			for _, f := range r.Findings {
				w("| %s | %s |\n", code(f.Where), inline(f.Message))
			}
			w("\n")
		})
	}

	var errs, devs, notRun, passed []Case
	for _, c := range r.Cases {
		switch {
		case IsError(c.Status):
			errs = append(errs, c)
		case c.Status == Deviation:
			devs = append(devs, c)
		case c.Status == Skipped || c.Status == NotBuildable:
			notRun = append(notRun, c)
		default:
			passed = append(passed, c)
		}
	}

	if len(errs) > 0 {
		section(fmt.Sprintf("❌ Errors (%d)", len(errs)), true, func() {
			for _, c := range errs {
				renderFailure(&b, c, "❌")
			}
		})
	}
	if len(devs) > 0 || len(r.Deviations) > 0 {
		section(fmt.Sprintf("🟡 Deviations (%d)", len(devs)), true, func() { renderDeviations(&b, r, devs) })
	}
	if len(notRun) > 0 {
		section(fmt.Sprintf("⏭ Not run (%d)", len(notRun)), false, func() {
			w("| Case | Status | Reason |\n|---|---|---|\n")
			for _, c := range notRun {
				w("| %s | `%s` | %s |\n", code(caseLabel(c)), c.Status, inline(c.Message))
			}
			w("\n")
		})
	}
	if len(passed) > 0 {
		section(fmt.Sprintf("✅ Passed (%d)", len(passed)), false, func() {
			if r.PassedDetails {
				for _, c := range passed {
					renderFailure(&b, c, "✅")
				}
				return
			}
			w("| Case | Request | Status |\n|---|---|---|\n")
			for _, c := range passed {
				name := code(caseLabel(c))
				if c.Precondition {
					name += " (precondition)"
				}
				w("| %s | %s | %s |\n", name, code(c.Method+" "+c.Target), inline(c.Actual))
			}
			w("\n")
		})
	}
	return []byte(b.String())
}

// renderDeviations lists the entries of the deviations file and the cases
// they covered.
func renderDeviations(b *strings.Builder, r *Report, devs []Case) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	if len(r.Deviations) > 0 {
		w("Entries of %s:\n\n| # | Case | Deviation | Reason | Ticket | Valid until | Used | State |\n|---:|---|---|---|---|---|---:|---|\n", code(r.DeviationsFile))
		for _, d := range r.Deviations {
			w("| %d | %s | %s | %s | %s | %s | %d | %s |\n", d.Index, code(d.Case), inline(d.Rule), inline(d.Reason), inline(d.Ticket), d.Expires, d.Uses, deviationState(d))
		}
		w("\n")
	}
	for _, c := range devs {
		renderFailure(b, c, "🟡")
	}
}

func renderFailure(b *strings.Builder, c Case, icon string) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("### %s %s — %s\n\n", icon, inline(caseLabel(c)), c.Status)
	w("| | |\n|---|---|\n")
	if c.Method != "" {
		w("| Request | %s |\n", code(c.Method+" "+c.Target))
	}
	if c.Expected != "" {
		w("| Expected | %s |\n", inline(c.Expected))
	}
	if c.Actual != "" {
		w("| Actual | %s |\n", inline(c.Actual))
	}
	w("\n%s\n\n", inline(c.Message))
	if len(c.Diffs) > 0 {
		w("**Differences**\n\n| Pointer | Expected | Actual |\n|---|---|---|\n")
		for _, d := range c.Diffs {
			actual := code(d.Actual)
			if d.Note != "" {
				actual += " (" + inline(d.Note) + ")"
			}
			w("| %s | %s | %s |\n", code(d.Pointer), code(d.Expected), actual)
		}
		w("\n")
	}
	if len(c.Problems) > 0 {
		w("**Schema errors**\n\n")
		for _, p := range c.Problems {
			w("- %s\n", inline(p))
		}
		w("\n")
	}
	if c.Precondition {
		w("*This case ran only as a precondition of a selected case.*\n\n")
	}
	for _, body := range []*Body{c.Request, c.Response, c.ResponseHeaders, c.Verify} {
		if body == nil {
			continue
		}
		w("<details><summary>%s</summary>\n\n", escapeHTML(body.Title))
		fence := fenceFor(body.Text)
		w("%s%s\n%s\n%s\n", fence, body.Lang, body.Text, fence)
		if body.Original > 0 {
			w("\n*Truncated, original size %d bytes.*\n", body.Original)
		}
		w("</details>\n\n")
	}
	if c.Curl != "" {
		w("<details><summary>Reproduce</summary>\n\n```bash\n%s\n```\n</details>\n\n", c.Curl)
	}
}

func deviationState(d DeviationEntry) string {
	switch {
	case d.Expired:
		return "❗ expired"
	case d.Uses == 0:
		return "unused, can be removed"
	case d.Soon:
		return "⚠️ expires soon"
	}
	return "active"
}

// Write renders r and writes it atomically to path (FR-REP-01): a temporary
// file in the same directory is renamed over the target.
func Write(path string, r *Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	return WriteFile(path, Render(r))
}

// WriteFile writes data atomically to path.
func WriteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("write report: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// inline makes text safe for a single Markdown table cell or line.
func inline(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

// code wraps s in a code span that survives backticks in s.
func code(s string) string {
	s = inline(s)
	if s == "" {
		return ""
	}
	ticks := "`"
	for strings.Contains(s, ticks) {
		ticks += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return ticks + " " + s + " " + ticks
	}
	return ticks + s + ticks
}

func fenceFor(text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func percent(a, b int) string {
	if b == 0 {
		return "–"
	}
	return fmt.Sprintf("%d %%", a*100/b)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// caseLabel is the case name, prefixed by its number if the run numbers
// its cases ("007 Organization/createOrganization/default").
func caseLabel(c Case) string {
	if c.Number != "" {
		return c.Number + " " + c.Name
	}
	return c.Name
}
