package apitest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// bookstoreScenario walks one author and one book of the Bookstore
// through their life: create, read, list, update, error and
// authentication steps, delete.
const bookstoreScenario = `
order: [Author, Book]
steps:
  - id: author1.create
    call: POST /authors
    body: {name: Ada Lovelace, born: 1815}
    save: {author1.authorId: /id}
    expect: 201
  - id: author1.read
    call: getAuthor
    path: {authorId: '{{author1.authorId}}'}
    expect: 200
    check: {equals: author1.create}
  - id: author.list
    call: GET /authors
    expect: 200
    check: {contains: {key: /id, values: ['{{author1.authorId}}']}}
  - id: author.read.unknown
    call: getAuthor
    path: {authorId: 999999}
    expect: 404
  - id: author1.read.unauthorized
    call: getAuthor
    path: {authorId: '{{author1.authorId}}'}
    token: none
    expect: 401
  - id: author1.update.forbidden
    call: updateAuthor
    path: {authorId: '{{author1.authorId}}'}
    body: {name: Ada King, born: 1815}
    token: forbidden
    expect: 403
  - id: book1.create
    call: createBook
    path: {authorId: '{{author1.authorId}}'}
    body: {title: Go, price: 29.9}
    save: {book1.bookId: header Location}
    expect: 201
  - id: book1.read
    call: getBook
    path: {bookId: '{{book1.bookId}}'}
    expect: 200
    check: {equals: book1.create}
  - id: book1.update
    call: updateBook
    path: {bookId: '{{book1.bookId}}'}
    body: {title: Go 2, price: 35}
    expect: 200
  - id: book1.read.afterUpdate
    call: getBook
    path: {bookId: '{{book1.bookId}}'}
    expect: 200
    check: {equals: book1.update}
  - id: book1.delete
    call: DELETE /books/{bookId}
    path: {bookId: '{{book1.bookId}}'}
    expect: 204
  - id: book1.read.afterDelete
    call: getBook
    path: {bookId: '{{book1.bookId}}'}
    expect: 404
`

// scenarioFiles writes a scenario into a temporary directory and returns
// the paths of the scenario and the golden snapshot.
func scenarioFiles(t *testing.T, text string) (sc, golden string) {
	t.Helper()
	dir := t.TempDir()
	sc = filepath.Join(dir, "tree-scenario_gen.yaml")
	if err := os.WriteFile(sc, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return sc, filepath.Join(dir, "tree-golden_gen.yaml")
}

func bookstoreScenarioConfig(t *testing.T, f testserver.Faults, sc, golden string) ScenarioConfig {
	t.Helper()
	return ScenarioConfig{
		Config: Config{
			SpecPath:       bookstoreSpec,
			Handler:        bookstore(f),
			Token:          StaticToken(testToken),
			ForbiddenToken: StaticToken(readerToken),
		},
		Scenario: sc,
		Golden:   golden,
	}
}

func runScenarioFake(t *testing.T, cfg ScenarioConfig) (*Result, *fakeT) {
	t.Helper()
	ft := newFakeT(t, "TestScenario")
	res := runScenario(ft, cfg)
	ft.finish()
	return res, ft
}

func statusOf(res *Result) map[string]Status {
	m := map[string]Status{}
	for _, c := range res.Cases {
		m[c.Name] = c.Status
	}
	return m
}

func TestScenarioUpdateThenCompare(t *testing.T) {
	sc, golden := scenarioFiles(t, bookstoreScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.Update = true
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("update failed:\n%s", ft.output())
	}
	b, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"kind: golden", "id: author1.create", "'{{author1.authorId}}'", "Location: '/v1/books/{{book1.bookId}}'", "name: Ada Lovelace"} {
		if !strings.Contains(text, want) {
			t.Errorf("golden snapshot misses %q:\n%s", want, text)
		}
	}
	snap, err := scenario.LoadSnapshot(golden)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Steps) != 12 {
		t.Errorf("golden has %d steps, want 12", len(snap.Steps))
	}

	// a fresh instance gives other ids for the seed-free records only by
	// chance; the placeholders keep the comparison independent of them
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	res, ft = runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("compare failed:\n%s", ft.output())
	}
	got := statusOf(res)
	if len(got) != 12 {
		t.Errorf("got %d results, want 12: %v", len(got), got)
	}
	for name, st := range got {
		if st != StatusPassed {
			t.Errorf("%s: %s", name, st)
		}
	}
	if got["Author/author1.read.unauthorized"] != StatusPassed {
		t.Errorf("authentication step missing: %v", got)
	}
}

