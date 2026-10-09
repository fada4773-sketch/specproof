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
	st := Compute(r)

	title := r.SpecTitle
	if r.SpecVersion != "" {
		title += " " + r.SpecVersion
	}
	w("# 🧪 apitest · %s\n\n", inline(title))
	result := "✅ **Passed**"
	if !r.Passed {
		result = "❌ **Failed**"
	}
	parts := []string{result, fmt.Sprintf("%d cases", st.Total)}
	for _, p := range []struct {
		n    int
		text string
	}{
		{st.Counts[Passed], "✅ %d passed"}, {st.Bad, "❌ %d failing"}, {st.Counts[Deviation], "🟡 %d deviation(s)"},
		{st.Counts[Tolerated], "🟠 %d tolerated"}, {st.Counts[Skipped] + st.Counts[NotBuildable], "⏭ %d not run"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf(p.text, p.n))
		}
	}
	parts = append(parts, "⏱ "+r.Duration.Round(time.Millisecond).String())
	cov := r.Coverage
	parts = append(parts, fmt.Sprintf("📊 %d / %d operations", cov.OperationsCovered, cov.Operations))
	w("%s\n\n", strings.Join(parts, " · "))
	if r.Aborted != "" {
		w("> ❗ **Run aborted:** %s\n\n", inline(r.Aborted))
	}
	w("| Spec | Base URL | Started | Duration | Go / apitest | Strict |\n|---|---|---|---|---|---|\n")
	w("| %s (OpenAPI %s) | %s | %s | %s | %s / %s | %s |\n\n", code(r.SpecFile), inline(r.OpenAPI), code(r.BaseURL),
		r.Started.UTC().Format(time.RFC3339), r.Duration.Round(time.Millisecond), inline(r.GoVersion), inline(r.Version), yesNo(r.Strict))
	if r.NotSelected > 0 {
		w("%d more cases were not selected by `go test -run`.\n\n", r.NotSelected)
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

	renderDashboard(&b, r, st)
	if len(st.Errors.Cases) > 0 {
		renderErrorCases(&b, r, st.Errors)
	}

	section(fmt.Sprintf("📊 Coverage: %d of %d operations (%s)", cov.OperationsCovered, cov.Operations, percent(cov.OperationsCovered, cov.Operations)), false, func() {
		w("| | Covered | Total | |\n|---|---:|---:|---|\n")
		w("| Operations with an executed case | %d | %d | %s %s |\n", cov.OperationsCovered, cov.Operations, bar(cov.OperationsCovered, cov.Operations, 20), percent(cov.OperationsCovered, cov.Operations))
		w("| Named examples executed | %d | %d | %s %s |\n\n", cov.ExamplesCovered, cov.Examples, bar(cov.ExamplesCovered, cov.Examples, 20), percent(cov.ExamplesCovered, cov.Examples))
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

	var errs, devs, tolerated, notRun, passed []Case
	for _, c := range r.Cases {
		switch {
		case IsError(c.Status):
			errs = append(errs, c)
		case c.Status == Deviation:
			devs = append(devs, c)
		case c.Status == Tolerated:
			tolerated = append(tolerated, c)
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
	if len(tolerated) > 0 {
		section(fmt.Sprintf("🟠 Tolerated error cases (%d)", len(tolerated)), false, func() {
			for _, c := range tolerated {
				renderFailure(&b, c, "🟠")
			}
		})
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
			w("| Case | Request | Status | Time |\n|---|---|---|---:|\n")
			for _, c := range passed {
				name := code(caseLabel(c))
				if c.Precondition {
					name += " (precondition)"
				}
				w("| %s | %s | %s | %s |\n", name, code(c.Method+" "+c.Target), inline(c.Actual), ms(c.Duration))
			}
			w("\n")
		})
	}
	return []byte(b.String())
}

// renderDashboard writes the figures of the run: statuses as a chart, the
// key figures, the tags, the response times, the status codes and the
// slowest requests.
func renderDashboard(b *strings.Builder, r *Report, st Stats) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("## 📈 Dashboard\n\n")
	if st.Total > 0 {
		w("```mermaid\npie showData title Cases by status\n")
		for _, s := range StatusOrder {
			if n := st.Counts[s]; n > 0 {
				w("    \"%s\" : %d\n", s, n)
			}
		}
		w("```\n\n")
	}
	run := st.Total - st.Counts[Skipped] - st.Counts[NotBuildable]
	w("| Key figure | Value |\n|---|---:|\n")
	w("| Cases that do not fail | %d of %d run (%s) |\n", st.Good, run, percent(st.Good, run))
	w("| Cases that fail | %d |\n", st.Bad)
	w("| Requests answered | %d |\n", st.Executed)
	if st.Executed > 0 {
		w("| Response time avg · p50 · p90 | %s · %s · %s |\n", ms(st.Avg), ms(st.P50), ms(st.P90))
		w("| Response time p95 · p99 · max | %s · %s · %s |\n", ms(st.P95), ms(st.P99), ms(st.Max))
		w("| Time in requests · whole run | %s · %s |\n", ms(st.Sum), ms(r.Duration))
	}
	w("\n")

	if len(st.Groups) > 0 {
		w("### Tags\n\n| Tag | Cases | ✅ | ❌ | 🟡 🟠 | ⏭ | Request time | Passed |\n|---|---:|---:|---:|---:|---:|---:|---|\n")
		for _, g := range st.Groups {
			bad, other, notRun := 0, g.Counts[Deviation]+g.Counts[Tolerated], g.Counts[Skipped]+g.Counts[NotBuildable]
			for s, n := range g.Counts {
				if IsError(s) {
					bad += n
				}
			}
			w("| %s | %d | %d | %d | %d | %d | %s | %s |\n", inline(g.Name), g.Total, g.Counts[Passed], bad, other, notRun, ms(g.Duration), bar(g.Counts[Passed], g.Total, 12))
		}
		w("\n")
	}
	if st.Executed == 0 {
		return
	}
	maxBucket := 0
	for _, h := range st.Histogram {
		maxBucket = max(maxBucket, h.Count)
	}
	w("### Response times\n\n| Range | Requests | |\n|---|---:|---|\n")
	for _, h := range st.Histogram {
		if h.Count > 0 {
			w("| %s | %d | %s |\n", h.Label, h.Count, bar(h.Count, maxBucket, 20))
		}
	}
	w("\n")
	maxCode := 0
	for _, c := range st.Codes {
		maxCode = max(maxCode, c.Count)
	}
	w("### Status codes\n\n| Code | Answers | |\n|---|---:|---|\n")
	for _, c := range st.Codes {
		w("| %s %d | %d | %s |\n", codeIcon(c.Code), c.Code, c.Count, bar(c.Count, maxCode, 20))
	}
	w("\n")
	w("### Slowest requests\n\n| # | Case | Request | Code | Time | |\n|---:|---|---|---:|---:|---|\n")
	for i, c := range st.Slowest {
		w("| %d | %s | %s | %d | %s | %s |\n", i+1, code(caseLabel(c)), code(c.Method+" "+c.Target), c.Code, ms(c.Duration), bar(int(c.Duration), int(st.Max), 12))
	}
	w("\n")
}

// renderErrorCases analyses the cases that test a documented error: per
// expected status what came, and every case with its answer.
func renderErrorCases(b *strings.Builder, r *Report, e ErrorStats) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("## 🎯 Error cases\n\n")
	w("%d error cases: **%d as documented**, **%d other**", len(e.Cases), e.AsDocumented, e.Other)
	if r.Tolerate {
		w(" (tolerated with `TolerateErrorCases`, they do not fail the test)")
	}
	w(".\n\n")
	w("| Expected ↓ · received → |")
	for _, a := range e.Actual {
		w(" %s |", a)
	}
	w("\n|---|")
	for range e.Actual {
		w("---:|")
	}
	w("\n")
	for _, x := range e.Expected {
		w("| **%s** |", x)
		for _, a := range e.Actual {
			n := e.Matrix[x][a]
			switch {
			case n == 0:
				w(" · |")
			case a == x:
				w(" ✅ %d |", n)
			default:
				w(" ⚠️ %d |", n)
			}
		}
		w("\n")
	}
	w("\n| Case | Kind | Request | Expected | Received | Result | Answer |\n|---|---|---|---:|---:|---|---|\n")
	for _, c := range e.Cases {
		got := "–"
		if c.Code != 0 {
			got = fmt.Sprint(c.Code)
		}
		icon := "✅"
		if c.Status != Passed {
			icon = statusIcon(c.Status)
		}
		w("| %s | %s | %s | %s | %s | %s %s | %s |\n", code(caseLabel(c)), inline(c.Kind), code(c.Method+" "+c.Target), inline(c.Expected), got,
			icon, inline(verdict(c)), code(excerpt(c)))
	}
	w("\n")
}

