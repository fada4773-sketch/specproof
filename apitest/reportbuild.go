package apitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/redact"
	"github.com/fada4773-sketch/specproof/internal/report"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// maxReportBody is the size above which bodies are truncated (FR-REP-06).
const maxReportBody = 64 << 10

// writeReport renders the current state. It is called after every group and
// at the end, so an aborted run leaves a partial report (FR-REP-01).
func (r *runner) writeReport() {
	if r.reportPath == "" { // Config.DisableReports
		return
	}
	rep := &report.Report{
		SpecFile:    r.cfg.SpecPath,
		BaseURL:     r.red.String(r.base),
		Started:     r.start,
		Duration:    timeSince(r.start),
		GoVersion:   goVersion(),
		Version:     libVersion(),
		Strict:      r.cfg.Strict,
		Passed:      !r.failed,
		Aborted:     r.aborted,
		Warnings:    r.warnings,
		NotSelected: r.notSelected,

		PassedDetails: r.cfg.ReportPassedDetails,
		Tolerate:      r.cfg.TolerateErrorCases,
	}
	if s := r.spec; s != nil {
		rep.SpecFile = s.Path
		rep.OpenAPI = s.Version
		if s.Doc.Info != nil {
			rep.SpecTitle, rep.SpecVersion = s.Doc.Info.Title, s.Doc.Info.Version
		}
	}
	// DisableWarnings also hides the spec findings: both are hints, not errors.
	if !r.cfg.DisableWarnings {
		for _, f := range r.findings {
			rep.Findings = append(rep.Findings, report.Finding{Where: f.Where, Message: r.red.String(f.Message)})
		}
	}
	if rep.SpecTitle == "" {
		rep.SpecTitle = "(spec not loaded)"
	}
	for _, o := range r.results {
		rep.Cases = append(rep.Cases, r.reportCase(o))
	}
	rep.Coverage = r.coverage()
	if r.devs != nil {
		rep.DeviationsFile = r.devs.Path
		now := r.now()
		for _, e := range r.devs.Entries {
			rep.Deviations = append(rep.Deviations, report.DeviationEntry{
				Index: e.Index, Case: e.Case, Rule: e.Rule(), Reason: e.Reason, Ticket: e.Ticket,
				Expires: e.Expires.Format("2006-01-02"), Uses: r.devs.Uses(e),
				Expired: e.Expired(now), Soon: e.ExpiresSoon(now),
			})
		}
	}
	if err := report.Write(r.reportPath, rep); err != nil {
		r.failed = true
		r.t.Errorf("apitest: %v", err)
	}
	if !r.cfg.DisableHTMLReport {
		if err := report.WriteHTML(htmlPath(r.reportPath), rep); err != nil {
			r.failed = true
			r.t.Errorf("apitest: %v", err)
		}
	}
	if r.cfg.ReportJSON {
		if err := r.writeJSON(); err != nil {
			r.failed = true
			r.t.Errorf("apitest: %v", err)
		}
	}
}

