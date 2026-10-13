package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/gen/contract"
	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// File names of the contract test, next to the settings file.
const (
	scenarioFile  = "contract-scenario_gen.yaml"
	goldenFile    = "contract-golden_gen.yaml"
	overridesFile = "contract-overrides.yaml"
	reviewFile    = "contract-review_gen.md"
)

const contractUsage = `apitest-gen contract: the contract test against an empty instance.

Usage:
  apitest-gen contract init -settings contract-settings.yaml [-base-url <url>] [-token <t> | -token-env NAME] [flags]
  apitest-gen contract run  -settings contract-settings.yaml [-base-url <url>] [-token <t> | -token-env NAME] [flags]

  init  generates the scenario from the spec (contract-scenario_gen.yaml), with
        the corrections of contract-overrides.yaml; runs it against the instance,
        which must be empty; writes the golden snapshot (contract-golden_gen.yaml)
        and the examples into the spec, and the review list
        (contract-review_gen.md). Run it again after a change of the API.
  run   runs the scenario and compares with the golden snapshot; exit code 1
        if a step fails (for CI).

The files sit next to the settings file. apitest starts nothing: the
instance under -base-url (or baseURL of the settings) must run.

Flags:
`

// tokenFlags are the token flags of contract and regression.
type tokenFlags struct {
	token, tokenEnv, forbidden, forbiddenEnv string
	headers                                  headerFlags
}

func (t *tokenFlags) register(fs *flag.FlagSet, forbidden bool) {
	fs.StringVar(&t.token, "token", "", "bearer token, given directly (redacted in reports)")
	fs.StringVar(&t.tokenEnv, "token-env", "", "environment variable holding the bearer token")
	if forbidden {
		fs.StringVar(&t.forbidden, "forbidden-token", "", "token with too few rights, for the 403 steps")
		fs.StringVar(&t.forbiddenEnv, "forbidden-token-env", "", "environment variable holding the token with too few rights")
	}
	fs.Var(&t.headers, "header", `extra header, "Name: value"; repeatable`)
}

// resolve returns the token: -token, else -token-env, else fallback
// ("env:NAME" or a value from a settings file).
func resolveToken(direct, env, fallback, what string) (string, error) {
	switch {
	case direct != "":
		return direct, nil
	case env != "":
		v := os.Getenv(env)
		if v == "" {
			return "", fmt.Errorf("environment variable %s (%s) is empty", env, what)
		}
		return v, nil
	case fallback != "":
		v, err := scenario.Expand(fallback)
		if err != nil {
			return "", fmt.Errorf("%s: %w", what, err)
		}
		return v, nil
	}
	return "", nil
}

type contractOptions struct {
	settings, baseURL                   string
	scenario, golden, overrides, review string
	errors, run, skip, junit, report    string
	noExamples, noLearn, remote, unique bool
	dryRun                              bool
	tok                                 tokenFlags
	timeout                             time.Duration
}

func contractCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" {
		fmt.Fprint(stdout, contractUsage)
		fs, _ := contractFlags("init", stdout)
		fs.PrintDefaults()
		return 0
	}
	sub := args[0]
	if sub != "init" && sub != "run" {
		fmt.Fprintf(stderr, "apitest-gen contract: unknown command %q; use init or run\n", sub)
		return 2
	}
	fs, o := contractFlags(sub, stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if o.settings == "" {
		fmt.Fprintf(stderr, "apitest-gen contract %s: -settings is required (contract-settings.yaml)\n", sub)
		return 2
	}
	st, err := scenario.LoadSettings(o.settings)
	if err != nil {
		fmt.Fprintf(stderr, "apitest-gen contract %s: %v\n", sub, err)
		return 2
	}
	o.defaults(st)
	if sub == "init" {
		code, err := contractInit(o, st, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "apitest-gen contract init: %v\n", err)
		}
		return code
	}
	code, err := contractRun(o, st, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "apitest-gen contract run: %v\n", err)
	}
	return code
}

