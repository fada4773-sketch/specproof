package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/regression"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// File names of the regression check.
const (
	sampleFile         = "regression-sample.yaml"
	regressionScenario = "regression-scenario_gen.yaml"
	preSnapshot        = "pre-snapshot_gen.yaml"
	postSnapshot       = "post-snapshot_gen.yaml"
	preReport          = "pre-report_gen.md"
	postReport         = "post-report_gen.md"
	diffReport         = "diff-report_gen.md"
)

const regressionUsage = `apitest-gen regression: does a deployment change what a stage answers?
Only GET requests are sent; nothing is ever written to the stage.

Usage:
  apitest-gen regression init -spec <openapi.yaml> [-out regression-sample.yaml]
  apitest-gen regression pre  -spec <openapi.yaml> -sample regression-sample.yaml -base-url <url> [-token … | -token-env NAME] [-out dir]
  apitest-gen regression post -spec <openapi.yaml> -base-url <url> [-token … | -token-env NAME] [-out dir]
  apitest-gen regression diff -pre dir/pre-snapshot_gen.yaml -post dir/post-snapshot_gen.yaml [-out dir]

  init  proposes the sample file: per resource the list GET, the values a
        record gives and the GETs that must answer for it; keeps what you
        changed in an existing file
  pre   before the deployment: draws the sample (a new one every time),
        reads every GET with it, keeps the answers (pre-snapshot_gen.yaml)
  post  after the deployment: the same reads with the same records
        (post-snapshot_gen.yaml), then the diff (diff-report_gen.md/.html);
        exit code 1 if a finding fails
  diff  compares two snapshots on their own

Flags:
`

type regressionOptions struct {
	spec, sample, baseURL, out, pre, post string
	seed, failOn, slower, ignore, junit   string
	probe                                 time.Duration
	probeSet                              bool
	strict                                bool
	tok                                   tokenFlags
}

func regressionCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" {
		fmt.Fprint(stdout, regressionUsage)
		fs, _ := regressionFlags("pre", stdout)
		fs.PrintDefaults()
		return 0
	}
	sub := args[0]
	fs, o := regressionFlags(sub, stderr)
	if fs == nil {
		fmt.Fprintf(stderr, "apitest-gen regression: unknown command %q; use init, pre, post or diff\n", sub)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "probe" {
			o.probeSet = true
		}
	})
	var code int
	var err error
	switch sub {
	case "init":
		code, err = regressionInit(o, stdout)
	case "pre":
		code, err = regressionPre(o, stdout)
	case "post":
		code, err = regressionPost(o, stdout)
	case "diff":
		code, err = regressionDiff(o, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "apitest-gen regression %s: %v\n", sub, err)
	}
	return code
}

func regressionFlags(name string, out io.Writer) (*flag.FlagSet, *regressionOptions) {
	if name != "init" && name != "pre" && name != "post" && name != "diff" {
		return nil, nil
	}
	o := &regressionOptions{}
	fs := flag.NewFlagSet("regression "+name, flag.ContinueOnError)
	fs.SetOutput(out)
	if name != "diff" {
		fs.StringVar(&o.spec, "spec", "", "the OpenAPI file of the version that runs on the stage, required")
	}
	switch name {
	case "init":
		fs.StringVar(&o.out, "out", sampleFile, "the sample file to write (an existing one keeps its sets)")
	case "pre", "post":
		fs.StringVar(&o.baseURL, "base-url", "", "the stage, with base path, required")
		o.tok.register(fs, false)
		fs.StringVar(&o.out, "out", "out", "folder of snapshots and reports")
		fs.DurationVar(&o.probe, "probe", 2*time.Second, "time between the two reads of each GET; 0 reads once (default: probe of the sample file)")
		fs.StringVar(&o.junit, "junit", "", "write the results as JUnit XML too, for the test tab of CI")
	}
	switch name {
	case "pre":
		fs.StringVar(&o.sample, "sample", sampleFile, "the sample file")
		fs.StringVar(&o.seed, "seed", "", "repeat a sample: overrides seed of the sample file")
		fs.BoolVar(&o.strict, "strict", false, "exit code 1 if a read fails or a set has no record")
	case "post":
		fs.StringVar(&o.pre, "pre", "", "the pre snapshot; default <out>/"+preSnapshot)
	case "diff":
		fs.StringVar(&o.pre, "pre", "", "the pre snapshot, required")
		fs.StringVar(&o.post, "post", "", "the post snapshot, required")
		fs.StringVar(&o.out, "out", "out", "folder of the diff report")
		fs.StringVar(&o.junit, "junit", "", "write the findings as JUnit XML too")
	}
	if name == "post" || name == "diff" {
		fs.StringVar(&o.sample, "sample", sampleFile, "the sample file: its ignore and lists.key apply to the diff (if it exists)")
		fs.StringVar(&o.failOn, "fail-on", "", "also fail on these findings, comma-separated: added, slower, other")
		fs.StringVar(&o.slower, "slower", "2x,200ms", "SLOWER: more than this factor and over this time")
		fs.StringVar(&o.ignore, "ignore", "", "more fields not to compare, comma-separated (names or JSON pointers)")
	}
	return fs, o
}