func (r *runner) reportCase(o *outcome) report.Case {
	rc := report.Case{
		Name:         o.c.Name,
		Number:       r.number(o.c),
		Group:        o.c.Group,
		Operation:    o.c.Op.ID,
		Status:       string(o.status),
		Kind:         o.c.Kind.String(),
		ErrorCase:    o.c.ErrorCase(),
		Tolerated:    string(o.tolerated),
		Code:         o.code,
		Duration:     o.duration,
		Message:      o.message,
		Expected:     o.expected,
		Actual:       o.actual,
		Precondition: o.precondition,
	}
	if rc.Expected == "" && o.c.Expect.Code != "" {
		rc.Expected = o.c.Expect.String()
	}
	if p := o.prepared; p != nil {
		rc.Method, rc.Target = p.Method, r.red.String(p.Target)
	}
	for _, d := range o.diffs {
		rc.Diffs = append(rc.Diffs, report.Diff{
			Pointer:  d.Pointer,
			Expected: r.red.String(d.Expected),
			Actual:   r.red.String(d.Actual),
			Note:     d.Note,
		})
	}
	for _, p := range o.problems {
		rc.Problems = append(rc.Problems, r.red.String(p.String()))
	}
	detailed := report.IsError(rc.Status) || o.status == StatusDeviation || o.status == StatusTolerated ||
		(r.cfg.ReportPassedDetails && o.status == StatusPassed)
	if detailed {
		if !r.cfg.OmitBodies {
			if p := o.prepared; p != nil && len(p.Body) > 0 {
				rc.Request = r.body("Request", p.Body, p.Header.Get("Content-Type"), writeOnlyNames(o.c))
			}
			if resp := o.resp; resp != nil {
				rc.Response = r.body(fmt.Sprintf("Response (%d)", resp.Status), resp.Body, resp.Header.Get("Content-Type"), nil)
			}
			if v := o.verify; v != nil {
				rc.Verify = r.body(fmt.Sprintf("Response (GET %s, %d)", r.red.String(v.prepared.Target), v.resp.Status), v.resp.Body, v.resp.Header.Get("Content-Type"), nil)
			}
		}
		if resp := o.resp; resp != nil {
			rc.ResponseHeaders = r.headers(resp)
		}
		if o.prepared != nil {
			rc.Curl = r.curl(o.prepared, o.sent, o.token, placeholder(o.c.Kind))
		}
	}
	return rc
}

// body prepares a body for display: JSON is redacted field by field and
// pretty-printed, other text is redacted as a string (FR-REP-03, FR-REP-06).
func (r *runner) body(title string, b []byte, contentType string, writeOnly []string) *report.Body {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	out := &report.Body{Title: title, Lang: "text"}
	var v any
	if err := spec.DecodeJSON(b, &v); err == nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if enc.Encode(r.red.Value(v, writeOnly...)) == nil {
			out.Lang, out.Text = "json", strings.TrimSuffix(buf.String(), "\n")
		}
	}
	if out.Text == "" {
		if !utf8.Valid(b) {
			out.Text = fmt.Sprintf("(binary content, %d bytes, Content-Type %q)", len(b), contentType)
			return out
		}
		out.Text = r.red.String(string(b))
	}
	if len(out.Text) > maxReportBody {
		out.Original = len(out.Text)
		cut := maxReportBody
		for cut > 0 && !utf8.RuneStart(out.Text[cut]) {
			cut--
		}
		out.Text = out.Text[:cut]
	}
	return out
}

// placeholder is the shell variable that stands for the token in curl
// commands.
func placeholder(k cases.Kind) string {
	switch k {
	case cases.InvalidToken:
		return "$INVALID_TOKEN"
	case cases.Forbidden:
		return "$FORBIDDEN_TOKEN"
	}
	return "$TOKEN"
}

// headers lists the response headers for the report; secret headers such
// as Set-Cookie are redacted.
func (r *runner) headers(resp *exec.Response) *report.Body {
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		for _, v := range resp.Header[k] {
			if r.red.SecretHeaderName(k) {
				v = redact.Mask
			}
			fmt.Fprintf(&b, "%s: %s\n", k, r.red.String(v))
		}
	}
	title := fmt.Sprintf("Response headers (%d)", resp.Status)
	if len(bytes.TrimSpace(resp.Body)) == 0 {
		title = fmt.Sprintf("Response headers (%d, no body)", resp.Status)
	}
	text := strings.TrimSuffix(b.String(), "\n")
	if text == "" {
		text = "(no headers)"
	}
	return &report.Body{Title: title, Lang: "http", Text: text}
}

