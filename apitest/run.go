package apitest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/deviations"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/plan"
	"github.com/fada4773-sketch/specproof/internal/redact"
	"github.com/fada4773-sketch/specproof/internal/spec"
	tokenpkg "github.com/fada4773-sketch/specproof/internal/token"
)

// readyTimeout bounds the readiness check (FR-HTTP-04).
const readyTimeout = 30 * time.Second

// deadlineMargin is added to the request timeout when deciding whether a
// case can still finish before the go test deadline (FR-GO-06).
const deadlineMargin = 5 * time.Second

// activeReports guards against two concurrent runs writing the same report
// (FR-REP-08). It is the only process-wide state of the package and holds no
// configuration or results.
var activeReports sync.Map

// Run executes all cases derived from the spec as subtests of t, writes the
// report and marks t as failed if a case fails. The context of the run is
// t.Context().
func Run(t *testing.T, cfg Config) *Result {
	t.Helper()
	return run(realT{t}, cfg)
}

type runner struct {
	t     tester
	cfg   Config
	start time.Time
	now   func() time.Time

	spec      *spec.Spec
	binds     *bind.Set
	plan      *plan.Plan
	cases     []*cases.Case // planned cases in execution order
	devs      *deviations.Set
	red       *redact.Redactor
	validator *spec.Validator
	client    *http.Client
	base      string
	schemes   openapi3.SecuritySchemes

	writesAllowed bool
	cutoff        time.Time
	reportPath    string

	// values holds bound values by binding key (FR-PARAM-01 rank 1).
	values map[string]any
	// producers remembers the first positive result of each producing
	// operation, to explain skipped dependents (FR-ORDER-04).
	producers map[string]*outcome
	// gone records DELETE operations whose resource an authentication case
	// deleted by mistake (FR-ORDER-07).
	gone map[string]string
	// tokenExp is the expiry of a static JWT (FR-AUTH-06).
	tokenExp time.Time
	// tokenCleaned is set once a token had to be cleaned (see cleanToken).
	tokenCleaned bool
	// fallbackWarned lists heuristic bindings replaced by Config.Params.
	fallbackWarned map[string]bool
	// numbers are the positions of the cases with Config.NumberCases.
	numbers     map[*cases.Case]int
	numberWidth int

	warnings    []string
	findings    []spec.Finding
	aborted     string
	failed      bool
	results     []*outcome
	notSelected int
}

// outcome is the result of one executed case.
type outcome struct {
	c            *cases.Case
	status       Status
	message      string
	expected     string
	actual       string
	code         int
	diffs        []compare.Diff
	problems     []spec.SchemaError
	prepared     *exec.Prepared
	token        string
	sent         http.Header // request headers as sent, after BeforeRequest
	resp         *exec.Response
	decoded      any // decoded JSON response body
	verify       *exchange
	duration     time.Duration
	precondition bool
	deviation    *deviations.Entry
	expired      bool
}

// exchange is an additional request made for a case, e.g. the GET check.
type exchange struct {
	prepared *exec.Prepared
	resp     *exec.Response
}

// fails reports whether the outcome fails the test.
func (o *outcome) fails(strict bool) bool {
	if o.status == StatusDeviation {
		return o.expired && strict
	}
	return o.status.fails(strict)
}

func run(t tester, cfg Config) *Result {
	t.Helper()
	r := &runner{
		t:         t,
		cfg:       cfg,
		start:     time.Now(),
		now:       cfg.now,
		red:       redact.New(cfg.Redact),
		validator: spec.NewValidator(),
		values:    map[string]any{},
		producers: map[string]*outcome{},
		gone:      map[string]string{},
	}
	if r.now == nil {
		r.now = time.Now
	}
	res := &Result{}

	if err := r.cfg.validate(); err != nil {
		t.Errorf("apitest: invalid configuration:\n%v", err)
		res.Failed = true
		return res
	}
	if !r.cfg.DisableReports {
		path, err := reportPath(t.Name(), r.cfg.ReportPath)
		if err != nil {
			t.Errorf("apitest: %v", err)
			res.Failed = true
			return res
		}
		if _, busy := activeReports.LoadOrStore(path, true); busy {
			t.Errorf("apitest: report path %s is already in use by another active run; set a unique Config.ReportPath per call", path)
			res.Failed = true
			return res
		}
		defer activeReports.Delete(path)
		r.reportPath = path
		res.Report = path
	}

	if err := r.prepare(); err != nil {
		r.abort(err.Error())
	} else {
		r.execute()
	}
	r.writeReport()
	return r.result(res)
}

