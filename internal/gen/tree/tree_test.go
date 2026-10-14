package tree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

func parse(t *testing.T, text string) *Tree {
	t.Helper()
	tr, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, text)
	}
	return tr
}

func TestParseForms(t *testing.T) {
	tr := parse(t, `
seed: 7
budget: 50
tree:
  planets:
    call: GET /planets
    pick: [mars, venus]
    key: /code
    write:
      create: none
    each:
      planet: GET /planets/{code} code=.code
      docks:
        call: listDocks
        path: {code: planets.code}
        query: {pageSize: 1}
        page: {param: page, max: 3, start: 1, size: {pageSize: 1}}
        pick: {first: 2}
        candidates: 4
        where:
          capacity: ">=5"
          any: {name: present, label: present}
        require:
          ships: GET /docks/{dockId}/ships dockId=.id
        save: {etag: header ETag, dockName: /name}
        write:
          create: POST /planets/{code}/docks code=planets.code
          update:
            call: PUT /docks/{dockId}
            path: {dockId: .id}
            body: from dock
            change: {name: Harbour}
            expect: 200
          patch:
            call: PATCH /docks/{dockId} dockId=.id
            change: auto
          delete:
            call: DELETE /docks/{dockId}
            path: {dockId: .id}
            off: true
        each:
          dock: GET /docks/{dockId} dockId=.id
  search:
    call: POST /ships/search
    read: true
    body: {pilotId: 1}
    items: ""
    pick: all
    off: true
params:
  shipNo: 7
  crew:
ignore: [GET /health]
`)
	if tr.Seed != 7 || tr.Budget != 50 || len(tr.Roots) != 2 || text(tr.Params["shipNo"]) != "7" || tr.Params["crew"] != nil ||
		len(tr.ParamNames) != 2 || tr.Ignore[0] != "GET /health" {
		t.Fatalf("tree: %+v", tr)
	}
	p := tr.Roots[0]
	if len(p.Pick.Keys) != 2 || p.Key[0] != "/code" || !p.Write[WriteCreate].None {
		t.Errorf("planets: %+v", p)
	}
	d := tr.Node("docks")
	if d == nil || d.Call != "listDocks" || d.Path["code"] != "planets.code" || d.Page.Max != 3 || *d.Page.Start != 1 ||
		!d.Pick.First || d.Pick.N != 2 || d.Candidates != 4 || len(d.Where) != 1 || len(d.WhereAny) != 2 ||
		len(d.Require) != 1 || !d.Require[0].IsRequire || d.Save["etag"] != "header ETag" {
		t.Errorf("docks: %+v", d)
	}
	if w := d.Write[WriteUpdate]; w.From != "dock" || w.Change["name"] != "Harbour" || w.Expect != 200 || w.Path["dockId"] != ".id" {
		t.Errorf("update: %+v", w)
	}
	if w := d.Write[WritePatch]; w.Change == nil || len(w.Change) != 0 || w.Path["dockId"] != ".id" {
		t.Errorf("patch: %+v", w)
	}
	if !d.Write[WriteDelete].Off {
		t.Error("delete off")
	}
	s := tr.Roots[1]
	if !s.Read || !s.HasBody || !s.HasItems || s.Items != "" || !s.Pick.All || !s.Off || s.Enabled() {
		t.Errorf("search: %+v", s)
	}
	if tr.Node("dock").PathName() != "planets/docks/dock" || tr.Node("dock").Ancestor("planets") != p {
		t.Error("path name or ancestor")
	}
}

