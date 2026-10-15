package main

import (
	"bytes"
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
	"github.com/fada4773-sketch/specproof/internal/gen/tree"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// File names of the tree, next to the tree file.
const (
	treeFile         = "tree.yaml"
	treeDataFile     = "tree-data_gen.yaml"
	treeScenarioFile = "tree-scenario_gen.yaml"
	treeGoldenFile   = "tree-golden_gen.yaml"
	treeReviewFile   = "tree-review_gen.md"
)

const treeUsage = `apitest-gen tree: fetch connected records from a running instance and
rebuild them as an integration test in an empty one.

Usage:
  apitest-gen tree -spec <openapi.yaml> [-file tree.yaml]
  apitest-gen tree -spec <openapi.yaml> [-file tree.yaml] [-tags A,B] [-exclude-tags X] [-exclude-ops opId] [-with-deps]
  apitest-gen tree -spec <openapi.yaml> [-file tree.yaml] -base-url <url> [-node name] [-token … | -token-env NAME]
  apitest-gen tree run -spec <openapi.yaml> -base-url <url> [-update] [-token … | -token-env NAME] [flags]

  tree       writes tree.yaml, the records to fetch and how they hang
             together, proposed from the spec; an existing file stays as it
             is and gets only the endpoints it does not name yet, at their
             place. -tags proposes only those tags (scope of the file; add
             tags later, step by step), -exclude-tags and -exclude-ops
             leave tags and operations out. With -base-url it fetches the
             records (GET only, and
             POSTs marked read: true) and writes next to tree.yaml:
               tree-data_gen.yaml      the records (data of the instance)
               tree-scenario_gen.yaml  the steps that rebuild them in an
                                       empty instance (apitest.RunScenario)
               tree-review_gen.md      the review: a section per step
                                       (tree, fetch, scenario, run,
                                       update), the state of each and
                                       the next command
             Without -base-url the scenario is built again from
             tree-data_gen.yaml, if it exists.
  run        runs the scenario against an empty instance and compares with
             tree-golden_gen.yaml if it exists; -update writes the golden
             snapshot and the examples into the spec instead; both write
             their section into tree-review_gen.md. Exit code 1 if a step
             fails (for CI).

Flags:
`

// tokenFlags are the token flags of tree and regression.
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

// resolveToken returns the token: -token, else -token-env, else fallback
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

type treeOptions struct {
	spec, file, baseURL              string
	tags, excludeTags, excludeOps    string
	only                             string
	deps                             bool
	scenario, golden, report, junit  string
	settings, errors, run, skip      string
	update, noExamples, remote, uniq bool
	tok                              tokenFlags
	timeout                          time.Duration
}

func treeCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h") {
		fmt.Fprint(stdout, treeUsage)
		fs, _ := treeFlags("tree", stdout)
		fs.PrintDefaults()
		fmt.Fprintln(stdout, "\nFlags of run:")
		fs, _ = treeFlags("run", stdout)
		fs.PrintDefaults()
		return 0
	}
	sub := "tree"
	if len(args) > 0 && args[0] == "run" {
		sub, args = "run", args[1:]
	} else if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "apitest-gen tree: unknown command %q; use run, or flags only\n", args[0])
		return 2
	}
	fs, o := treeFlags(sub, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.spec == "" && o.settings == "" {
		fmt.Fprintf(stderr, "apitest-gen %s: -spec is required\n", treeName(sub))
		return 2
	}
	var code int
	var err error
	if sub == "run" {
		code, err = treeRun(o, stdout)
	} else {
		code, err = treeMain(o, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "apitest-gen %s: %v\n", treeName(sub), err)
	}
	return code
}

func treeName(sub string) string {
	if sub == "run" {
		return "tree run"
	}
	return "tree"
}