// prepare loads the spec, plans the cases and sets up the target.
func (r *runner) prepare() error {
	ctx := r.t.Context()
	s, err := spec.Load(ctx, r.cfg.SpecPath)
	if err != nil {
		return err
	}
	r.spec = s
	r.findings = append(r.findings, s.Findings...)
	if r.cfg.Token != nil {
		if scheme, ok := s.AssumeBearer(); ok {
			msg := fmt.Sprintf("the spec declares no security, so Config.Token is sent as a bearer token to every operation (scheme %q); declare securitySchemes and security in the spec to make this explicit", scheme)
			r.findings = append(r.findings, spec.Finding{Kind: spec.FindingAuth, Where: "security", Message: msg})
			r.warn(msg)
		}
	}
	r.checkScopedParams(s)
	if s.Doc.Components != nil {
		r.schemes = s.Doc.Components.SecuritySchemes
	}
	for _, p := range exec.AllPlacements(r.schemes) {
		switch {
		case p.In == "header" && !p.Bearer:
			r.red.AddHeader(p.Name)
		case p.In == "query" || p.In == "cookie":
			r.red.AddParam(p.Name)
		}
	}

	opt := cases.Options{Tags: r.cfg.Tags, IncludeOps: r.cfg.IncludeOps, ExcludeOps: r.cfg.ExcludeOps}
	if err := cases.CheckOptions(s, opt); err != nil {
		return err
	}
	r.binds, err = bind.Resolve(s)
	if err != nil {
		return err
	}
	r.findings = append(r.findings, r.binds.Findings...)
	r.findings = append(r.findings, cases.AuthFindings(s)...)
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		return err
	}
	cases.ApplyMethodOrder(all, r.cfg.MethodOrder)
	// Config.SkipAuthCases removes cases completely: not run, not reported.
	configSelected := func(c *cases.Case) bool {
		skipped := c.Kind.IsAuth() && slices.Contains(r.cfg.SkipAuthCases, c.Example)
		return cases.Selected(c.Op, opt) && !skipped
	}
	popt := plan.Options{Tags: r.cfg.Tags, DeleteLast: r.cfg.DeleteLast}
	if r.cfg.NumberCases {
		// Numbers come from the plan without -run, so a case keeps its
		// number when only a part of the run is selected.
		full, err := plan.Build(all, configSelected, r.binds, popt)
		if err != nil {
			return err
		}
		r.numbers = map[*cases.Case]int{}
		for i, c := range full.Cases() {
			r.numbers[c] = i + 1
		}
		r.numberWidth = len(strconv.Itoa(len(r.numbers)))
	}
	selected := func(c *cases.Case) bool { return configSelected(c) && r.t.selects(r.testName(c)) }
	r.plan, err = plan.Build(all, selected, r.binds, popt)
	if err != nil {
		return err
	}
	r.cases = r.plan.Cases()
	planned := map[*cases.Case]bool{}
	for _, c := range r.cases {
		planned[c] = true
	}
	for _, c := range all {
		if configSelected(c) && !planned[c] {
			r.notSelected++
		}
	}

	if r.cfg.Token == nil {
		for _, c := range r.cases {
			if exec.ResolveAuth(c.Op.Security, r.schemes).Needed() {
				return fmt.Errorf("Config.Token missing: operation %s requires a token according to the spec (%s); pass apitest.StaticToken or apitest.TokenFunc", c.Op.ID, c.Op.Where)
			}
		}
	}
	if err := r.checkStaticToken(); err != nil {
		return err
	}
	if r.cfg.DeviationsPath != "" {
		if r.devs, err = deviations.Load(r.cfg.DeviationsPath); err != nil {
			return err
		}
		r.checkDeviationDates()
	}

	serverURL := ""
	r.client = r.cfg.HTTPClient
	if r.cfg.Handler != nil {
		srv := httptest.NewServer(r.cfg.Handler)
		r.t.Cleanup(srv.Close)
		serverURL = srv.URL
		if r.client == nil {
			r.client = srv.Client()
		}
	}
	if r.client == nil {
		r.client = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	}
	base, warning, err := exec.Base(r.cfg.BaseURL, serverURL, s.BasePath())
	if err != nil {
		return err
	}
	r.base = base
	if warning != "" {
		r.warn(warning)
	}
	r.writesAllowed = r.cfg.AllowRemoteWrites || exec.WritesAllowed(base, r.cfg.AllowedHosts)
	if d, ok := r.t.Deadline(); ok {
		r.cutoff = d.Add(-(r.cfg.RequestTimeout + deadlineMargin))
	}
	if r.cfg.Handler == nil {
		if err := exec.WaitReady(ctx, r.client, base, r.cfg.HealthPath, readyTimeout); err != nil {
			return errors.New(r.red.String(err.Error()))
		}
	}
	return nil
}

