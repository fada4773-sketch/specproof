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

// listStarport is a Starport whose ships name several pilots: in a list of
// keys (pilotIds) or in a list of objects (crew: [{pilotId, role}]).
type listStarport struct {
	name  string
	field string // the body field that names the pilots (pilotIds, crew.pilotId)
	props string // the property of NewShip and Ship in place of pilotId
	crew  bool   // testserver.Starport.Crew, else PilotLists
}

var listStarports = []listStarport{
	{name: "list of keys", field: "pilotIds", props: "        pilotIds:\n          type: array\n          items: {type: integer}"},
	{name: "list of objects", field: "crew.pilotId", crew: true,
		props: "        crew:\n          type: array\n          items:\n            type: object\n            required: [pilotId]\n            properties:\n              pilotId: {type: integer}\n              role: {type: string}"},
}

// load writes the spec of the variant and loads it.
func (v listStarport) load(t *testing.T) (*spec.Spec, string) {
	t.Helper()
	b, err := os.ReadFile(starportSpec)
	if err != nil {
		t.Fatal(err)
	}
	top := strings.SplitN(v.field, ".", 2)[0]
	src := strings.NewReplacer(
		"      required: [name, pilotId]\n      additionalProperties: false\n      properties:\n        name: {type: string, minLength: 1}\n        pilotId: {type: integer}",
		"      required: [name, "+top+"]\n      additionalProperties: false\n      properties:\n        name: {type: string, minLength: 1}\n"+v.props,
		"      required: [id, dockId, name, pilotId]\n      properties:\n        id: {type: integer, readOnly: true}\n        dockId: {type: integer, readOnly: true}\n        name: {type: string}\n        pilotId: {type: integer}",
		"      required: [id, dockId, name, "+top+"]\n      properties:\n        id: {type: integer, readOnly: true}\n        dockId: {type: integer, readOnly: true}\n        name: {type: string}\n"+v.props,
	).Replace(string(b))
	if strings.Count(src, "        "+top+":\n") != 2 {
		t.Fatal("the Starport spec changed: the ships keep pilotId")
	}
	path := filepath.Join(t.TempDir(), "starport-lists.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func (v listStarport) server(first int) *testserver.Starport {
	st := testserver.NewStarport(treeToken, first)
	st.PilotLists, st.Crew = !v.crew, v.crew
	return st
}

// withoutSource is orderTree for the variant without the pilot below the
// ships and without its source in order.
func (v listStarport) withoutSource() string {
	return strings.NewReplacer(
		"  - ships: {pilotId: shipPilot.id}\n", "  - ships\n",
		"            each:\n              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n", "",
	).Replace(orderTree)
}

// TestListRefsProposal: a field that names several records of another
// resource, as keys or inside the objects of a list, gets a node below
// that reads all of them (pick: all) and its source in order.
func TestListRefsProposal(t *testing.T) {
	for _, v := range listStarports {
		s, _ := v.load(t)
		b, err := Propose(s).Encode()
		if err != nil {
			t.Fatal(err)
		}
		tr := parse(t, string(b))
		if problems := Check(s, tr); len(problems) > 0 {
			t.Errorf("%s: the proposal fails the check: %v", v.name, problems)
		}
		e := tr.Entry("ships")
		if e == nil || e.Values[v.field] != "shipPilot.id" {
			t.Errorf("%s: the source of the pilots in order:\n%s", v.name, b)
		}
		n := tr.Node("shipPilot")
		if n == nil || n.Parent != tr.Node("ships") || n.Path["pilotId"] != "."+v.field || !n.Pick.All {
			t.Errorf("%s: shipPilot reads every pilot by .%s:\n%s", v.name, v.field, b)
		}
	}
}

// TestListRefsFix: without the source, the check names the field and -fix
// adds the node that reads all the records it names, and the source.
func TestListRefsFix(t *testing.T) {
	for _, v := range listStarports {
		s, _ := v.load(t)
		tr := parse(t, v.withoutSource())
		problems := Check(s, tr)
		want := "order: ships." + v.field + " holds the id of a pilot the test creates, but order gives it no source"
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) && strings.Contains(p, "(-fix does it)") }) {
			t.Errorf("%s: want %q, got %v", v.name, want, problems)
			continue
		}
		fixed, _, err := Repair(s, tr)
		if err != nil {
			t.Fatalf("%s: -fix: %v", v.name, err)
		}
		b, _ := fixed.Bytes()
		if left := Check(s, fixed); len(left) > 0 {
			t.Errorf("%s: -fix left %v\n%s", v.name, left, b)
		}
		e := fixed.Entry("ships")
		ref, _ := ParseRef(e.Values[v.field], fixed.names())
		src := fixed.Node(ref.Node)
		if src == nil || src.Parent != fixed.Node("ships") || !src.Pick.All ||
			!slices.ContainsFunc(src.Where, func(c Clause) bool { return c.Field == "id" && c.Test == "ships."+v.field }) {
			t.Errorf("%s: the node -fix adds lists every pilot of the ship:\n%s", v.name, b)
		}
	}
}

