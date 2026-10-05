package bind

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

func load(t *testing.T, file string) *spec.Spec {
	t.Helper()
	s, err := spec.Load(t.Context(), "../../testdata/specs/"+file)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func bindingOf(t *testing.T, s *spec.Spec, set *Set, opID, param string) *Binding {
	t.Helper()
	op := s.Op(opID)
	if op == nil {
		t.Fatalf("operation %s missing", opID)
	}
	for _, p := range op.Params {
		if p.Name == param {
			return set.For(op, p)
		}
	}
	t.Fatalf("parameter %s missing in %s", param, opID)
	return nil
}

func TestResolveSources(t *testing.T) {
	s := load(t, "bind.yaml")
	set, err := Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		op, param, producer string
		kind                Kind
		src                 Source
	}{
		{"createBook", "authorId", "createAuthor", Explicit, Source{Pointer: "/id"}},
		{"getBook", "bookId", "createBook", Link, Source{Header: "Location"}},
		{"createReview", "bookId", "createBook", Link, Source{Header: "Location"}},
		{"createReview", "lang", "createBook", Link, Source{Const: "de", HasConst: true}},
		{"getReview", "reviewId", "createReview", Link, Source{Pointer: "/review/id"}},
		{"getAuthor", "authorId", "createAuthor", Heuristic, Source{Pointer: "/id"}},
		// The response has no username, so the heuristic falls back to the request body.
		{"getUser", "username", "createUser", Heuristic, Source{Pointer: "/username", FromRequest: true}},
	}
	for _, tc := range tests {
		b := bindingOf(t, s, set, tc.op, tc.param)
		if b == nil {
			t.Errorf("%s.%s: no binding", tc.op, tc.param)
			continue
		}
		if b.Producer.ID != tc.producer || b.Kind != tc.kind || b.Source != tc.src {
			t.Errorf("%s.%s: got %s %v %+v, want %s %v %+v", tc.op, tc.param, b.Producer.ID, b.Kind, b.Source, tc.producer, tc.kind, tc.src)
		}
	}
	if b := bindingOf(t, s, set, "getOrphan", "orphanId"); b != nil {
		t.Errorf("no producer exists, got %+v", b)
	}
	// createBook is in group Book, createAuthor in Author: the heuristic must
	// not bind across groups, only the explicit declaration may.
	if b := bindingOf(t, s, set, "createBook", "authorId"); !b.CrossGroup() {
		t.Error("createBook.authorId must be a cross-group binding")
	}

	var heuristics, problems []string
	for _, f := range set.Findings {
		switch f.Kind {
		case spec.FindingHeuristic:
			heuristics = append(heuristics, f.Where)
		case spec.FindingBinding:
			problems = append(problems, f.Message)
		}
	}
	if len(heuristics) != 2 {
		t.Errorf("heuristic findings: %v, want getAuthor and getUser", heuristics)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, `"$url"`) || !strings.Contains(joined, `"doesNotExist"`) || !strings.Contains(joined, `"broken"`) {
		t.Errorf("link problems must be reported: %v", problems)
	}
	if got := len(set.By(s.Op("createBook"))); got != 3 {
		t.Errorf("createBook produces %d bindings, want 3", got)
	}
}

func TestExplicitBindingErrors(t *testing.T) {
	_, err := Resolve(load(t, "bind_errors.yaml"))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{`operation "nope" does not exist`, "set pointer", `pointer "id" must start with /`, "paths./a/{x}.get.parameters[x]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q:\n%v", want, err)
		}
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTPL1_12_PointerIntoNestedBody(t *testing.T) {
	body := decode(t, `{"data":[{"code":"l","meta":{"a/b":7}}],"id":3}`)
	for ptr, want := range map[string]string{
		"/id":               "3",
		"/data/0/code":      "l",
		"/data/0/meta/a~1b": "7",
	} {
		v, ok := Pointer(body, ptr)
		if !ok || jsonString(v) != want {
			t.Errorf("Pointer(%s): got %v %v, want %s", ptr, v, ok, want)
		}
	}
	for _, ptr := range []string{"/missing", "/data/5", "/data/x", "id"} {
		if _, ok := Pointer(body, ptr); ok {
			t.Errorf("Pointer(%s) must fail", ptr)
		}
	}
	v, ok := Extract(Source{Pointer: "/data/0/code"}, Explicit, http.Header{}, body, nil)
	if !ok || v != "l" {
		t.Errorf("Extract: got %v %v", v, ok)
	}
}