func (r *runner) warn(msg string) {
	if r.cfg.DisableWarnings {
		return
	}
	r.warnings = append(r.warnings, msg)
	r.t.Logf("apitest: %s", msg)
}

// checkDeviationDates reports expired entries (an error in strict mode,
// FR-DEV-03) and entries that expire soon (FR-DEV-05).
func (r *runner) checkDeviationDates() {
	now := r.now()
	for _, e := range r.devs.Entries {
		switch {
		case e.Expired(now) && r.cfg.Strict:
			r.failed = true
			r.t.Errorf("apitest: deviation %d (%s) expired on %s; fix the API or the spec, or extend the entry", e.Index, e.Case, e.Expires.Format(time.DateOnly))
		case e.Expired(now):
			r.warn(fmt.Sprintf("deviation %d (%s) expired on %s", e.Index, e.Case, e.Expires.Format(time.DateOnly)))
		case e.ExpiresSoon(now):
			r.warn(fmt.Sprintf("deviation %d (%s) expires on %s", e.Index, e.Case, e.Expires.Format(time.DateOnly)))
		}
	}
}

// abort ends the run with one error (FR-AUTH-03, FR-REP-01).
func (r *runner) abort(msg string) {
	msg = r.red.String(msg)
	if r.aborted == "" {
		r.aborted = msg
	}
	r.failed = true
	r.t.Errorf("apitest: %s", msg)
}

func (r *runner) execute() {
	ctx := r.t.Context()
	groupSkip := map[string]string{}
	for _, seg := range r.plan.Segments {
		if hook := r.cfg.Hooks.BeforeGroup; hook != nil && seg.First && r.aborted == "" {
			if err := safeCall("BeforeGroup", func() error { return hook(ctx, seg.Group) }); err != nil {
				groupSkip[seg.Group] = "BeforeGroup failed: " + r.red.String(err.Error())
				r.failed = true
				r.t.Errorf("apitest: group %s: %s", seg.Group, groupSkip[seg.Group])
			}
		}
		for _, c := range seg.Cases {
			r.runCase(c, groupSkip[c.Group])
		}
		if hook := r.cfg.Hooks.AfterGroup; hook != nil && seg.Last && groupSkip[seg.Group] == "" && r.aborted == "" {
			if err := safeCall("AfterGroup", func() error { return hook(ctx, seg.Group) }); err != nil {
				r.failed = true
				r.t.Errorf("apitest: group %s: AfterGroup failed: %s", seg.Group, r.red.String(err.Error()))
			}
		}
		r.writeReport()
	}
}

