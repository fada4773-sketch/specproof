package apitest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/deviations"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/redact"
	"github.com/fada4773-sketch/specproof/internal/regexgen"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// ScenarioConfig configures RunScenario: the configuration of Run plus the
// files of the scenario mode. Fields of Config that order cases (Tags,
// IncludeOps and ExcludeOps only select) are not used, since the scenario
// fixes the order: MethodOrder, DeleteLast, LastInTag and ErrorCases must
// stay unset.
type ScenarioConfig struct {
	Config

	// Settings is an optional settings file (YAML). It gives the
	// spec, the base URL, the tokens, the headers and the report; a field
	// set in Config wins. Paths in the file are relative to the file.
	Settings string
	// Scenario is the scenario file: the steps in the order they run.
	Scenario string
	// Golden is a golden snapshot file the answers are compared with. Empty
	// takes the answers the scenario file holds (its golden section, written
	// by Update), and without them checks status, schema and the checks of
	// the steps only. With Update and Golden empty the answers are written
	// into the scenario file.
	Golden string
	// Errors selects the error steps (status 400 and above) that run and
	// orders them: a status ("401", "404") or a class ("4xx", "5xx"). Error
	// steps that match no entry are left out; consecutive error steps run
	// in the order of the list, at the place the scenario gives them, so
	// the records they need still exist. Empty runs every step as written.
	Errors []string
	// Unique makes the body values the scenario marks as unique new for
	// this run, so the scenario works on an instance that is not empty.
	Unique bool
	// Update writes the golden snapshot instead of comparing with it: every
	// step must still answer as the scenario expects. A reading step is sent
	// twice; the fields that change by themselves are volatile.
	Update bool
	// OnStep is called after every step that got an answer, with what was
	// sent and received (secrets included, as sent). apitest-gen uses it
	// to write the examples into the spec.
	OnStep func(StepExchange)
}

// StepExchange is one step as sent and answered, for ScenarioConfig.OnStep.
type StepExchange struct {
	ID          string
	OperationID string
	Method      string
	// Target is path and query as sent, relative to the base URL.
	Target string
	// Path, Query and Header are the parameter values of the step with
	// the saved values filled in.
	Path, Query, Header map[string]any
	Body                any // request body; nil without
	HasBody             bool
	Status              int
	ResponseHeader      http.Header
	ResponseBody        any // decoded JSON; nil without or for other content
	Duration            time.Duration
	// Passed reports whether every check of the step passed; Outcome and
	// Message say how it ended, Problems lists the schema violations.
	Passed   bool
	Outcome  Status
	Message  string
	Problems []string
}

// RunScenario runs the steps of a scenario as subtests of t ("<tag>/<id>")
// against a Handler or a BaseURL, checks status, schema and the golden
// snapshot, writes the report and marks t as failed if a step fails.
func RunScenario(t *testing.T, cfg ScenarioConfig) *Result {
	t.Helper()
	return runScenario(realT{t}, cfg)
}

// scenarioRunner runs a scenario. It reuses the runner of Run for
// reports, deviations, tokens and redaction: every step is a case.
type scenarioRunner struct {
	*runner
	sc       *scenario.Scenario
	golden   *scenario.Snapshot
	scfg     ScenarioConfig
	steps    []*scenario.Step
	caseOf   map[*scenario.Step]*cases.Case
	vars     map[string]any
	savedBy  map[string]string // value name → step id
	sent     map[string]any    // step id → body sent
	done     map[string]*outcome
	shots    []*scenario.Shot
	unique   map[string]string // original value → value of this run
	runID    string
	needed   map[*scenario.Step]bool
	selected map[*scenario.Step]bool
}

func runScenario(t tester, cfg ScenarioConfig) *Result {
	t.Helper()
	res := &Result{}
	if err := cfg.resolve(); err != nil {
		t.Errorf("apitest: invalid configuration:\n%v", err)
		res.Failed = true
		return res
	}
	r := &runner{
		t:         t,
		cfg:       cfg.Config,
		start:     time.Now(),
		now:       cfg.now,
		red:       redact.New(cfg.Redact),
		validator: spec.NewValidator(),
		values:    map[string]any{},
		producers: map[string]*outcome{},
	}
	if r.now == nil {
		r.now = time.Now
	}
	if err := r.cfg.validate(); err != nil {
		t.Errorf("apitest: invalid configuration:\n%v", err)
		res.Failed = true
		return res
	}
	if !r.cfg.DisableReports && r.cfg.ReportPath != "" {
		path, err := reportPath(r.cfg.ReportPath)
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
	s := &scenarioRunner{
		runner: r, scfg: cfg,
		caseOf: map[*scenario.Step]*cases.Case{}, vars: map[string]any{}, savedBy: map[string]string{},
		sent: map[string]any{}, done: map[string]*outcome{}, unique: map[string]string{},
		needed: map[*scenario.Step]bool{}, selected: map[*scenario.Step]bool{},
	}
	s.runID = newRunID()
	if err := s.prepare(); err != nil {
		r.abort(err.Error())
	} else {
		s.execute()
		if cfg.Update && !r.failed && r.aborted == "" {
			if err := s.writeGolden(); err != nil {
				r.failed = true
				t.Errorf("apitest: golden snapshot: %v", err)
			}
		} else if cfg.Update {
			where := cfg.Golden
			if where == "" {
				where = "in " + cfg.Scenario
			}
			r.warn(fmt.Sprintf("the golden snapshot %s was not written: steps failed", where))
		}
	}
	r.writeReport()
	return r.result(res)
}

// resolve merges the settings file into the configuration; a field set in
// the configuration wins.
func (c *ScenarioConfig) resolve() error {
	var errs []error
	if c.MethodOrder != nil || c.DeleteLast || c.LastInTag != nil || c.ErrorCases {
		errs = append(errs, errors.New("Config.MethodOrder, DeleteLast, LastInTag and ErrorCases are not used by RunScenario: the scenario fixes the order and holds the error steps; use ScenarioConfig.Errors to choose error steps"))
	}
	if c.Scenario == "" {
		errs = append(errs, errors.New("ScenarioConfig.Scenario missing: set the scenario file (tree-test_gen.yaml)"))
	}
	for _, e := range c.Errors {
		if !errorEntry.MatchString(e) {
			errs = append(errs, fmt.Errorf("ScenarioConfig.Errors: %q is no status (\"404\") or class (\"4xx\", \"5xx\")", e))
		}
	}
	if c.Settings != "" {
		st, err := scenario.LoadSettings(c.Settings)
		if err != nil {
			errs = append(errs, fmt.Errorf("ScenarioConfig.Settings: %w", err))
		} else {
			errs = append(errs, c.merge(st)...)
		}
	}
	return errors.Join(errs...)
}

var errorEntry = regexp.MustCompile(`^[45](\d\d|[xX][xX])$`)

func (c *ScenarioConfig) merge(st *scenario.Settings) []error {
	var errs []error
	expand := func(key, v string) string {
		out, err := scenario.Expand(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("settings %s: %w", key, err))
		}
		return out
	}
	if c.SpecPath == "" && st.Spec != "" {
		c.SpecPath = st.Rel(st.Spec)
	}
	if c.BaseURL == "" && c.Handler == nil && st.BaseURL != "" {
		c.BaseURL = expand("baseURL", st.BaseURL)
	}
	if c.HealthPath == "" {
		c.HealthPath = st.Health
	}
	if c.Token == nil && st.Tokens.Default != "" {
		if tok := expand("tokens.default", st.Tokens.Default); tok != "" {
			c.Token = StaticToken(tok)
		}
	}
	if c.ForbiddenToken == nil && st.Tokens.Forbidden != "" {
		// without it the 403 steps are skipped, so a missing variable is no error
		if tok, err := scenario.Expand(st.Tokens.Forbidden); err == nil {
			c.ForbiddenToken = StaticToken(tok)
		}
	}
	if len(st.Headers) > 0 {
		h := map[string]string{}
		for k, v := range st.Headers {
			h[k] = expand("headers."+k, v)
		}
		for k, v := range c.Headers {
			h[k] = v
		}
		c.Headers = h
	}
	if c.ReportPath == "" && !c.DisableReports && st.Report != "" {
		c.ReportPath = st.Rel(st.Report)
	}
	if st.Unique {
		c.Unique = true
	}
	return errs
}

