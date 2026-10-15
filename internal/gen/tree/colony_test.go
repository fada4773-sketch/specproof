package tree

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/spec"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

const colonySpec = "../../../testdata/gen/tree-colony.yaml"

func loadColony(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Load(context.Background(), colonySpec)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestColonyProposal: the chain planets → moons → gardens → beds, filters
// named only in operationIds (getGardensByPersonName and the other way
// round, getPersonsByGardenName), require and where from them, from reads
// that answer 404 and from lists below, states from actions and state
// words, and a tag left out.
func TestColonyProposal(t *testing.T) {
	s := loadColony(t)
	b, err := ProposeScope(s, Scope{ExcludeTags: []string{"Admin"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("check: %v\n%s", problems, out)
	}
	t.Logf("\n%s", out)
	if strings.Contains(out, "admin") || !strings.Contains(out, "exclude:\n    tags: [Admin]") {
		t.Errorf("Admin is out:\n%s", out)
	}
	// the chain
	for child, parent := range map[string]string{"moons": "planets", "gardens": "moons", "beds": "gardens", "bed": "beds",
		"details": "gardens", "gardenReport": "gardens", "gardenPerson": "gardens"} {
		if n := tr.Node(child); n == nil || n.Parent == nil || n.Parent.Name != parent {
			t.Errorf("%s below %s:\n%s", child, parent, out)
		}
	}
	// a filter named in the operationId: below the node that gives it, its
	// parent requires an answer, the value must be there
	persons, gardens := tr.Node("persons"), tr.Node("gardens")
	gs, ps := tr.Node("garden-search"), tr.Node("person-search")
	if gs.Parent != persons || gs.Query["name"] != ".name" || ps.Parent != gardens || ps.Query["gardenName"] != ".name" {
		t.Errorf("filters: %s under %s, %s under %s", gs.Name, gs.Parent.Name, ps.Name, ps.Parent.Name)
	}
	require := func(n *Node, call string, least int) {
		t.Helper()
		if !slices.ContainsFunc(n.Require, func(r *Node) bool { return r.Call == call && r.Min == least }) {
			t.Errorf("%s requires %s (min %d):\n%s", n.Name, call, least, out)
		}
	}
	require(persons, "GET /garden-search", 1)
	require(gardens, "GET /person-search", 1)
	require(gardens, "GET /gardens/{gardenId}/details", 0)
	require(gardens, "GET /gardens/{gardenId}/beds", 1)
	require(tr.Node("planets"), "GET /planets/{planetCode}/moons", 1)
	require(tr.Node("moons"), "GET /moons/{moonId}/gardens", 1)
	where := func(n *Node, field string, test any) {
		t.Helper()
		if !slices.ContainsFunc(n.Where, func(c Clause) bool { return c.Field == field && text(c.Test) == text(test) && c.Note != "" }) {
			t.Errorf("%s where %s %v:\n%s", n.Name, field, test, out)
		}
	}
	where(persons, "name", "present")
	where(gardens, "personId", "present")
	// open starts from DRAFT or CLOSED, archive from anything but ARCHIVED,
	// and the create sends the state
	where(gardens, "status", []any{"DRAFT", "CLOSED"})
	where(tr.Node("open-gardens"), "status", "OPEN")
	// read, updated and deleted by id, code and name: one write block, the
	// id first, the other keys as variants; the reads below the gardens
	if k := gardens.Kinds(); !slices.Equal(k, []string{"create", "update", "update-by-code", "update-by-name",
		"delete", "delete-by-code", "delete-by-name", "open", "archive"}) {
		t.Errorf("kinds: %v", k)
	}
	if !slices.Equal(gardens.Key, []string{"/id", "/code", "/name"}) {
		t.Errorf("key, id first: %v", gardens.Key)
	}
	for name, call := range map[string]string{"garden": "GET /gardens/{gardenId}", "gardenByCode": "GET /gardens/code/{code}",
		"gardenByName": "GET /gardens/name/{name}"} {
		if n := tr.Node(name); n == nil || n.Call != call || n.Parent != gardens || len(n.Write) != 0 {
			t.Errorf("%s: %s below gardens, no writes:\n%s", name, call, out)
		}
	}
	// the season of a harvest is in no record
	if h := tr.Node("harvests"); h.Parent != persons || h.Query["personId"] != ".id" || h.Query["season"] != "$season" ||
		len(tr.ParamNames) != 1 || tr.ParamNames[0] != "season" {
		t.Errorf("harvests:\n%s", out)
	}
	if len(tr.Ignore) != 0 {
		t.Errorf("every write has a node: %v", tr.Ignore)
	}
}

// TestColonyRebuild fetches the proposal from a stage and rebuilds it in an
// empty instance: the requires and wheres pick exactly the gardens that
// have a person, a size, beds and a state the actions start from.
func TestColonyRebuild(t *testing.T) {
	s := loadColony(t)
	st := testserver.NewColony(treeToken, 500)
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)
	b, err := ProposeScope(s, Scope{ExcludeTags: []string{"Admin"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	tr := parse(t, string(b))
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: hs.URL + "/api", Token: treeToken})
	if err != nil {
		t.Fatal(err)
	}
	if n := st.Requests["POST"] + st.Requests["PUT"] + st.Requests["PATCH"] + st.Requests["DELETE"]; n != 0 {
		t.Fatalf("a fetch only reads: %v", st.Requests)
	}
	var names []string
	for _, r := range d.Of(tr.Node("gardens")) {
		names = append(names, text(r.Value.(map[string]any)["name"]))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Mint", "Rose", "Sage"}) {
		t.Errorf("gardens picked: %v (notes %v)", names, d.Notes)
	}
	var planets []string
	for _, r := range d.Of(tr.Node("planets")) {
		planets = append(planets, text(r.Value.(map[string]any)["code"]))
	}
	if slices.Contains(planets, "venus") {
		t.Errorf("venus has no moons: %v", planets)
	}
	if !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, "harvests: left out, $season has no value") }) {
		t.Errorf("notes: %v", d.Notes)
	}
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	text, err := built.Scenario.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tree-scenario_gen.yaml")
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	// with Unique the updates send the new code and name of the create: the
	// update by code after the update by id finds the garden
	for _, unique := range []bool{false, true} {
		var log strings.Builder
		res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
			Config:   apitest.Config{SpecPath: colonySpec, Handler: testserver.NewColony(treeToken, 1), Token: apitest.StaticToken(treeToken)},
			Scenario: path, Unique: unique,
		}, apitest.StandaloneOptions{Out: &log})
		if err != nil {
			t.Fatal(err)
		}
		if res.Failed {
			t.Fatalf("unique %v: the scenario failed on an empty instance:\n%s\n%s\nnotes: %+v", unique, log.String(), text, built.Notes)
		}
	}
	// every update on the first garden, each delete on a garden of its own,
	// each followed by a read
	for _, step := range []string{"gardens1.open", "gardens1.archive", "gardens1.update", "gardens1.read.afterUpdate",
		"gardens1.update-by-code", "gardens1.read.afterUpdateByCode", "gardens1.update-by-name", "gardens1.read.afterUpdateByName",
		"gardens3.delete", "gardens3.read.afterDelete", "gardens2.delete-by-code", "gardens2.read.afterDeleteByCode",
		"gardens1.delete-by-name", "gardens1.read.afterDeleteByName"} {
		if !strings.Contains(string(text), "id: "+step) {
			t.Errorf("step %s missing:\n%s", step, text)
		}
	}
	if strings.Index(string(text), "id: gardens1.open") > strings.Index(string(text), "id: gardens1.archive") {
		t.Error("archive ends what a garden can do: it runs last")
	}
}