func (r *runner) runCase(c *cases.Case, groupSkip string) {
	do := func(ctx context.Context) *outcome {
		var o *outcome
		if groupSkip != "" {
			o = &outcome{c: c, status: StatusSkipped, message: groupSkip}
		} else {
			o = r.check(ctx, c)
		}
		r.applyDeviation(o)
		o.message = r.red.String(o.message)
		r.results = append(r.results, o)
		if c.Kind == cases.Positive && r.producers[c.Op.ID] == nil {
			r.producers[c.Op.ID] = o
		}
		return o
	}

	// Preconditions run without a subtest of their own (FR-GO-02): they were
	// not selected, but a selected case needs their values.
	if r.plan.Precondition[c] {
		o := do(r.t.Context())
		o.precondition = true
		if o.fails(r.cfg.Strict) {
			r.t.Logf("apitest: precondition %s: %s: %s", c.Name, o.status, o.message)
		}
		return
	}
	r.t.Run(r.testName(c), func(st tester) {
		o := do(st.Context())
		switch {
		case o.fails(r.cfg.Strict):
			r.failed = true
			if r.reportPath != "" {
				st.Errorf("%s: %s (details in report %s, section %q)", o.status, o.message, r.reportPath, c.Name)
			} else {
				st.Errorf("%s: %s", o.status, o.message)
			}
		case o.status == StatusSkipped || o.status == StatusNotBuildable:
			st.Skipf("%s: %s", o.status, o.message)
		case o.status == StatusDeviation:
			st.Logf("%s: %s", o.status, o.message)
		}
	})
}

// check executes one case and runs the checks of FR-CMP and FR-VERIFY.
func (r *runner) check(ctx context.Context, c *cases.Case) *outcome {
	o := &outcome{c: c}
	switch {
	case r.aborted != "":
		o.status, o.message = StatusSkipped, "run aborted: "+r.aborted
		return o
	case ctx.Err() != nil:
		o.status, o.message = StatusSkipped, "run canceled: "+ctx.Err().Error()
		return o
	case !r.cutoff.IsZero() && time.Now().After(r.cutoff):
		o.status, o.message = StatusSkipped, "go test deadline reached; the case was not started (increase -timeout)"
		return o
	case c.Skip != "":
		o.status, o.message = StatusSkipped, "x-apitest-skip: "+c.Skip
		return o
	case c.Kind == cases.Forbidden && r.cfg.ForbiddenToken == nil:
		o.status, o.message = StatusSkipped, "Config.ForbiddenToken is not set (FR-CASE-11)"
		return o
	case !c.Kind.IsAuth() && c.Op.Method == http.MethodDelete && r.gone[c.Op.ID] != "":
		o.status = StatusSkipped
		o.message = fmt.Sprintf("the resource was already deleted by %s, which the API should have rejected (FR-ORDER-07)", r.gone[c.Op.ID])
		return o
	}
	if msg := r.missingDependency(c); msg != "" {
		o.status, o.message = StatusSkipped, msg
		return o
	}

	p, token, err := r.prepareRequest(ctx, c, nil)
	o.token = token
	if err != nil {
		var nb *exec.NotBuildableError
		if errors.As(err, &nb) {
			o.status, o.message = StatusNotBuildable, nb.Reason
			return o
		}
		o.status, o.message = StatusError, err.Error()
		return o
	}
	o.prepared = p
	if exec.IsWrite(p.Method) && !r.writesAllowed {
		o.status = StatusError
		o.message = fmt.Sprintf("writing request to %s refused (protection against misuse); for test environments set Config.AllowedHosts or Config.AllowRemoteWrites", hostOf(r.base))
		return o
	}

	rctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := p.Request(rctx)
	if err != nil {
		o.status, o.message = StatusError, err.Error()
		return o
	}
	if hook := r.cfg.Hooks.BeforeRequest; hook != nil {
		hc := hookCase(c)
		if err := safeCall("BeforeRequest", func() error { return hook(rctx, hc, req) }); err != nil {
			o.status, o.message = StatusError, err.Error()
			return o
		}
	}
	o.sent = req.Header.Clone()
	resp, err := exec.Do(r.client, req, r.cfg.RequestTimeout)
	if err != nil {
		o.status, o.message = StatusError, err.Error()
		return o
	}
	o.resp, o.code, o.duration = resp, resp.Status, resp.Duration
	o.actual = strconv.Itoa(resp.Status)
	o.expected = c.Expect.String()
	o.status = StatusPassed

	r.checkResponse(o)
	if c.Kind.IsAuth() && c.Op.Method == http.MethodDelete && resp.Status/100 == 2 {
		r.gone[c.Op.ID] = c.Name
	}
	r.checkTokenExpiry(o)
	success := resp.Status/100 == 2 && c.Kind == cases.Positive
	if success {
		r.capture(o)
		r.verifyWrite(ctx, o)
	}
	if o.status != StatusPassed {
		return o
	}

	if hook := r.cfg.Hooks.AfterResponse; hook != nil {
		hc := hookCase(c)
		hr := &Response{StatusCode: resp.Status, Header: resp.Header.Clone(), Body: append([]byte(nil), resp.Body...)}
		if panicked, err := safeCallP("AfterResponse", func() error { return hook(rctx, hc, hr) }); err != nil {
			o.status, o.message = StatusFailed, "AfterResponse: "+err.Error()
			if panicked {
				o.status, o.message = StatusError, err.Error()
			}
		}
	}
	return o
}