// curl builds a command to reproduce the request with $BASE_URL and a token
// placeholder such as $TOKEN (FR-REP-04). sent are the headers as sent,
// including changes by BeforeRequest; nil means the prepared ones.
func (r *runner) curl(p *exec.Prepared, sent http.Header, token, ph string) string {
	withPlaceholder := func(s string) string {
		if token == "" {
			return s
		}
		s = strings.ReplaceAll(s, url.QueryEscape(token), ph)
		return strings.ReplaceAll(s, token, ph)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s %s", p.Method, dq("$BASE_URL"+r.red.String(withPlaceholder(p.Target))))
	header := sent
	if header == nil {
		header = p.Header
	}
	names := make([]string, 0, len(header))
	for k := range header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range header[k] {
			v = withPlaceholder(v)
			if r.red.SecretHeaderName(k) && !strings.Contains(v, ph) {
				v = redact.Mask
			}
			fmt.Fprintf(&b, " \\\n  -H %s", dq(k+": "+r.red.String(v)))
		}
	}
	if len(p.Body) > 0 {
		body := string(p.Body)
		var v any
		if spec.DecodeJSON(p.Body, &v) == nil {
			if red, err := json.Marshal(r.red.Value(v)); err == nil {
				body = string(red)
			}
		} else {
			body = r.red.String(body)
		}
		fmt.Fprintf(&b, " \\\n  -d %s", sq(body))
	}
	return b.String()
}

// dq quotes s for a POSIX shell in double quotes, keeping the placeholders
// $BASE_URL and the token variables expandable.
func dq(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(s)
	for _, v := range []string{"$BASE_URL", "$TOKEN", "$INVALID_TOKEN", "$FORBIDDEN_TOKEN"} {
		s = strings.ReplaceAll(s, `\`+v, v)
	}
	return `"` + s + `"`
}

// sq quotes s for a POSIX shell in single quotes.
func sq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeOnlyNames lists writeOnly properties of the request schema, which are
// redacted in the report (FR-REP-03).
func writeOnlyNames(c *cases.Case) []string {
	rb := c.Op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return nil
	}
	media := rb.Value.Content[c.MediaType]
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		return nil
	}
	var out []string
	collectWriteOnly(media.Schema.Value, &out, 0)
	return out
}

func collectWriteOnly(s *openapi3.Schema, out *[]string, depth int) {
	if depth > 12 {
		return
	}
	for name, ref := range s.Properties {
		if ref == nil || ref.Value == nil {
			continue
		}
		if ref.Value.WriteOnly {
			*out = append(*out, name)
		}
		collectWriteOnly(ref.Value, out, depth+1)
	}
	if s.Items != nil && s.Items.Value != nil {
		collectWriteOnly(s.Items.Value, out, depth+1)
	}
	for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.OneOf, s.AnyOf} {
		for _, ref := range refs {
			if ref != nil && ref.Value != nil {
				collectWriteOnly(ref.Value, out, depth+1)
			}
		}
	}
}

// htmlPath is the path of the HTML report next to the Markdown one.
func htmlPath(md string) string {
	return strings.TrimSuffix(md, filepath.Ext(md)) + ".html"
}

// coverage computes the coverage section of the report.
func (r *runner) coverage() report.Coverage {
	var cov report.Coverage
	sent := map[string]bool{}
	reason := map[string]string{}
	examples, examplesRun := 0, 0
	byName := map[string]*outcome{}
	for _, o := range r.results {
		byName[o.c.Name] = o
		if o.resp != nil {
			sent[o.c.Op.ID] = true
		} else if reason[o.c.Op.ID] == "" {
			reason[o.c.Op.ID] = string(o.status) + ": " + o.message
		}
	}
	var ops []string
	seen := map[string]bool{}
	for _, c := range r.cases {
		if c.Example != cases.DefaultExample && !c.Kind.Generated() {
			examples++
			if o, ok := byName[c.Name]; ok && o.resp != nil {
				examplesRun++
			}
		}
		if !seen[c.Op.ID] {
			seen[c.Op.ID] = true
			ops = append(ops, c.Op.ID)
		}
	}
	cov.Operations, cov.Examples, cov.ExamplesCovered = len(ops), examples, examplesRun
	for _, id := range ops {
		if sent[id] {
			cov.OperationsCovered++
			continue
		}
		why := reason[id]
		if why == "" {
			why = "not selected or not run"
		}
		cov.Uncovered = append(cov.Uncovered, report.Uncovered{Operation: id, Reason: why})
	}
	return cov
}