func TestParseProblems(t *testing.T) {
	for text, want := range map[string]string{
		"nope: 1":                               `unknown key "nope"`,
		"seed: x":                               "seed must be a number",
		"budget: 0":                             "budget must be a positive",
		"tree: [1]":                             "expected a mapping",
		"tree: {a: {path: {}}}":                 "call is missing",
		"tree: {a: {call: GET /a, color: red}}": `unknown key "color"`,
		"tree: {a: {call: GET /a, pick: zero}}": "a number, all",
		"tree: {a: {call: GET /a, pick: {x: 1}}}":                 "a number, all",
		"tree: {a: {call: GET /a, items: content}}":               "JSON pointer",
		"tree: {a: {call: GET /a, key: [id]}}":                    "no JSON pointer",
		"tree: {a: {call: GET /a, save: {x: id}}}":                "JSON pointer (/id)",
		"tree: {a: {call: GET /a, read: maybe}}":                  "true or false",
		"tree: {a: {call: GET /a, candidates: 0}}":                "positive number",
		"tree: {a: {call: GET /a, page: {max: 2}}}":               "page.param is missing",
		"tree: {a: {call: GET /a, page: {param: p, in: header}}}": "query or body",
		"tree: {a: {call: GET /a, write: {remove: none}}}":        `unknown key "remove"`,
		"tree: {a: {call: GET /a, write: {create: {expect: 1}}}}": "HTTP status",
		"tree: {a: {call: GET /a, write: {create: {path: {}}}}}":  "call is missing",
		"tree: {a: GET /a x}":                                     "is no name=value",
		"tree: {a: get a/b}":                                      "METHOD /path",
		"tree: {a: GET /a, b: {call: GET /b, each: {a: GET /c}}}": "used twice",
		"tree: {'a b': GET /a}":                                   "letters, digits",
		"tree: {a: {call: GET /a, where: [1]}}":                   "mapping of fields",
		"ignore: x":                                               "list of operations",
	} {
		_, err := Parse([]byte(text))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	if _, err := Parse([]byte("- a")); err == nil {
		t.Error("a list is no tree file")
	}
}

func TestWhere(t *testing.T) {
	rec := map[string]any{"year": 1974, "name": "North", "status": "OPEN", "route": map[string]any{"origin": "mars"}, "gone": nil}
	for _, c := range []struct {
		field string
		test  any
		want  bool
	}{
		{"name", "present", true}, {"label", "present", false}, {"gone", "present", false},
		{"label", "absent", true}, {"gone", nil, true}, {"name", nil, false},
		{"year", ">=1974", true}, {"year", ">1974", false}, {"year", "<2000", true}, {"year", "<=1900", false},
		{"status", "!=CLOSED", true}, {"status", []any{"OPEN", "RESERVED"}, true}, {"status", []any{"X"}, false},
		{"route.origin", "mars", true}, {"route.Origin", "mars", true}, {"name", "South", false}, {"year", 1974, true},
		{"label", ">=1", false}, {"label", []any{"x"}, false}, {"name", ">M", true},
	} {
		if got := (Clause{Field: c.field, Test: c.test}).holds(rec); got != c.want {
			t.Errorf("%s %v: got %v", c.field, c.test, got)
		}
	}
	n := &Node{Where: []Clause{{"name", "present"}}, WhereAny: []Clause{{"label", "present"}, {"status", "OPEN"}}}
	if !n.Holds(rec) || n.Holds(map[string]any{"name": "x", "status": "CLOSED"}) {
		t.Error("any")
	}
	if v, ok := Field(map[string]any{"l": []any{map[string]any{"id": 3}}}, "l.0.id"); !ok || text(v) != "3" {
		t.Error("field in a list")
	}
	if _, ok := Field(1, "x"); ok {
		t.Error("field of a number")
	}
	if _, ok := Pointer(map[string]any{"a~b": map[string]any{"c/d": 1}}, "/a~0b/c~1d"); !ok {
		t.Error("escaped pointer")
	}
	if _, ok := Pointer([]any{1}, "/5"); ok {
		t.Error("index out of range")
	}
}

func TestCheck(t *testing.T) {
	s := loadStarport(t)
	tr := parse(t, `
tree:
  planets:
    call: GET /planets
    each:
      docks: GET /planets/{code}/docks
      write-it: POST /planets/{code}/docks code=.code
      wrong: GET /nowhere
      dock:
        call: GET /docks/{dockId}
        path: {dockId: ships.id}
        write:
          create: POST /nope
          update: PUT /docks/{dockId}
          patch:
            call: PATCH /docks/{dockId}
            path: {dockId: .id}
            body: from nothing
        require:
          r: POST /pilots
  ships: GET /docks/{dockId}/ships dockId=7
  off-one:
    call: DELETE /docks/{dockId}
    off: true
`)
	problems := strings.Join(Check(s, tr), "\n")
	for _, want := range []string{
		"docks: no value for the path parameter {code}",
		"write-it: POST /planets/{code}/docks would write",
		`wrong: "GET /nowhere" is no operation`,
		`names node ships, which is neither above it nor fetched before it`,
		`dock.write.create: "POST /nope" is no operation`,
		"dock.write.update: no value for the path parameter {dockId}",
		`body from "nothing": no node of that name`,
		"require r: \"POST /pilots\" is no reading operation",
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing %q in:\n%s", want, problems)
		}
	}
	if strings.Contains(problems, "off-one") {
		t.Error("a node that is off is not checked")
	}
	if _, err := Fetch(context.Background(), s, tr, Options{BaseURL: "http://x"}); err == nil {
		t.Error("fetch must refuse a tree with problems")
	}
	if _, err := Build(s, tr, &Data{}); err == nil {
		t.Error("build must refuse a tree with problems")
	}
}

func fetch(t *testing.T, s *spec.Spec, text, base string) (*Tree, *Data) {
	t.Helper()
	tr := parse(t, text)
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: base, Token: treeToken})
	if err != nil {
		t.Fatal(err)
	}
	return tr, d
}

