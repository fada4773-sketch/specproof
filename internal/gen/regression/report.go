package regression

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// DiffReport is what the diff report shows.
type DiffReport struct {
	Pre, Post *scenario.Snapshot
	Findings  []scenario.Finding
	// Ignored lists what was not compared.
	Ignored []string
}

// Failed reports whether a finding fails.
func (d *DiffReport) Failed() bool { return scenario.Failed(d.Findings) }

func (d *DiffReport) gap() string {
	a, err1 := time.Parse(time.RFC3339, d.Pre.Created)
	b, err2 := time.Parse(time.RFC3339, d.Post.Created)
	if err1 != nil || err2 != nil {
		return ""
	}
	return b.Sub(a).Round(time.Second).String()
}

func (d *DiffReport) counts() (fails, infos int) {
	for _, f := range d.Findings {
		if f.Fails {
			fails++
		} else {
			infos++
		}
	}
	return
}

func (d *DiffReport) unchanged() []string {
	changed := map[string]bool{}
	for _, f := range d.Findings {
		changed[f.Step] = true
	}
	var out []string
	for _, sh := range d.Pre.Steps {
		if !changed[sh.ID] {
			out = append(out, sh.Request)
		}
	}
	return out
}

func version(s *scenario.Snapshot) string {
	if s.SpecVersion != "" {
		return s.SpecVersion
	}
	return "?"
}

// Markdown renders the diff report.
func (d *DiffReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Regression: %s, %s → %s\n\n", d.Post.BaseURL, version(d.Pre), version(d.Post))
	fmt.Fprintf(&b, "pre %s · post %s", d.Pre.Created, d.Post.Created)
	if g := d.gap(); g != "" {
		fmt.Fprintf(&b, " · %s apart", g)
	}
	fmt.Fprintf(&b, "\nseed %s · %d reads", d.Pre.Seed, len(d.Pre.Steps))
	for _, ss := range d.Pre.Sample {
		fmt.Fprintf(&b, " · %d %s", len(ss.Picked), ss.Name)
	}
	b.WriteString("\n\n")
	fails, infos := d.counts()
	switch {
	case fails > 0:
		fmt.Fprintf(&b, "**FAILED** – %d findings fail, %d for information.\n", fails, infos)
	case infos > 0:
		fmt.Fprintf(&b, "**PASSED** – %d findings for information.\n", infos)
	default:
		b.WriteString("**PASSED** – pre and post answer the same.\n")
	}
	if len(d.Findings) > 0 {
		b.WriteString("\n| | Finding | Request | Where | Before | After | Note |\n|---|---|---|---|---|---|---|\n")
		for _, f := range d.Findings {
			mark := "i"
			if f.Fails {
				mark = "✗"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", mark, f.Code, cell(f.Request), cell(f.Pointer), code(f.Old), code(f.New), cell(f.Message))
		}
	}
	if len(d.Ignored) > 0 {
		fmt.Fprintf(&b, "\nNot compared: %s.\n", strings.Join(d.Ignored, ", "))
	}
	if u := d.unchanged(); len(u) > 0 {
		fmt.Fprintf(&b, "\n## Unchanged (%d)\n\n%s\n", len(u), strings.Join(u, ", "))
	}
	return b.String()
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func code(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return "`" + strings.ReplaceAll(cell(s), "`", "'") + "`"
}

// HTML renders the diff report as one self-contained page.
func (d *DiffReport) HTML() ([]byte, error) {
	fails, infos := d.counts()
	data := map[string]any{
		"Title": fmt.Sprintf("Regression %s → %s", version(d.Pre), version(d.Post)),
		"Stage": d.Post.BaseURL, "Pre": d.Pre, "Post": d.Post, "Gap": d.gap(),
		"Failed": fails > 0, "Fails": fails, "Infos": infos, "Findings": d.Findings,
		"Ignored": strings.Join(d.Ignored, ", "), "Unchanged": d.unchanged(), "Reads": len(d.Pre.Steps),
	}
	var buf bytes.Buffer
	if err := diffTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var diffTemplate = template.Must(template.New("diff").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{--bg:#fbfbfa;--fg:#1d1d1b;--muted:#6b6b66;--line:#e2e1dc;--card:#fff;--bad:#b42318;--bad-bg:#fdecea;--ok:#1a7f37;--ok-bg:#e8f5ec;--info:#475467;--info-bg:#eef1f4}
@media (prefers-color-scheme: dark){:root:not([data-theme="light"]){--bg:#161615;--fg:#ecebe6;--muted:#a3a29b;--line:#34332f;--card:#1f1f1d;--bad:#ff8a80;--bad-bg:#3a1d1a;--ok:#7ee2a0;--ok-bg:#16301f;--info:#c1c7d0;--info-bg:#262a30}}
:root[data-theme="dark"]{--bg:#161615;--fg:#ecebe6;--muted:#a3a29b;--line:#34332f;--card:#1f1f1d;--bad:#ff8a80;--bad-bg:#3a1d1a;--ok:#7ee2a0;--ok-bg:#16301f;--info:#c1c7d0;--info-bg:#262a30}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1100px;margin:0 auto;padding:24px 16px}
h1{font-size:22px;margin:0 0 4px}.meta{color:var(--muted);font-size:13px}
.verdict{display:inline-block;margin:16px 0;padding:6px 12px;border-radius:6px;font-weight:600}
.fail{background:var(--bad-bg);color:var(--bad)}.pass{background:var(--ok-bg);color:var(--ok)}
.wrap{overflow-x:auto;background:var(--card);border:1px solid var(--line);border-radius:8px}
table{border-collapse:collapse;width:100%;font-size:13px}th,td{text-align:left;padding:8px 10px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-weight:600}code{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;word-break:break-all}
.tag{font-size:11px;font-weight:600;padding:2px 6px;border-radius:4px}.tag.f{background:var(--bad-bg);color:var(--bad)}.tag.i{background:var(--info-bg);color:var(--info)}
.unchanged{color:var(--muted);font-size:13px;margin-top:16px}
</style></head><body><main>
<h1>{{.Title}}</h1>
<div class="meta">{{.Stage}} · pre {{.Pre.Created}} · post {{.Post.Created}}{{if .Gap}} · {{.Gap}} apart{{end}} · seed {{.Pre.Seed}} · {{.Reads}} reads</div>
{{if .Failed}}<div class="verdict fail">FAILED · {{.Fails}} findings fail, {{.Infos}} for information</div>{{else}}<div class="verdict pass">PASSED{{if .Infos}} · {{.Infos}} findings for information{{else}} · pre and post answer the same{{end}}</div>{{end}}
{{if .Findings}}<div class="wrap"><table><thead><tr><th></th><th>Finding</th><th>Request</th><th>Where</th><th>Before</th><th>After</th><th>Note</th></tr></thead><tbody>
{{range .Findings}}<tr><td>{{if .Fails}}<span class="tag f">fails</span>{{else}}<span class="tag i">info</span>{{end}}</td><td>{{.Code}}</td><td><code>{{.Request}}</code></td><td><code>{{.Pointer}}</code></td><td><code>{{.Old}}</code></td><td><code>{{.New}}</code></td><td>{{.Message}}</td></tr>
{{end}}</tbody></table></div>{{end}}
{{if .Ignored}}<p class="meta">Not compared: {{.Ignored}}</p>{{end}}
{{if .Unchanged}}<div class="unchanged"><strong>Unchanged ({{len .Unchanged}}):</strong> {{range $i, $u := .Unchanged}}{{if $i}}, {{end}}{{$u}}{{end}}</div>{{end}}
</main></body></html>
`))