// prepare loads spec, scenario and golden snapshot, builds a case per step
// and sets up the target.
func (s *scenarioRunner) prepare() error {
	r := s.runner
	ctx := r.t.Context()
	sp, err := spec.Load(ctx, r.cfg.SpecPath)
	if err != nil {
		return err
	}
	r.spec = sp
	r.findings = append(r.findings, sp.Findings...)
	if sp.Doc.Components != nil {
		r.schemes = sp.Doc.Components.SecuritySchemes
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
	if err := cases.CheckOptions(sp, opt); err != nil {
		return err
	}
	sc, err := scenario.Load(s.scfg.Scenario)
	if err != nil {
		return err
	}
	s.sc = sc
	switch {
	case s.scfg.Update:
	case s.scfg.Golden != "":
		if s.golden, err = scenario.LoadSnapshot(s.scfg.Golden); err != nil {
			return fmt.Errorf("golden snapshot: %w; write it with ScenarioConfig.Update (apitest-gen tree run -update)", err)
		}
	default:
		// the answers the scenario file holds, if it has them
		s.golden = sc.Golden
	}
	var errs []string
	for _, st := range sc.Steps {
		op := lookupOp(sp, st.Call)
		if op == nil {
			errs = append(errs, fmt.Sprintf("%s: call %q names no operation of the spec", st.ID, st.Call))
			continue
		}
		s.caseOf[st] = cases.Step(op, st.ID, st.Expect, st.Token)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	s.steps = s.arrange(sc.Steps)
	s.choose(opt)
	for _, st := range s.steps {
		if s.needed[st] {
			r.cases = append(r.cases, s.caseOf[st])
		} else if s.configSelected(st, opt) {
			r.notSelected++
		}
	}
	if r.cfg.NumberCases {
		r.numbers = map[*cases.Case]int{}
		n := 0
		for _, st := range s.steps {
			if s.configSelected(st, opt) {
				n++
				r.numbers[s.caseOf[st]] = n
			}
		}
		r.numberWidth = len(strconv.Itoa(n))
	}
	if r.cfg.Token == nil {
		for _, c := range r.cases {
			if exec.ResolveAuth(c.Op.Security, r.schemes).Needed() {
				return fmt.Errorf("token missing: operation %s requires a token according to the spec (%s); set Config.Token, tokens.default in the settings, or -token", c.Op.ID, c.Op.Where)
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
	base, warning, err := exec.Base(r.cfg.BaseURL, serverURL, sp.BasePath())
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

// lookupOp finds an operation by operationId or "METHOD /path" (any case
// of the method).
func lookupOp(s *spec.Spec, call string) *spec.Operation {
	if op := s.Op(call); op != nil {
		return op
	}
	if f := strings.Fields(call); len(f) == 2 {
		return s.Op(strings.ToUpper(f[0]) + " " + f[1])
	}
	return nil
}

// arrange applies ScenarioConfig.Errors: error steps no entry matches are
// left out, and every run of consecutive error steps is ordered by the
// list, so they stay where the records they need exist.
func (s *scenarioRunner) arrange(all []*scenario.Step) []*scenario.Step {
	if len(s.scfg.Errors) == 0 {
		return all
	}
	rank := func(st *scenario.Step) int {
		code := strconv.Itoa(st.Expect)
		for i, e := range s.scfg.Errors {
			e = strings.ToLower(e)
			if e == code || (strings.HasSuffix(e, "xx") && e[0] == code[0]) {
				return i
			}
		}
		return -1
	}
	var out, block []*scenario.Step
	flush := func() {
		slices.SortStableFunc(block, func(a, b *scenario.Step) int { return rank(a) - rank(b) })
		out = append(out, block...)
		block = nil
	}
	for _, st := range all {
		if st.Expect < 400 {
			flush()
			out = append(out, st)
			continue
		}
		if rank(st) >= 0 {
			block = append(block, st)
		}
	}
	flush()
	return out
}

// configSelected reports whether the configuration selects a step: its
// operation by Tags, IncludeOps and ExcludeOps, and not left out by
// SkipAuthCases.
func (s *scenarioRunner) configSelected(st *scenario.Step, opt cases.Options) bool {
	c := s.caseOf[st]
	if !cases.Selected(c.Op, opt) {
		return false
	}
	switch st.Token {
	case scenario.TokenNone:
		return !slices.Contains(s.cfg.SkipAuthCases, AuthUnauthorized)
	case scenario.TokenInvalid:
		return !slices.Contains(s.cfg.SkipAuthCases, AuthInvalidToken)
	case scenario.TokenForbidden:
		return !slices.Contains(s.cfg.SkipAuthCases, AuthForbidden)
	}
	return true
}

// choose decides which steps run: the selected ones (configuration and
// "go test -run"), and the steps that give them values, bodies or the body
// to compare with, which run as preconditions without a subtest.
func (s *scenarioRunner) choose(opt cases.Options) {
	needVars := map[string]bool{}
	needIDs := map[string]bool{}
	for i := len(s.steps) - 1; i >= 0; i-- {
		st := s.steps[i]
		skippedAuth := st.Token != "" && !s.configSelected(st, opt) && cases.Selected(s.caseOf[st].Op, opt)
		sel := s.configSelected(st, opt) && s.t.selects(s.testName(s.caseOf[st]))
		provides := needIDs[st.ID]
		for name := range st.Save {
			if needVars[name] {
				provides = true
			}
		}
		if !sel && (!provides || skippedAuth) {
			continue
		}
		s.selected[st] = sel
		s.needed[st] = true
		for _, name := range st.Uses() {
			needVars[name] = true
		}
		if st.BodyFrom != "" {
			needIDs[st.BodyFrom] = true
		}
		if st.Equals != "" {
			needIDs[st.Equals] = true
		}
	}
}

// execute runs the needed steps in order, with the group hooks around each
// group of consecutive steps.
func (s *scenarioRunner) execute() {
	r := s.runner
	ctx := r.t.Context()
	var run []*scenario.Step
	for _, st := range s.steps {
		if s.needed[st] {
			run = append(run, st)
		}
	}
	first, last := map[string]int{}, map[string]int{}
	for i, st := range run {
		g := s.caseOf[st].Group
		if _, ok := first[g]; !ok {
			first[g] = i
		}
		last[g] = i
	}
	groupSkip := map[string]string{}
	for i, st := range run {
		g := s.caseOf[st].Group
		if hook := r.cfg.Hooks.BeforeGroup; hook != nil && first[g] == i && r.aborted == "" {
			if err := safeCall("BeforeGroup", func() error { return hook(ctx, g) }); err != nil {
				groupSkip[g] = "BeforeGroup failed: " + r.red.String(err.Error())
				r.failed = true
				r.t.Errorf("apitest: group %s: %s", g, groupSkip[g])
			}
		}
		s.runStep(st, groupSkip[g])
		if hook := r.cfg.Hooks.AfterGroup; hook != nil && last[g] == i && groupSkip[g] == "" && r.aborted == "" {
			if err := safeCall("AfterGroup", func() error { return hook(ctx, g) }); err != nil {
				r.failed = true
				r.t.Errorf("apitest: group %s: AfterGroup failed: %s", g, r.red.String(err.Error()))
			}
		}
	}
}

func (s *scenarioRunner) runStep(st *scenario.Step, groupSkip string) {
	r := s.runner
	c := s.caseOf[st]
	do := func(ctx context.Context) *outcome {
		var o *outcome
		if groupSkip != "" {
			o = &outcome{c: c, status: StatusSkipped, message: groupSkip}
		} else {
			o = s.check(ctx, st)
		}
		r.applyDeviation(o)
		r.tolerate(o)
		o.message = r.red.String(o.message)
		r.results = append(r.results, o)
		s.done[st.ID] = o
		s.observe(st, o)
		return o
	}
	if !s.selected[st] {
		o := do(r.t.Context())
		o.precondition = true
		if o.fails(r.cfg.Strict) {
			r.failed = true
			r.t.Errorf("apitest: precondition %s: %s: %s", c.Name, o.status, o.message)
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
		case o.status == StatusDeviation || o.status == StatusTolerated:
			st.Logf("%s: %s", o.status, o.message)
		}
	})
}

// check sends one step and checks its answer: status, schema, saved
// values, the golden snapshot and the checks of the step.
func (s *scenarioRunner) check(ctx context.Context, st *scenario.Step) *outcome {
	r := s.runner
	c := s.caseOf[st]
	o := &outcome{c: c, expected: strconv.Itoa(st.Expect)}
	switch {
	case r.aborted != "":
		o.status, o.message = StatusSkipped, "run aborted: "+r.aborted
		return o
	case ctx.Err() != nil:
		o.status, o.message = StatusSkipped, "run canceled: "+ctx.Err().Error()
		return o
	case !r.cutoff.IsZero() && time.Now().After(r.cutoff):
		o.status, o.message = StatusSkipped, "go test deadline reached; the step was not started (increase -timeout)"
		return o
	case st.Token == scenario.TokenForbidden && r.cfg.ForbiddenToken == nil:
		o.status, o.message = StatusSkipped, "no forbidden token: set Config.ForbiddenToken, tokens.forbidden in the settings, or -forbidden-token"
		return o
	}
	if msg := s.dependency(st); msg != "" {
		o.status, o.message = StatusSkipped, msg
		return o
	}
	p, token, err := s.prepareStep(ctx, st, c)
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
	resp, sent, err := s.send(ctx, c, p)
	o.sent = sent
	if err != nil {
		o.status, o.message = StatusError, err.Error()
		return o
	}
	o.resp, o.code, o.duration = resp, resp.Status, resp.Duration
	o.actual = strconv.Itoa(resp.Status)
	o.status = StatusPassed
	o.decoded = decodeJSON(resp)

	if resp.Status != st.Expect {
		o.status = StatusFailed
		if documented(c.Op.Op.Responses, resp.Status) {
			o.message = fmt.Sprintf("status code %d, expected %d", resp.Status, st.Expect)
		} else {
			o.message = fmt.Sprintf("status code %d is not documented in the spec, expected %d", resp.Status, st.Expect)
		}
		if hint := authHint(c.Kind, r.cfg.TamperToken != nil, resp.Status); hint != "" {
			o.message += "; " + hint
		}
		r.checkTokenExpiry(o)
		return o
	}
	if c.Expect.Response == nil {
		r.warn(fmt.Sprintf("%s: the spec documents no response %d for %s; only the status is checked", st.ID, st.Expect, c.Op.ID))
	} else {
		problems, decoded, _ := compare.Response(r.validator, c.Expect.Response, resp.Header, resp.Body)
		o.decoded = decoded
		if len(problems) > 0 {
			o.status, o.problems = StatusSchemaViolation, problems
			o.expected += ", schema of the spec"
			o.message = fmt.Sprintf("response violates the schema (%s, first: %s)", plural(len(problems), "error", "errors"), problems[0])
			return o
		}
	}
	if missing := s.save(st, resp, o.decoded); len(missing) > 0 {
		o.status = StatusError
		o.message = fmt.Sprintf("save: no value at %s; later steps that need it are skipped", strings.Join(missing, ", "))
		return o
	}
	switch {
	case s.scfg.Update:
		s.record(ctx, st, c, p, resp, o.decoded)
	case s.golden != nil && !s.compareGolden(st, c, o):
		return o
	}
	if !s.checks(st, c, o) {
		return o
	}
	if hook := r.cfg.Hooks.AfterResponse; hook != nil {
		hc := s.hookCaseOf(c)
		hr := &Response{StatusCode: resp.Status, Header: resp.Header.Clone(), Body: append([]byte(nil), resp.Body...)}
		if panicked, err := safeCallP("AfterResponse", func() error { return hook(ctx, hc, hr) }); err != nil {
			o.status, o.message = StatusFailed, "AfterResponse: "+err.Error()
			if panicked {
				o.status, o.message = StatusError, err.Error()
			}
		}
	}
	return o
}

// observe passes a step with an answer to ScenarioConfig.OnStep.
func (s *scenarioRunner) observe(st *scenario.Step, o *outcome) {
	if s.scfg.OnStep == nil || o.resp == nil {
		return
	}
	path, _ := scenario.Fill(anyOrNil(st.Path), s.vars)
	query, _ := scenario.Fill(anyOrNil(st.Query), s.vars)
	hdr, _ := scenario.Fill(anyOrNil(st.Header), s.vars)
	ex := StepExchange{
		ID: st.ID, OperationID: o.c.Op.ID, Method: o.c.Op.Method,
		Status: o.resp.Status, ResponseHeader: o.resp.Header.Clone(), ResponseBody: s.originals(o.decoded),
		Passed: o.status == StatusPassed, Outcome: o.status, Message: o.message, Duration: o.duration,
	}
	if o.prepared != nil {
		ex.Target = o.prepared.Target
	}
	for _, p := range o.problems {
		ex.Problems = append(ex.Problems, p.String())
	}
	ex.Path, _ = s.originals(path).(map[string]any)
	ex.Query, _ = s.originals(query).(map[string]any)
	ex.Header, _ = hdr.(map[string]any)
	if b, ok := s.sent[st.ID]; ok {
		ex.Body, ex.HasBody = s.originals(b), true
	}
	s.scfg.OnStep(ex)
}

// dependency explains why a step cannot run: a value, body or step it
// needs is missing because the step that gives it failed or was skipped.
func (s *scenarioRunner) dependency(st *scenario.Step) string {
	for _, name := range st.Uses() {
		if _, ok := s.vars[name]; ok {
			continue
		}
		by := s.savedBy[name]
		if by == "" {
			by = s.saver(name)
		}
		if o := s.done[by]; o != nil {
			return fmt.Sprintf("dependency failed: %s is %s (%s), so {{%s}} has no value", by, o.status, o.message, name)
		}
		return fmt.Sprintf("dependency not available: {{%s}} is saved by %s, which did not run", name, by)
	}
	for _, id := range []string{st.BodyFrom, st.Equals} {
		if id == "" {
			continue
		}
		if o := s.done[id]; o == nil {
			return fmt.Sprintf("dependency not available: %s did not run", id)
		} else if _, ok := s.sent[id]; !ok {
			return fmt.Sprintf("dependency failed: %s is %s, so its body is unknown", id, o.status)
		}
	}
	return ""
}

// saver is the step that saves a value, by the scenario.
func (s *scenarioRunner) saver(name string) string {
	for _, st := range s.sc.Steps {
		if _, ok := st.Save[name]; ok {
			return st.ID
		}
	}
	return "(no step)"
}

// prepareStep builds the request of a step: placeholders filled in,
// parameters serialized by the rules of the spec, the token of the step.
func (s *scenarioRunner) prepareStep(ctx context.Context, st *scenario.Step, c *cases.Case) (*exec.Prepared, string, error) {
	r := s.runner
	op := c.Op
	auth := exec.ResolveAuth(op.Security, r.schemes)
	if auth.Unsupported != "" {
		return nil, "", &exec.NotBuildableError{Reason: auth.Unsupported}
	}
	token := ""
	if auth.Needed() && st.Token != scenario.TokenNone {
		src := r.cfg.Token
		if st.Token == scenario.TokenForbidden {
			src = r.cfg.ForbiddenToken
		}
		if src == nil {
			return nil, "", errors.New("no token: the operation requires one; set Config.Token, tokens.default in the settings, or -token")
		}
		tok, err := src.Token(ctx)
		if err != nil {
			r.aborted = "token source returned an error: " + r.red.String(err.Error())
			return nil, "", errors.New(r.aborted)
		}
		tok = r.cleanToken(tok)
		r.red.AddSecret(tok)
		token = tok
		if st.Token == scenario.TokenInvalid {
			token = r.tamper(tok)
			if token == tok || token == "" {
				return nil, "", errors.New("Config.TamperToken must return a different, non-empty token")
			}
			r.red.AddSecret(token)
		}
	}
	if st.Token == scenario.TokenNone {
		auth.Placements = nil
	}
	path, _ := scenario.Fill(anyOrNil(st.Path), s.vars)
	query, _ := scenario.Fill(anyOrNil(st.Query), s.vars)
	hdr, _ := scenario.Fill(anyOrNil(st.Header), s.vars)
	pm, _ := path.(map[string]any)
	qm, _ := query.(map[string]any)
	hm, _ := hdr.(map[string]any)

	target := op.Path
	var pairs []params.Pair
	header := http.Header{}
	fixed := func(p *openapi3.Parameter) (any, bool) {
		if v, ok := r.cfg.Params[op.ID+"."+p.Name]; ok {
			return v, true
		}
		v, ok := r.cfg.Params[p.Name]
		return v, ok
	}
	given := map[string]map[string]any{openapi3.ParameterInPath: pm, openapi3.ParameterInQuery: qm, openapi3.ParameterInHeader: hm}
	for in, m := range given {
		for name := range m {
			if in != openapi3.ParameterInHeader && findParam(op, in, name) == nil {
				return nil, "", &exec.NotBuildableError{Reason: fmt.Sprintf("%s parameter %q is not declared for %s in the spec", in, name, op.ID)}
			}
		}
	}
	for _, p := range op.Params {
		v, ok := given[p.In][p.Name]
		if !ok {
			v, ok = fixed(p)
		}
		if !ok {
			if p.Required || p.In == openapi3.ParameterInPath {
				return nil, "", &exec.NotBuildableError{Reason: fmt.Sprintf("no value for required %s parameter %q; set it in the step (%s)", p.In, p.Name, st.ID)}
			}
			continue
		}
		v = coerceParam(v, p)
		switch p.In {
		case openapi3.ParameterInPath:
			sv, err := params.Path(p, v)
			if err != nil {
				return nil, "", &exec.NotBuildableError{Reason: err.Error()}
			}
			target = strings.ReplaceAll(target, "{"+p.Name+"}", sv)
		case openapi3.ParameterInQuery:
			ps, err := params.Query(p, v)
			if err != nil {
				return nil, "", &exec.NotBuildableError{Reason: err.Error()}
			}
			pairs = append(pairs, ps...)
		case openapi3.ParameterInHeader:
			sv, err := params.Header(p, v)
			if err != nil {
				return nil, "", &exec.NotBuildableError{Reason: err.Error()}
			}
			header.Set(p.Name, sv)
		}
	}
	// headers the spec does not declare as parameters are sent as they are
	for name, v := range hm {
		if findParam(op, openapi3.ParameterInHeader, name) == nil {
			header.Set(name, scenario.Text(v))
		}
	}
	for k, v := range r.cfg.Headers {
		if header.Get(k) == "" {
			header.Set(k, v)
		}
	}
	if auth.Needed() {
		for _, pl := range auth.Placements {
			switch pl.In {
			case "header":
				if pl.Bearer {
					header.Set("Authorization", "Bearer "+token)
				} else {
					header.Set(pl.Name, token)
				}
			case "query":
				pairs = append(pairs, params.Pair{Key: pl.Name, Value: token})
			case "cookie":
				header.Add("Cookie", pl.Name+"="+token)
			}
		}
	}
	var body []byte
	if st.HasBody || st.BodyFrom != "" {
		var v any
		if st.BodyFrom != "" {
			v = scenario.Clone(s.sent[st.BodyFrom])
		} else {
			v, _ = scenario.Fill(st.Body, s.vars)
			v = coerceSchema(scenario.Clone(v), requestSchema(c))
		}
		v = s.makeUnique(st, c, v)
		v = spec.Normalize(v)
		s.sent[st.ID] = v
		mt := c.MediaType
		if mt == "" {
			mt = "application/json"
		}
		b, err := encodeStepBody(mt, v)
		if err != nil {
			return nil, "", &exec.NotBuildableError{Reason: err.Error()}
		}
		body = b
		header.Set("Content-Type", mt)
	}
	if resp := c.Expect.Response; resp != nil && len(resp.Content) > 0 && header.Get("Accept") == "" {
		mt, _ := cases.PickMedia(resp.Content)
		header.Set("Accept", mt)
	}
	if len(pairs) > 0 {
		target += "?" + params.EncodeQuery(pairs)
	}
	return &exec.Prepared{Method: op.Method, URL: r.base + target, Target: target, Header: header, Body: body, Auth: auth}, token, nil
}

func anyOrNil(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func findParam(op *spec.Operation, in, name string) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.In == in && (p.Name == name || in == openapi3.ParameterInHeader && strings.EqualFold(p.Name, name)) {
			return p
		}
	}
	return nil
}

// coerceParam converts a value into the type of its parameter: a number
// saved from an answer into the text of a string parameter.
func coerceParam(v any, p *openapi3.Parameter) any {
	if p.Schema == nil || p.Schema.Value == nil {
		return v
	}
	sch := p.Schema.Value
	switch {
	case sch.Type.Is("string"):
		if n, ok := v.(json.Number); ok {
			return n.String()
		}
	case sch.Type.Is("integer") || sch.Type.Is("number"):
		if str, ok := v.(string); ok {
			if _, err := strconv.ParseFloat(str, 64); err == nil {
				return json.Number(str)
			}
		}
	}
	return v
}

// coerceSchema converts the scalars of a filled value into the types of its
// schema, as coerceParam does for a parameter: a saved value is text when
// it came from a header (the id of a Location), the field may be a number.
func coerceSchema(v any, sch *openapi3.Schema) any {
	if sch == nil {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			x[k] = coerceSchema(c, schemaProperty(sch, k))
		}
		return x
	case []any:
		if sch.Items == nil || sch.Items.Value == nil {
			return x
		}
		for i, c := range x {
			x[i] = coerceSchema(c, sch.Items.Value)
		}
		return x
	}
	switch {
	case sch.Type.Is("string"):
		if n, ok := v.(json.Number); ok {
			return n.String()
		}
	case sch.Type.Is("integer") || sch.Type.Is("number"):
		if str, ok := v.(string); ok {
			if _, err := strconv.ParseFloat(str, 64); err == nil {
				return json.Number(str)
			}
		}
	}
	return v
}

// schemaProperty is the schema of property k, also in allOf, oneOf and
// anyOf; nil without.
func schemaProperty(s *openapi3.Schema, k string) *openapi3.Schema {
	if s == nil {
		return nil
	}
	if ref := s.Properties[k]; ref != nil && ref.Value != nil {
		return ref.Value
	}
	for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.OneOf, s.AnyOf} {
		for _, ref := range refs {
			if ref != nil && ref.Value != nil {
				if p := schemaProperty(ref.Value, k); p != nil {
					return p
				}
			}
		}
	}
	return nil
}

// requestSchema is the schema of the body a case sends, nil without.
func requestSchema(c *cases.Case) *openapi3.Schema {
	if c == nil || c.Op == nil || c.Op.Op == nil || c.Op.Op.RequestBody == nil || c.Op.Op.RequestBody.Value == nil {
		return nil
	}
	mt := c.MediaType
	if mt == "" {
		mt = "application/json"
	}
	if media := c.Op.Op.RequestBody.Value.Content.Get(mt); media != nil && media.Schema != nil {
		return media.Schema.Value
	}
	return nil
}

func encodeStepBody(mt string, v any) ([]byte, error) {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mt)), "application/x-www-form-urlencoded") {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("form body must be an object")
		}
		var pairs []params.Pair
		for _, k := range scenarioKeys(obj) {
			pairs = append(pairs, params.Pair{Key: k, Value: params.Scalar(obj[k])})
		}
		return []byte(params.EncodeQuery(pairs)), nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("cannot encode body as JSON: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func scenarioKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// send sends a prepared request through the BeforeRequest hook.
func (s *scenarioRunner) send(ctx context.Context, c *cases.Case, p *exec.Prepared) (*exec.Response, http.Header, error) {
	r := s.runner
	rctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := p.Request(rctx)
	if err != nil {
		return nil, nil, err
	}
	if hook := r.cfg.Hooks.BeforeRequest; hook != nil {
		hc := s.hookCaseOf(c)
		if err := safeCall("BeforeRequest", func() error { return hook(rctx, hc, req) }); err != nil {
			return nil, req.Header.Clone(), err
		}
	}
	sent := req.Header.Clone()
	resp, err := exec.Do(r.client, req, r.cfg.RequestTimeout)
	return resp, sent, err
}

func (s *scenarioRunner) hookCaseOf(c *cases.Case) *Case {
	return &Case{Name: c.Name, Group: c.Group, OperationID: c.Op.ID, Method: c.Op.Method, Path: c.Op.Path, Example: c.Example}
}

// save keeps the values a step saves; missing lists the sources without
// value.
func (s *scenarioRunner) save(st *scenario.Step, resp *exec.Response, body any) (missing []string) {
	reqBody := s.sent[st.ID]
	path, _ := scenario.Fill(anyOrNil(st.Path), s.vars)
	query, _ := scenario.Fill(anyOrNil(st.Query), s.vars)
	got := map[string]any{}
	for _, name := range scenarioKeys(anyMap(st.Save)) {
		from := st.Save[name]
		v, ok := source(from, resp.Header, body, reqBody, path, query)
		if !ok {
			missing = append(missing, from)
			continue
		}
		got[name] = v
	}
	if len(missing) > 0 {
		return missing
	}
	for name, v := range got {
		s.vars[name] = v
		s.savedBy[name] = st.ID
	}
	return nil
}

func anyMap(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// source reads the value of a save source.
func source(from string, header http.Header, body, reqBody, path, query any) (any, bool) {
	if rest, ok := strings.CutPrefix(from, "request "); ok {
		if name, ok := strings.CutPrefix(rest, "path "); ok {
			m, _ := path.(map[string]any)
			v, found := m[name]
			return v, found
		}
		if name, ok := strings.CutPrefix(rest, "query "); ok {
			m, _ := query.(map[string]any)
			v, found := m[name]
			return v, found
		}
		if rest == "/" {
			return reqBody, reqBody != nil
		}
		return bind.Pointer(reqBody, rest)
	}
	if name, ok := strings.CutPrefix(from, "header "); ok {
		h := header.Get(name)
		if h == "" {
			return nil, false
		}
		if strings.EqualFold(name, "Location") {
			return bind.LastSegment(h), true
		}
		return h, true
	}
	if from == "/" {
		return body, body != nil
	}
	return bind.Pointer(body, from)
}

// compareGolden compares the answer with the golden snapshot: the body as
// a subset (or exact, Config.CompareMode), the compared headers, with the
// saved values of this run filled in and the volatile fields left out.
func (s *scenarioRunner) compareGolden(st *scenario.Step, c *cases.Case, o *outcome) bool {
	r := s.runner
	sh := s.golden.Shot(st.ID)
	if sh == nil {
		o.status = StatusFailed
		o.message = "the golden snapshot has no answer for this step; write it again with \"apitest-gen tree run -update\""
		return false
	}
	o.expected += ", golden snapshot"
	if sh.Status != 0 && sh.Status != o.code {
		o.status = StatusFailed
		o.message = fmt.Sprintf("status code %d, the golden snapshot has %d", o.code, sh.Status)
		return false
	}
	var diffs []compare.Diff
	for _, name := range scenarioKeys(anyMap(sh.Headers)) {
		if slices.ContainsFunc(sh.Volatile, func(v string) bool { return strings.EqualFold(v, "header "+name) }) || volatileHeader(name) {
			continue
		}
		want, _ := scenario.Fill(s.uniqueValue(sh.Headers[name]), s.vars)
		got := o.resp.Header.Get(name)
		if scenario.Text(want) != got {
			diffs = append(diffs, compare.Diff{Pointer: "header:" + name, Expected: scenario.Text(want), Actual: got})
		}
	}
	mode, err := r.mode(c)
	if err != nil {
		o.status, o.message = StatusError, "x-apitest-compare: "+err.Error()
		return false
	}
	if sh.HasBody && mode != compare.ModeSchema {
		want, _ := scenario.Fill(s.uniqueValues(sh.Body), s.vars)
		ignore := append(r.ignore(c), sh.Volatile...)
		schema := compare.Schema(c.Expect.Response, o.resp.Header.Get("Content-Type"))
		diffs = append(diffs, compare.Values(spec.Normalize(coerceSchema(scenario.Clone(want), schema)), o.decoded, compare.Options{
			Mode:       mode,
			Ignore:     ignore,
			Schema:     schema,
			Unordered:  true,
			IgnoreCase: r.cfg.CaseInsensitive,
		})...)
	}
	if len(diffs) > 0 {
		o.status, o.diffs = StatusExampleMismatch, diffs
		o.actual += ", body differs"
		o.message = fmt.Sprintf("response differs from the golden snapshot (%s, first at %s)", plural(len(diffs), "difference", "differences"), diffs[0].Pointer)
		return false
	}
	return true
}

// volatileHeader names headers that differ from run to run by nature.
func volatileHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Date", "Etag", "Last-Modified", "Expires", "Age", "X-Request-Id", "Set-Cookie":
		return true
	}
	return false
}