func ids(rs []*Record) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestFetchPickWhereRequirePage(t *testing.T) {
	s := loadStarport(t)
	srv, base := stage(t)
	tr, d := fetch(t, s, `
tree:
  planets:
    call: GET /planets
    pick: [venus, mars, pluto]
    key: [/code]
    each:
      docks:
        call: GET /planets/{code}/docks
        path: {code: .code}
        page: {param: page, size: {pageSize: 1}, max: 5}
        pick: all
        where: {capacity: ">=5"}
        require:
          ships:
            call: GET /docks/{dockId}/ships
            path: {dockId: .id}
          planet: GET /planets/{code} code=planets.code
        save: {label: /name}
        each:
          ships:
            call: GET /docks/{dockId}/ships
            path: {dockId: .id}
            pick: {first: 1}
  pilots:
    call: GET /pilots
    pick: 2
    each:
      flown:
        call: POST /ships/search
        read: true
        body: {pilotId: .id}
        pick: all
`, base)
	if srv.Requests["POST"] != 2 || srv.Requests["PUT"]+srv.Requests["DELETE"] != 0 {
		t.Errorf("only the two searches may be POSTs: %v", srv.Requests)
	}
	planets := d.Of(tr.Node("planets"))
	if got := ids(planets); !slices.Equal(got, []string{"planets1", "planets2"}) || text(planets[0].Value.(map[string]any)["code"]) != "venus" {
		t.Errorf("planets: %v", got)
	}
	if !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, "no record with key pluto") }) {
		t.Errorf("the missing key is noted: %v", d.Notes)
	}
	// venus: Cloud (capacity 7); mars: North 4 (left out by where), South 5, East 6
	docks := d.Of(tr.Node("docks"))
	var names []string
	for _, r := range docks {
		names = append(names, text(r.Saved["label"]))
	}
	if !slices.Equal(names, []string{"Cloud", "South", "East"}) {
		t.Errorf("docks: %v (notes %v)", names, d.Notes)
	}
	if len(d.Children(docks[0], tr.Node("ships"))) != 1 {
		t.Error("pick first 1 ship per dock")
	}
	if !strings.Contains(docks[1].Request, "page=") {
		t.Errorf("pages: %s", docks[1].Request)
	}
	pilots := d.Of(tr.Node("pilots"))
	if len(pilots) != 2 {
		t.Fatalf("pilots: %v", ids(pilots))
	}
	for _, p := range pilots {
		for _, sh := range d.Children(p, tr.Node("flown")) {
			if text(sh.Value.(map[string]any)["pilotId"]) != text(p.Value.(map[string]any)["id"]) {
				t.Errorf("search found a ship of another pilot: %v", sh.Value)
			}
		}
	}
	// the data file keeps the records and their places
	path := filepath.Join(t.TempDir(), "data.yaml")
	b, err := d.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	back, stale, err := LoadData(path, tr)
	if err != nil || len(stale) != 0 || len(back.Records) != len(d.Records) || back.Records[2].Parent == nil || back.Source != base {
		t.Fatalf("round trip: %v %v %d/%d", err, stale, len(back.Records), len(d.Records))
	}
	smaller := parse(t, "tree:\n  planets: {call: GET /planets, key: [/code]}\n")
	if _, stale, _ := LoadData(path, smaller); len(stale) == 0 {
		t.Error("records of nodes the tree lost are stale")
	}
}

func TestFetchNotesAndBudget(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	_, d := fetch(t, s, `
budget: 3
tree:
  planets:
    call: GET /planets
    key: [/code]
    pick: all
    each:
      planet: GET /planets/{code} code=.code
`, base)
	if d.Requests != 3 || !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, "budget of 3 requests") }) {
		t.Errorf("budget: %d %v", d.Requests, d.Notes)
	}
	_, d = fetch(t, s, `
tree:
  missing:
    call: GET /planets/{code}
    path: {code: pluto}
  docks:
    call: GET /planets/{code}/docks
    path: {code: mars}
    query: {page: [1, 2]}
    where: {capacity: ">=5"}
    pick: 9
  none:
    call: GET /planets/{code}/docks
    path: {code: terra}
`, base)
	for _, want := range []string{"answered 404", "docks: 2 of 9 picked from 3 candidates (1 left out by where)", "none: 0 of 1 picked from 0 candidates"} {
		if !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, want) }) {
			t.Errorf("notes lack %q: %v", want, d.Notes)
		}
	}
	if len(d.Notes) != 3 {
		t.Errorf("only these notes: %v", d.Notes)
	}
	if _, err := Fetch(context.Background(), s, parse(t, "tree: {}"), Options{}); err == nil {
		t.Error("no base URL")
	}
	if _, err := Fetch(context.Background(), s, parse(t, "tree:\n  planets: {call: GET /planets, key: [/code]}\n"), Options{BaseURL: "http://127.0.0.1:1/api"}); err == nil {
		t.Error("an unreachable instance stops the fetch")
	}
}

