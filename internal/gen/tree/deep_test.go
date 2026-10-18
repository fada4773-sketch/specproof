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

// TestDeepFind: a reference field the record lacks (pilotId) is found
// deeper under its own name, a name that ends with it, or as the id of an
// object named after it; the best tier wins, lists and the fields the body
// takes under their own name are not searched, the search stops at its
// limits.
func TestDeepFind(t *testing.T) {
	none := func(string) bool { return false }
	for name, c := range map[string]struct {
		rec   map[string]any
		field string
		own   func(string) bool
		want  []string // path=value
	}{
		"same name deeper": {
			rec:   map[string]any{"name": "Comet", "pilotDetails": map[string]any{"pilotId": 7, "name": "Vega"}},
			field: "pilotId", want: []string{"pilotDetails.pilotId=7"},
		},
		"name that ends with it, deep": {
			rec:   map[string]any{"info": map[string]any{"crew": map[string]any{"AktuellePilotId": 8}}},
			field: "pilotId", want: []string{"info.crew.AktuellePilotId=8"},
		},
		"name that ends with it, on top": {
			rec:   map[string]any{"currentPilotId": 9},
			field: "pilotId", want: []string{"currentPilotId=9"},
		},
		"id of an object named after it": {
			rec:   map[string]any{"currentPilot": map[string]any{"id": 4, "name": "Vega"}},
			field: "pilotId", want: []string{"currentPilot.id=4"},
		},
		"plural object": {
			rec:   map[string]any{"pilotsInfo": map[string]any{"id": 5}},
			field: "pilotId", want: []string{"pilotsInfo.id=5"},
		},
		"same name beats the others": {
			rec: map[string]any{"pilot": map[string]any{"id": 1}, "lastPilotId": 2,
				"details": map[string]any{"pilotId": 3}},
			field: "pilotId", want: []string{"details.pilotId=3"},
		},
		"several of one tier": {
			rec:   map[string]any{"a": map[string]any{"pilotId": 1}, "b": map[string]any{"pilotId": 2}},
			field: "pilotId", want: []string{"a.pilotId=1", "b.pilotId=2"},
		},
		"no lists": {
			rec:   map[string]any{"crew": []any{map[string]any{"pilotId": 1}}},
			field: "pilotId",
		},
		"a field the body takes under its own name": {
			rec:   map[string]any{"coPilotId": 6},
			field: "pilotId", own: func(k string) bool { return k == "coPilotId" },
		},
		"no reference field": {
			rec:   map[string]any{"details": map[string]any{"name": "Vega"}},
			field: "name",
		},
		"id alone": {
			rec:   map[string]any{"details": map[string]any{"id": 1}},
			field: "id",
		},
		"other words": {
			rec:   map[string]any{"dock": map[string]any{"id": 1}, "copilotId": 2, "details": map[string]any{"pilotName": "Vega"}},
			field: "pilotId",
		},
		"too deep": {
			rec:   nest(deepDepth, map[string]any{"pilotId": 1}),
			field: "pilotId",
		},
		"deep enough": {
			rec:   nest(deepDepth-1, map[string]any{"pilotId": 1}),
			field: "pilotId", want: []string{strings.Repeat("n.", deepDepth-1) + "pilotId=1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			own := c.own
			if own == nil {
				own = none
			}
			hits, cut := deepFind(c.rec, c.field, own)
			var got []string
			for _, h := range hits {
				got = append(got, fmt.Sprintf("%s=%v", h.path, h.val))
			}
			if !slices.Equal(got, c.want) || cut {
				t.Errorf("got %v (cut %v), want %v", got, cut, c.want)
			}
		})
	}
}

// nest puts m depth objects deep: {n: {n: … m}}.
func nest(depth int, m map[string]any) map[string]any {
	for range depth {
		m = map[string]any{"n": m}
	}
	return m
}

// TestDeepFindLimit: a record with more fields than deepVisit stops the
// search, which says so; a large record stays fast.
func TestDeepFindLimit(t *testing.T) {
	rec := map[string]any{}
	for i := range deepVisit + 10 {
		rec[fmt.Sprintf("f%05d", i)] = i
	}
	rec["zz"] = map[string]any{"pilotId": 1}
	if hits, cut := deepFind(rec, "pilotId", func(string) bool { return false }); !cut || len(hits) != 0 {
		t.Errorf("the search stops at its limit: %v %v", hits, cut)
	}
}

const deepSpec = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /pilots:
    get:
      operationId: listPilots
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Pilot'}}
    post:
      operationId: createPilot
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/PilotWrite'}
      responses:
        "201":
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Pilot'}
  /ships:
    get:
      operationId: listShips
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Ship'}}
    post:
      operationId: createShip
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ShipWrite'}
      responses:
        "201":
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Ship'}
components:
  schemas:
    Pilot:
      type: object
      properties:
        id: {type: integer, readOnly: true}
        name: {type: string}
    PilotWrite:
      type: object
      required: [name]
      properties:
        name: {type: string}
    Ship:
      type: object
      properties:
        id: {type: integer, readOnly: true}
        name: {type: string}
        pilotDetails:
          type: object
          properties:
            pilotId: {type: integer}
            name: {type: string}
        log:
          type: object
          properties:
            lastPilotId: {type: integer}
            firstPilotId: {type: integer}
    ShipWrite:
      type: object
      required: [name, pilotId]
      properties:
        name: {type: string}
        pilotId: {type: integer}