// checks runs the checks of a step: "equals" (the answer holds what an
// earlier step sent) and "contains" (a list answer holds these records).
func (s *scenarioRunner) checks(st *scenario.Step, c *cases.Case, o *outcome) bool {
	r := s.runner
	if st.Equals != "" {
		want := prune(scenario.Clone(s.sent[st.Equals]), compare.Schema(c.Expect.Response, o.resp.Header.Get("Content-Type")), 0)
		diffs := compare.Values(want, o.decoded, compare.Options{
			Mode:       compare.ModeSubset,
			Ignore:     r.ignore(c),
			Schema:     compare.Schema(c.Expect.Response, o.resp.Header.Get("Content-Type")),
			Unordered:  true,
			IgnoreCase: r.cfg.CaseInsensitive,
		})
		if len(diffs) > 0 {
			o.status, o.diffs = StatusDataMismatch, diffs
			o.expected += ", body of " + st.Equals
			o.message = fmt.Sprintf("the answer does not hold what %s sent (%s, first at %s)", st.Equals, plural(len(diffs), "difference", "differences"), diffs[0].Pointer)
			return false
		}
	}
	if ct := st.Contains; ct != nil {
		list := listIn(o.decoded)
		if list == nil {
			o.status, o.message = StatusDataMismatch, "check.contains: the answer holds no list"
			return false
		}
		var missing []string
		for _, want := range ct.Values {
			wv, _ := scenario.Fill(want, s.vars)
			found := slices.ContainsFunc(list, func(el any) bool {
				v, ok := bind.Pointer(el, ct.Key)
				return ok && scenario.Text(v) == scenario.Text(wv)
			})
			if !found {
				missing = append(missing, ct.Key+"="+scenario.Text(wv))
			}
		}
		if len(missing) > 0 {
			o.status = StatusDataMismatch
			o.message = fmt.Sprintf("the list misses %s", strings.Join(missing, ", "))
			return false
		}
	}
	return true
}