func TestBuildFollowsTheChain(t *testing.T) {
	// the pilot of a ship is fetched below it; the scenario creates it
	// first, through the create of the pilots list, which the tree does not
	// fetch at all
	s := loadStarport(t)
	_, base := stage(t)
	tr, d := fetch(t, s, `
tree:
  planets:
    call: GET /planets
    pick: [mars]
    key: [/code]
    write: {create: none}
    each:
      docks:
        call: GET /planets/{code}/docks
        path: {code: .code}
        pick: {first: 1}
        key: [/id]
        write:
          create: POST /planets/{code}/docks code=planets.code
          update:
            call: PUT /docks/{dockId}
            path: {dockId: .id}
            change: {capacity: 9}
        each:
          ships:
            call: GET /docks/{dockId}/ships
            path: {dockId: .id}
            pick: all
            key: [/id]
            write:
              create: POST /docks/{dockId}/ships dockId=docks.id
            each:
              pilot: GET /pilots/{pilotId} pilotId=.pilotId
  pilots:
    call: GET /pilots
    pick: 1
    off: true
    write:
      create: POST /pilots
`, base)
	b, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	var creates []string
	for _, st := range b.Scenario.Steps {
		if strings.HasSuffix(st.ID, ".create") {
			creates = append(creates, st.ID)
		}
	}
	if !slices.Equal(creates, []string{"docks1.create", "pilot1.create", "ships1.create", "pilot2.create", "ships2.create"}) {
		t.Errorf("creates: %v", creates)
	}
	if st := b.Scenario.Step("ships1.create"); st == nil || text(st.Body.(map[string]any)["pilotId"]) != "{{pilot1.id}}" {
		t.Errorf("the ship names its pilot by placeholder: %+v", st)
	}
	if st := b.Scenario.Step("docks1.update"); st == nil || text(st.Body.(map[string]any)["capacity"]) != "9" || st.Header["If-Match"] != "{{docks1.etag}}" {
		t.Errorf("update: %+v", st)
	}
	if len(b.Seeds) != 1 || b.Seeds[0].ID != "planets1" {
		t.Errorf("seed: %v", ids(b.Seeds))
	}
	res, log := runEmpty(t, b)
	if res.Failed {
		t.Fatalf("the scenario failed:\n%s\n%+v", log, b.Notes)
	}
	review := Review(ReviewInput{Spec: "starport", SpecVersion: "1.0", Data: d, Built: b, Changes: []Change{{"a", "b"}}, Stale: []string{"x1"}})
	for _, want := range []string{"# Review: tree", "## Seed", "| planets1 | GET /planets | mars |", "## Changes", "no longer has"} {
		if !strings.Contains(review, want) {
			t.Errorf("review lacks %q:\n%s", want, review)
		}
	}
}