func contractFlags(name string, out io.Writer) (*flag.FlagSet, *contractOptions) {
	o := &contractOptions{}
	fs := flag.NewFlagSet("contract "+name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.settings, "settings", "", "the settings file (contract-settings.yaml), required")
	fs.StringVar(&o.baseURL, "base-url", "", "the instance, with base path; overrides baseURL of the settings")
	o.tok.register(fs, true)
	fs.StringVar(&o.scenario, "scenario", "", "the scenario; default "+scenarioFile+" next to the settings")
	fs.StringVar(&o.golden, "golden", "", "the golden snapshot; default "+goldenFile+" next to the settings")
	fs.StringVar(&o.report, "report", "", "Markdown report (HTML and JSON next to it); overrides report of the settings")
	fs.StringVar(&o.junit, "junit", "", "write the results as JUnit XML too, for the test tab of CI")
	fs.BoolVar(&o.remote, "allow-remote-writes", false, "allow writing requests to a host other than localhost")
	fs.DurationVar(&o.timeout, "timeout", 0, "timeout of each request (default 10s)")
	if name == "init" {
		fs.StringVar(&o.overrides, "overrides", "", "the overrides; default "+overridesFile+" next to the settings")
		fs.StringVar(&o.review, "review", "", "the review list; default "+reviewFile+" next to the settings")
		fs.BoolVar(&o.noExamples, "no-examples", false, "do not write the examples into the spec")
		fs.BoolVar(&o.noLearn, "no-learn", false, "do not read learnFrom of the settings")
		fs.BoolVar(&o.dryRun, "dry-run", false, "generate and show the scenario, send nothing, write nothing")
	} else {
		fs.StringVar(&o.errors, "errors", "", `error steps to run, in this order: statuses or classes, e.g. "401,404,4xx"; default all`)
		fs.StringVar(&o.run, "run", "", `run only these steps, like "go test -run": "Book/" or "Book/book1.read"`)
		fs.StringVar(&o.skip, "skip", "", `leave these steps out, like "go test -skip"`)
		fs.BoolVar(&o.unique, "unique", false, "make the unique values of the scenario new for this run (an instance that is not empty)")
	}
	return fs, o
}

func (o *contractOptions) defaults(st *scenario.Settings) {
	next := func(v *string, name string) {
		if *v == "" {
			*v = filepath.Join(st.Dir, name)
		}
	}
	next(&o.scenario, scenarioFile)
	next(&o.golden, goldenFile)
	next(&o.overrides, overridesFile)
	next(&o.review, reviewFile)
}

// config is the configuration of the library for a contract run.
func (o *contractOptions) config(st *scenario.Settings) (apitest.ScenarioConfig, error) {
	cfg := apitest.ScenarioConfig{Settings: o.settings, Scenario: o.scenario, Golden: o.golden}
	cfg.BaseURL = o.baseURL
	cfg.AllowRemoteWrites = o.remote
	cfg.RequestTimeout = o.timeout
	cfg.Unique = o.unique
	if o.report != "" {
		cfg.ReportPath = o.report
	}
	if len(o.tok.headers) > 0 {
		cfg.Headers = o.tok.headers
	}
	tok, err := resolveToken(o.tok.token, o.tok.tokenEnv, st.Tokens.Default, "tokens.default")
	if err != nil {
		return cfg, err
	}
	if tok != "" {
		cfg.Token = apitest.StaticToken(tok)
	}
	forbidden, err := resolveToken(o.tok.forbidden, o.tok.forbiddenEnv, "", "forbidden token")
	if err != nil {
		return cfg, err
	}
	if forbidden != "" {
		cfg.ForbiddenToken = apitest.StaticToken(forbidden)
	}
	if o.errors != "" {
		for _, e := range strings.Split(o.errors, ",") {
			cfg.Errors = append(cfg.Errors, strings.TrimSpace(e))
		}
	}
	return cfg, nil
}