// listIn is the list in an answer: the answer itself, or its one field
// that is a list.
func listIn(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		var found []any
		n := 0
		for _, c := range x {
			if l, ok := c.([]any); ok {
				found = l
				n++
			}
		}
		if n == 1 {
			return found
		}
	}
	return nil
}

// prune removes from a body sent the fields the answer cannot hold: those
// its schema does not declare and writeOnly ones.
func prune(v any, s *openapi3.Schema, depth int) any {
	if s == nil || depth > 20 {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		props := allProperties(s)
		if len(props) == 0 {
			return v
		}
		for k, c := range x {
			p := props[k]
			if p == nil || p.Value == nil || p.Value.WriteOnly {
				delete(x, k)
				continue
			}
			x[k] = prune(c, p.Value, depth+1)
		}
	case []any:
		if s.Items != nil {
			for i, c := range x {
				x[i] = prune(c, s.Items.Value, depth+1)
			}
		}
	}
	return v
}

func allProperties(s *openapi3.Schema) openapi3.Schemas {
	out := openapi3.Schemas{}
	var add func(x *openapi3.Schema, depth int)
	add = func(x *openapi3.Schema, depth int) {
		if x == nil || depth > 20 {
			return
		}
		for k, p := range x.Properties {
			if out[k] == nil {
				out[k] = p
			}
		}
		for _, part := range x.AllOf {
			add(part.Value, depth+1)
		}
	}
	add(s, 0)
	return out
}