func TestBuildNotes(t *testing.T) {
	s := loadStarport(t)
	tr := parse(t, `
tree:
  pilots:
    call: GET /pilots
    key: [/id]
    write:
      create: POST /pilots
      delete: DELETE /pilots/{pilotId} pilotId=.id
  docks:
    call: GET /planets/{code}/docks
    path: {code: mars}
    key: [/id]
    write:
      create:
        call: POST /planets/{code}/docks
        path: {code: mars}
        body: {name: Fixed, capacity: 3}
      patch: PATCH /docks/{dockId} dockId=.id
  ships:
    call: GET /docks/{dockId}/ships
    path: {dockId: 1}
    key: [/id]
    write:
      create: POST /docks/{dockId}/ships dockId=1
`)
	rec := func(id string, n *Node, v map[string]any) *Record {
		return &Record{ID: id, Node: n, Value: v, Request: "GET /x"}
	}
	d := &Data{Records: []*Record{
		rec("pilots1", tr.Node("pilots"), map[string]any{"id": 5, "name": "Ada"}),
		rec("docks1", tr.Node("docks"), map[string]any{"id": 5, "name": "North", "capacity": 4}),
		// pilotId 5 fits a pilot (by name), the dock with id 5 does not count;
		// the ship has no name: generated
		rec("ships1", tr.Node("ships"), map[string]any{"id": 9, "pilotId": 5}),
	}}
	b, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	ship := b.Scenario.Step("ships1.create")
	if text(ship.Body.(map[string]any)["pilotId"]) != "{{pilots1.id}}" {
		t.Errorf("pilotId: %v", ship.Body)
	}
	if st := b.Scenario.Step("docks1.create"); text(st.Body.(map[string]any)["name"]) != "Fixed" {
		t.Errorf("a body of the tree is sent as it is: %v", st.Body)
	}
	if st := b.Scenario.Step("docks1.patch"); len(st.Body.(map[string]any)) != 1 {
		t.Errorf("a patch sends one changed field: %v", st.Body)
	}
	if st := b.Scenario.Step("ships1.create"); st.Save["ships1.id"] != "header Location" {
		t.Errorf("save: %v", st.Save)
	}
	var notes []string
	for _, n := range b.Notes {
		notes = append(notes, n.What+": "+n.Why)
	}
	all := strings.Join(notes, "\n")
	if !strings.Contains(all, "name = ") || !strings.Contains(all, "generated") {
		t.Errorf("notes: %s", all)
	}
	if !strings.Contains(all, "pilots1 is not deleted") {
		t.Errorf("a pilot a ship refers to stays when the ship is not deleted: %s", all)
	}
	// a value that fits two records by name stays and is noted
	tr2 := parse(t, `
tree:
  pilots: {call: GET /pilots, key: [/id], write: {create: POST /pilots}}
  more: {call: GET /pilots, key: [/id], write: {create: POST /pilots}}
  ships:
    call: GET /docks/{dockId}/ships
    path: {dockId: 1}
    key: [/id]
    write:
      create: POST /docks/{dockId}/ships dockId=1
`)
	d2 := &Data{Records: []*Record{
		rec("pilots1", tr2.Node("pilots"), map[string]any{"id": 5, "name": "Ada"}),
		rec("more1", tr2.Node("more"), map[string]any{"id": 6, "name": "Lin"}),
		rec("ships1", tr2.Node("ships"), map[string]any{"id": 9, "name": "Comet", "pilotId": 5}),
	}}
	d2.Records[1].Value.(map[string]any)["id"] = 5
	if _, err := Build(s, tr2, d2); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFromViewsAndSearch(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	tr, d := fetch(t, s, `
tree:
  pilots:
    call: GET /pilots
    pick: {first: 1}
    key: [/id]
    write:
      create: {call: POST /pilots, body: from pilot}
    each:
      pilot: GET /pilots/{pilotId} pilotId=.id
      flown:
        call: POST /ships/search
        read: true
        body: {pilotId: .id}
        pick: {first: 1}
`, base)
	b, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	search := b.Scenario.Step("pilots1.flown")
	if search == nil || search.Call != "POST /ships/search" || text(search.Body.(map[string]any)["pilotId"]) != "{{pilots1.id}}" {
		t.Fatalf("the search reads with the placeholder: %+v", search)
	}
	res, log := runEmpty(t, b)
	if res.Failed {
		t.Fatalf("%s", log)
	}
}

func TestMergeKeepsTheFileAndAddsNewEndpoints(t *testing.T) {
	s := loadStarport(t)
	// the file of a user: changed pick, own comment, a removed node in
	// ignore, a node for an operation the spec lost, a renamed path
	file := `# mine
seed: 3
tree:
  planets:
    call: GET /planets # my comment
    pick: [mars]
    key: [/code]
    write:
      create: none
    each:
      docks:
        call: GET /planets/{code}/docks # listDocks
        path: {code: .code}
        pick: 1
        key: [/id]
        each:
          dock: GET /docks/{dockId} dockId=.id # getDock
          crew: GET /docks/{dockId}/crew dockId=.id # listCrew
          renamed: GET /old/ships/{dockId} dockId=.id # listShips
ignore: [GET /pilots, getHealth]
`
	tr := parse(t, file)
	changes := Merge(s, tr, Propose(s), "1.0")
	b, err := tr.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"# mine", "seed: 3", "# my comment", "pick: [mars]", "pick: 1",
		"crew: GET /docks/{dockId}/crew dockId=.id # gone: not in the spec 1.0",
		"  renamed:\n            call: GET /docks/{dockId}/ships # listShips",
		"call: PUT /docks/{dockId} # updateDock · new in 1.0",
		"body: from docks",
		// the list of pilots is ignored: a pilot hangs below the ships, whose
		// field names it, and takes the writes of pilots
		"pilot:\n                call: GET /pilots/{pilotId} # getPilot · new in 1.0\n                path: {pilotId: .pilotId}",
		"call: POST /pilots # createPilot · new in 1.0",
		"getShipLog · new in 1.0",
		"ignore: [GET /pilots, getHealth]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("merged file lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "listPilots") {
		t.Errorf("an ignored operation must not come back:\n%s", out)
	}
	if strings.Contains(out, "planet: GET /planets/{code}") == false {
		t.Errorf("the read of a planet is new below planets:\n%s", out)
	}
	var what []string
	for _, c := range changes {
		what = append(what, c.Where+": "+c.What)
	}
	if !slices.ContainsFunc(what, func(s string) bool { return strings.Contains(s, "path changed") }) ||
		!slices.ContainsFunc(what, func(s string) bool { return strings.Contains(s, "not in the spec any more") }) {
		t.Errorf("changes: %v", what)
	}
	// the node of the operation the spec lost stays, but is neither checked
	// nor fetched
	if problems := Check(s, parse(t, out)); len(problems) > 0 {
		t.Errorf("a gone node must not stop the run: %v", problems)
	}
	if n := parse(t, out).Node("crew"); n == nil || !n.Gone || n.Enabled() {
		t.Errorf("crew: %+v", n)
	}
	// a second merge changes nothing
	again := parse(t, out)
	if ch := Merge(s, again, Propose(s), "1.0"); len(ch) != 0 {
		t.Errorf("a second merge must change nothing: %+v", ch)
	}
	if b2, _ := again.Bytes(); string(b2) != out {
		t.Errorf("a second merge must keep the file:\n%s", b2)
	}
}