func contractInit(o *contractOptions, st *scenario.Settings, out io.Writer) (int, error) {
	ctx := context.Background()
	specPath := st.Rel(st.Spec)
	if specPath == "" {
		return 2, errors.New("the settings name no spec (spec: ../../api/openapi.yaml)")
	}
	s, err := spec.Load(ctx, specPath)
	if err != nil {
		return 1, err
	}
	ov, err := contract.LoadOverrides(o.overrides)
	if err != nil {
		return 1, err
	}
	in := contract.Input{Spec: s, Records: st.Records, Overrides: ov}
	var learnNotes []string
	if st.LearnFrom != nil && st.LearnFrom.BaseURL != "" && !o.noLearn && !o.dryRun {
		lf, err := scenario.Expand(st.LearnFrom.BaseURL)
		if err != nil {
			return 2, fmt.Errorf("learnFrom.baseURL: %w", err)
		}
		tok := ""
		if st.LearnFrom.Token != "" {
			if tok, err = scenario.Expand(st.LearnFrom.Token); err != nil {
				return 2, fmt.Errorf("learnFrom.token: %w", err)
			}
		}
		fmt.Fprintf(out, "learning values from %s (GET only)\n", lf)
		in.Learned, learnNotes = contract.Learn(ctx, s, discover.Options{BaseURL: lf, Token: tok, Headers: o.tok.headers})
		in.LearnedFrom = lf
	}
	gen, err := contract.Generate(in)
	if err != nil {
		return 1, err
	}
	text, err := gen.Scenario.Encode(" Generated by \"apitest-gen contract init\" from " + filepath.Base(specPath) + ".\n Do not edit: put changes into " + overridesFile + " and run init again.\n \"sure:\" and \"guess:\" give the reason of each decision; " + reviewFile + " lists the guesses.")
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(out, "scenario: %d steps, %d records, %d guesses\n", len(gen.Scenario.Steps), len(gen.Scenario.Records), len(gen.Guesses))
	if o.dryRun {
		_, err := out.Write(text)
		return 0, err
	}
	if err := scenario.WriteFile(o.scenario, text); err != nil {
		return 1, err
	}
	fmt.Fprintf(out, "  written %s\n", o.scenario)

	cfg, err := o.config(st)
	if err != nil {
		return 2, err
	}
	cfg.Update = true
	var run []apitest.StepExchange
	cfg.OnStep = func(ex apitest.StepExchange) { run = append(run, ex) }
	fmt.Fprintln(out, "running the scenario (the instance must be empty)")
	res, err := apitest.RunScenarioStandalone(ctx, cfg, apitest.StandaloneOptions{Out: out})
	if err != nil {
		return 2, err
	}
	rin := contract.ReviewInput{Spec: filepath.Base(specPath), BaseURL: baseOf(cfg, st), Now: time.Now(), Out: gen, Result: res, Learn: learnNotes}
	if s.Doc.Info != nil {
		rin.SpecVersion = s.Doc.Info.Version
	}
	code := 0
	if res.Failed {
		code = 1
	} else {
		if rin.Golden, err = scenario.LoadSnapshot(o.golden); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "  written %s\n", o.golden)
		if !o.noExamples {
			msg, err := writeExamples(specPath, s, gen.Scenario, run)
			if err != nil {
				return 1, err
			}
			rin.Examples = msg
			fmt.Fprintf(out, "  %s: %s\n", specPath, msg)
		}
	}
	if err := scenario.WriteFile(o.review, []byte(contract.Review(rin))); err != nil {
		return 1, err
	}
	fmt.Fprintf(out, "  written %s\n", o.review)
	if o.junit != "" {
		if err := writeJUnit(o.junit, junitResult("contract", res)); err != nil {
			return 1, err
		}
	}
	if code != 0 {
		fmt.Fprintf(out, "\nsteps did not answer as expected; see %s (fix the API or add overrides, then run init again)\n", o.review)
	}
	return code, nil
}

func baseOf(cfg apitest.ScenarioConfig, st *scenario.Settings) string {
	if cfg.BaseURL != "" {
		return cfg.BaseURL
	}
	return st.BaseURL
}

// writeExamples writes the answers of the run into the spec, in place.
func writeExamples(specPath string, s *spec.Spec, sc *scenario.Scenario, run []apitest.StepExchange) (string, error) {
	doc, err := yamldoc.Load(specPath)
	if err != nil {
		return "", err
	}
	res, err := contract.Examples(s, doc, sc, run)
	if err != nil {
		return "", err
	}
	if len(res.Problems) > 0 {
		var parts []string
		for _, p := range res.Problems {
			parts = append(parts, p.Where+": "+p.Message)
		}
		return fmt.Sprintf("examples not written (%s)", strings.Join(parts, "; ")), nil
	}
	if !res.SpecChanged {
		return "every example is up to date", nil
	}
	tmp, _, err := stageSpec(doc, specPath)
	if err != nil {
		return "", err
	}
	if err := commitSpec(tmp, specPath); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return fmt.Sprintf("%d examples and %d schema examples written", res.Examples, res.SchemaExamples), nil
}

func contractRun(o *contractOptions, st *scenario.Settings, out io.Writer) (int, error) {
	cfg, err := o.config(st)
	if err != nil {
		return 2, err
	}
	res, err := apitest.RunScenarioStandalone(context.Background(), cfg, apitest.StandaloneOptions{Out: out, Run: o.run, Skip: o.skip})
	if err != nil {
		return 2, err
	}
	if o.junit != "" {
		if err := writeJUnit(o.junit, junitResult("contract", res)); err != nil {
			return 1, err
		}
	}
	summary(out, res)
	if res.Failed {
		return 1, nil
	}
	return 0, nil
}

// summary writes the counts of a run.
func summary(out io.Writer, res *apitest.Result) {
	var parts []string
	for _, s := range []apitest.Status{apitest.StatusPassed, apitest.StatusFailed, apitest.StatusSchemaViolation, apitest.StatusExampleMismatch,
		apitest.StatusDataMismatch, apitest.StatusError, apitest.StatusDeviation, apitest.StatusTolerated, apitest.StatusNotBuildable, apitest.StatusSkipped} {
		if n := res.Summary.Counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	verdict := "PASSED"
	if res.Failed {
		verdict = "FAILED"
	}
	fmt.Fprintf(out, "%s: %d steps (%s) in %s\n", verdict, res.Summary.Total, strings.Join(parts, ", "), res.Summary.Duration.Round(time.Millisecond))
	if res.Report != "" {
		fmt.Fprintf(out, "report: %s\n", res.Report)
	}
}