// TestColonyStepByStep proposes tag by tag: first gardens and beds, then
// the persons; the person a garden search takes from params comes from the
// new node of persons then, and the params entry goes.
func TestColonyStepByStep(t *testing.T) {
	s := loadColony(t)
	first := ProposeScope(s, Scope{Tags: []string{"Planet", "Moon", "Garden", "Bed"}})
	b, err := first.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if tr.Node("persons") != nil || tr.Node("harvests") != nil || !strings.Contains(out, "tags: [Planet, Moon, Garden, Bed]") {
		t.Fatalf("only the four tags:\n%s", out)
	}
	// without persons, the name of a person is a value of params
	gs := tr.Node("garden-search")
	if gs == nil || gs.Query["name"] != "$name" {
		t.Fatalf("garden search:\n%s", out)
	}
	// next step: Person too
	changes := tr.AddScope(Scope{Tags: []string{"Person"}})
	changes = append(changes, Merge(s, tr, ProposeScope(s, tr.Scope), "2.0")...)
	b2, _ := tr.Bytes()
	out2 := string(b2)
	again := parse(t, out2)
	if problems := Check(s, again); len(problems) > 0 {
		t.Fatalf("check: %v\n%s", problems, out2)
	}
	if !strings.Contains(out2, "tags: [Planet, Moon, Garden, Bed, Person]") || again.Node("persons") == nil {
		t.Errorf("persons added:\n%s", out2)
	}
	if v := again.Node("garden-search").Query["name"]; v != "persons.name" {
		t.Errorf("the name comes from the persons now: %v\n%s", v, out2)
	}
	if _, ok := again.Params["name"]; ok {
		t.Errorf("params.name is gone:\n%s", out2)
	}
	var what []string
	for _, c := range changes {
		what = append(what, c.Where+": "+c.What)
	}
	for _, want := range []string{"scope: tag Person", "params.name: removed"} {
		if !slices.ContainsFunc(what, func(s string) bool { return strings.HasPrefix(s, want) }) {
			t.Errorf("changes lack %q: %v", want, what)
		}
	}
	// with deps the persons come with the first step already
	withDeps := ProposeScope(s, Scope{Tags: []string{"Garden", "Bed", "Moon", "Planet"}, Deps: true})
	if withDeps.Node("persons") == nil || len(withDeps.ParamNames) != 0 {
		t.Errorf("deps: params %v", withDeps.ParamNames)
	}
}