// checkResponse runs stages 1 to 3 (FR-CMP). Each stage only runs if the
// previous one passed.
func (r *runner) checkResponse(o *outcome) {
	c, resp := o.c, o.resp

	// Stage 1: status code (FR-CMP-09 for undocumented codes).
	if !c.Expect.Matches(resp.Status) {
		o.status = StatusFailed
		if documented(c.Op.Op.Responses, resp.Status) {
			o.message = fmt.Sprintf("status code %d, expected %s", resp.Status, c.Expect)
		} else {
			o.message = fmt.Sprintf("status code %d is not documented in the spec, expected %s", resp.Status, c.Expect)
		}
		if hint := authHint(c.Kind, r.cfg.TamperToken != nil, resp.Status); hint != "" {
			o.message += "; " + hint
		}
		o.decoded = decodeJSON(resp)
		return
	}

	// Stage 2: schema.
	problems, decoded, isJSON := compare.Response(r.validator, c.Expect.Response, resp.Header, resp.Body)
	o.decoded = decoded
	if len(problems) > 0 {
		o.status, o.problems = StatusSchemaViolation, problems
		o.expected += ", schema of the spec"
		o.message = fmt.Sprintf("response violates the schema (%s, first: %s)", plural(len(problems), "error", "errors"), problems[0])
		return
	}

	// Stage 3: expected example.
	mode, err := r.mode(c)
	if err != nil {
		o.status, o.message = StatusError, "x-apitest-compare: "+err.Error()
		return
	}
	if c.Expect.HasExample && mode != compare.ModeSchema && isJSON {
		diffs := compare.Values(c.Expect.Example, decoded, compare.Options{
			Mode:      mode,
			Ignore:    r.ignore(c),
			Schema:    compare.Schema(c.Expect.Response, resp.Header.Get("Content-Type")),
			Unordered: c.Unordered,
		})
		if len(diffs) > 0 {
			o.status, o.diffs = StatusExampleMismatch, diffs
			o.expected += exampleNote(mode)
			o.actual += ", body differs"
			o.message = fmt.Sprintf("response differs from the example (%s, first at %s)", plural(len(diffs), "difference", "differences"), diffs[0].Pointer)
		}
	}
}

func (r *runner) mode(c *cases.Case) (compare.Mode, error) {
	if c.Compare != "" {
		return compare.ParseMode(c.Compare)
	}
	return compare.Mode(r.cfg.CompareMode), nil
}

func (r *runner) ignore(c *cases.Case) []string {
	return append(append([]string(nil), r.cfg.IgnoreFields...), c.Ignore...)
}