func TestProposalRoundTrip(t *testing.T) {
	s := loadStarport(t)
	p := Propose(s)
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	tr := parse(t, string(b))
	if problems := Check(s, tr); len(problems) > 0 {
		t.Errorf("the proposal must pass its own check: %v", problems)
	}
	for _, name := range []string{"planets", "docks", "dock", "ships", "ship", "log", "shipPilot", "pilots", "pilot", "health", "licenses", "profile", "permit"} {
		if tr.Node(name) == nil {
			t.Errorf("node %s missing:\n%s", name, b)
		}
	}
	if w := tr.Node("docks").Write; w[WriteCreate].Path["code"] != "planets.code" || w[WritePatch] == nil || w[WriteUpdate] == nil {
		t.Errorf("writes of docks: %+v", w)
	}
	if !tr.Node("planets").Write[WriteCreate].None {
		t.Error("planets have no create")
	}
	if tr.Node("shipPilot").Path["pilotId"] != ".pilotId" {
		t.Error("the pilot of a ship comes from its field")
	}
	// {pilotId} of another tag hangs below the pilots; a read with values of
	// two nodes below the one fetched last, the other read by name
	if lic := tr.Node("licenses"); lic.Parent.Name != "pilots" || lic.Path["pilotId"] != ".id" {
		t.Errorf("licenses: %+v", lic)
	}
	if pm := tr.Node("permit"); pm.Path["dockId"] != "docks.id" || pm.Path["pilotId"] != ".id" || pm.Parent.Name != "shipPilot" {
		t.Errorf("permit: %+v under %s", pm.Path, pm.Parent.Name)
	}
	if len(tr.Params) != 0 {
		t.Errorf("every value comes from a node: %v", tr.Params)
	}
	// the create of a pilot takes its body from the profile, which has
	// homePlanet; the ship takes its dock from the node above
	if w := tr.Node("pilots").Write[WriteCreate]; w.From != "profile" {
		t.Errorf("body source: %+v", w)
	}
	if w := tr.Node("ships").Write[WriteCreate]; w.Path["dockId"] != "docks.id" {
		t.Errorf("ship create: %+v", w.Path)
	}
	if pg := tr.Node("docks").Page; pg == nil || pg.Param != "page" || text(pg.Size["pageSize"]) != "50" {
		t.Errorf("page: %+v", pg)
	}
	// a merge of the proposal into itself changes nothing
	if ch := Merge(s, tr, p, "1.0"); len(ch) != 0 {
		t.Errorf("changes: %+v", ch)
	}
}

func TestShortFormAndRefs(t *testing.T) {
	call, params, err := shortForm("get /docks/{dockId} dockId=.id x=3")
	if err != nil || call != "GET /docks/{dockId}" || params["dockId"] != ".id" || params["x"] != "3" {
		t.Errorf("%q %v %v", call, params, err)
	}
	if call, _, _ := shortForm("getDock"); call != "getDock" {
		t.Error("operationId")
	}
	scope := func(n string) bool { return n == "docks" }
	for in, want := range map[string]string{".id": ".id", "docks.id": "docks.id", ".": "."} {
		r, ok := ParseRef(in, scope)
		if !ok || r.String() != want {
			t.Errorf("%s: %v %v", in, r, ok)
		}
	}
	for _, in := range []any{"v1.0", "x.y", 3, ""} {
		if _, ok := ParseRef(in, scope); ok {
			t.Errorf("%v is no reference", in)
		}
	}
	if s, ok := shortText("GET /a/{x}/{y}", map[string]any{"y": ".b", "x": ".a"}); !ok || s != "GET /a/{x}/{y} x=.a y=.b" {
		t.Errorf("short text: %q", s)
	}
	if _, ok := shortText("GET /a", map[string]any{"x": "a b"}); ok {
		t.Error("a value with a space has no short form")
	}
	if KeyText(map[string]any{"a": 1}, []string{"/a", "/b"}) != "" {
		t.Error("a missing key part gives no key")
	}
	if Method("getDock") != "" || Method("GET /x") != "GET" {
		t.Error("Method")
	}
	if !strings.Contains(scenario.Text(map[string]any{"a": 1}), "a") {
		t.Error("text")
	}
}

func TestReferencesAcrossTheTree(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	tr, d := fetch(t, s, `
tree:
  pilots:
    call: GET /pilots
    pick: {first: 1}
    key: [/id]
    write:
      create: POST /pilots
    each:
      profile: GET /pilots/{pilotId}/profile pilotId=.id
  planets:
    call: GET /planets
    pick: [mars]
    key: [/code]
    write: {create: none}
    each:
      docks:
        call: GET /planets/{code}/docks
        path: {code: .code}
        pick: {first: 1}
        key: [/id]
        write:
          create: POST /planets/{code}/docks code=planets.code
        each:
          permit: GET /docks/{dockId}/pilots/{pilotId}/permit dockId=.id pilotId=pilots.id
`, base)
	permits := d.Of(tr.Node("permit"))
	pilots := d.Of(tr.Node("pilots"))
	if len(permits) != 1 || text(permits[0].Value.(map[string]any)["pilotId"]) != text(pilots[0].Value.(map[string]any)["id"]) {
		t.Fatalf("the permit reads the first pilot: %v / %v (notes %v)", permits, pilots, d.Notes)
	}
	b, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	st := b.Scenario.Step("permit1.read")
	if st == nil || st.Path["pilotId"] != "{{pilots1.id}}" || st.Path["dockId"] != "{{docks1.id}}" {
		t.Fatalf("permit: %+v", st)
	}
	// no "from": the body still takes homePlanet from the profile below
	if c := b.Scenario.Step("pilots1.create"); text(c.Body.(map[string]any)["homePlanet"]) != "mars" {
		t.Errorf("the body takes the fields of the reads about the record: %v", c.Body)
	}
	res, log := runEmpty(t, b)
	if res.Failed {
		t.Fatalf("%s", log)
	}
	// a node read before it is fetched is an error
	bad := parse(t, `
tree:
  planets:
    call: GET /planets
    each:
      permit: GET /docks/{dockId}/pilots/{pilotId}/permit dockId=1 pilotId=pilots.id
  pilots: GET /pilots
`)
	if problems := strings.Join(Check(s, bad), "\n"); !strings.Contains(problems, "neither above it nor fetched before it") {
		t.Errorf("problems: %s", problems)
	}
}