// record keeps the answer of a step for the golden snapshot. A reading
// step is sent once more: what changed is volatile.
func (s *scenarioRunner) record(ctx context.Context, st *scenario.Step, c *cases.Case, p *exec.Prepared, resp *exec.Response, decoded any) {
	vars := s.reverseVars()
	sh := &scenario.Shot{ID: st.ID, Op: c.Op.ID, Request: c.Op.Method + " " + scenario.TemplatedTarget(s.original(p.Target), vars), Status: resp.Status}
	if loc := resp.Header.Get("Location"); loc != "" {
		sh.Headers = map[string]string{"Location": scenario.TemplatedHeader("Location", s.original(loc), vars)}
	}
	if r := c.Expect.Response; r != nil {
		for name := range r.Headers {
			if v := resp.Header.Get(name); v != "" && !strings.EqualFold(name, "Content-Type") {
				if sh.Headers == nil {
					sh.Headers = map[string]string{}
				}
				sh.Headers[http.CanonicalHeaderKey(name)] = scenario.TemplatedHeader(name, s.original(v), vars)
			}
		}
	}
	if len(bytes.TrimSpace(resp.Body)) > 0 && decoded != nil {
		sh.Body, sh.HasBody = scenario.Templated(s.originals(decoded), vars), true
	}
	if c.Op.Method == http.MethodGet && resp.Status/100 == 2 && decoded != nil {
		again, _, err := s.send(ctx, c, p)
		if err == nil && again.Status == resp.Status {
			var second any
			if spec.DecodeJSON(again.Body, &second) == nil {
				for _, ptr := range changed("", decoded, second) {
					g := scenario.Generalize(ptr)
					if !slices.Contains(sh.Volatile, g) {
						sh.Volatile = append(sh.Volatile, g)
					}
				}
			}
		}
	}
	for name := range sh.Headers {
		if volatileHeader(name) && !slices.Contains(sh.Volatile, "header "+name) {
			sh.Volatile = append(sh.Volatile, "header "+name)
		}
	}
	slices.Sort(sh.Volatile)
	s.shots = append(s.shots, sh)
}