func regressionInit(o *regressionOptions, out io.Writer) (int, error) {
	if o.spec == "" {
		return 2, errors.New("-spec is required")
	}
	s, err := spec.Load(context.Background(), o.spec)
	if err != nil {
		return 1, err
	}
	var old *regression.Sample
	if regression.Exists(o.out) {
		if old, err = regression.LoadSample(o.out); err != nil {
			return 1, err
		}
	}
	sample := regression.Propose(s, old)
	if len(sample.Sets) == 0 {
		return 1, errors.New("the spec has no resource with a list GET; write the sets of the sample file yourself")
	}
	if err := sample.Save(o.out); err != nil {
		return 1, err
	}
	added := len(sample.Sets)
	if old != nil {
		added -= len(old.Sets)
	}
	fmt.Fprintf(out, "%s: %d sets (%d new); check from, where, check and as, then run regression pre\n", o.out, len(sample.Sets), added)
	return 0, nil
}

func (o *regressionOptions) token() (string, error) {
	return resolveToken(o.tok.token, o.tok.tokenEnv, "", "token")
}

func regressionPre(o *regressionOptions, out io.Writer) (int, error) {
	if o.spec == "" || o.baseURL == "" {
		return 2, errors.New("-spec and -base-url are required")
	}
	ctx := context.Background()
	s, err := spec.Load(ctx, o.spec)
	if err != nil {
		return 1, err
	}
	sample, err := regression.LoadSample(o.sample)
	if err != nil {
		return 1, err
	}
	if len(sample.Sets) == 0 {
		return 1, regression.ErrNoSets
	}
	if o.seed != "" {
		sample.Seed = o.seed
	}
	if o.probeSet {
		sample.Probe = o.probe
	}
	tok, err := o.token()
	if err != nil {
		return 2, err
	}
	picked, err := regression.Draw(ctx, sample, discover.Options{BaseURL: o.baseURL, Token: tok, Headers: o.tok.headers})
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(out, "sample  seed %s\n", picked.Seed)
	empty := 0
	for _, ss := range picked.Sets {
		line := fmt.Sprintf("  %-12s %d of %d candidates", ss.Name, len(ss.Picked), ss.Candidates)
		var left []string
		for _, k := range sortedKeys(ss.LeftOut) {
			left = append(left, fmt.Sprintf("%d %s", ss.LeftOut[k], k))
		}
		if len(left) > 0 {
			line += " (left out: " + strings.Join(left, ", ") + ")"
		}
		fmt.Fprintln(out, line)
		if len(ss.Picked) == 0 {
			empty++
		}
	}
	sc, notes := regression.Plan(s, sample, picked)
	fmt.Fprintf(out, "reads   %d GETs\n", len(sc.Steps))
	for _, n := range notes {
		fmt.Fprintf(out, "  not read: %s\n", n)
	}
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return 1, err
	}
	scPath := filepath.Join(o.out, regressionScenario)
	text, err := sc.Encode(" Generated by \"apitest-gen regression pre\": the reads of the regression check (GET only).")
	if err != nil {
		return 1, err
	}
	if err := scenario.WriteFile(scPath, text); err != nil {
		return 1, err
	}
	snap, res, err := regression.Read(ctx, regression.ReadInput{
		Spec: o.spec, Scenario: scPath, BaseURL: o.baseURL, Token: tok, Headers: o.tok.headers,
		Report: filepath.Join(o.out, preReport), Probe: sample.Probe, Out: io.Discard,
		Kind: scenario.KindRegressionPre, Seed: picked.Seed,
	})
	if err != nil {
		return 1, err
	}
	snap.Sample = picked.Sets
	snap.Scenario = regressionScenario
	if s.Doc.Info != nil {
		snap.SpecVersion = s.Doc.Info.Version
	}
	if err := snap.Save(filepath.Join(o.out, preSnapshot), " Generated by \"apitest-gen regression pre\". Real data of the stage: do not commit."); err != nil {
		return 1, err
	}
	if o.junit != "" {
		if err := writeJUnit(o.junit, junitResult("regression-pre", res)); err != nil {
			return 1, err
		}
	}
	summary(out, res)
	fmt.Fprintf(out, "written %s\n", filepath.Join(o.out, preSnapshot))
	if res.Failed {
		fmt.Fprintln(out, "some reads do not fit the spec before the deployment; they are not new, the diff after the deployment shows what changes")
	}
	if o.strict && (res.Failed || empty > 0) {
		return 1, nil
	}
	return 0, nil
}

