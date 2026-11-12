package apitest

import (
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// toleratedScenario: book1.read saves the key book1.update takes, so a
// read that ends with a schema violation skips the update unless the
// violation is tolerated.
const toleratedScenario = `
order: [Author, Book]
steps:
  - id: author1.create
    call: POST /authors
    body: {name: Ada Lovelace, born: 1815}
    save: {author1.authorId: /id}
    expect: 201
  - id: book1.create
    call: createBook
    path: {authorId: '{{author1.authorId}}'}
    body: {title: Go, price: 29.9}
    save: {book1.bookId: header Location}
    expect: 201
  - id: book1.read
    call: getBook
    path: {bookId: '{{book1.bookId}}'}
    save: {book1.again: /id}
    expect: 200
  - id: book1.update
    call: updateBook
    path: {bookId: '{{book1.again}}'}
    body: {title: Go 2, price: 35}
    expect: 200
  - id: book1.read.afterUpdate
    call: getBook
    path: {bookId: '{{book1.again}}'}
    expect: 200
    check: {equals: book1.update}
`

func TestScenarioTolerateSchemaViolation(t *testing.T) {
	sc, _ := scenarioFiles(t, toleratedScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{SchemaViolation: true}, sc, "")
	cfg.CompareMode = CompareSchema
	res, _ := runScenarioFake(t, cfg)
	if got := statusOf(res)["Book/book1.read"]; got != StatusSchemaViolation || !res.Failed {
		t.Fatalf("without tolerate: book1.read %s, failed %v", got, res.Failed)
	}
	if got := statusOf(res)["Book/book1.update"]; got != StatusSkipped {
		t.Fatalf("without tolerate: book1.update %s, want SKIPPED", got)
	}

	cfg = bookstoreScenarioConfig(t, testserver.Faults{SchemaViolation: true}, sc, "")
	cfg.CompareMode = CompareSchema
	cfg.Tolerate = []Status{StatusSchemaViolation}
	var seen []StepExchange
	cfg.OnStep = func(ex StepExchange) { seen = append(seen, ex) }
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("tolerated schema violation fails the run:\n%s", ft.output())
	}
	got := statusOf(res)
	if got["Book/book1.read"] != StatusTolerated || got["Book/book1.update"] != StatusPassed {
		t.Fatalf("book1.read %s (want TOLERATED), book1.update %s (want PASSED)\n%s", got["Book/book1.read"], got["Book/book1.update"], ft.output())
	}
	i := slices.IndexFunc(res.Cases, func(c CaseResult) bool { return c.Name == "Book/book1.read" })
	if c := res.Cases[i]; c.Tolerated != StatusSchemaViolation || !strings.Contains(c.Message, "SCHEMA_VIOLATION") {
		t.Errorf("case of the tolerated read: %+v", c)
	}
	// the problems stay for the review
	j := slices.IndexFunc(seen, func(ex StepExchange) bool { return ex.ID == "book1.read" })
	if j < 0 || seen[j].Tolerated != StatusSchemaViolation || len(seen[j].Problems) == 0 {
		t.Errorf("OnStep of the tolerated read: %+v", seen)
	}
}

func TestScenarioTolerateFromFile(t *testing.T) {
	// the tolerate list of the scenario file: a data mismatch passes the run
	sc, _ := scenarioFiles(t, "tolerate: [DATA_MISMATCH]\n"+toleratedScenario)
	cfg := bookstoreScenarioConfig(t, testserver.Faults{DropOnUpdate: true}, sc, "")
	cfg.CompareMode = CompareSchema
	var seen []StepExchange
	cfg.OnStep = func(ex StepExchange) { seen = append(seen, ex) }
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("tolerated data mismatch fails the run:\n%s", ft.output())
	}
	if got := statusOf(res)["Book/book1.read.afterUpdate"]; got != StatusTolerated {
		t.Fatalf("book1.read.afterUpdate: %s, want TOLERATED\n%s", got, ft.output())
	}
	j := slices.IndexFunc(seen, func(ex StepExchange) bool { return ex.ID == "book1.read.afterUpdate" })
	if j < 0 || seen[j].Tolerated != StatusDataMismatch || len(seen[j].Diffs) == 0 {
		t.Errorf("OnStep of the tolerated mismatch: %+v", seen)
	}

	// a result tolerate cannot name
	bad, _ := scenarioFiles(t, "tolerate: [FAILED]\n"+toleratedScenario)
	res, ft = runScenarioFake(t, bookstoreScenarioConfig(t, testserver.Faults{}, bad, ""))
	if !res.Failed || !strings.Contains(ft.output(), "tolerate") {
		t.Errorf("tolerate: [FAILED] accepted:\n%s", ft.output())
	}
}