// changed lists the JSON pointers where two values differ.
func changed(ptr string, a, b any) []string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return []string{ptrOrRoot(ptr)}
		}
		var out []string
		for _, k := range scenarioKeys(x) {
			out = append(out, changed(ptr+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), x[k], y[k])...)
		}
		return out
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return []string{ptrOrRoot(ptr)}
		}
		var out []string
		for i := range x {
			out = append(out, changed(fmt.Sprintf("%s/%d", ptr, i), x[i], y[i])...)
		}
		return out
	}
	if !compare.Equal(a, b) {
		return []string{ptrOrRoot(ptr)}
	}
	return nil
}

func ptrOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// reverseVars are the saved values as the golden snapshot keeps them:
// unique values in their original form.
func (s *scenarioRunner) reverseVars() map[string]any {
	out := map[string]any{}
	for k, v := range s.vars {
		if str, ok := v.(string); ok {
			out[k] = s.original(str)
		} else {
			out[k] = v
		}
	}
	return out
}

func (s *scenarioRunner) writeGolden() error {
	if s.scfg.Golden == "" {
		// into the scenario file: one file holds the steps and the answers
		snap := &scenario.Snapshot{
			Kind:    scenario.KindGolden,
			Spec:    relTo(filepath.Dir(s.scfg.Scenario), s.spec.Path),
			Created: s.now().UTC().Format(time.RFC3339),
			Reread:  true,
			Steps:   s.shots,
		}
		if s.spec.Doc.Info != nil {
			snap.SpecVersion = s.spec.Doc.Info.Version
		}
		return scenario.WriteAnswers(s.scfg.Scenario, snap)
	}
	dir := filepath.Dir(s.scfg.Golden)
	snap := &scenario.Snapshot{
		Kind:     scenario.KindGolden,
		Spec:     relTo(dir, s.spec.Path),
		Scenario: relTo(dir, s.scfg.Scenario),
		Created:  s.now().UTC().Format(time.RFC3339),
		Reread:   true,
		Steps:    s.shots,
	}
	if s.spec.Doc.Info != nil {
		snap.SpecVersion = s.spec.Doc.Info.Version
	}
	return snap.Save(s.scfg.Golden, " Generated by apitest (ScenarioConfig.Update): the answers of the scenario.\n Do not edit; \"apitest-gen tree run -update\" writes it again.\n Review it as a git diff.")
}