func treeFlags(name string, out io.Writer) (*flag.FlagSet, *treeOptions) {
	o := &treeOptions{}
	fs := flag.NewFlagSet(treeName(name), flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.spec, "spec", "", "the OpenAPI file, required")
	fs.StringVar(&o.file, "file", treeFile, "the tree file; the generated files sit next to it")
	fs.DurationVar(&o.timeout, "timeout", 0, "timeout of each request (default 10s)")
	if name == "run" {
		fs.StringVar(&o.baseURL, "base-url", "", "the empty instance, with base path, required (or baseURL of -settings)")
		o.tok.register(fs, true)
		fs.StringVar(&o.scenario, "scenario", "", "the scenario; default "+treeScenarioFile+" next to the tree file")
		fs.StringVar(&o.golden, "golden", "", "the golden snapshot; default "+treeGoldenFile+" next to the tree file")
		fs.BoolVar(&o.update, "update", false, "write the golden snapshot and the examples of the spec instead of comparing")
		fs.BoolVar(&o.noExamples, "no-examples", false, "with -update: do not write the examples into the spec")
		fs.StringVar(&o.settings, "settings", "", "optional settings file (spec, baseURL, health, tokens, headers, report)")
		fs.StringVar(&o.report, "report", "", "Markdown report (HTML and JSON next to it)")
		fs.StringVar(&o.junit, "junit", "", "write the results as JUnit XML too, for the test tab of CI")
		fs.StringVar(&o.errors, "errors", "", `error steps to run, in this order: statuses or classes, e.g. "401,404"; default all`)
		fs.StringVar(&o.run, "run", "", `run only these steps, like "go test -run": "Dock/" or "Dock/docks1.read"`)
		fs.StringVar(&o.skip, "skip", "", `leave these steps out, like "go test -skip"`)
		fs.BoolVar(&o.uniq, "unique", false, "make the unique values of the scenario new for this run (an instance that is not empty)")
		fs.BoolVar(&o.remote, "allow-remote-writes", false, "allow writing requests to a host other than localhost")
	} else {
		fs.StringVar(&o.baseURL, "base-url", "", "the instance to fetch the records from, with base path (GET only)")
		o.tok.register(fs, false)
		fs.StringVar(&o.tags, "tags", "", `propose only these tags, comma separated; added to scope.tags of the file (step by step: "Book", later "Book,Author")`)
		fs.StringVar(&o.excludeTags, "exclude-tags", "", "never propose these tags; added to scope.exclude.tags (* matches any text)")
		fs.StringVar(&o.excludeOps, "exclude-ops", "", `never propose these operations (operationId or "METHOD /path"); added to scope.exclude.ops`)
		fs.BoolVar(&o.deps, "with-deps", false, "also propose the reads of other tags that give values the scope needs (scope.deps)")
		fs.StringVar(&o.only, "node", "", "with -base-url: fetch only this node and what hangs below it again; the other records stay")
	}
	return fs, o
}

func (o *treeOptions) next(name string) string {
	return filepath.Join(filepath.Dir(o.file), name)
}

