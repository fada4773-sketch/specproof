package tree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// TestRepairNumberedNodes: six nodes whose names differ by a number only
// read through an operation the scope excludes; each fix (off: true)
// solves one problem, so -fix makes all six instead of stopping after
// three as if the same change came again and again.
func TestRepairNumberedNodes(t *testing.T) {
	var logs strings.Builder
	for i := 1; i <= 6; i++ {
		name := "shipLog"
		if i > 1 {
			name += fmt.Sprint(i)
		}
		fmt.Fprintf(&logs, "              %s: GET /ships/{shipId}/log shipId=.id\n", name)
	}
	file := "scope:\n  exclude:\n    ops: [getShipLog]\n" + strings.Replace(orderTree,
		"            each:\n              shipPilot:",
		"            each:\n"+logs.String()+"              shipPilot:", 1)
	fixed := repairAll(t, "numbered nodes", file)
	for i := 1; i <= 6; i++ {
		name := "shipLog"
		if i > 1 {
			name += fmt.Sprint(i)
		}
		if n := fixed.Node(name); n == nil || !n.Off {
			b, _ := fixed.Bytes()
			t.Fatalf("%s is off:\n%s", name, b)
		}
	}
}

// TestFixesOfOneRoundNameApart: two fixes found on one tree add a read of
// the pilots below two nodes; the names were free in the tree, but the
// first fix takes the name: the second gets another one, both apply and
// the file has no name twice.
func TestFixesOfOneRoundNameApart(t *testing.T) {
	s := loadStarport(t)
	tr := parse(t, strings.Replace(orderTree, "              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n", "", 1))
	pilot := modelOf(s).Resource("Pilot")
	need := refNeed{field: "pilotId", to: pilot, key: "id"}
	var fixes []Fix
	var names []string
	for _, at := range []string{"ships", "docks"} {
		e := OrderEntry{Node: "ships", Line: tr.Entry("ships").Line}
		f := newSourceFix(s, tr, tr.Node(at), e, need)
		if f.adds == "" {
			t.Fatalf("a node below %s: %+v", at, f)
		}
		fixes = append(fixes, f)
		names = append(names, f.adds)
	}
	if names[0] == names[1] {
		t.Fatalf("two fixes of one round add %q twice", names[0])
	}
	fixed, done, err := ApplyFixes(tr, fixes)
	if err != nil || len(done) != 2 {
		t.Fatalf("both fixes: %v %v", done, err)
	}
	if problems := Check(s, fixed); slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "used twice") }) {
		t.Errorf("a name twice: %v", problems)
	}
}