// excerpt is a short form of the answer of a case for tables.
func excerpt(c Case) string {
	text := c.Message
	if c.Response != nil {
		text = strings.Join(strings.Fields(c.Response.Text), " ")
	}
	if c.Status == Passed && c.Response == nil {
		text = ""
	}
	if r := []rune(text); len(r) > 120 {
		text = string(r[:119]) + "…"
	}
	return text
}

// bar draws n of total as a bar of width characters.
func bar(n, total, width int) string {
	if total <= 0 {
		return ""
	}
	full := (n*width + total/2) / total
	if n > 0 && full == 0 {
		full = 1
	}
	return strings.Repeat("█", full) + strings.Repeat("░", width-full)
}

func statusIcon(s string) string {
	switch {
	case s == Passed:
		return "✅"
	case IsError(s):
		return "❌"
	case s == Deviation:
		return "🟡"
	case s == Tolerated:
		return "🟠"
	}
	return "⏭"
}

func statusWord(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", " "))
}

func codeIcon(c int) string {
	switch c / 100 {
	case 2:
		return "🟢"
	case 3:
		return "🔵"
	case 4:
		return "🟠"
	}
	return "🔴"
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
	if c.Tolerated != "" {
		w("| Without tolerance | `%s` |\n", c.Tolerated)
	}
	if c.Duration > 0 {
		w("| Time | %s |\n", ms(c.Duration))
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