func TestProposalNames(t *testing.T) {
	s := loadStarport(t)
	p := &proposer{s: s, names: map[string]int{}}
	op := lookupOp(s, "GET /pilots/{pilotId}")
	if a, b, c := p.name("pilot", op), p.name("pilot", op), p.name("pilot", op); a != "pilot" || b != "getPilot" || c != "pilot2" {
		t.Errorf("names: %s %s %s", a, b, c)
	}
	if n := p.name("a b/c", nil); n != "a-b-c" {
		t.Errorf("clean: %s", n)
	}
}

// gardenSpec has values no record gives: a garden is read by its number
// only, the harvest needs a season, and a maintenance run writes nothing a
// node reads.
const gardenSpec = `openapi: 3.0.3
info: {title: Garden, version: "1.0"}
paths:
  /gardens/{gardenNo}:
    parameters:
      - {name: gardenNo, in: path, required: true, schema: {type: string}}
    get:
      operationId: getGarden
      tags: [Garden]
      responses:
        '200':
          description: one garden
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Garden'}
    put:
      operationId: updateGarden
      tags: [Garden]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/GardenWrite'}
      responses:
        '200':
          description: updated
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Garden'}
  /gardens/{gardenNo}/beds:
    get:
      operationId: listBeds
      tags: [Bed]
      parameters:
        - {name: gardenNo, in: path, required: true, schema: {type: string}}
      responses:
        '200':
          description: the beds of a garden
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Bed'}}
  /harvest:
    get:
      operationId: getHarvest
      tags: [Harvest]
      parameters:
        - {name: season, in: query, required: true, schema: {type: string}}
      responses:
        '200':
          description: the harvest of a season
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Harvest'}
  /maintenance/run:
    post:
      operationId: runMaintenance
      tags: [System]
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {mode: {type: string}}}
      responses:
        '202': {description: started}
components:
  schemas:
    Garden:
      type: object
      required: [gardenNo, name]
      properties:
        gardenNo: {type: string}
        name: {type: string}
        size: {type: integer}
    GardenWrite:
      type: object
      properties:
        name: {type: string}
        size: {type: integer}
    Bed:
      type: object
      properties:
        id: {type: integer}
        gardenNo: {type: string}
        plant: {type: string}
    Harvest:
      type: object
      properties:
        season: {type: string}
        total: {type: integer}
`

