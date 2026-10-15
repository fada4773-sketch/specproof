package tree

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// berthSpec lists one kind of dock below planets and below moons, without a
// read of one dock by its id; the settings of a dock are read by that id
// (/Dock/id/{id}/Settings, /Dock/{id}/Settings/pilot/{pilotCode}).
const berthSpec = `openapi: 3.0.3
info: {title: Berths, version: "1"}
paths:
  /Planet:
    get:
      operationId: GetPlanets
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Planet'}}}}}
  /Planet/{planetCode}/Moon:
    parameters: [{name: planetCode, in: path, required: true, schema: {type: string}}]
    get:
      operationId: GetMoons
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Moon'}}}}}
  /Pilot:
    get:
      operationId: GetPilots
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Pilot'}}}}}
  /Dock/Planet/{planetCode}:
    parameters: [{name: planetCode, in: path, required: true, schema: {type: string}}]
    get:
      operationId: GetDocksByPlanet
      tags: [Dock]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Dock'}}}}}
    post:
      operationId: CreateDockForPlanet
      tags: [Dock]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Dock'}}}}
      responses:
        '200': {description: ok}
  /Dock/Planet/{planetCode}/Name/{name}:
    parameters:
      - {name: planetCode, in: path, required: true, schema: {type: string}}
      - {name: name, in: path, required: true, schema: {type: string}}
    patch:
      operationId: UpdateDockByPlanetAndName
      tags: [Dock]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Dock'}}}}
      responses:
        '200': {description: ok}
  /Dock/Planet/{planetCode}/Moon/{moonCode}:
    parameters:
      - {name: planetCode, in: path, required: true, schema: {type: string}}
      - {name: moonCode, in: path, required: true, schema: {type: string}}
    get:
      operationId: GetDocksByMoon
      tags: [Dock]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Dock'}}}}}
    post:
      operationId: CreateDockForMoon
      tags: [Dock]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Dock'}}}}
      responses:
        '200': {description: ok}
  /Survey/{kind}/Planet/{planetCode}:
    parameters:
      - {name: kind, in: path, required: true, schema: {type: string, enum: [full, short]}}
      - {name: planetCode, in: path, required: true, schema: {type: string}}
    get:
      operationId: GetSurveyByKind
      tags: [Survey]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Dock'}}}}}
  /Dock/id/{id}/Settings:
    parameters: [{name: id, in: path, required: true, schema: {type: integer}}]
    get:
      operationId: GetDockSettingsById
      tags: [Settings]
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/DockSettings'}}}}
  /Dock/{id}/Settings/pilot/{pilotCode}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: integer}}
      - {name: pilotCode, in: path, required: true, schema: {type: string}}
    get:
      operationId: GetDockSettingsByPilot
      tags: [Settings]
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/PilotSettings'}}}}
    put:
      operationId: UpdateDockSettingsByPilot
      tags: [Settings]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/PilotSettings'}}}}
      responses:
        '200':
          description: ok
          headers:
            X-Receipt-No: {schema: {type: string}}
  /receipts/{receiptNo}:
    parameters: [{name: receiptNo, in: path, required: true, schema: {type: string}}]
    get:
      operationId: GetReceipt
      tags: [Receipt]
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Receipt'}}}}
  /berths/{berthKey}:
    parameters: [{name: berthKey, in: path, required: true, schema: {type: string}}]
    get:
      operationId: GetBerth
      tags: [Berth]
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Receipt'}}}}
components:
  schemas:
    Planet:
      type: object
      properties: {Id: {type: integer}, PlanetCode: {type: string}}
    Moon:
      type: object
      properties: {Id: {type: integer}, MoonCode: {type: string}}
    Pilot:
      type: object
      properties: {Id: {type: integer}, PilotCode: {type: string}}
    Dock:
      type: object
      properties: {id: {type: integer}, Name: {type: string}, Size: {type: integer}}
    DockSettings:
      type: object
      properties: {Id: {type: integer}, Mode: {type: string}}
    PilotSettings:
      type: object
      properties: {Level: {type: integer}}
    Receipt:
      type: object
      properties: {Id: {type: string}}
`