// applyDeviation turns a failing outcome into DEVIATION if an entry of the
// deviations file matches exactly (FR-DEV-01).
func (r *runner) applyDeviation(o *outcome) {
	if r.devs == nil {
		return
	}
	switch o.status {
	case StatusPassed, StatusSkipped, StatusDeviation:
		return
	}
	var pointers []string
	for _, d := range o.diffs {
		pointers = append(pointers, d.Pointer)
	}
	for _, p := range o.problems {
		pointers = append(pointers, p.Pointer)
	}
	actual := ""
	if o.code != 0 {
		actual = strconv.Itoa(o.code)
	}
	e := r.devs.Match(deviations.Outcome{
		Case:     o.c.Name,
		Status:   string(o.status),
		Expected: o.c.Expect.String(),
		Actual:   actual,
		Pointers: pointers,
	})
	if e == nil {
		// Explain entries that name the case but accept another result.
		got := string(o.status)
		if o.status == StatusFailed {
			got = o.c.Expect.String() + " → " + actual
		} else if len(pointers) > 0 {
			got += " at " + strings.Join(pointers, ", ")
		}
		for _, n := range r.devs.Near(o.c.Name) {
			o.message += fmt.Sprintf("; deviation entry %d names this case but accepts %s, the result is %s", n.Index, n.Rule(), got)
		}
		return
	}
	o.deviation = e
	o.expired = e.Expired(r.now())
	prefix := "accepted deviation"
	if o.expired {
		prefix = "EXPIRED deviation"
	}
	o.message = fmt.Sprintf("%s: %s: %s (was %s)", prefix, e.Describe(), o.message, o.status)
	o.status = StatusDeviation
}

func exampleNote(mode compare.Mode) string {
	if mode == compare.ModeExact {
		return ", body = example"
	}
	return ", body ⊇ example"
}

// documented reports whether the spec documents status for the operation.
func documented(responses *openapi3.Responses, status int) bool {
	if responses == nil {
		return false
	}
	code := strconv.Itoa(status)
	if responses.Value(code) != nil || responses.Value(code[:1]+"XX") != nil || responses.Value(code[:1]+"xx") != nil {
		return true
	}
	return responses.Default() != nil
}

func hookCase(c *cases.Case) *Case {
	return &Case{Name: c.Name, Group: c.Group, OperationID: c.Op.ID, Method: c.Op.Method, Path: c.Op.Path, Example: c.Example}
}

// safeCall runs fn and turns a panic into an error (FR-GO-05, TP-L2-19).
func safeCall(name string, fn func() error) error {
	_, err := safeCallP(name, fn)
	return err
}

// safeCallP is safeCall that also reports whether fn panicked.
func safeCallP(name string, fn func() error) (panicked bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			panicked, err = true, fmt.Errorf("panic in %s: %v", name, p)
		}
	}()
	return false, fn()
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Host
	}
	return base
}

// reportPath returns the absolute report path (FR-REP-08).
func reportPath(testName, configured string) (string, error) {
	p := configured
	if p == "" {
		p = filepath.Join("apitest-report", fileSafe(testName)+".md")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("report path %q: %w", p, err)
	}
	return abs, nil
}

func fileSafe(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
			return '_'
		}
		return r
	}, name)
}

func (r *runner) result(res *Result) *Result {
	counts := map[Status]int{}
	ops := map[string]bool{}
	covered := map[string]bool{}
	for _, c := range r.cases {
		ops[c.Op.ID] = true
	}
	for _, o := range r.results {
		counts[o.status]++
		if o.resp != nil {
			covered[o.c.Op.ID] = true
		}
		res.Cases = append(res.Cases, CaseResult{
			Name:         o.c.Name,
			Number:       r.numbers[o.c],
			Group:        o.c.Group,
			Operation:    o.c.Op.ID,
			Example:      o.c.Example,
			Status:       o.status,
			Message:      o.message,
			StatusCode:   o.code,
			Duration:     o.duration,
			Precondition: o.precondition,
		})
	}
	res.Summary = Summary{
		Counts:            counts,
		Total:             len(r.results),
		Duration:          time.Since(r.start),
		Operations:        len(ops),
		OperationsCovered: len(covered),
		NotSelected:       r.notSelected,
	}
	res.Failed = r.failed
	return res
}

func libVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	const path = "github.com/fada4773-sketch/specproof"
	if info.Main.Path == path && info.Main.Version != "" {
		return info.Main.Version
	}
	for _, d := range info.Deps {
		if d.Path == path {
			return d.Version
		}
	}
	return "(devel)"
}

func goVersion() string { return runtime.Version() }

func timeSince(t time.Time) time.Duration { return time.Since(t) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// checkStaticToken reads the expiry of a static JWT (FR-AUTH-06): an
// expired token aborts the run, a token that expires before the go test
// deadline gives a warning.
func (r *runner) checkStaticToken() error {
	st, ok := r.cfg.Token.(staticToken)
	if !ok {
		return nil
	}
	exp, ok := tokenpkg.Expiry(r.cleanToken(string(st)))
	if !ok {
		return nil
	}
	r.tokenExp = exp
	if !r.now().Before(exp) {
		return fmt.Errorf("Config.Token is a JWT that expired at %s; create a fresh token or use apitest.TokenFunc", exp.Format(time.RFC3339))
	}
	if d, ok := r.t.Deadline(); ok && exp.Before(d) {
		r.warn(fmt.Sprintf("Config.Token expires at %s, before the go test deadline %s; use apitest.TokenFunc for long runs", exp.Format(time.RFC3339), d.UTC().Format(time.RFC3339)))
	}
	return nil
}

// checkTokenExpiry aborts the run when a regular case gets 401 after the
// static token expired, instead of producing a 401 for every case.
func (r *runner) checkTokenExpiry(o *outcome) {
	if r.tokenExp.IsZero() || o.c.Kind.IsAuth() || o.resp.Status != http.StatusUnauthorized || r.now().Before(r.tokenExp) {
		return
	}
	r.aborted = fmt.Sprintf("Config.Token expired at %s during the run", r.tokenExp.Format(time.RFC3339))
	o.status, o.message = StatusError, r.aborted
}

// checkScopedParams warns about Config.Params keys of the form
// "<operationId>.<name>" that match no parameter of that operation.
func (r *runner) checkScopedParams(s *spec.Spec) {
	names := map[string]bool{}
	for _, o := range s.Ops {
		for _, p := range o.Params {
			names[p.Name] = true
			names[o.ID+"."+p.Name] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(r.cfg.Params)) {
		if strings.Contains(k, ".") && !names[k] {
			r.warn(fmt.Sprintf("Config.Params key %q matches no parameter; use \"<operationId>.<name>\" or the plain parameter name", k))
		}
	}
}

// authHint explains an authentication case that the API answered with
// success, since the redacted report does not show which token was sent.
func authHint(k cases.Kind, customTamper bool, status int) string {
	if status < 200 || status > 299 {
		return ""
	}
	switch k {
	case cases.Unauthorized:
		return "the request was sent without a token, so the API does not require authentication here"
	case cases.InvalidToken:
		if customTamper {
			return "the request was sent with the token from Config.TamperToken, so the API does not validate tokens"
		}
		return fmt.Sprintf("the request was sent with a truncated copy of the real token (at most its first %d characters, no signature), so the API accepts tokens it cannot have verified", tokenpkg.TruncateLen)
	}
	return ""
}

// testName is the subtest name of c: its case name, with Config.NumberCases
// prefixed by its position, e.g. "007_Organization/createOrganization/default".
func (r *runner) testName(c *cases.Case) string {
	if n := r.number(c); n != "" {
		return n + "_" + c.Name
	}
	return c.Name
}

// number is the zero-padded position of c, "" without Config.NumberCases.
func (r *runner) number(c *cases.Case) string {
	n, ok := r.numbers[c]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%0*d", r.numberWidth, n)
}