func TestTPL1_13_LocationHeader(t *testing.T) {
	for loc, want := range map[string]string{
		"/books/42":                   "42",
		"/books/42/":                  "42",
		"http://h:8080/v1/books/42?x": "42",
		"/users/John%20Doe":           "John Doe",
	} {
		if got := LastSegment(loc); got != want {
			t.Errorf("LastSegment(%q): got %q, want %q", loc, got, want)
		}
	}
	h := http.Header{"Location": {"/books/42"}}
	if v, ok := Extract(Source{Header: "Location"}, Link, h, nil, nil); !ok || v != "42" {
		t.Errorf("Extract Location: got %v %v", v, ok)
	}
	if _, ok := Extract(Source{Header: "Location"}, Link, http.Header{}, nil, nil); ok {
		t.Error("missing header must not give a value")
	}
	if v, ok := Extract(Source{Header: "X-Id"}, Link, http.Header{"X-Id": {"a/b"}}, nil, nil); !ok || v != "a/b" {
		t.Errorf("other headers are used verbatim: %v", v)
	}
}

func TestHeuristicFallbacks(t *testing.T) {
	loc := http.Header{"Location": {"/authors/9"}}
	if v, ok := Extract(Source{Pointer: "/id"}, Heuristic, loc, nil, nil); !ok || v != "9" {
		t.Errorf("heuristic /id falls back to Location: %v %v", v, ok)
	}
	if _, ok := Extract(Source{Pointer: "/id"}, Explicit, loc, nil, nil); ok {
		t.Error("explicit pointer bindings must not guess")
	}
	req := decode(t, `{"username":"theUser"}`)
	if v, ok := Extract(Source{Pointer: "/username"}, Heuristic, http.Header{}, decode(t, `{}`), req); !ok || v != "theUser" {
		t.Errorf("heuristic falls back to the request body: %v %v", v, ok)
	}
	if v, ok := Extract(Source{Const: 5, HasConst: true}, Link, nil, nil, nil); !ok || v != 5 {
		t.Errorf("constant: %v", v)
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

func TestHeuristicCaseInsensitiveAndResourceNames(t *testing.T) {
	s := load(t, "heuristic_ci.yaml")
	set, err := Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		op, param, producer string
		src                 Source
	}{
		// the collection is found behind the literal segment "by-id"; "Id" matches case-insensitively
		{"getTicketById", "ticketId", "createTicket", Source{Pointer: "/Id"}},
		{"getBin", "ticketId", "createTicket", Source{Pointer: "/Id"}},
		// found by the resource name of the parameter
		{"getBin", "binCode", "createBin", Source{Pointer: "/BinCode"}},
		// found by the request field with the parameter's name
		{"getLot", "lotNumber", "receiveGoods", Source{Pointer: "/LotNumber", FromRequest: true}},
	}
	for _, tc := range tests {
		b := bindingOf(t, s, set, tc.op, tc.param)
		if b == nil {
			t.Errorf("%s.%s: no binding", tc.op, tc.param)
			continue
		}
		if b.Producer.ID != tc.producer || b.Source != tc.src || b.Kind != Heuristic {
			t.Errorf("%s.%s: got %s %+v %v, want %s %+v heuristic", tc.op, tc.param, b.Producer.ID, b.Source, b.Kind, tc.producer, tc.src)
		}
	}
}

func TestHeuristicDoesNotCloseCycle(t *testing.T) {
	s := load(t, "heuristic_ci.yaml")
	set, err := Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	if b := bindingOf(t, s, set, "createPallet", "crateId"); b == nil || b.Producer.ID != "createCrate" {
		t.Fatalf("explicit binding missing: %+v", b)
	}
	if b := bindingOf(t, s, set, "createCrate", "palletId"); b != nil {
		t.Errorf("heuristic binding closes a cycle: %s from %s", b.Param.Name, b.Producer.ID)
	}
}

func TestHeuristicAcrossGroups(t *testing.T) {
	s := load(t, "heuristic_ci.yaml")
	set, err := Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	if b := bindingOf(t, s, set, "assignCarrier", "carrierId"); b == nil || b.Producer.ID != "createCarrier" || b.Source != (Source{Pointer: "/Id"}) {
		t.Errorf("carrierId: %+v", b)
	}
	// Gate → Dock would close the group cycle Dock → Gate → Dock.
	if b := bindingOf(t, s, set, "getGateDock", "dockId"); b != nil {
		t.Errorf("dockId bound from %s, closing a group cycle", b.Producer.ID)
	}
	if b := bindingOf(t, s, set, "getGateDock", "gateId"); b == nil || b.Producer.ID != "createGate" {
		t.Errorf("gateId: %+v", b)
	}
}