// TestListRefsRebuild: the proposal and the file -fix repaired rebuild the
// ships in an empty instance: each create names every pilot it had, as the
// placeholder of the pilot the test created; the read of a pilot by a value
// of the list takes its own.
func TestListRefsRebuild(t *testing.T) {
	for _, v := range listStarports {
		s, path := v.load(t)
		stage := v.server(100)
		stage.Fill()
		hs := httptest.NewServer(stage)
		t.Cleanup(hs.Close)
		proposal, err := Propose(s).Encode()
		if err != nil {
			t.Fatal(err)
		}
		fixed, _, err := Repair(s, parse(t, v.withoutSource()))
		if err != nil {
			t.Fatal(err)
		}
		for name, tr := range map[string]*Tree{"proposal": parse(t, string(proposal)), "fixed": fixed} {
			d, err := Fetch(context.Background(), s, tr, Options{BaseURL: hs.URL + "/api", Token: treeToken})
			if err != nil {
				t.Fatal(err)
			}
			b, err := Build(s, tr, d)
			if err != nil {
				t.Fatalf("%s, %s: %v", v.name, name, err)
			}
			ships := 0
			for _, st := range b.Scenario.Steps {
				if st.Call != "POST /docks/{dockId}/ships" {
					continue
				}
				ships++
				got, _ := Field(st.Body, v.field)
				l, _ := got.([]any)
				if len(l) != 2 || slices.ContainsFunc(l, func(x any) bool { return !strings.HasPrefix(text(x), "{{") }) {
					t.Errorf("%s, %s: %s names every pilot by its placeholder: %v", v.name, name, st.ID, st.Body)
				}
			}
			if ships == 0 {
				t.Fatalf("%s, %s: no create of a ship", v.name, name)
			}
			res, log := runEmptyAt(t, path, v.server(1), b)
			if res.Failed {
				t.Errorf("%s, %s: the scenario failed on an empty instance:\n%s", v.name, name, log)
			}
		}
	}
}

// runEmptyAt runs a scenario against an empty instance of a spec.
func runEmptyAt(t *testing.T, specPath string, h *testserver.Starport, b *Built) (*apitest.Result, string) {
	t.Helper()
	text, err := b.Scenario.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tree-test_gen.yaml")
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: specPath, Handler: h, Token: apitest.StaticToken(treeToken)},
		Scenario: path,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	return res, out.String() + "\n" + string(text)
}

// TestFieldThroughList: a name after a list reads it in each element.
func TestFieldThroughList(t *testing.T) {
	ship := map[string]any{
		"crew":  []any{map[string]any{"pilotId": 1}, map[string]any{"role": "cook"}, map[string]any{"pilotId": 3}},
		"decks": []any{map[string]any{"rooms": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}}, map[string]any{"rooms": []any{map[string]any{"id": "c"}}}},
		"none":  []any{},
		"tags":  []any{map[string]any{"x": 1}},
	}
	for path, want := range map[string]string{
		"crew.pilotId":   "[1,3]",
		"crew.1.role":    `"cook"`,
		"decks.rooms.id": `["a","b","c"]`,
		"none.pilotId":   "[]",
	} {
		got, ok := Field(ship, path)
		if !ok || encodeJSON(got) != want {
			t.Errorf("%s: %s %v, want %s", path, encodeJSON(got), ok, want)
		}
	}
	if v, ok := Field(ship, "tags.pilotId"); ok {
		t.Errorf("no element has it: %v", v)
	}
}

// TestListPointerOneRecord: a record with one list of objects (a ship with
// its crew) is no page; a page by its name stays one.
func TestListPointerOneRecord(t *testing.T) {
	s, _ := listStarports[1].load(t)
	if op := lookupOp(s, "GET /ships/{shipId}"); op == nil || isList(op) {
		t.Error("the read of a ship with its crew is no list")
	}
	if ptr, ok := listPointer(lookupOp(s, "GET /planets/{code}/docks")); !ok || ptr != "/items" {
		t.Errorf("the page of docks: %q %v", ptr, ok)
	}
}