// treeMain proposes or merges the tree file, fetches with -base-url, and
// builds the scenario.
func treeMain(o *treeOptions, out io.Writer) (int, error) {
	ctx := context.Background()
	s, err := spec.Load(ctx, o.spec)
	if err != nil {
		return 1, err
	}
	version := ""
	if s.Doc.Info != nil {
		version = s.Doc.Info.Version
	}
	add := tree.Scope{Tags: splitList(o.tags), ExcludeTags: splitList(o.excludeTags), ExcludeOps: splitList(o.excludeOps), Deps: o.deps}
	review := o.review(version)
	var t *tree.Tree
	var changes []tree.Change
	proposed := false
	if _, statErr := os.Stat(o.file); errors.Is(statErr, os.ErrNotExist) {
		proposed = true
		proposal := tree.ProposeScope(s, add)
		b, err := proposal.Encode()
		if err != nil {
			return 1, err
		}
		if err := scenario.WriteFile(o.file, b); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "%s: proposed from the spec; check it (pick, where, write), then fetch with -base-url\n", o.file)
		if t, err = tree.Parse(b); err != nil {
			return 1, err
		}
	} else {
		if t, err = tree.Load(o.file); err != nil {
			return 1, fmt.Errorf("%s:\n%w", o.file, err)
		}
		changes = t.AddScope(add)
		changes = append(changes, tree.Merge(s, t, tree.ProposeScope(s, t.Scope), version)...)
		if len(changes) > 0 {
			b, err := t.Bytes()
			if err != nil {
				return 1, err
			}
			if err := scenario.WriteFile(o.file, b); err != nil {
				return 1, err
			}
			fmt.Fprintf(out, "%s: %d changes\n", o.file, len(changes))
			for _, c := range changes {
				fmt.Fprintf(out, "  %s: %s\n", c.Where, c.What)
			}
			if t, err = tree.Load(o.file); err != nil {
				return 1, fmt.Errorf("%s:\n%w", o.file, err)
			}
		} else {
			fmt.Fprintf(out, "%s: up to date with the spec\n", o.file)
		}
	}
	summary, body := tree.FileSection(t, changes, proposed)
	problems := tree.Check(s, t)
	if len(problems) > 0 {
		summary = fmt.Sprintf("%d problems: fix tree.yaml", len(problems))
		body += fmt.Sprintf("\n### Problems (%d)\n\nFix them in tree.yaml; nothing was fetched or built.\n\n", len(problems))
		for _, p := range problems {
			body += "- " + p + "\n"
		}
	}
	review.Set(tree.ReviewTree, summary, body, len(problems) > 0)
	switch {
	case proposed:
		review.Outdate(tree.ReviewTree, "tree.yaml was proposed anew; run the steps again")
	case len(changes) > 0:
		review.Outdate(tree.ReviewTree, fmt.Sprintf("tree.yaml got %d changes after this step (section 1); fetch again if nodes were added", len(changes)))
	}
	if len(problems) > 0 {
		if err := o.writeReview(review, out); err != nil {
			return 1, err
		}
		return 1, fmt.Errorf("%s:\n  %s", o.file, strings.Join(problems, "\n  "))
	}
	if unset := t.UnsetParams(); len(unset) > 0 {
		fmt.Fprintf(out, "%s: params without value: %s; set them at the top of the file (the nodes that use them are left out)\n", o.file, strings.Join(unset, ", "))
	}
	var data *tree.Data
	var stale []string
	dataPath := o.next(treeDataFile)
	switch {
	case o.baseURL != "":
		tok, err := resolveToken(o.tok.token, o.tok.tokenEnv, "", "token")
		if err != nil {
			return 2, err
		}
		fmt.Fprintf(out, "fetching from %s (GET only)\n", o.baseURL)
		opt := tree.Options{BaseURL: o.baseURL, Token: tok, Headers: o.tok.headers, Timeout: o.timeout, Only: o.only}
		if o.only != "" {
			if _, err := os.Stat(dataPath); err == nil {
				if opt.Before, _, err = tree.LoadData(dataPath, t); err != nil {
					return 1, err
				}
			}
		}
		data, err = tree.Fetch(ctx, s, t, opt)
		if err != nil {
			return 1, err
		}
		head := " Generated by \"apitest-gen tree\" from " + o.baseURL + ": the records the tree picked.\n Data of that instance; commit it only if it may be in git."
		b, err := data.Encode(head)
		if err != nil {
			return 1, err
		}
		if err := scenario.WriteFile(dataPath, b); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "  %s: %d records, %d requests\n", dataPath, len(data.Records), data.Requests)
		for _, n := range data.Notes {
			fmt.Fprintf(out, "  note: %s\n", n)
		}
		summary, body := tree.FetchSection(data, nil)
		if o.only != "" {
			body = "Only " + o.only + " and what hangs below it was fetched again; the other records are from before.\n\n" + body
		}
		review.Set(tree.ReviewFetch, summary, body, false)
	default:
		if _, err := os.Stat(dataPath); err != nil {
			fmt.Fprintf(out, "no records yet: fetch them with -base-url <instance>\n")
			return 0, o.writeReview(review, out)
		}
		if data, stale, err = tree.LoadData(dataPath, t); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "  %s: %d records (fetched before)\n", dataPath, len(data.Records))
		if len(stale) > 0 {
			fmt.Fprintf(out, "  %d records belong to nodes the tree no longer has; fetch again with -base-url\n", len(stale))
			review.Mark(tree.ReviewFetch, fmt.Sprintf("%d records belong to nodes tree.yaml no longer has: %s; fetch again", len(stale), strings.Join(stale, ", ")))
		}
		if !review.Has(tree.ReviewFetch) {
			summary, body := tree.FetchSection(data, stale)
			review.Set(tree.ReviewFetch, summary, "From "+treeDataFile+", fetched before.\n\n"+body, false)
		}
	}
	built, err := tree.Build(s, t, data)
	if err != nil {
		return 1, err
	}
	text, err := built.Scenario.Encode(" Generated by \"apitest-gen tree\" from " + filepath.Base(o.file) + " and " + treeDataFile + ".\n Do not edit: change tree.yaml and run apitest-gen tree again.\n It rebuilds the records in an empty instance: apitest.RunScenario or \"apitest-gen tree run\".")
	if err != nil {
		return 1, err
	}
	before, _ := os.ReadFile(o.next(treeScenarioFile))
	if err := scenario.WriteFile(o.next(treeScenarioFile), text); err != nil {
		return 1, err
	}
	fmt.Fprintf(out, "  %s: %d steps, %d records created, %d of the seed\n", o.next(treeScenarioFile), len(built.Scenario.Steps), len(built.Created), len(built.Seeds))
	summary, body = tree.ScenarioSection(built)
	review.Set(tree.ReviewScenario, summary, body, false)
	if !bytes.Equal(before, text) {
		review.Outdate(tree.ReviewScenario, "the scenario changed after this run; run it again")
	}
	return 0, o.writeReview(review, out)
}

// review loads tree-review_gen.md of an earlier step.
func (o *treeOptions) review(version string) *tree.ReviewDoc {
	r := tree.LoadReview(o.next(treeReviewFile))
	if o.spec != "" {
		r.Spec = o.spec
	}
	if version != "" {
		r.SpecVersion = version
	}
	r.File = o.file
	return r
}

// writeReview writes tree-review_gen.md with the sections of this run.
func (o *treeOptions) writeReview(r *tree.ReviewDoc, out io.Writer) error {
	if err := scenario.WriteFile(o.next(treeReviewFile), []byte(r.Render())); err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s: the review, with the next step\n", o.next(treeReviewFile))
	return nil
}