// TestColonyGardenOnly proposes the Garden tag alone: the moons are out of
// scope, yet moonId is no key of a garden, and the write block goes to the
// gardens of a moon, not to the open gardens the actions cannot start from.
func TestColonyGardenOnly(t *testing.T) {
	s := loadColony(t)
	b, err := ProposeScope(s, Scope{Tags: []string{"Garden"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("check: %v\n%s", problems, out)
	}
	open, gardens := tr.Node("open-gardens"), tr.Node("gardens")
	if open == nil || gardens == nil || len(open.Write) != 0 || gardens.Write["create"] == nil || gardens.Write["open"] == nil {
		t.Fatalf("the write block belongs to gardens:\n%s", out)
	}
	if !slices.Equal(gardens.Key, []string{"/id", "/code", "/name"}) || !slices.Equal(open.Key, gardens.Key) {
		t.Errorf("keys: %v, %v", gardens.Key, open.Key)
	}
	if _, ok := tr.Params["moonId"]; ok {
		t.Errorf("moonId comes from the record:\n%s", out)
	}
}

func TestOperationIDWords(t *testing.T) {
	for id, want := range map[string]string{
		"getGardensByPersonName":       "get|gardens|person name|",
		"GetHarvestsByPersonAndSeason": "get|harvests|person,season|",
		"list_open_gardens":            "list|open gardens||",
		"findBooksWithReviews":         "find|books||reviews",
		"getHTTPStatus2":               "get|http status 2||",
	} {
		n := parseOpName(id)
		var by, with []string
		for _, p := range n.by {
			by = append(by, strings.Join(p, " "))
		}
		for _, p := range n.with {
			with = append(with, strings.Join(p, " "))
		}
		got := n.verb + "|" + strings.Join(n.subject, " ") + "|" + strings.Join(by, ",") + "|" + strings.Join(with, ",")
		if got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	for verb, want := range map[string][]string{"open": {"DRAFT", "CLOSED"}, "archive": {"DRAFT", "OPEN", "CLOSED"}, "fly": nil} {
		if got := actionStates([]string{"DRAFT", "OPEN", "CLOSED", "ARCHIVED"}, verb); !slices.Equal(got, want) {
			t.Errorf("%s: %v", verb, got)
		}
	}
	for pattern, ok := range map[string]bool{"Admin": true, "adm*": true, "*min": true, "Ad": false} {
		if glob(pattern, "Admin") != ok {
			t.Errorf("glob %s", pattern)
		}
	}
}

func TestFetchOnlyOneNode(t *testing.T) {
	s := loadColony(t)
	st := testserver.NewColony(treeToken, 500)
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)
	tr := parse(t, `
tree:
  persons:
    call: GET /persons
    pick: all
    each:
      person: GET /persons/{personId} personId=.id
  planets:
    call: GET /planets
    pick: all
    key: [/code]
`)
	opt := Options{BaseURL: hs.URL + "/api", Token: treeToken}
	before, err := Fetch(context.Background(), s, tr, opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), s, tr, Options{BaseURL: opt.BaseURL, Token: treeToken, Only: "person"}); err == nil {
		t.Error("a node below others needs the fetch before")
	}
	sent := st.Requests["GET"]
	opt.Only, opt.Before = "person", before
	d, err := Fetch(context.Background(), s, tr, opt)
	if err != nil {
		t.Fatal(err)
	}
	// only the four persons are read again, below the persons fetched before
	if n := st.Requests["GET"] - sent; n != 4 {
		t.Errorf("requests: %d", n)
	}
	if len(d.Records) != len(before.Records) || len(d.Of(tr.Node("person"))) != 4 || d.Of(tr.Node("person"))[0].Parent.Node.Name != "persons" {
		t.Errorf("records: %d of %d", len(d.Records), len(before.Records))
	}
	if _, err := Fetch(context.Background(), s, tr, Options{BaseURL: opt.BaseURL, Only: "nope", Before: before}); err == nil {
		t.Error("an unknown node")
	}
}

func TestParseScopeMinActions(t *testing.T) {
	tr := parse(t, `
scope:
  tags: [Garden, Bed]
  exclude: {tags: [Admin*], ops: [purgeAll, "DELETE /x"]}
  deps: true
tree:
  gardens:
    call: GET /moons/{moonId}/gardens
    path: {moonId: 1}
    where:
      status: [DRAFT] # open starts from it
    require:
      beds:
        call: GET /gardens/{gardenId}/beds
        path: {gardenId: .id}
        min: 2
    write:
      open: POST /gardens/{gardenId}/open gardenId=.id
      archive: POST /gardens/{gardenId}/archive gardenId=.id
      publish-later: POST /gardens/{gardenId}/open gardenId=.id
`)
	sc := tr.Scope
	if !slices.Equal(sc.Tags, []string{"Garden", "Bed"}) || sc.ExcludeTags[0] != "Admin*" || len(sc.ExcludeOps) != 2 || !sc.Deps {
		t.Errorf("scope: %+v", sc)
	}
	g := tr.Node("gardens")
	if g.Require[0].Min != 2 || g.Where[0].Note != "open starts from it" {
		t.Errorf("gardens: %+v", g)
	}
	if k := g.Kinds(); !slices.Equal(k, []string{"open", "publish-later", "archive"}) {
		t.Errorf("kinds: %v", k)
	}
	// variants by another key are updates and deletes, not actions
	for kind, base := range map[string]string{"update-by-name": "update", "delete-by-code": "delete", "patch-by-person-id": "patch",
		"update-by-": "update-by-", "updated": "updated", "delete-items": "delete-items"} {
		if KindBase(kind) != base || IsAction(kind) != (base != "update" && base != "delete" && base != "patch") {
			t.Errorf("%s: base %s, action %v", kind, KindBase(kind), IsAction(kind))
		}
	}
	v := parse(t, "tree:\n  gardens:\n    call: GET /moons/{moonId}/gardens\n    path: {moonId: 1}\n    write:\n"+
		"      delete-by-name: DELETE /gardens/name/{name} name=.name\n      open: POST /gardens/{gardenId}/open gardenId=.id\n"+
		"      update-by-code: PUT /gardens/code/{code} code=.code\n      update: PUT /gardens/{gardenId} gardenId=.id\n")
	if k := v.Node("gardens").Kinds(); !slices.Equal(k, []string{"update", "update-by-code", "delete-by-name", "open"}) {
		t.Errorf("kinds with variants: %v", k)
	}
	if b, _ := (&Tree{Scope: sc, Budget: 1}).Encode(); !strings.Contains(string(b), "exclude:\n    tags: [Admin*]\n    ops: [purgeAll, DELETE /x]\n  deps: true") {
		t.Errorf("encode:\n%s", b)
	}
	s := loadColony(t)
	for id, in := range map[string]bool{"listGardens": true, "getAdminStats": false, "purgeAll": false, "listPersons": false} {
		if got := sc.InScope(s.Op(id)); got != in {
			t.Errorf("%s in scope: %v", id, got)
		}
	}
	for _, bad := range []string{"scope: [x]", "scope: {tags: {a: 1}}", "scope: {exclude: [x]}", "scope: {nope: 1}", "scope: {exclude: {nope: 1}}",
		"tree: {a: {call: GET /a, min: x}}"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: no problem found", bad)
		}
	}
}