func loadBerths(t *testing.T) *spec.Spec {
	t.Helper()
	path := filepath.Join(t.TempDir(), "berths.yaml")
	if err := os.WriteFile(path, []byte(berthSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestProposeSameRecordBelowTwoParents: the docks of a planet and of a moon
// are one kind of record; the generic {id} of /Dock/id/{id}/Settings and of
// /Dock/{id}/Settings/pilot/{pilotCode} is the id of a dock, not a value of
// params and not the id of a pilot.
func TestProposeSameRecordBelowTwoParents(t *testing.T) {
	s := loadBerths(t)
	b, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Errorf("check: %v\n%s", problems, out)
	}
	if _, ok := tr.Params["id"]; ok {
		t.Errorf("id is no value of params:\n%s", out)
	}
	byCall := map[string]*Node{}
	tr.Walk(func(n *Node) { byCall[n.Call] = n })
	docks := func(n *Node) bool {
		return n != nil && n.Parent != nil && strings.HasSuffix(n.Parent.Name, "Docks")
	}
	settings := byCall["GET /Dock/id/{id}/Settings"]
	if !docks(settings) || settings.Path["id"] != ".id" {
		t.Errorf("settings below the docks, id=.id:\n%s", out)
	}
	pilot := byCall["GET /Dock/{id}/Settings/pilot/{pilotCode}"]
	if !docks(pilot) || pilot.Path["id"] != ".id" || pilot.Path["pilotCode"] != "pilots.PilotCode" {
		t.Errorf("the settings of a pilot below the docks, the pilot read by name:\n%s", out)
	}
}

// TestComposedValueInFetch: a text with {{…}} composes references; in
// params it reads in place of $name, at the node that uses it.
func TestComposedValueInFetch(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	tr, d := fetch(t, s, `
params:
  note: '{{planets.code}}-{{.id}}'
tree:
  planets:
    call: GET /planets
    pick: {first: 1}
    key: [/code]
    each:
      docks:
        call: GET /planets/{code}/docks
        path: {code: .code}
        pick: {first: 1}
        each:
          dock:
            call: GET /docks/{dockId}
            path: {dockId: '{{.id}}'}
            query: {note: $note}
`, base)
	p, k, dk := d.Of(tr.Node("planets")), d.Of(tr.Node("docks")), d.Of(tr.Node("dock"))
	if len(p) != 1 || len(k) != 1 || len(dk) != 1 {
		t.Fatalf("records: %v %v %v %v", ids(p), ids(k), ids(dk), d.Notes)
	}
	code, id := text(p[0].Value.(map[string]any)["code"]), text(k[0].Value.(map[string]any)["id"])
	// a reference alone keeps its type, so the path is the number
	if !strings.Contains(dk[0].Request, "/docks/"+id) || !strings.Contains(dk[0].Request, "note="+code+"-"+id) {
		t.Errorf("request: %s", dk[0].Request)
	}
	// a node that is neither above nor fetched before is a problem, also
	// in a part
	late := parse(t, "tree:\n  dock: GET /docks/{dockId} dockId='{{docks.id}}'\n  docks: GET /planets/{code}/docks code=x\n")
	if problems := Check(s, late); len(problems) != 1 || !strings.Contains(problems[0], "neither above it nor fetched before") {
		t.Errorf("check: %v", problems)
	}
	nested := parse(t, "params:\n  a: '{{$b}}-1'\n  b: 1\ntree:\n  pilot: GET /pilots/{pilotId} pilotId=$a\n")
	if problems := Check(s, nested); len(problems) != 1 || !strings.Contains(problems[0], "no other $name") {
		t.Errorf("check nested: %v", problems)
	}
	// a $name in a part counts: unset, the node is left out
	unset := parse(t, "params:\n  b:\ntree:\n  pilot: GET /pilots/{pilotId} pilotId='P-{{$b}}'\n")
	if u := unsetParams(unset, unset.Node("pilot")); !slices.Equal(u, []string{"b"}) {
		t.Errorf("unset: %v", u)
	}
}

// saveTree is a planet of the seed whose update answers with a receipt number; the
// receipt is read by that id, a berth by a key composed of planet and dock.
const saveTree = `
params:
  receiptNo: R-stage
  berthKey: '{{planets.PlanetCode}}-{{docks.id}}'
tree:
  planets:
    call: GET /Planet
    key: [/PlanetCode]
    write:
      create: none
    each:
      docks:
        call: GET /Dock/Planet/{planetCode}
        path: {planetCode: .PlanetCode}
        key: [/id]
        write:
          create: none
        each:
          pilotSettings:
            call: GET /Dock/{id}/Settings/pilot/{pilotCode}
            path: {id: .id, pilotCode: P1}
            write:
              update:
                call: PUT /Dock/{id}/Settings/pilot/{pilotCode}
                path: {id: docks.id, pilotCode: P1}
                save: {receiptNo: header X-Receipt-No}
                onSeed: true
          berth: GET /berths/{berthKey} berthKey=$berthKey
  receipt: GET /receipts/{receiptNo} receiptNo=$receiptNo
`

// saveData are the records of a fetch of saveTree.
func saveData(tr *Tree) *Data {
	planet := &Record{ID: "planets1", Node: tr.Node("planets"), Value: map[string]any{"Id": 1, "PlanetCode": "mars"}}
	dock := &Record{ID: "docks1", Node: tr.Node("docks"), Parent: planet, Value: map[string]any{"id": 7, "Name": "South"}}
	set := &Record{ID: "pilotSettings1", Node: tr.Node("pilotSettings"), Parent: dock, Value: map[string]any{"Level": 2}}
	berth := &Record{ID: "berth1", Node: tr.Node("berth"), Parent: dock, Value: map[string]any{"Id": "mars-7"}}
	receipt := &Record{ID: "receipt1", Node: tr.Node("receipt"), Value: map[string]any{"Id": "R-stage"}}
	return &Data{Records: []*Record{planet, dock, set, berth, receipt}}
}

func stepOf(sc *scenario.Scenario, call string) (int, *scenario.Step) {
	for i, st := range sc.Steps {
		if st.Call == call {
			return i, st
		}
	}
	return -1, nil
}

// TestWriteSaveAndOnSeed: a write's save gives $receiptNo its value in the
// scenario, and the read that takes it runs after that write; onSeed runs
// the update on a record of the seed. Without the write the value of
// params stands in. A composed value is built from the stage values.
func TestWriteSaveAndOnSeed(t *testing.T) {
	s := loadBerths(t)
	tr := parse(t, saveTree)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("check: %v", problems)
	}
	built, err := Build(s, tr, saveData(tr))
	if err != nil {
		t.Fatal(err)
	}
	sc := built.Scenario
	ui, upd := stepOf(sc, "PUT /Dock/{id}/Settings/pilot/{pilotCode}")
	ti, receipt := stepOf(sc, "GET /receipts/{receiptNo}")
	if upd == nil || upd.Save["receiptNo"] != "header X-Receipt-No" || text(upd.Path["id"]) != "7" {
		t.Fatalf("the update of the seed record saves the receipt number: %+v", upd)
	}
	if receipt == nil || receipt.Path["receiptNo"] != "{{receiptNo}}" || ti != ui+1 {
		t.Errorf("the receipt is read right after the update (%d, %d): %+v", ui, ti, receipt)
	}
	if _, berth := stepOf(sc, "GET /berths/{berthKey}"); berth == nil || berth.Path["berthKey"] != "mars-7" {
		t.Errorf("berth: %+v", berth)
	}
	notes := func(b *Built) string {
		var out []string
		for _, n := range b.Notes {
			out = append(out, n.Step+": "+n.What+" – "+n.Why)
		}
		return strings.Join(out, "\n")
	}
	if n := notes(built); !strings.Contains(n, "runs on pilotSettings1 of the seed") || !strings.Contains(n, "runs after "+upd.ID) {
		t.Errorf("notes:\n%s", n)
	}
	// without onSeed the update does not run: the value of params stands in
	off := parse(t, strings.Replace(saveTree, "                onSeed: true\n", "", 1))
	built, err = Build(s, off, saveData(off))
	if err != nil {
		t.Fatal(err)
	}
	if _, upd := stepOf(built.Scenario, "PUT /Dock/{id}/Settings/pilot/{pilotCode}"); upd != nil {
		t.Errorf("no update without onSeed: %+v", upd)
	}
	if _, receipt := stepOf(built.Scenario, "GET /receipts/{receiptNo}"); receipt == nil || receipt.Path["receiptNo"] != "R-stage" {
		t.Errorf("receipt: %+v", receipt)
	}
	if n := notes(built); !strings.Contains(n, "$receiptNo from params") || !strings.Contains(n, "onSeed: true changes a record of the seed") {
		t.Errorf("notes:\n%s", n)
	}
	// the file keeps save and onSeed; onSeed only for changes
	b, err := tr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	again := parse(t, string(b))
	if w := again.Node("pilotSettings").Write[WriteUpdate]; w == nil || w.Save["receiptNo"] != "header X-Receipt-No" || !w.OnSeed {
		t.Errorf("encode:\n%s", b)
	}
	if again.Params["berthKey"] != "{{planets.PlanetCode}}-{{docks.id}}" {
		t.Errorf("composed value:\n%s", b)
	}
	if _, err := Parse([]byte("tree:\n  docks:\n    call: GET /x\n    write:\n      delete:\n        call: DELETE /x\n        onSeed: true\n")); err == nil || !strings.Contains(err.Error(), "only an update, a patch or an action") {
		t.Errorf("onSeed at a delete: %v", err)
	}
	bad := parse(t, strings.Replace(saveTree, "save: {receiptNo:", "save: {'receipt number':", 1))
	if problems := Check(s, bad); len(problems) != 1 || !strings.Contains(problems[0], "a name of letters") {
		t.Errorf("save name: %v", problems)
	}
}
