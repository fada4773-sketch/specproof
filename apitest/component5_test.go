package apitest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// Component tests for the order and output options: NumberCases,
// MethodOrder, DeleteLast, DisableWarnings and ReportPassedDetails.

func indexOf(names []string, name string) int { return slices.Index(names, name) }

func TestNumberCases(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.NumberCases = true
	out := runFake(t, cfg)
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}

	// Subtests carry the number of their position, results and report too.
	numbered := regexp.MustCompile(`^TestBookstore/(\d+)_(.+)$`)
	numbers := map[string]string{}
	for i, sub := range out.ft.subs {
		m := numbered.FindStringSubmatch(sub.name)
		if m == nil {
			t.Fatalf("subtest %q has no number", sub.name)
		}
		if want := i + 1; out.res.Cases[i].Number != want || m[1] != fmt.Sprintf("%0*d", len(m[1]), want) {
			t.Fatalf("subtest %d is %q, result number %d", i, sub.name, out.res.Cases[i].Number)
		}
		numbers[m[2]] = m[1]
	}
	first := out.res.Cases[0]
	if first.Name != executionOrder[0] {
		t.Errorf("case names must stay without number: %q", first.Name)
	}
	if !strings.Contains(out.report, numbers[executionOrder[0]]+" "+executionOrder[0]) {
		t.Errorf("report does not show the number of %s", executionOrder[0])
	}

	// With -run the selected case keeps its number from the full run.
	target := "Book/getBook/default"
	want := numbers[target] + "_" + target
	ft := newFakeT(t, "TestBookstore")
	ft.filter = func(full string) bool { return strings.HasSuffix(full, want) }
	res := run(ft, cfg)
	ft.finish()
	if len(ft.subs) != 1 || ft.subs[0].name != "TestBookstore/"+want {
		var names []string
		for _, s := range ft.subs {
			names = append(names, s.name)
		}
		t.Errorf("filtered run started %v, want only %s", names, want)
	}
	if res.Failed {
		t.Errorf("filtered run failed:\n%s", ft.output())
	}
}

func TestMethodOrder(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.MethodOrder = []string{"PUT", "POST", "GET"}
	// After the PUT, GET returns the changed data, not its example.
	cfg.CompareMode = CompareSchema
	out := runFake(t, cfg)
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}
	names := order(out.res)
	create, update, get := indexOf(names, "Author/createAuthor/valid-author"), indexOf(names, "Author/updateAuthor/default"), indexOf(names, "Author/getAuthor/default")
	// PUT before GET as configured; the POST still runs first, because the
	// PUT needs the id it creates.
	if create > update || update > get {
		t.Errorf("order create=%d update=%d get=%d:\n%s", create, update, get, strings.Join(names, "\n"))
	}

	for _, bad := range [][]string{{"DELETE", "GET"}, {"GET", "get"}, {"FETCH"}} {
		c := Config{SpecPath: "x", BaseURL: "http://localhost", MethodOrder: bad}
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), "Config.MethodOrder") {
			t.Errorf("%v: %v", bad, err)
		}
	}
	ok := Config{SpecPath: "x", BaseURL: "http://localhost", MethodOrder: []string{"post", "GET", "DELETE"}}
	if err := ok.validate(); err != nil {
		t.Errorf("valid order rejected: %v", err)
	}
}

// LastInTag runs listAuthors after every other case of Author, whatever
// the method; only the DELETE of the tag follows. An unknown operation
// stops the run before a request.
func TestLastInTag(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.LastInTag = []string{"listAuthors"}
	// the list runs after the PUT, so it shows the changed author
	cfg.CompareMode = CompareSchema
	out := runFake(t, cfg)
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}
	names := order(out.res)
	list, del := -1, -1
	for i, n := range names {
		switch {
		case strings.HasPrefix(n, "Author/listAuthors/") && list < 0:
			list = i
		case strings.HasPrefix(n, "Author/deleteAuthor/") && del < 0:
			del = i
		}
	}
	for i, n := range names {
		if strings.HasPrefix(n, "Author/") && !strings.HasPrefix(n, "Author/listAuthors/") && !strings.HasPrefix(n, "Author/deleteAuthor/") && i > list {
			t.Errorf("%s runs after listAuthors:\n%s", n, strings.Join(names, "\n"))
		}
	}
	if list < 0 || del < list {
		t.Errorf("list=%d delete=%d:\n%s", list, del, strings.Join(names, "\n"))
	}

	cfg = bookstoreConfig(t, testserver.Faults{})
	cfg.LastInTag = []string{"archiveAuthor"}
	if out := runFake(t, cfg); !out.res.Failed || !strings.Contains(out.ft.output(), `Config.LastInTag: operation "archiveAuthor" from LastInTag does not exist`) {
		t.Errorf("unknown operation:\n%s", out.ft.output())
	}
}