func regressionPost(o *regressionOptions, out io.Writer) (int, error) {
	if o.spec == "" || o.baseURL == "" {
		return 2, errors.New("-spec and -base-url are required")
	}
	ctx := context.Background()
	if o.pre == "" {
		o.pre = filepath.Join(o.out, preSnapshot)
	}
	pre, err := scenario.LoadSnapshot(o.pre)
	if err != nil {
		return 1, fmt.Errorf("pre snapshot: %w; run regression pre before the deployment", err)
	}
	s, err := spec.Load(ctx, o.spec)
	if err != nil {
		return 1, err
	}
	scPath := filepath.Join(filepath.Dir(o.pre), pre.Scenario)
	sc, err := scenario.Load(scPath)
	if err != nil {
		return 1, err
	}
	// reads of operations the new spec no longer has are not sent
	var kept []*scenario.Step
	for _, st := range sc.Steps {
		if s.Op(st.Call) != nil {
			kept = append(kept, st)
		}
	}
	postScenario := filepath.Join(o.out, "post-"+regressionScenario)
	if len(kept) != len(sc.Steps) {
		sc.Steps = kept
		text, err := sc.Encode(" The reads of the pre snapshot the new spec still has.")
		if err != nil {
			return 1, err
		}
		if err := scenario.WriteFile(postScenario, text); err != nil {
			return 1, err
		}
		scPath = postScenario
	}
	tok, err := o.token()
	if err != nil {
		return 2, err
	}
	probe := o.probe
	if !o.probeSet && pre.Reread {
		probe = 2 * time.Second
	} else if !o.probeSet {
		probe = 0
	}
	snap, res, err := regression.Read(ctx, regression.ReadInput{
		Spec: o.spec, Scenario: scPath, BaseURL: o.baseURL, Token: tok, Headers: o.tok.headers,
		Report: filepath.Join(o.out, postReport), Probe: probe, Out: io.Discard,
		Kind: scenario.KindRegressionPost, Pre: filepath.Base(o.pre), Seed: pre.Seed,
	})
	if err != nil {
		return 1, err
	}
	if s.Doc.Info != nil {
		snap.SpecVersion = s.Doc.Info.Version
	}
	if err := snap.Save(filepath.Join(o.out, postSnapshot), " Generated by \"apitest-gen regression post\". Real data of the stage: do not commit."); err != nil {
		return 1, err
	}
	summary(out, res)
	fmt.Fprintf(out, "written %s\n", filepath.Join(o.out, postSnapshot))
	return o.compare(pre, snap, out)
}