// splitList reads a comma separated flag value.
func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// treeRun runs the scenario against an empty instance.
func treeRun(o *treeOptions, out io.Writer) (int, error) {
	if o.scenario == "" {
		o.scenario = o.next(treeScenarioFile)
	}
	if o.golden == "" {
		o.golden = o.next(treeGoldenFile)
	}
	cfg := apitest.ScenarioConfig{Settings: o.settings, Scenario: o.scenario, Update: o.update, Unique: o.uniq}
	if _, err := os.Stat(o.golden); err == nil || o.update {
		cfg.Golden = o.golden
	}
	cfg.SpecPath = o.spec
	cfg.BaseURL = o.baseURL
	cfg.AllowRemoteWrites = o.remote
	cfg.RequestTimeout = o.timeout
	cfg.ReportPath = o.report
	if len(o.tok.headers) > 0 {
		cfg.Headers = o.tok.headers
	}
	tok, err := resolveToken(o.tok.token, o.tok.tokenEnv, "", "token")
	if err != nil {
		return 2, err
	}
	if tok != "" {
		cfg.Token = apitest.StaticToken(tok)
	}
	forbidden, err := resolveToken(o.tok.forbidden, o.tok.forbiddenEnv, "", "forbidden token")
	if err != nil {
		return 2, err
	}
	if forbidden != "" {
		cfg.ForbiddenToken = apitest.StaticToken(forbidden)
	}
	if o.errors != "" {
		for _, e := range strings.Split(o.errors, ",") {
			cfg.Errors = append(cfg.Errors, strings.TrimSpace(e))
		}
	}
	if o.baseURL == "" && o.settings == "" {
		return 2, errors.New("-base-url is required: the empty instance the scenario runs against")
	}
	var exchanges []apitest.StepExchange
	if o.update && !o.noExamples {
		cfg.OnStep = func(ex apitest.StepExchange) { exchanges = append(exchanges, ex) }
	}
	res, err := apitest.RunScenarioStandalone(context.Background(), cfg, apitest.StandaloneOptions{Out: out, Run: o.run, Skip: o.skip})
	if err != nil {
		return 2, err
	}
	if o.junit != "" {
		if err := writeJUnit(o.junit, junitResult("tree", res)); err != nil {
			return 1, err
		}
	}
	summary(out, res)
	review := o.review("")
	if review.Spec == "" {
		review.Spec = cfg.SpecPath
	}
	sum, body, failed := tree.RunSection(res, cfg.BaseURL, o.uniq)
	if o.run != "" || o.skip != "" {
		body = fmt.Sprintf("Only a part of the steps: -run %q -skip %q.\n\n", o.run, o.skip) + body
	}
	if cfg.Golden != "" && !o.update {
		body += "\nCompared with the golden snapshot " + cfg.Golden + ".\n"
	}
	review.Set(tree.ReviewRun, sum, body, failed)
	if res.Failed {
		if o.update {
			fmt.Fprintf(out, "golden snapshot and examples not written: steps failed\n")
			review.Set(tree.ReviewUpdate, "not written: steps failed", "Golden snapshot and examples not written: steps failed (section 4).\n", true)
		}
		return 1, o.writeReview(review, out)
	}
	if o.update {
		fmt.Fprintf(out, "  written %s\n", o.golden)
		body := fmt.Sprintf("Golden snapshot written: %s. Review it as a git diff; a later run without -update compares with it.\n", o.golden)
		sum := "golden written"
		if !o.noExamples {
			specPath := o.spec
			if specPath == "" {
				specPath = cfg.SpecPath
			}
			msg, err := writeTreeExamples(specPath, o.scenario, exchanges)
			if err != nil {
				return 1, err
			}
			fmt.Fprintf(out, "  %s: %s\n", specPath, msg)
			body += fmt.Sprintf("\nExamples of the spec %s: %s.\n", specPath, msg)
			sum += "; " + msg
		} else {
			body += "\nExamples not written (-no-examples).\n"
		}
		review.Set(tree.ReviewUpdate, sum, body, false)
	}
	return 0, o.writeReview(review, out)
}

// writeTreeExamples writes the answers of a passed run into the spec, in
// place.
func writeTreeExamples(specPath, scenarioPath string, run []apitest.StepExchange) (string, error) {
	if specPath == "" {
		return "no spec given (-spec): examples not written", nil
	}
	s, err := spec.Load(context.Background(), specPath)
	if err != nil {
		return "", err
	}
	sc, err := scenario.Load(scenarioPath)
	if err != nil {
		return "", err
	}
	doc, err := yamldoc.Load(specPath)
	if err != nil {
		return "", err
	}
	res, err := tree.Examples(s, doc, sc, run)
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
