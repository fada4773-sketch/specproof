package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/record"
)

// runLog is what -show-bodies writes into logFile: every request with its
// body and answer, the findings, what their codes mean and the summary.
type runLog struct {
	Spec, BaseURL string
	Started       time.Time
	Entries       []record.Entry
	Findings      []finding
	Summary       string
	Files         []string
	Err           error
}

// write renders the log as one HTML page and returns its absolute path.
func (l *runLog) write(path string) (string, error) {
	var b bytes.Buffer
	if err := logPage.Execute(&b, l.view()); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return absolute(path), nil
}

// The view of the page.
type (
	logView struct {
		Title, Spec, BaseURL, Started, Summary, Err string
		Counts                                      []countView
		Findings                                    []findingView
		Codes                                       []codeView
		Tags                                        []tagView
		Files                                       []string
		Requests                                    int
	}
	countView struct {
		Label, Class string
		N            int
	}
	findingView struct{ Label, Class, Code, Where, Message string }
	codeView    struct{ Code, Meaning, Fix string }
	tagView     struct {
		Name     string
		Requests []requestView
		Failed   int
	}
	requestView struct {
		N                   string
		Method, URL, Why    string
		Status, StatusClass string
		Failed              bool
		Sent, Answer        template.HTML
		HasSent, HasAnswer  bool
		Error               string
	}
)

func (l *runLog) view() logView {
	v := logView{Title: "apitest-gen record", Spec: l.Spec, BaseURL: l.BaseURL, Started: l.Started.Format("2006-01-02 15:04:05"),
		Summary: l.Summary, Files: l.Files, Requests: len(l.Entries)}
	if l.Err != nil {
		v.Err = l.Err.Error()
	}
	count := map[string]int{}
	for _, f := range l.Findings {
		count[f.Label]++
		v.Findings = append(v.Findings, findingView{f.Label, strings.ToLower(f.Label), f.Code, f.Where, f.Message})
	}
	for _, label := range labels {
		if count[label] > 0 {
			v.Counts = append(v.Counts, countView{label, strings.ToLower(label), count[label]})
		}
	}
	for _, c := range codesOf(l.Findings) {
		if _, meaning, fix := record.Explain(c); meaning != "" {
			v.Codes = append(v.Codes, codeView{c, meaning, fix})
		}
	}
	for _, e := range l.Entries {
		if len(v.Tags) == 0 || v.Tags[len(v.Tags)-1].Name != e.Tag {
			v.Tags = append(v.Tags, tagView{Name: e.Tag})
		}
		t := &v.Tags[len(v.Tags)-1]
		r := requestView{N: fmt.Sprintf("#%03d", e.N), Method: e.Method, URL: e.URL, Why: e.Why, Status: fmt.Sprint(e.Status), StatusClass: "ok"}
		switch {
		case e.Err != nil:
			r.Status, r.StatusClass, r.Error = "ERR", "fail", e.Err.Error()
		case e.Status/100 == 4:
			r.StatusClass = "warn"
		case e.Status/100 != 2:
			r.StatusClass = "fail"
		}
		r.Failed = r.StatusClass != "ok"
		if e.Body != nil {
			r.Sent, r.HasSent = jsonHTML(e.Body), true
		}
		if e.Resp != nil {
			r.Answer, r.HasAnswer = jsonHTML(e.Resp), true
		}
		if r.Failed {
			t.Failed++
		}
		t.Requests = append(t.Requests, r)
	}
	return v
}

// jsonHTML renders a value as indented JSON with a class per kind of
// token, every text escaped.
func jsonHTML(v any) template.HTML {
	var b strings.Builder
	writeJSON(&b, v, 0)
	return template.HTML(b.String()) //nolint:gosec // every text in it went through html.EscapeString
}