// TestFixWriteKeyAndSeedDefaults: a write that takes the id where the API
// wants the number of a moon gets the number; the update of moons the
// test never creates (create: none) runs on one of the seed (onSeed); a
// PUT on the very path a node reads, with nothing else creating moons,
// becomes its create.
func TestFixWriteKeyAndSeedDefaults(t *testing.T) {
	s := loadRegistry(t)
	seed := `
tree:
  moons:
    call: GET /moons
    pick: all
    key: [/Number, /Id]
    write:
      create: none
      update: PUT /moons/{moonNumber} moonNumber=.Id
`
	problems := Check(s, parse(t, seed))
	if !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "moons.write.update: {moonNumber} takes the Number of a moon, but .Id is its Id") &&
			strings.Contains(p, "moonNumber=.Id → moonNumber=.Number (-fix does it)")
	}) {
		t.Fatalf("the key of the write: %v", problems)
	}
	fixed, _, err := Repair(s, parse(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	w := fixed.Node("moons").Write[WriteUpdate]
	if w.Path["moonNumber"] != ".Number" || !w.OnSeed {
		b, _ := fixed.Bytes()
		t.Errorf("the number, and onSeed:\n%s", b)
	}
	upsert := `
params:
  moonNumber: M001
tree:
  moon:
    call: GET /moons/{moonNumber}
    path: {moonNumber: $moonNumber}
    key: [/Number]
    write:
      update: PUT /moons/{moonNumber}
`
	fixed, _, err = Repair(s, parse(t, upsert))
	if err != nil {
		t.Fatal(err)
	}
	if n := fixed.Node("moon"); n.Write[WriteCreate] == nil || n.Write[WriteCreate].Call != "PUT /moons/{moonNumber}" || n.Write[WriteUpdate] != nil {
		b, _ := fixed.Bytes()
		t.Errorf("the PUT creates the moon:\n%s", b)
	}
}

// cycleSpec: planets and moons choose their codes and name each other by
// lists of codes; a planet can be cloned.
const cycleSpec = `openapi: 3.0.3
info: {title: cycle, version: "1"}
paths:
  /planets:
    get: {operationId: listPlanets, responses: {"200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/Planet"}}}}}}}
    post: {operationId: createPlanet, requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/Planet"}}}}, responses: {"201": {description: ok}}}
  /planets/{code}:
    parameters: [{name: code, in: path, required: true, schema: {type: string}}]
    get: {operationId: getPlanet, responses: {"200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Planet"}}}}}}
    put: {operationId: updatePlanet, requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/Planet"}}}}, responses: {"200": {description: ok}}}
  /planets/{code}/clone:
    parameters: [{name: code, in: path, required: true, schema: {type: string}}]
    post: {operationId: clonePlanet, requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/Planet"}}}}, responses: {"201": {description: ok}}}
  /moons:
    get: {operationId: listMoons, responses: {"200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/Moon"}}}}}}}
    post: {operationId: createMoon, requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/Moon"}}}}, responses: {"201": {description: ok}}}
  /moons/{code}:
    parameters: [{name: code, in: path, required: true, schema: {type: string}}]
    get: {operationId: getMoon, responses: {"200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Moon"}}}}}}
components:
  schemas:
    Planet:
      type: object
      required: [code, name]
      properties: {code: {type: string}, name: {type: string}, moonCodes: {type: array, items: {type: string}}}
    Moon:
      type: object
      required: [code, name]
      properties: {code: {type: string}, name: {type: string}, planetCodes: {type: array, items: {type: string}}}
`

// TestFixCycleLeave: the creates of planets and moons name each other by
// codes the client gives: no source is needed, but each wants the other
// first. -fix creates the planets without moonCodes and sends them by an
// update after the moons; the clone of a planet gets a code of its own.
func TestFixCycleLeave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cycle.yaml")
	if err := os.WriteFile(path, []byte(cycleSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	file := `
order:
  - planets
  - moons
tree:
  planets:
    call: GET /planets
    pick: all
    key: [/code]
    write:
      create: POST /planets
      update: PUT /planets/{code}
      clone: POST /planets/{code}/clone
  moons:
    call: GET /moons
    pick: all
    key: [/code]
    write:
      create: POST /moons
`
	problems := Check(s, parse(t, file))
	if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "they need each other") }) {
		t.Fatalf("the cycle: %v", problems)
	}
	fixed, _, err := Repair(s, parse(t, file))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	if left := Check(s, fixed); len(left) > 0 {
		t.Fatalf("left: %v\n%s", left, b)
	}
	if w := fixed.Node("planets").Write[WriteCreate]; !slices.Equal(w.Leave, []string{"moonCodes"}) || fixed.StepIndex("planets.update") < fixed.OrderIndex("moons") {
		t.Fatalf("planets without moonCodes, the update after the moons:\n%s", b)
	}
	d := &Data{Records: []*Record{
		{ID: "planets1", Node: fixed.Node("planets"), Request: "GET /planets", Value: map[string]any{"code": "mars", "name": "Mars", "moonCodes": []any{"phobos"}}},
		{ID: "moons1", Node: fixed.Node("moons"), Request: "GET /moons", Value: map[string]any{"code": "phobos", "name": "Phobos", "planetCodes": []any{"mars"}}},
	}}
	built, err := Build(s, fixed, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range built.Scenario.Steps {
		body, _ := st.Body.(map[string]any)
		switch st.Call {
		case "POST /planets":
			if _, has := body["moonCodes"]; has {
				t.Errorf("the create leaves moonCodes out: %v", body)
			}
		case "PUT /planets/{code}":
			if body["moonCodes"] == nil {
				t.Errorf("the update sends them: %v", body)
			}
		case "POST /planets/{code}/clone":
			if body["code"] != "marscopy" {
				t.Errorf("the clone has a code of its own: %v", body)
			}
		}
	}
}