func TestDeleteLast(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.DeleteLast = true
	out := runFake(t, cfg)
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}
	firstDelete := -1
	for i, c := range out.res.Cases {
		isDelete := strings.HasPrefix(c.Operation, "delete")
		switch {
		case isDelete && firstDelete < 0:
			firstDelete = i
		case !isDelete && firstDelete >= 0:
			t.Errorf("%s runs after a DELETE (%s)", c.Name, out.res.Cases[firstDelete].Name)
		}
	}
	if firstDelete < 0 {
		t.Fatal("no DELETE cases")
	}
}

func TestDisableWarnings(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.Params = map[string]string{"noSuchOperation.id": "1"} // produces a warning
	cfg.DisableWarnings = true
	out := runFake(t, cfg)
	const warning = `Config.Params key "noSuchOperation.id" matches no parameter`
	if strings.Contains(out.ft.output(), warning) {
		t.Error("warning printed despite DisableWarnings")
	}
	if strings.Contains(out.report, warning) {
		t.Error("warning in the report despite DisableWarnings")
	}
	if strings.Contains(out.report, "Spec findings") {
		t.Error("spec findings in the report despite DisableWarnings")
	}

	cfg.DisableWarnings = false
	out = runFake(t, cfg)
	if !strings.Contains(out.ft.output(), warning) || !strings.Contains(out.report, warning) {
		t.Error("warning not printed and reported by default")
	}
	if !strings.Contains(out.report, "🔎 Spec findings (") {
		t.Error("spec findings missing by default")
	}
}

func TestReportPassedDetails(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	out := runFake(t, cfg)
	if strings.Contains(out.report, "### ✅") {
		t.Error("passed cases have details by default")
	}

	cfg.ReportPassedDetails = true
	out = runFake(t, cfg)
	section := "### ✅ " + executionOrder[0] + " — PASSED"
	i := strings.Index(out.report, section)
	if i < 0 {
		t.Fatalf("report has no detail section %q", section)
	}
	detail := out.report[i:]
	if next := strings.Index(detail[len(section):], "\n### "); next >= 0 {
		detail = detail[:len(section)+next]
	}
	for _, want := range []string{"<summary>Request</summary>", "<summary>Response (201)</summary>", "Response headers (201)", "<summary>Reproduce</summary>", "$TOKEN"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail of %s misses %q", executionOrder[0], want)
		}
	}
	if strings.Contains(out.report, testToken) {
		t.Error("report leaks the token")
	}
}

// Deviation entries may use names copied from go test or an IDE, also with
// the number of NumberCases; an entry that names the case but accepts another
// result explains itself in the message.
func TestDeviationNamesAndNearMisses(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{WrongStatus: true}) // POST /authors answers 200
	cfg.NumberCases = true
	cfg.DeviationsPath = writeDeviations(t, `
- case: TestBookstore/01_Author/createAuthor/valid-author
  expected: 201
  actual: 200
  reason: copied from the IDE, with test name and number
  expires: 2099-12-31
`)
	out := runFake(t, cfg)
	if st := statuses(out.res)["Author/createAuthor/valid-author"]; st != StatusDeviation {
		t.Errorf("numbered name: %s %q", st, message(out.res, "Author/createAuthor/valid-author"))
	}

	cfg.DeviationsPath = writeDeviations(t, `
- case: Author/createAuthor/valid-author
  expected: 201
  actual: 202
  reason: wrong actual status
  expires: 2099-12-31
`)
	out = runFake(t, cfg)
	msg := message(out.res, "Author/createAuthor/valid-author")
	if st := statuses(out.res)["Author/createAuthor/valid-author"]; st != StatusFailed || !strings.Contains(msg, "deviation entry 1 names this case but accepts 201 → 202, the result is 201 → 200") {
		t.Errorf("near miss: %s %q", st, msg)
	}
}