func writeJSON(b *strings.Builder, v any, depth int) {
	pad := func(d int) string { return "\n" + strings.Repeat("  ", d) }
	span := func(class, s string) {
		b.WriteString(`<span class="` + class + `">` + html.EscapeString(s) + `</span>`)
	}
	switch x := v.(type) {
	case nil:
		span("j-null", "null")
	case bool:
		span("j-bool", fmt.Sprint(x))
	case json.Number:
		span("j-num", x.String())
	case float64, int, int64:
		span("j-num", fmt.Sprint(x))
	case string:
		q, _ := json.Marshal(x)
		span("j-str", string(q))
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(pad(depth + 1))
			writeJSON(b, e, depth+1)
		}
		b.WriteString(pad(depth) + "]")
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(pad(depth + 1))
			q, _ := json.Marshal(k)
			span("j-key", string(q))
			b.WriteString(": ")
			writeJSON(b, x[k], depth+1)
		}
		b.WriteString(pad(depth) + "}")
	default:
		q, err := json.MarshalIndent(x, strings.Repeat("  ", depth), "  ")
		if err != nil {
			q = []byte(fmt.Sprint(x))
		}
		b.WriteString(html.EscapeString(string(q)))
	}
}

var logPage = template.Must(template.New("log").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} – {{.Spec}}</title>
<style>
:root{--bg:#f7f7f9;--panel:#fff;--text:#1d1f24;--dim:#6b7080;--line:#e2e4ea;--ok:#1a7f37;--warn:#9a6700;--fail:#c62828;--info:#1f5fbf;
--ok-bg:#e6f4ea;--warn-bg:#fff4d6;--fail-bg:#fde7e7;--info-bg:#e5effc;--code:#f2f3f6;--key:#7c3aed;--str:#0b7a43;--num:#b45309;--lit:#1f5fbf}
@media (prefers-color-scheme: dark){:root{--bg:#14161b;--panel:#1c1f26;--text:#e6e8ee;--dim:#9096a6;--line:#2c313c;--ok:#4ac26b;--warn:#e3b341;--fail:#ff6b6b;--info:#6ea8ff;
--ok-bg:#16301f;--warn-bg:#352a0f;--fail-bg:#3a1a1a;--info-bg:#162641;--code:#161920;--key:#c4a1ff;--str:#7ee2a8;--num:#f6b26b;--lit:#8ab4ff}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1200px;margin:0 auto;padding:24px 16px 64px}
h1{font-size:20px;margin:0 0 4px}
h2{font-size:15px;margin:0}
.meta{color:var(--dim);font-size:13px;word-break:break-all}
.chips{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0}
.chip{border-radius:999px;padding:2px 10px;font-weight:600;font-size:12px}
.fatal,.problem,.fail{color:var(--fail);background:var(--fail-bg)}
.warn{color:var(--warn);background:var(--warn-bg)}
.info{color:var(--info);background:var(--info-bg)}
.ok{color:var(--ok);background:var(--ok-bg)}
section{background:var(--panel);border:1px solid var(--line);border-radius:10px;margin:16px 0;overflow:hidden}
section>details>summary,section>.head{padding:12px 16px;cursor:pointer;display:flex;gap:10px;align-items:center}
section>details[open]>summary{border-bottom:1px solid var(--line)}
summary{list-style:none}
summary::-webkit-details-marker{display:none}
summary::before{content:"▸";color:var(--dim);display:inline-block;width:1em;transition:transform .15s}
details[open]>summary::before{transform:rotate(90deg)}
.body{padding:12px 16px}
.error{color:var(--fail);font-weight:600;white-space:pre-wrap}
table{border-collapse:collapse;width:100%}
td,th{text-align:left;vertical-align:top;padding:6px 8px;border-bottom:1px solid var(--line)}
th{color:var(--dim);font-weight:500;font-size:12px}
td.msg{white-space:pre-wrap;word-break:break-word}
.badge{display:inline-block;border-radius:6px;padding:0 6px;font-weight:700;font-size:12px}
.mono,pre,code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12.5px}
.req{border-top:1px solid var(--line)}
.req>summary{padding:6px 16px;display:flex;gap:10px;align-items:baseline;cursor:pointer}
.req>summary:hover{background:var(--code)}
.req .n{color:var(--dim)}
.req .m{width:4.5em;font-weight:700}
.req .url{word-break:break-all}
.req .why{color:var(--dim);margin-left:auto;text-align:right}
.req .pane{padding:4px 16px 12px 40px;display:grid;gap:8px}
.label{color:var(--dim);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
pre{margin:0;background:var(--code);border:1px solid var(--line);border-radius:8px;padding:10px;overflow:auto;max-height:480px}
.j-key{color:var(--key)}.j-str{color:var(--str)}.j-num{color:var(--num)}.j-bool,.j-null{color:var(--lit)}
.tools{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0}
.tools button,.tools input[type=search]{font:inherit;border:1px solid var(--line);background:var(--panel);color:var(--text);border-radius:8px;padding:4px 10px}
.tools label{display:flex;align-items:center;gap:6px}
.tools input[type=search]{flex:1;min-width:200px}
.hidden{display:none}
ul.files{margin:0;padding-left:20px}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<div class="meta">{{.Spec}} ← {{.BaseURL}} · {{.Started}} · {{.Requests}} requests</div>
<div class="chips">{{range .Counts}}<span class="chip {{.Class}}">{{.N}} {{.Label}}</span>{{else}}<span class="chip ok">no findings</span>{{end}}</div>
{{if .Err}}<section><div class="head error">{{.Err}}</div></section>{{end}}

<section><details open><summary><h2>Summary</h2></summary><div class="body">
<p class="mono">{{.Summary}}</p>
{{if .Files}}<ul class="files mono">{{range .Files}}<li>{{.}}</li>{{end}}</ul>{{end}}
</div></details></section>

{{if .Findings}}<section><details open><summary><h2>Findings</h2><span class="meta">{{len .Findings}}</span></summary>
<table><tr><th></th><th>Code</th><th>Where</th><th>Message</th></tr>
{{range .Findings}}<tr><td><span class="badge {{.Class}}">{{.Label}}</span></td><td class="mono">{{.Code}}</td><td class="mono">{{.Where}}</td><td class="msg">{{.Message}}</td></tr>
{{end}}</table></details></section>

<section><details><summary><h2>What the codes mean</h2></summary>
<table><tr><th>Code</th><th>Meaning</th><th>What to do</th></tr>
{{range .Codes}}<tr><td class="mono">{{.Code}}</td><td>{{.Meaning}}</td><td>{{.Fix}}</td></tr>
{{end}}</table></details></section>{{end}}

<div class="tools">
<button type="button" onclick="all(true)">Expand all</button>
<button type="button" onclick="all(false)">Collapse all</button>
<label><input type="checkbox" id="failed" onchange="filter()"> only failed</label>
<input type="search" id="q" placeholder="Filter by URL, method, reason, body …" oninput="filter()">
</div>

{{range .Tags}}<section class="tag"><details open><summary><h2>{{.Name}}</h2><span class="meta">{{len .Requests}} requests</span>{{if .Failed}}<span class="chip fail">{{.Failed}} failed</span>{{end}}</summary>
{{range .Requests}}<details class="req{{if .Failed}} failed{{end}}"{{if .Failed}} open{{end}}><summary class="mono"><span class="n">{{.N}}</span><span class="m">{{.Method}}</span><span class="badge {{.StatusClass}}">{{.Status}}</span><span class="url">{{.URL}}</span><span class="why">{{.Why}}</span></summary>
<div class="pane">
{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
{{if .HasSent}}<div class="label">sent</div><pre>{{.Sent}}</pre>{{end}}
{{if .HasAnswer}}<div class="label">answer</div><pre>{{.Answer}}</pre>{{else}}<div class="label">no answer body</div>{{end}}
</div></details>
{{end}}</details></section>
{{end}}
</main>
<script>
function all(open){document.querySelectorAll("details").forEach(function(d){d.open=open})}
function filter(){
  var only=document.getElementById("failed").checked, q=document.getElementById("q").value.toLowerCase();
  document.querySelectorAll("details.req").forEach(function(d){
    var hit=(!only||d.classList.contains("failed"))&&(!q||d.textContent.toLowerCase().indexOf(q)>=0);
    d.classList.toggle("hidden",!hit);
  });
  document.querySelectorAll("section.tag").forEach(function(s){
    s.classList.toggle("hidden",s.querySelectorAll("details.req:not(.hidden)").length===0);
  });
}
</script>
</body>
</html>
`))
