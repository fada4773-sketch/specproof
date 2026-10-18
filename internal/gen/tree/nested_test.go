package tree

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// loadNestedStarport is the Starport whose ships name their pilot inside
// an object: crew: {pilotId, role}.
func loadNestedStarport(t *testing.T) *spec.Spec {
	t.Helper()
	b, err := os.ReadFile(starportSpec)
	if err != nil {
		t.Fatal(err)
	}
	crew := "        crew:\n          type: object\n          required: [pilotId]\n          properties:\n            pilotId: {type: integer}\n            role: {type: string}"
	src := strings.NewReplacer(
		"      required: [name, pilotId]\n      additionalProperties: false\n      properties:\n        name: {type: string, minLength: 1}\n        pilotId: {type: integer}",
		"      required: [name, crew]\n      additionalProperties: false\n      properties:\n        name: {type: string, minLength: 1}\n"+crew,
		"      required: [id, dockId, name, pilotId]\n      properties:\n        id: {type: integer, readOnly: true}\n        dockId: {type: integer, readOnly: true}\n        name: {type: string}\n        pilotId: {type: integer}",
		"      required: [id, dockId, name, crew]\n      properties:\n        id: {type: integer, readOnly: true}\n        dockId: {type: integer, readOnly: true}\n        name: {type: string}\n"+crew,
	).Replace(string(b))
	if src == string(b) || strings.Count(src, "crew:") != 2 {
		t.Fatal("the Starport spec changed: the ships have no crew")
	}
	path := filepath.Join(t.TempDir(), "starport-nested.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// nestedTree is orderTree for ships with crew.pilotId.
var nestedTree = strings.NewReplacer(
	"{pilotId: shipPilot.id}", "{crew.pilotId: shipPilot.id}",
	"pilotId=.pilotId", "pilotId=.crew.pilotId",
).Replace(orderTree)

// TestOrderNestedProposal: a reference inside an object of the body
// (crew.pilotId) gets a node below that reads its record and a source in
// order, as one on top does; the check takes it.
func TestOrderNestedProposal(t *testing.T) {
	s := loadNestedStarport(t)
	b, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if e := tr.Entry("ships"); e == nil || e.Values["crew.pilotId"] != "shipPilot.id" {
		t.Errorf("the source of the pilot inside crew:\n%s", out)
	}
	if n := tr.Node("shipPilot"); n == nil || n.Path["pilotId"] != ".crew.pilotId" {
		t.Errorf("shipPilot reads the pilot by .crew.pilotId:\n%s", out)
	}
	if problems := Check(s, tr); len(problems) > 0 {
		t.Errorf("the proposal fails the check: %v", problems)
	}
	if problems := Check(s, parse(t, nestedTree)); len(problems) > 0 {
		t.Errorf("crew.pilotId by hand: %v", problems)
	}
}

// TestOrderNestedFix: -fix gives a create the source of a reference inside
// an object: in order, with the node below that reads it when the tree
// lacks one; a source on top that the body does not have goes.
func TestOrderNestedFix(t *testing.T) {
	s := loadNestedStarport(t)
	for name, c := range map[string]struct {
		from, to, drop, want, change string
	}{
		"source below": {"  - ships: {crew.pilotId: shipPilot.id}\n", "  - ships\n", "",
			"order: ships.crew.pilotId holds the id of a pilot the test creates, but order gives it no source",
			"- ships → - ships: {…, crew.pilotId: shipPilot.id}"},
		"no node below": {"  - ships: {crew.pilotId: shipPilot.id}\n", "  - ships\n", "              shipPilot: GET /pilots/{pilotId} pilotId=.crew.pilotId\n",
			"order: ships.crew.pilotId holds the id of a pilot the test creates, but order gives it no source",
			"add below ships the node pilot: GET /pilots/{pilotId} pilotId=.crew.pilotId, and - ships: {…, crew.pilotId: pilot.id}"},
		"source on top": {"{crew.pilotId: shipPilot.id}", "{pilotId: shipPilot.id}", "",
			"order: ships: the body of POST /docks/{dockId}/ships has no field pilotId",
			"remove pilotId: shipPilot.id"},
	} {
		file := strings.Replace(nestedTree, c.from, c.to, 1)
		if c.drop != "" {
			file = strings.Replace(file, c.drop, "", 1)
		}
		tr := parse(t, file)
		problems := Check(s, tr)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) && strings.Contains(p, c.change) }) {
			t.Errorf("%s: want %q and %q, got %v", name, c.want, c.change, problems)
		}
		fixed, done, err := Repair(s, tr)
		if err != nil {
			t.Errorf("%s: -fix: %v", name, err)
			continue
		}
		b, _ := fixed.Bytes()
		if left := Check(s, fixed); len(done) == 0 || len(left) > 0 {
			t.Errorf("%s: -fix made %v, left %v\n%s", name, done, left, b)
		}
		e := fixed.Entry("ships")
		if e == nil || len(e.Values) != 1 || e.Values["crew.pilotId"] == nil {
			t.Errorf("%s: the source after -fix:\n%s", name, b)
			continue
		}
		ref, _ := ParseRef(e.Values["crew.pilotId"], fixed.names())
		if src := fixed.Node(ref.Node); src == nil || src.Parent != fixed.Node("ships") || src.Path["pilotId"] != ".crew.pilotId" {
			t.Errorf("%s: the source reads the pilot of the crew:\n%s", name, b)
		}
	}
}

// TestOrderNestedBuild: the create of a ship sends the pilot the test
// creates inside crew, the other fields of crew as the record has them;
// without the source the build stops and names the field by its path.
func TestOrderNestedBuild(t *testing.T) {
	_, base := stage(t)
	_, d := fetch(t, loadStarport(t), orderTree, base)
	// the records as the nested API answers: the pilot inside crew
	for _, r := range d.Records {
		if r.Node.Name != "ships" {
			continue
		}
		v := map[string]any{}
		for k, x := range r.Value.(map[string]any) {
			if k != "pilotId" {
				v[k] = x
			}
		}
		v["crew"] = map[string]any{"pilotId": r.Value.(map[string]any)["pilotId"], "role": "captain"}
		r.Value = v
	}
	s := loadNestedStarport(t)
	built, err := Build(s, parse(t, nestedTree), d)
	if err != nil {
		t.Fatal(err)
	}
	ships := 0
	for _, st := range built.Scenario.Steps {
		if st.Call != "POST /docks/{dockId}/ships" {
			continue
		}
		ships++
		crew, _ := st.Body.(map[string]any)["crew"].(map[string]any)
		if !strings.HasPrefix(text(crew["pilotId"]), "{{shipPilot") || crew["role"] != "captain" {
			t.Errorf("the crew of a ship: %+v", st.Body)
		}
	}
	if ships == 0 {
		t.Fatal("no create of a ship")
	}
	plain := parse(t, strings.Replace(nestedTree, "  - ships: {crew.pilotId: shipPilot.id}\n", "  - ships\n", 1))
	_, err = Build(s, plain, d)
	if err == nil || !strings.Contains(err.Error(), "order: ships.crew.pilotId holds the id of a pilot the test creates, but order gives it no source") ||
		!strings.Contains(err.Error(), "- ships → - ships: {…, crew.pilotId: shipPilot.id} (-fix does it)") {
		t.Errorf("a ship without the source of its pilot: %v", err)
	}
}