func TestScenarioFaults(t *testing.T) {
	sc, golden := scenarioFiles(t, bookstoreScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.Update = true
	if res, ft := runScenarioFake(t, cfg); res.Failed {
		t.Fatalf("update failed:\n%s", ft.output())
	}
	tests := []struct {
		fault  testserver.Faults
		step   string
		status Status
	}{
		{testserver.Faults{ExampleMismatch: true}, "Author/author1.create", StatusExampleMismatch},
		{testserver.Faults{WrongStatus: true}, "Author/author1.create", StatusFailed},
		{testserver.Faults{SchemaViolation: true}, "Book/book1.read", StatusSchemaViolation},
		{testserver.Faults{DropOnUpdate: true}, "Book/book1.read.afterUpdate", StatusExampleMismatch},
		{testserver.Faults{IgnoreAuth: true}, "Author/author1.read.unauthorized", StatusFailed},
		{testserver.Faults{IgnoreRoles: true}, "Author/author1.update.forbidden", StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.step, func(t *testing.T) {
			res, ft := runScenarioFake(t, bookstoreScenarioConfig(t, tt.fault, sc, golden))
			if !res.Failed {
				t.Fatalf("run passed:\n%s", ft.output())
			}
			if got := statusOf(res)[tt.step]; got != tt.status {
				t.Errorf("%s: got %s, want %s\n%s", tt.step, got, tt.status, ft.output())
			}
		})
	}
	// a failed producer skips the steps that need its value
	res, _ := runScenarioFake(t, bookstoreScenarioConfig(t, testserver.Faults{FailCreateAuthor: true}, sc, golden))
	if got := statusOf(res)["Author/author1.read"]; got != StatusSkipped {
		t.Errorf("author1.read after a failed create: %s", got)
	}
}

func TestScenarioEqualsCheck(t *testing.T) {
	// without golden comparison (schema mode), the equals check finds the
	// update that was not stored
	sc, golden := scenarioFiles(t, bookstoreScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.Update = true
	if res, ft := runScenarioFake(t, cfg); res.Failed {
		t.Fatalf("update failed:\n%s", ft.output())
	}
	cfg = bookstoreScenarioConfig(t, testserver.Faults{DropOnUpdate: true}, sc, golden)
	cfg.CompareMode = CompareSchema
	res, ft := runScenarioFake(t, cfg)
	if got := statusOf(res)["Book/book1.read.afterUpdate"]; got != StatusDataMismatch {
		t.Errorf("got %s, want DATA_MISMATCH\n%s", got, ft.output())
	}
}

func TestScenarioErrorsAndSelection(t *testing.T) {
	sc, golden := scenarioFiles(t, bookstoreScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.Update = true
	if res, ft := runScenarioFake(t, cfg); res.Failed {
		t.Fatalf("update failed:\n%s", ft.output())
	}

	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.Errors = []string{"401", "403"}
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("run failed:\n%s", ft.output())
	}
	got := statusOf(res)
	if _, ok := got["Author/author.read.unknown"]; ok {
		t.Error("404 step ran although Errors lists only 401 and 403")
	}
	var order []string
	for _, c := range res.Cases {
		order = append(order, c.Name)
	}
	if i, j := indexOf(order, "Author/author1.read.unauthorized"), indexOf(order, "Author/author1.update.forbidden"); i < 0 || j < 0 || i > j {
		t.Errorf("401 must run before 403: %v", order)
	}
	cfg.Errors = []string{"403", "401"}
	res, _ = runScenarioFake(t, cfg)
	order = nil
	for _, c := range res.Cases {
		order = append(order, c.Name)
	}
	if i, j := indexOf(order, "Author/author1.read.unauthorized"), indexOf(order, "Author/author1.update.forbidden"); i < j {
		t.Errorf("Errors 403, 401 must run 403 first: %v", order)
	}

	// -run selects one step; the steps it needs run as preconditions
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	ft = newFakeT(t, "TestScenario")
	ft.filter = func(name string) bool {
		return name == "TestScenario/Book" || name == "TestScenario/Book/book1.read" || !strings.Contains(strings.TrimPrefix(name, "TestScenario/"), "/") && name == "TestScenario"
	}
	res = runScenario(ft, cfg)
	ft.finish()
	if res.Failed {
		t.Fatalf("selected run failed:\n%s", ft.output())
	}
	pre := map[string]bool{}
	for _, c := range res.Cases {
		pre[c.Name] = c.Precondition
	}
	if len(pre) != 3 || !pre["Author/author1.create"] || !pre["Book/book1.create"] || pre["Book/book1.read"] {
		t.Errorf("want book1.read with the preconditions author1.create and book1.create, got %v", pre)
	}

	// Tags selects by tag; SkipAuthCases leaves the authentication steps out
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.SkipAuthCases = []string{AuthUnauthorized, AuthForbidden}
	res, ft = runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("run failed:\n%s", ft.output())
	}
	for name := range statusOf(res) {
		if strings.HasSuffix(name, ".unauthorized") || strings.HasSuffix(name, ".forbidden") {
			t.Errorf("%s ran although SkipAuthCases leaves it out", name)
		}
	}
}

func TestScenarioConfigErrors(t *testing.T) {
	sc, golden := scenarioFiles(t, bookstoreScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.MethodOrder = []string{"POST"}
	cfg.Errors = []string{"40"}
	res, ft := runScenarioFake(t, cfg)
	out := ft.output()
	if !res.Failed || !strings.Contains(out, "MethodOrder") || !strings.Contains(out, `"40" is no status`) {
		t.Errorf("want configuration errors, got:\n%s", out)
	}
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.BaseURL = "http://localhost:1"
	if res, ft := runScenarioFake(t, cfg); !res.Failed || !strings.Contains(ft.output(), "both set") {
		t.Errorf("Handler and BaseURL together must fail:\n%s", ft.output())
	}
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	if res, ft := runScenarioFake(t, cfg); !res.Failed || !strings.Contains(ft.output(), "golden snapshot") {
		t.Errorf("a missing golden snapshot must fail:\n%s", ft.output())
	}
	// Update without Golden writes the answers into the scenario file; a
	// later run compares with them
	own, _ := scenarioFiles(t, bookstoreScenario)
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, own, "")
	cfg.Update = true
	if res, ft := runScenarioFake(t, cfg); res.Failed {
		t.Errorf("Update without Golden writes into the scenario:\n%s", ft.output())
	}
	if loaded, err := scenario.Load(own); err != nil || loaded.Golden == nil || len(loaded.Golden.Steps) != len(loaded.Steps) {
		t.Errorf("the answers in the scenario file: %v %+v", err, loaded)
	}
	cfg = bookstoreScenarioConfig(t, testserver.Faults{ExampleMismatch: true}, own, "")
	if res, ft := runScenarioFake(t, cfg); !res.Failed || !strings.Contains(ft.output(), "golden snapshot") {
		t.Errorf("a run compares with the answers of the scenario file:\n%s", ft.output())
	}
	// without golden snapshot: status, schema and checks only
	cfg = bookstoreScenarioConfig(t, testserver.Faults{ExampleMismatch: true}, sc, "")
	var seen []StepExchange
	cfg.OnStep = func(ex StepExchange) { seen = append(seen, ex) }
	if res, ft := runScenarioFake(t, cfg); res.Failed {
		t.Errorf("a run without golden snapshot compares no values:\n%s", ft.output())
	}
	if len(seen) != 12 || seen[0].Target != "/authors" || seen[0].Status != 201 || !seen[0].HasBody {
		t.Errorf("OnStep: %+v", seen)
	}
	bad, _ := scenarioFiles(t, "steps:\n  - id: a\n    call: getNothing\n    expect: 200\n")
	cfg = bookstoreScenarioConfig(t, testserver.Faults{}, bad, golden)
	cfg.Update = true
	if res, ft := runScenarioFake(t, cfg); !res.Failed || !strings.Contains(ft.output(), "names no operation") {
		t.Errorf("an unknown call must fail:\n%s", ft.output())
	}
}

func TestScenarioSettingsAndUnique(t *testing.T) {
	dir := t.TempDir()
	spec, err := filepath.Abs(bookstoreSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOKS_TEST_TOKEN", testToken)
	settings := filepath.Join(dir, "settings.yaml")
	text := "spec: " + spec + "\ntokens:\n  default: env:BOOKS_TEST_TOKEN\nreport: out/tree-report_gen.md\nunique: true\n"
	if err := os.WriteFile(settings, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := filepath.Join(dir, "tree-scenario_gen.yaml")
	if err := os.WriteFile(sc, []byte(`
steps:
  - id: shelf1.create
    call: createShelf
    body: {code: l, name: Logistics}
    save: {shelf1.code: request /code}
    unique: [/code]
    expect: 201
  - id: shelf1.read
    call: getShelf
    path: {code: '{{shelf1.code}}'}
    expect: 200
    check: {equals: shelf1.create}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(dir, "tree-golden_gen.yaml")
	srv := bookstore(testserver.Faults{})
	cfg := ScenarioConfig{Config: Config{Handler: srv}, Settings: settings, Scenario: sc, Golden: golden, Update: true}
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("update failed:\n%s", ft.output())
	}
	b, _ := os.ReadFile(golden)
	if strings.Contains(string(b), "l-") || !strings.Contains(string(b), "Logistics") {
		t.Errorf("the golden snapshot must hold the original values:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "tree-report_gen.md")); err != nil {
		t.Errorf("report from the settings not written: %v", err)
	}
	// the same instance again: unique values keep the steps apart
	cfg = ScenarioConfig{Config: Config{Handler: srv, DisableReports: true}, Settings: settings, Scenario: sc, Golden: golden}
	for range 2 {
		res, ft = runScenarioFake(t, cfg)
		if res.Failed {
			t.Fatalf("compare failed:\n%s", ft.output())
		}
	}
	t.Setenv("BOOKS_TEST_TOKEN", "")
	cfg = ScenarioConfig{Config: Config{Handler: srv, DisableReports: true}, Settings: settings, Scenario: sc, Golden: golden}
	if res, ft := runScenarioFake(t, cfg); !res.Failed || !strings.Contains(ft.output(), "BOOKS_TEST_TOKEN is not set") {
		t.Errorf("a missing token variable must fail:\n%s", ft.output())
	}
}

func TestScenarioStandalone(t *testing.T) {
	sc, golden := scenarioFiles(t, bookstoreScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{}, sc, golden)
	cfg.Update = true
	var out strings.Builder
	res, err := RunScenarioStandalone(context.Background(), cfg, StandaloneOptions{Out: &out})
	if err != nil || res.Failed {
		t.Fatalf("update failed: %v\n%s", err, out.String())
	}
	cfg = bookstoreScenarioConfig(t, testserver.Faults{ExampleMismatch: true}, sc, golden)
	out.Reset()
	res, err = RunScenarioStandalone(context.Background(), cfg, StandaloneOptions{Out: &out, Run: "Author/"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !res.Failed || !strings.Contains(text, "--- FAIL: Author/author1.create") || strings.Contains(text, "Book/book1.read") && strings.Contains(text, "--- PASS: Book/book1.read") {
		t.Errorf("unexpected output:\n%s", text)
	}
	if _, err := RunScenarioStandalone(context.Background(), cfg, StandaloneOptions{Run: "a["}); err == nil {
		t.Error("an invalid -run pattern must be an error")
	}
}