func loadGarden(t *testing.T) *spec.Spec {
	t.Helper()
	path := filepath.Join(t.TempDir(), "garden.yaml")
	if err := os.WriteFile(path, []byte(gardenSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProposalParamsInsteadOfUnplaced(t *testing.T) {
	s := loadGarden(t)
	b, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Errorf("the proposal must pass its own check: %v\n%s", problems, out)
	}
	// the values no node gives are listed at the top, with their users
	if !strings.Contains(out, "params:\n  gardenNo: # getGarden") || !strings.Contains(out, "season: # getHarvest") ||
		strings.Index(out, "params:") > strings.Index(out, "tree:") {
		t.Errorf("params at the top:\n%s", out)
	}
	// every read is in the tree: the garden by $gardenNo, its beds below it
	g := tr.Node("gardens")
	if g == nil || g.Parent != nil || g.Path["gardenNo"] != "$gardenNo" {
		t.Fatalf("garden:\n%s", out)
	}
	if beds := tr.Node("beds"); beds == nil || beds.Parent != g || beds.Path["gardenNo"] != ".gardenNo" {
		t.Errorf("beds below the garden:\n%s", out)
	}
	if h := tr.Node("harvest"); h == nil || h.Query["season"] != "$season" {
		t.Errorf("harvest:\n%s", out)
	}
	// the update of a garden goes to the node that reads it, with a body
	if w := g.Write[WriteUpdate]; w == nil || w.Path["gardenNo"] != ".gardenNo" || w.From != "gardens" {
		t.Errorf("update:\n%s", out)
	}
	// a write no node reads goes to ignore, with the reason
	if !slices.Contains(tr.Ignore, "POST /maintenance/run") || !strings.Contains(out, "runMaintenance · no node reads") {
		t.Errorf("ignore:\n%s", out)
	}
	if strings.Contains(out, "unplaced") || strings.Contains(out, "off: true") {
		t.Errorf("nothing is unplaced or off:\n%s", out)
	}
	// a merge of the proposal into itself changes nothing
	if ch := Merge(s, tr, Propose(s), "1.0"); len(ch) != 0 {
		t.Errorf("changes: %+v", ch)
	}
}

func TestMergeDissolvesUnplaced(t *testing.T) {
	s := loadGarden(t)
	tr := parse(t, `# mine
seed: 2
tree:
  harvest:
    call: GET /harvest
    query: {season: summer}
unplaced:
  garden:
    call: GET /gardens/{gardenNo}
    path: {gardenNo: G-7}
    off: true
`)
	changes := Merge(s, tr, Propose(s), "1.0")
	b, _ := tr.Bytes()
	out := string(b)
	if strings.Contains(out, "unplaced") {
		t.Errorf("unplaced must go:\n%s", out)
	}
	// the value written in unplaced is kept in params, at the top below the
	// head of the file
	if !strings.HasPrefix(out, "# mine\n") || !strings.Contains(out, "params:\n  gardenNo: G-7 # getGarden") {
		t.Errorf("params:\n%s", out)
	}
	again := parse(t, out)
	if g := again.Node("gardens"); g == nil || g.Path["gardenNo"] != "$gardenNo" {
		t.Errorf("the garden is in the tree:\n%s", out)
	}
	if again.Node("harvest").Query["season"] != "summer" {
		t.Error("the value of the user stays")
	}
	if _, ok := again.Params["season"]; ok {
		t.Errorf("season is given in the file, no params entry: %v", again.Params)
	}
	if problems := Check(s, again); len(problems) > 0 {
		t.Errorf("check: %v", problems)
	}
	var what []string
	for _, c := range changes {
		what = append(what, c.Where+": "+c.What)
	}
	if !slices.ContainsFunc(what, func(s string) bool { return strings.HasPrefix(s, "unplaced: removed") }) {
		t.Errorf("changes: %v", what)
	}
}

func TestParamsInFetchAndBuild(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	_, d := fetch(t, s, `
tree:
  pilots:
    call: GET /pilots
    pick: {first: 1}
`, base)
	id := text(d.Records[0].Value.(map[string]any)["id"])
	file := `
params:
  pid: %s
tree:
  pilot: GET /pilots/{pilotId} pilotId=$pid
`
	// without a value the node is left out, noted once
	tr, d := fetch(t, s, fmt.Sprintf(file, ""), base)
	if len(d.Records) != 0 || len(d.Notes) != 1 || !strings.Contains(d.Notes[0], "$pid has no value") {
		t.Errorf("unset: %v %v", d.Records, d.Notes)
	}
	if u := tr.UnsetParams(); len(u) != 1 || u[0] != "pid" {
		t.Errorf("unset: %v", u)
	}
	if r := Review(ReviewInput{Data: d, Unset: tr.UnsetParams()}); !strings.Contains(r, "## Params without value (1)") {
		t.Errorf("review:\n%s", r)
	}
	if _, err := Build(s, tr, d); err != nil {
		t.Errorf("build: %v", err)
	}
	// with a value the node reads it, and so does the scenario
	tr, d = fetch(t, s, fmt.Sprintf(file, id), base)
	if len(d.Records) != 1 || text(d.Records[0].Value.(map[string]any)["id"]) != id {
		t.Fatalf("set: %v %v", d.Records, d.Notes)
	}
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	if st := built.Scenario.Steps[0]; text(st.Path["pilotId"]) != id {
		t.Errorf("step: %+v", st)
	}
	// a value of params may name a node: it reads like the reference in
	// place of $name, so that node must be fetched before
	tr, d = fetch(t, s, `
params:
  pid: pilots.Id
tree:
  pilots:
    call: GET /pilots
    pick: {first: 1}
  pilot: GET /pilots/{pilotId} pilotId=$pid
`, base)
	if got := d.Of(tr.Node("pilot")); len(got) != 1 || text(got[0].Value.(map[string]any)["id"]) != id {
		t.Errorf("reference in params: %v %v", got, d.Notes)
	}
	late := parse(t, "params:\n  pid: pilots.id\ntree:\n  pilot: GET /pilots/{pilotId} pilotId=$pid\n  pilots: GET /pilots\n")
	if problems := Check(s, late); len(problems) != 1 || !strings.Contains(problems[0], "pilot ($pid)") {
		t.Errorf("check order: %v", problems)
	}
	nested := parse(t, "params:\n  a: $b\n  b: 1\ntree:\n  pilot: GET /pilots/{pilotId} pilotId=$a\n")
	if problems := Check(s, nested); len(problems) != 1 || !strings.Contains(problems[0], "no other $name") {
		t.Errorf("check nested: %v", problems)
	}
	// a $name params does not have is a problem of the file
	bad := parse(t, "tree:\n  pilot: GET /pilots/{pilotId} pilotId=$nope\n")
	if problems := Check(s, bad); len(problems) != 1 || !strings.Contains(problems[0], "params has no entry nope") {
		t.Errorf("check: %v", problems)
	}
}