// makeUnique replaces the values at the unique pointers of a step by
// values new for this run (ScenarioConfig.Unique); the same original
// value gets the same new one in every step.
func (s *scenarioRunner) makeUnique(st *scenario.Step, c *cases.Case, body any) any {
	if !s.scfg.Unique || len(st.Unique) == 0 {
		return body
	}
	_, media := cases.RequestMedia(c.Op)
	var root *openapi3.Schema
	if media != nil && media.Schema != nil {
		root = media.Schema.Value
	}
	for _, ptr := range st.Unique {
		v, ok := bind.Pointer(body, ptr)
		if !ok {
			continue
		}
		orig, isString := v.(string)
		if !isString {
			continue
		}
		nv, ok := s.unique[orig]
		if !ok {
			nv = s.uniqueFor(orig, schemaAt(root, ptr))
			s.unique[orig] = nv
		}
		body = setPointer(body, ptr, nv)
	}
	return body
}

// uniqueFor is a value for this run that fits the schema of the field.
func (s *scenarioRunner) uniqueFor(orig string, sch *openapi3.Schema) string {
	if sch != nil && sch.Pattern != "" {
		lim := regexgen.Limits{Min: int(sch.MinLength), Max: -1}
		if sch.MaxLength != nil {
			lim.Max = int(*sch.MaxLength)
		}
		seed := uint64(0)
		for _, b := range []byte(s.runID + orig) {
			seed = seed*31 + uint64(b)
		}
		if v, ok := regexgen.Generate(sch.Pattern, lim, mrand.New(mrand.NewPCG(seed, 7)), 100); ok {
			return v
		}
		s.warn(fmt.Sprintf("unique: no new value for %q matches the pattern %q; the original is sent", orig, sch.Pattern))
		return orig
	}
	if at := strings.Index(orig, "@"); at > 0 && sch != nil && sch.Format == "email" {
		return orig[:at] + "+" + s.runID + orig[at:]
	}
	nv := orig + "-" + s.runID
	if sch != nil && sch.MaxLength != nil && uint64(len(nv)) > *sch.MaxLength {
		keep := int(*sch.MaxLength) - len(s.runID) - 1
		if keep < 0 {
			return s.runID[:*sch.MaxLength]
		}
		nv = orig[:min(keep, len(orig))] + "-" + s.runID
	}
	return nv
}

