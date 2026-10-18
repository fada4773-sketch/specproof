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
	"slices"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/gen/tree"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/redact"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// File names of the tree, next to the tree file.
const (
	treeFile       = "tree.yaml"
	treeDataFile   = "tree-data_gen.yaml"
	treeTestFile   = "tree-test_gen.yaml"
	treeReviewFile = "tree-review_gen.md"
	// files of an earlier version, replaced by the test file
	oldScenarioFile = "tree-scenario_gen.yaml"
	oldGoldenFile   = "tree-golden_gen.yaml"
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
               tree-test_gen.yaml      the test: the steps that rebuild
                                       them in an empty instance, and the
                                       answers of the last run -update
                                       (apitest.RunScenario)
               tree-review_gen.md      the review: a section per step
                                       (tree, fetch, scenario, run,
                                       update), the state of each and
                                       the next command
             Without -base-url the scenario is built again from
             tree-data_gen.yaml, if it exists.
  run        runs the test against an empty instance and compares with the
             answers tree-test_gen.yaml holds; -update writes the answers
             into it and the examples into the spec instead; both write
             their section into tree-review_gen.md. Exit code 1 if a step
             fails (for CI).
` + treeSyntax + `
Flags:
`

// treeSyntax explains tree.yaml for -help.
const treeSyntax = `
THE TREE FILE (tree.yaml)

A node is one request and the records it answers with. The records are
filtered (where, require), picked (pick), and every child node (each) runs
once per picked record. write says how the test creates, changes and
deletes a record of the node in an empty instance.

  order:                          # the plan of the test (see ORDER)
    - pilots
    - docks
    - ships: {pilotId: shipPilot.id}
  params:                         # values no record gives, read as $name
    season: summer
  where:                          # for every record of a resource, at every node
    Dock.status: [OPEN, RESERVED]
  prefer:                         # picked first, the rest filled with others
    Dock.name: [North]
  scope:                          # what the generator proposes
    tags: [Dock, Ship]            #   only these tags (all without)
    exclude: {tags: [Admin], ops: [runMaintenance]}   # * matches any text
    deps: false                   #   true: also reads of other tags that give needed values
  seed: 1                         # random picks repeat with the same seed
  budget: 2000                    # a fetch sends at most this many requests
  tree:
    planets:
      call: GET /planets
      pick: [mars]
      key: [/code]
      write: {create: none}       # the instance holds the planets (seed)
      each:
        docks:
          call: GET /planets/{code}/docks
          path: {code: .code}     # .code: the planet this node runs under
          where: {status: OPEN}
          pick: 2
          key: [/id]
          write:
            create: POST /planets/{code}/docks
            update: PUT /docks/{dockId}
            delete: DELETE /docks/{dockId}
          each:
            dock: GET /docks/{dockId} dockId=.id     # short form: one line
            ships:
              call: GET /docks/{dockId}/ships
              path: {dockId: .id}
              pick: all
              write:
                create: POST /docks/{dockId}/ships
              each:
                shipPilot: GET /pilots/{pilotId} pilotId=.pilotId
    pilots:
      call: GET /pilots
      pick: 3
      write: {create: POST /pilots}
  ignore:                         # operations never proposed again
    - GET /health

VALUES (in path, query, header, body, write, order, params)

  .field          a field of the record the node runs under (in a write:
                  of the record itself); a dot path: route.originId, items.0.id
  node.field      a field of the record of that node above, else of the
                  first record of a node fetched before (a root further up)
  $name           the value of params; a node whose $name has no value is
                  left out of the fetch
  '{{a}}-{{b}}'   a text composed of references (quote it in YAML)
  anything else   sent as it is; a list as a query value sends one request
                  per value; a reference to a list in a path does the same

  Mind the dot: .docks.id reads the field "docks" of the record itself;
  docks.id reads the record of the node docks.

KEYS OF A NODE

  call            METHOD /path or the operationId; a node only reads: GET,
                  or a POST with read: true
  path, query, header   parameter values: {name: value}
  body            the body of a POST with read: true
  read            true: this POST only reads (a search); the fetch sends it
  pick            3 (at random, by seed) | {first: 3} | all | [mars, venus]
                  (records whose first key has these values)
  where           conditions on fields; all must hold:
                    status: OPEN              equal
                    status: [OPEN, RESERVED]  one of them
                    capacity: ">=5"           also <=, >, <, != (numbers,
                                              else text: ISO dates compare)
                    pilotId: present          there and not empty: not
                                              null, "", [] or {}
                    pilotId: absent           missing, null or empty
                    any: {name: present, label: present}   one must hold
  require         named requests per candidate, each must answer 2xx (and
                  pass its own where); min: n = its list holds n at least
                    require:
                      ships:
                        call: GET /docks/{dockId}/ships
                        path: {dockId: .id}
                        min: 1
                  (a path with {…} in a one-line {…} mapping is no valid
                  YAML: write such a mapping as a block)
  candidates      require checks at most this many candidates
  key             JSON pointers that identify a record (default [/id])
  save            values of the answer under a name: {etag: header ETag},
                  {no: /number}
  items           JSON pointer of the list in the answer (/content)
  page            {param: page, size: {pageSize: 50}, max: 3}; also start,
                  step, next (pointer of a cursor), in: body
  each            the child nodes
  off             true: the node and its children are not fetched

KEYS OF write

  create          POST /path, or none: the API cannot create it (seed)
  update, patch, delete   the request; variants by another key:
                  update-by-name: PUT /docks/name/{name} name=.name
  <any other name>   an action: open: POST /docks/{dockId}/open
  A write in long form takes:
    call, path, query, header   the request (short: POST /path p=value)
    body          sent as it is, or "from <node>": built from that record
    change        update/patch: {field: value}, or auto (one field changed)
    expect        the status (default: the lowest documented 2xx)
    save          values of the answer, read later as $name
    onSeed        true: an update, patch or action also runs on a record
                  of the seed
    off           true: not used
  A write leaves out what follows from its node: the path values the node
  reads with, the key of the record, body from the node itself.

ORDER

  order names every node with a create, in the order the test creates
  them; deletes run the other way round, at the end.
    - docks                         the create of docks runs here
    - ships: {pilotId: shipPilot.id}   the body field pilotId takes the key
                                    of the record of shipPilot below each
                                    ship (or of a node above it)
    - ships: {crew.pilotId: shipPilot.id}
                                    a field inside an object of the body
                                    by its path
    - docks.update                  a step at this place: read, list,
                                    update, patch, a variant, an action
  The first run writes order; later runs add new creates, marked
  "# new: check its place". Every run checks it before anything is sent,
  also that a body field holding the key of a record the test creates
  (pilotId, when the test creates pilots) has a source: without one the
  create sends the key of the stage, which the empty instance lacks.
  scope.exclude holds for the whole file, also for nodes written before:
  what it excludes is named, -fix sets off: true.

MESSAGES

  A problem names the line, then the change to make there:
    line 23: order: ships.pilotId: the records of shipPilot have no field rank
      change line 23: pilotId: shipPilot.rank → pilotId: shipPilot.id (-fix does it)
  "(-fix does it)": apitest-gen tree … -fix makes the change in tree.yaml
  (comments stay) and goes on. A change that needs the records again
  (a where to add) is made, then fetch again; "next:" says what to run.
  The notes of a fetch (printed and in tree-review_gen.md) say why a node
  took no records: a failed request, an empty list, where or require.
  docs/scenario.md has the full reference.
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
	dryRun, fix                      bool
	tok                              tokenFlags
	timeout                          time.Duration
}

func treeCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "help" || slices.ContainsFunc(args, isHelpFlag) {
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

// isHelpFlag reports whether an argument asks for the help: -h, -help,
// --help.
func isHelpFlag(a string) bool {
	return a == "-h" || a == "-help" || a == "--help" || a == "--h"
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
	fs.BoolVar(&o.dryRun, "dry-run", false, "send nothing and write no data or test: show the plan, check tree.yaml, and list what would be sent")
	if name == "run" {
		fs.StringVar(&o.baseURL, "base-url", "", "the empty instance, with base path, required (or baseURL of -settings)")
		o.tok.register(fs, true)
		fs.StringVar(&o.scenario, "scenario", "", "the test; default "+treeTestFile+" next to the tree file")
		fs.StringVar(&o.golden, "golden", "", "a golden snapshot file of its own; default the answers the test file holds")
		fs.BoolVar(&o.update, "update", false, "write the answers into the test file and the examples into the spec instead of comparing")
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
		fs.BoolVar(&o.fix, "fix", false, `make the changes the problems name with "(-fix does it)" in tree.yaml, then go on`)
	}
	return fs, o
}

// repair makes the fixes of the check in tree.yaml and reads it again.
func (o *treeOptions) repair(s *spec.Spec, t *tree.Tree, out io.Writer) (*tree.Tree, error) {
	fixed, done, err := tree.Repair(s, t)
	if len(done) > 0 {
		if werr := o.writeFixes(fixed, done, out); werr != nil {
			return t, werr
		}
		if fixed, werr := tree.Load(o.file); werr == nil {
			return fixed, err
		}
	}
	return fixed, err
}

// writeFixes writes tree.yaml after fixes and says what changed.
func (o *treeOptions) writeFixes(t *tree.Tree, done []string, out io.Writer) error {
	b, err := t.Bytes()
	if err != nil {
		return err
	}
	if err := scenario.WriteFile(o.file, b); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d fixes\n", o.file, len(done))
	for _, d := range done {
		fmt.Fprintf(out, "  fixed %s\n", d)
	}
	return nil
}

// fixHint is the line after problems that -fix can change.
func (o *treeOptions) fixHint(n int) string {
	if n == 0 || o.fix {
		return ""
	}
	return fmt.Sprintf("\n  %d of them -fix changes in tree.yaml: add -fix to the same command", n)
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
	if o.fix {
		if t, err = o.repair(s, t, out); err != nil {
			return 1, err
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
	review.SetPlan(tree.PlanSection(t, nil))
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
		return 1, fmt.Errorf("%s:\n  %s%s", o.file, strings.Join(problems, "\n  "), o.fixHint(len(tree.Fixes(s, t))))
	}
	if unset := t.UnsetParams(); len(unset) > 0 {
		fmt.Fprintf(out, "%s: params without value: %s; set them at the top of the file (the nodes that use them are left out)\n", o.file, strings.Join(unset, ", "))
	}
	if o.dryRun {
		return o.dryTree(s, t, review, body, out)
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
		red := redact.New(nil)
		red.AddSecret(tok)
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
		review.SetTimeline(tree.ReviewFetch, tree.FetchTimeline(data.Log, red))
		review.SetTrace(tree.FetchTrace(data, red))
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
	review.SetPlan(tree.PlanSection(t, data))
	built, err := tree.Build(s, t, data)
	if err != nil {
		var fe *tree.FindingError
		if errors.As(err, &fe) {
			// the records do not fit the plan: the review says so
			body := fmt.Sprintf("The records do not fit tree.yaml; nothing was written.\n\n### Problems (%d)\n\n", len(fe.Problems))
			for _, p := range fe.Problems {
				body += "- " + p + "\n"
			}
			review.Set(tree.ReviewScenario, fmt.Sprintf("%d problems: fix tree.yaml", len(fe.Problems)), body, true)
			review.SetTimeline(tree.ReviewScenario, nil)
			if werr := o.writeReview(review, out); werr != nil {
				return 1, werr
			}
			if o.fix && len(fe.Fixes) > 0 {
				// the changes need the records fetched again: the test is
				// built after that fetch
				if _, done, ferr := tree.ApplyFixes(t, fe.Fixes); ferr != nil {
					return 1, ferr
				} else if werr := o.writeFixes(t, done, out); werr != nil {
					return 1, werr
				}
				return 1, fmt.Errorf("%w\n  tree.yaml changed: fetch again with -base-url (the change lines say which -node)", err)
			}
			return 1, fmt.Errorf("%w%s", err, o.fixHint(len(fe.Fixes)))
		}
		return 1, err
	}
	text, err := built.Scenario.Encode(testHead(o.file))
	if err != nil {
		return 1, err
	}
	testPath := o.next(treeTestFile)
	before, _ := os.ReadFile(testPath)
	if old, err := scenario.Load(testPath); err == nil && old.Golden != nil {
		// the answers of the steps the test still has stay; run -update
		// writes the others
		built.Scenario.Golden = keepAnswers(old.Golden, built.Scenario)
		if text, err = built.Scenario.Encode(testHead(o.file)); err != nil {
			return 1, err
		}
	}
	if err := scenario.WriteFile(testPath, text); err != nil {
		return 1, err
	}
	fmt.Fprintf(out, "  %s: %d steps, %d records created, %d of the seed\n", testPath, len(built.Scenario.Steps), len(built.Created), len(built.Seeds))
	for _, f := range []string{oldScenarioFile, oldGoldenFile} {
		if _, err := os.Stat(o.next(f)); err == nil {
			fmt.Fprintf(out, "  %s: replaced by %s; delete it\n", o.next(f), treeTestFile)
		}
	}
	summary, body = tree.ScenarioSection(built)
	review.Set(tree.ReviewScenario, summary, body, false)
	review.SetTimeline(tree.ReviewScenario, tree.TestTimeline(built))
	if !bytes.Equal(before, text) {
		review.Outdate(tree.ReviewScenario, "the scenario changed after this run; run it again")
	}
	return 0, o.writeReview(review, out)
}

// dryTree shows the plan and what a fetch would send, and the steps of the
// test when records were fetched before; it sends nothing and writes only
// the review.
func (o *treeOptions) dryTree(s *spec.Spec, t *tree.Tree, review *tree.ReviewDoc, treeBody string, out io.Writer) (int, error) {
	fmt.Fprintf(out, "dry run: nothing sent, no data or test written\n\n%s\n", tree.PlanText(t))
	plan := tree.FetchPlan(t)
	fmt.Fprintf(out, "the fetch would send (templates; values from the records above):\n")
	for _, l := range plan {
		fmt.Fprintf(out, "  %s\n", l)
	}
	treeBody += "\n### The fetch would send (dry run)\n\nTemplates; the values come from the records above.\n\n```\n" + strings.Join(plan, "\n") + "\n```\n"
	sum := "dry run: " + fmt.Sprint(len(plan)) + " requests (templates), nothing sent"
	review.Set(tree.ReviewTree, sum, treeBody, false)
	dataPath := o.next(treeDataFile)
	if _, err := os.Stat(dataPath); err == nil {
		data, _, err := tree.LoadData(dataPath, t)
		if err != nil {
			return 1, err
		}
		review.SetPlan(tree.PlanSection(t, data))
		built, err := tree.Build(s, t, data)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "\nthe test of the records fetched before (%s), not written:\n", dataPath)
		for _, st := range built.Scenario.Steps {
			fmt.Fprintf(out, "  %-32s %-40s → %d\n", st.ID, st.Call, st.Expect)
		}
	}
	fmt.Fprintf(out, "\nnext: change tree.yaml (order, tree, writes) and run it again; then fetch with -base-url\n")
	return 0, o.writeReview(review, out)
}

// testHead heads the test file.
func testHead(file string) string {
	return " Generated by \"apitest-gen tree\" from " + filepath.Base(file) + " and " + treeDataFile + ".\n" +
		" Do not edit: change tree.yaml and run apitest-gen tree again.\n" +
		" It rebuilds the records in an empty instance (apitest.RunScenario, \"apitest-gen tree run\");\n" +
		" \"apitest-gen tree run -update\" writes the answers next to the steps."
}

// keepAnswers are the answers of an earlier test for the steps the new one
// still has.
func keepAnswers(old *scenario.Snapshot, sc *scenario.Scenario) *scenario.Snapshot {
	keep := *old
	keep.Steps = nil
	for _, sh := range old.Steps {
		if sc.Step(sh.ID) != nil {
			keep.Steps = append(keep.Steps, sh)
		}
	}
	return &keep
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
	path := o.next(treeReviewFile)
	if err := scenario.WriteFile(path, []byte(r.Render())); err != nil {
		return err
	}
	if err := scenario.WriteFile(tree.HTMLPath(path), []byte(r.RenderHTML())); err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s: the review, with the next step\n", path)
	fmt.Fprintf(out, "  %s: every request and step with its bodies, in the order it ran; the fetch as a tree\n", tree.HTMLPath(path))
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
		o.scenario = o.next(treeTestFile)
	}
	if o.dryRun {
		sc, err := scenario.Load(o.scenario)
		if err != nil {
			return 2, err
		}
		fmt.Fprintf(out, "dry run: nothing sent\n%s: %d steps", o.scenario, len(sc.Steps))
		if sc.Golden != nil {
			fmt.Fprintf(out, ", answers of %s", sc.Golden.Created)
		}
		fmt.Fprintln(out)
		for _, st := range sc.Steps {
			fmt.Fprintf(out, "  %-32s %-40s → %d\n", st.ID, st.Call, st.Expect)
		}
		return 0, nil
	}
	cfg := apitest.ScenarioConfig{Settings: o.settings, Scenario: o.scenario, Update: o.update, Unique: o.uniq, Golden: o.golden}
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
	// every step for the review, and for the examples of -update
	var exchanges []apitest.StepExchange
	cfg.OnStep = func(ex apitest.StepExchange) { exchanges = append(exchanges, ex) }
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
	red := redact.New(nil)
	red.AddSecret(tok)
	red.AddSecret(forbidden)
	// the steps as they ran: the review lists them in this order
	timeline := make([]tree.Entry, 0, len(exchanges))
	for _, ex := range exchanges {
		e := tree.Entry{Step: ex.ID, Method: ex.Method, Target: red.String(ex.Target), Status: ex.Status, Ms: ex.Duration.Milliseconds(),
			Failed: !ex.Passed, HasRequest: ex.HasBody, Equals: ex.Equals}
		if !ex.Passed {
			e.Result = red.String(fmt.Sprintf("%s: %s", ex.Outcome, ex.Message))
			e.Outcome = string(ex.Outcome)
		}
		for _, p := range ex.Problems {
			e.Problems = append(e.Problems, red.String(p))
		}
		for _, d := range ex.Diffs {
			e.Diffs = append(e.Diffs, tree.EntryDiff{Pointer: d.Pointer, Expected: red.String(d.Expected), Actual: red.String(d.Actual), Note: red.String(d.Note)})
		}
		if ex.HasBody {
			e.Request = red.Value(ex.Body)
		}
		if ex.ResponseBody != nil {
			e.Answer = red.Value(ex.ResponseBody)
		}
		timeline = append(timeline, e)
	}
	if o.run != "" || o.skip != "" {
		body = fmt.Sprintf("Only a part of the steps: -run %q -skip %q.\n\n", o.run, o.skip) + body
	}
	if o.update {
		body += "\nWith -update every read was sent twice, the second right after the step: what differs is volatile and not compared later. The timeline lists each step once.\n"
	}
	if !o.update {
		switch {
		case cfg.Golden != "":
			body += "\nCompared with the golden snapshot " + cfg.Golden + ".\n"
		case hasAnswers(o.scenario):
			body += "\nCompared with the answers in " + o.scenario + ".\n"
		default:
			body += "\nNo answers yet: status, schema and checks only; run -update to write them.\n"
		}
	}
	review.Set(tree.ReviewRun, sum, body, failed)
	review.SetTimeline(tree.ReviewRun, timeline)
	if res.Failed {
		if o.update {
			fmt.Fprintf(out, "answers and examples not written: steps failed\n")
			review.Set(tree.ReviewUpdate, "not written: steps failed", "Answers and examples not written: steps failed (section 4).\n", true)
		}
		return 1, o.writeReview(review, out)
	}
	if o.update {
		where := o.golden
		if where == "" {
			where = o.scenario
		}
		fmt.Fprintf(out, "  answers written into %s\n", where)
		body := fmt.Sprintf("Answers written into %s. Review them as a git diff; a later run without -update compares with them.\n", where)
		sum := "answers written"
		if !o.noExamples {
			specPath := o.spec
			if specPath == "" {
				specPath = cfg.SpecPath
			}
			msg, examples, err := writeTreeExamples(specPath, o.scenario, exchanges)
			if err != nil {
				return 1, err
			}
			review.SetTimeline(tree.ReviewUpdate, examples)
			fmt.Fprintf(out, "  %s: %s\n", specPath, msg)
			body += fmt.Sprintf("\nExamples of the spec %s: %s.\n", specPath, msg)
			sum += "; " + msg
		} else {
			body += "\nExamples not written (-no-examples).\n"
			review.SetTimeline(tree.ReviewUpdate, nil)
		}
		review.Set(tree.ReviewUpdate, sum, body, false)
	}
	return 0, o.writeReview(review, out)
}

// hasAnswers reports whether a test file holds the answers of a run.
func hasAnswers(path string) bool {
	sc, err := scenario.Load(path)
	return err == nil && sc.Golden != nil
}

// writeTreeExamples writes the answers of a passed run into the spec, in
// place; the timeline says which step gave which example, in the order
// they ran.
func writeTreeExamples(specPath, scenarioPath string, run []apitest.StepExchange) (string, []tree.Entry, error) {
	if specPath == "" {
		return "no spec given (-spec): examples not written", nil, nil
	}
	s, err := spec.Load(context.Background(), specPath)
	if err != nil {
		return "", nil, err
	}
	sc, err := scenario.Load(scenarioPath)
	if err != nil {
		return "", nil, err
	}
	doc, err := yamldoc.Load(specPath)
	if err != nil {
		return "", nil, err
	}
	res, err := tree.Examples(s, doc, sc, run)
	if err != nil {
		return "", nil, err
	}
	timeline := tree.ExampleTimeline(s, sc, run)
	if len(res.Problems) > 0 {
		var parts []string
		for _, p := range res.Problems {
			parts = append(parts, p.Where+": "+p.Message)
		}
		return fmt.Sprintf("examples not written (%s)", strings.Join(parts, "; ")), nil, nil
	}
	if !res.SpecChanged {
		return "every example is up to date", timeline, nil
	}
	tmp, _, err := stageSpec(doc, specPath)
	if err != nil {
		return "", nil, err
	}
	if err := commitSpec(tmp, specPath); err != nil {
		os.Remove(tmp)
		return "", nil, err
	}
	return fmt.Sprintf("%d examples and %d schema examples written", res.Examples, res.SchemaExamples), timeline, nil
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