`

func loadDeepSpec(t *testing.T) *spec.Spec {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deep.yaml")
	if err := os.WriteFile(path, []byte(deepSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// deepBuild builds the ships of tree from a ship with pilotDetails and a
// log; it returns the body of the ship's create
// and the notes of that step.
func deepBuild(t *testing.T, tree string, ship map[string]any) (map[string]any, []string) {
	t.Helper()
	s := loadDeepSpec(t)
	tr := parse(t, tree)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("check: %v", problems)
	}
	recs := []*Record{{ID: "ships1", Node: tr.Node("ships"), Request: "GET /ships", Value: ship}}
	b, err := Build(s, tr, &Data{Records: recs})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(b.Scenario.Steps, func(st *scenario.Step) bool { return st.Call == "POST /ships" })
	if i < 0 {
		t.Fatalf("no create of the ship: %+v", b.Scenario.Steps)
	}
	st := b.Scenario.Steps[i]
	var notes []string
	for _, n := range b.Notes {
		if n.Step == st.ID {
			notes = append(notes, n.What+": "+n.Why)
		}
	}
	body, _ := st.Body.(map[string]any)
	return body, notes
}

const deepShipsTree = `
tree:
  ships:
    call: GET /ships
    key: [/id]
    write:
      create: POST /ships
`

func deepShip() map[string]any {
	return map[string]any{"id": 3, "name": "Comet",
		"pilotDetails": map[string]any{"pilotId": 11, "name": "Vega"},
		"log":          map[string]any{"lastPilotId": 12, "firstPilotId": 13}}
}

// TestBuildDeepRef: the create of a ship takes the pilotId the record
// holds in pilotDetails.pilotId and notes where it came from.
func TestBuildDeepRef(t *testing.T) {
	body, notes := deepBuild(t, deepShipsTree, deepShip())
	if fmt.Sprint(body["pilotId"]) != "11" {
		t.Errorf("pilotId from pilotDetails.pilotId: %v", body)
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.HasPrefix(n, "pilotId = 11 from pilotDetails.pilotId") }) {
		t.Errorf("the note names the source: %v", notes)
	}

}

// TestBuildDeepRefAmbiguous: fields of one tier with different values are
// not taken; the note names them and how fields decides.
func TestBuildDeepRefAmbiguous(t *testing.T) {
	ship := deepShip()
	delete(ship, "pilotDetails")
	body, notes := deepBuild(t, deepShipsTree, ship)
	if fmt.Sprint(body["pilotId"]) == "12" || fmt.Sprint(body["pilotId"]) == "13" {
		t.Errorf("no guess between lastPilotId and firstPilotId: %v", body)
	}
	if !slices.ContainsFunc(notes, func(n string) bool {
		return strings.Contains(n, "pilotId: several fields of the record name it, none taken") &&
			strings.Contains(n, "log.firstPilotId = 13, log.lastPilotId = 12") && strings.Contains(n, "fields: pilotId: .log.firstPilotId")
	}) {
		t.Errorf("the note names the candidates: %v", notes)
	}
}

// TestBuildFieldsRule: fields of the node decide where a body field comes
// from, over the search; check names a field no body has.
func TestBuildFieldsRule(t *testing.T) {
	tree := strings.Replace(deepShipsTree, "    key: [/id]\n", "    key: [/id]\n    fields:\n      pilotId: .log.lastPilotId\n", 1)
	body, notes := deepBuild(t, tree, deepShip())
	if fmt.Sprint(body["pilotId"]) != "12" {
		t.Errorf("pilotId from fields: %v", body)
	}
	if !slices.ContainsFunc(notes, func(n string) bool {
		return strings.HasPrefix(n, "pilotId = 12 from fields: fields of ships: pilotId: .log.lastPilotId")
	}) {
		t.Errorf("the note names the rule: %v", notes)
	}
	if slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "from pilotDetails") }) {
		t.Errorf("no search for a field fields gives: %v", notes)
	}

	s := loadDeepSpec(t)
	bad := strings.Replace(tree, "pilotId: .log.lastPilotId", "pilotRef: .log.lastPilotId", 1)
	if problems := Check(s, parse(t, bad)); !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "ships.fields.pilotRef: no body of a write of ships has the field pilotRef")
	}) {
		t.Errorf("check names the field no body has: %v", problems)
	}
	none := strings.Replace(tree, "    write:\n      create: POST /ships\n", "", 1)
	if problems := Check(s, parse(t, none)); !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "ships.fields.pilotId: no write of ships builds a body from the record")
	}) {
		t.Errorf("check names fields without a write: %v", problems)
	}
	b, err := parse(t, tree).Encode()
	if err != nil || !strings.Contains(string(b), "fields:") {
		t.Errorf("encode keeps fields: %v\n%s", err, b)
	}
}

// TestMergeKeepsFields: merging a new proposal into a file keeps the fields
// of a node.
func TestMergeKeepsFields(t *testing.T) {
	s := loadDeepSpec(t)
	tree := strings.Replace(deepShipsTree, "    key: [/id]\n", "    key: [/id]\n    fields:\n      pilotId: .log.lastPilotId # the last one\n", 1)
	tr := parse(t, tree)
	Merge(s, tr, ProposeScope(s, Scope{}), "")
	b, err := tr.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "pilotId: .log.lastPilotId # the last one") {
		t.Errorf("merge keeps fields:\n%s", b)
	}
}