// TestColonyGlobalWhere: the where at the top of the file holds at every
// node that reads gardens: the gardens of the moons, the open gardens, the
// gardens a person search finds, and the require of the persons (Lin
// tends Fern and Mint only, so she is left out).
func TestColonyGlobalWhere(t *testing.T) {
	s := loadColony(t)
	st := testserver.NewColony(treeToken, 500)
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)
	b, err := ProposeScope(s, Scope{ExcludeTags: []string{"Admin"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	file := string(b) + "where:\n  Garden.Name: [Rose, Sage, Iris, Thyme]\n  Garden:\n    code: [rose, sage] # both forms\n  Harvest.season: [summer]\n"
	tr := parse(t, file)
	if len(tr.Where) != 3 || tr.Where[1].Resource != "Garden" || tr.Where[1].Clause.Field != "code" || tr.Where[1].Clause.Note != "both forms" {
		t.Fatalf("where: %+v", tr.Where)
	}
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: hs.URL + "/api", Token: treeToken})
	if err != nil {
		t.Fatal(err)
	}
	names := func(node, field string) []string {
		var out []string
		for _, r := range d.Of(tr.Node(node)) {
			if v := text(r.Value.(map[string]any)[field]); !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		slices.Sort(out)
		return out
	}
	if g := names("gardens", "name"); !slices.Equal(g, []string{"Rose", "Sage"}) {
		t.Errorf("gardens: %v", g)
	}
	if g := names("garden-search", "name"); !slices.Equal(g, []string{"Rose", "Sage"}) {
		t.Errorf("garden-search: %v", g)
	}
	if g := names("open-gardens", "name"); len(g) != 0 {
		t.Errorf("open gardens: Fern is out: %v", g)
	}
	if p := names("persons", "name"); !slices.Equal(p, []string{"Ada", "Grace"}) {
		t.Errorf("persons with a garden of the where: %v", p)
	}
	if !slices.ContainsFunc(d.Notes, func(n string) bool {
		return strings.Contains(n, "where Harvest: no node of the tree reads records of Harvest")
	}) {
		t.Errorf("notes: %v", d.Notes)
	}
	// two gardens: the third delete has no record left
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(built.Notes, func(n Note) bool { return strings.Contains(n.What, "gardens: delete-by-name not run") }) {
		t.Errorf("notes: %+v", built.Notes)
	}
	// written and read again, kept by a merge
	enc, err := tr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	again := parse(t, string(enc))
	if len(again.Where) != 3 || !strings.Contains(string(enc), "where:\n  Garden.Name: [Rose, Sage, Iris, Thyme]\n  Garden.code: [rose, sage] # both forms") {
		t.Errorf("encode:\n%s", enc)
	}
	Merge(s, tr, ProposeScope(s, tr.Scope), "2.0")
	if merged, _ := tr.Bytes(); !strings.Contains(string(merged), "  Garden:\n    code: [rose, sage] # both forms") {
		t.Errorf("merge keeps the where as written:\n%s", merged)
	}
	// a resource or field the spec does not have, a key without field
	for bad, want := range map[string]string{
		"where:\n  Gardn.name: [x]\n":    "Gardn is no resource or schema of the spec",
		"where:\n  Garden.colour: [x]\n": "Garden has no field colour (fields: code, id, moonId, name, personId, size, status)",
		"where:\n  Garden: [x]\n":        "Resource.field",
	} {
		other, err := Parse([]byte(string(b) + bad))
		var problems []string
		if err != nil {
			problems = []string{err.Error()}
		} else {
			problems = Check(s, other)
		}
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("%q: %v", bad, problems)
		}
	}
}