func regressionDiff(o *regressionOptions, out io.Writer) (int, error) {
	if o.pre == "" || o.post == "" {
		return 2, errors.New("-pre and -post are required")
	}
	pre, err := scenario.LoadSnapshot(o.pre)
	if err != nil {
		return 1, err
	}
	post, err := scenario.LoadSnapshot(o.post)
	if err != nil {
		return 1, err
	}
	return o.compare(pre, post, out)
}

// compare diffs two snapshots and writes the diff report.
func (o *regressionOptions) compare(pre, post *scenario.Snapshot, out io.Writer) (int, error) {
	opt := scenario.DiffOptions{}
	for _, f := range strings.Split(o.failOn, ",") {
		if f = strings.TrimSpace(f); f != "" {
			if f != scenario.FailAdded && f != scenario.FailSlower && f != scenario.FailOther {
				return 2, fmt.Errorf("-fail-on: unknown %q (added, slower, other)", f)
			}
			opt.FailOn = append(opt.FailOn, f)
		}
	}
	if o.slower != "" {
		factor, minMs, err := parseSlower(o.slower)
		if err != nil {
			return 2, err
		}
		opt.SlowerFactor, opt.SlowerMinMs = factor, minMs
	}
	for _, f := range strings.Split(o.ignore, ",") {
		if f = strings.TrimSpace(f); f != "" {
			opt.Ignore = append(opt.Ignore, f)
		}
	}
	if regression.Exists(o.sample) {
		sample, err := regression.LoadSample(o.sample)
		if err != nil {
			return 1, err
		}
		opt.Ignore = append(opt.Ignore, sample.Ignore...)
		opt.ListKey = sample.ListKey
	}
	fs := scenario.Diff(pre, post, opt)
	rep := &regression.DiffReport{Pre: pre, Post: post, Findings: fs, Ignored: opt.Ignore}
	md := filepath.Join(o.out, diffReport)
	if err := scenario.WriteFile(md, []byte(rep.Markdown())); err != nil {
		return 1, err
	}
	html, err := rep.HTML()
	if err != nil {
		return 1, err
	}
	if err := scenario.WriteFile(strings.TrimSuffix(md, ".md")+".html", html); err != nil {
		return 1, err
	}
	if o.junit != "" {
		if err := writeJUnit(o.junit, junitDiff("regression", pre, fs)); err != nil {
			return 1, err
		}
	}
	fails := 0
	for _, f := range fs {
		mark := "i"
		if f.Fails {
			mark = "✗"
			fails++
		}
		fmt.Fprintf(out, "  %s %-16s %s %s: %s → %s\n", mark, f.Code, f.Request, f.Pointer, f.Old, f.New)
	}
	switch {
	case fails > 0:
		fmt.Fprintf(out, "FAILED: %d findings fail; report %s\n", fails, md)
		return 1, nil
	default:
		fmt.Fprintf(out, "PASSED: pre and post answer the same (%d findings for information); report %s\n", len(fs), md)
	}
	return 0, nil
}

// parseSlower reads "2x,200ms".
func parseSlower(s string) (float64, int64, error) {
	f, d, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, fmt.Errorf("-slower %q: write factor and time, e.g. 2x,200ms", s)
	}
	factor, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(f), "x"), 64)
	if err != nil || factor <= 1 {
		return 0, 0, fmt.Errorf("-slower %q: the factor must be a number above 1, e.g. 2x", s)
	}
	dur, err := time.ParseDuration(strings.TrimSpace(d))
	if err != nil {
		return 0, 0, fmt.Errorf("-slower %q: %w", s, err)
	}
	return factor, dur.Milliseconds(), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