// uniqueValue maps an original unique value of the golden snapshot to the
// value of this run.
func (s *scenarioRunner) uniqueValue(v string) any {
	if nv, ok := s.unique[v]; ok {
		return nv
	}
	return v
}

func (s *scenarioRunner) uniqueValues(v any) any {
	if len(s.unique) == 0 {
		return v
	}
	switch x := v.(type) {
	case string:
		return s.uniqueValue(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, c := range x {
			m[k] = s.uniqueValues(c)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, c := range x {
			l[i] = s.uniqueValues(c)
		}
		return l
	}
	return v
}

// original maps a unique value of this run back to the value of the
// scenario.
func (s *scenarioRunner) original(v string) string {
	for orig, nv := range s.unique {
		if nv == v {
			return orig
		}
		if strings.Contains(v, nv) {
			v = strings.ReplaceAll(v, nv, orig)
		}
	}
	return v
}

func (s *scenarioRunner) originals(v any) any {
	if len(s.unique) == 0 {
		return v
	}
	switch x := v.(type) {
	case string:
		return s.original(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, c := range x {
			m[k] = s.originals(c)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, c := range x {
			l[i] = s.originals(c)
		}
		return l
	}
	return v
}

// schemaAt is the schema at a JSON pointer into a value of schema s.
func schemaAt(s *openapi3.Schema, ptr string) *openapi3.Schema {
	cur := s
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		if cur == nil {
			return nil
		}
		if _, err := strconv.Atoi(seg); err == nil && cur.Items != nil {
			cur = cur.Items.Value
			continue
		}
		p := allProperties(cur)[strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")]
		if p == nil {
			return nil
		}
		cur = p.Value
	}
	return cur
}

// setPointer sets the value at a JSON pointer of a body.
func setPointer(v any, ptr, nv string) any {
	segs := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var set func(cur any, i int) any
	set = func(cur any, i int) any {
		seg := strings.ReplaceAll(strings.ReplaceAll(segs[i], "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			if i == len(segs)-1 {
				x[seg] = nv
			} else {
				x[seg] = set(x[seg], i+1)
			}
		case []any:
			n, err := strconv.Atoi(seg)
			if err != nil || n < 0 || n >= len(x) {
				return cur
			}
			if i == len(segs)-1 {
				x[n] = nv
			} else {
				x[n] = set(x[n], i+1)
			}
		}
		return cur
	}
	return set(v, 0)
}

// relTo is path relative to dir, so a file written on one machine reads
// the same on another; the path itself if that fails.
func relTo(dir, path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(absDir, abs)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func newRunID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()%1000000, 36)
	}
	return hex.EncodeToString(b)
}
