package tree

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// nestedSpec is a Starport whose docks live below their planet only: a
// dock is read by GET /planets/{code}/docks/{dockId}, there is no read by
// the id alone. A ship names its home dock (homeDockId), a dock of the
// same planet.
const nestedSpec = `openapi: 3.0.3
info: {title: Nested Starport, version: "1.0"}
servers:
  - url: /api
paths:
  /planets:
    get:
      operationId: listPlanets
      tags: [Planet]
      responses:
        "200":
          description: the planets
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Planet'}}
  /planets/{code}/docks:
    parameters:
      - {name: code, in: path, required: true, schema: {type: string}}
    get:
      operationId: listDocks
      tags: [Dock]
      responses:
        "200":
          description: the docks of the planet
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Dock'}}
    post:
      operationId: createDock
      tags: [Dock]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/DockWrite'}
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Dock'}
  /planets/{code}/docks/{dockId}:
    parameters:
      - {name: code, in: path, required: true, schema: {type: string}}
      - {name: dockId, in: path, required: true, schema: {type: integer}}
    get:
      operationId: getDock
      tags: [Dock]
      responses:
        "200":
          description: the dock
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Dock'}
        "404": {description: no such dock}
  /planets/{code}/ships:
    parameters:
      - {name: code, in: path, required: true, schema: {type: string}}
    get:
      operationId: listShips
      tags: [Ship]
      responses:
        "200":
          description: the ships of the planet
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Ship'}}
    post:
      operationId: createShip
      tags: [Ship]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ShipWrite'}
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Ship'}
        "422": {description: no such dock}
  /planets/{code}/ships/{shipId}:
    parameters:
      - {name: code, in: path, required: true, schema: {type: string}}
      - {name: shipId, in: path, required: true, schema: {type: integer}}
    get:
      operationId: getShip
      tags: [Ship]
      responses:
        "200":
          description: the ship
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Ship'}
        "404": {description: no such ship}
    put:
      operationId: updateShip
      tags: [Ship]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ShipWrite'}
      responses:
        "200":
          description: updated
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Ship'}
        "404": {description: no such ship}
        "422": {description: no such dock}
components:
  schemas:
    Planet:
      type: object
      required: [code, name]
      properties:
        code: {type: string}
        name: {type: string}
    Dock:
      type: object
      required: [id, name]
      properties:
        id: {type: integer, readOnly: true}
        name: {type: string}
    DockWrite:
      type: object
      required: [name]
      additionalProperties: false
      properties:
        name: {type: string, minLength: 1}
    Ship:
      type: object
      required: [id, name, homeDockId]
      properties:
        id: {type: integer, readOnly: true}
        name: {type: string}
        homeDockId: {type: integer}
    ShipWrite:
      type: object
      required: [name, homeDockId]
      additionalProperties: false
      properties:
        name: {type: string, minLength: 1}
        homeDockId: {type: integer}
`

// nestedDockTree reads the docks and the ships of mars; order gives the home
// dock of a ship no source: the create sends the dock id of the stage.
const nestedDockTree = `order:
  - docks
  - ships
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
        pick: all
        key: [/id]
        write:
          create: POST /planets/{code}/docks code=planets.code
      ships:
        call: GET /planets/{code}/ships
        path: {code: .code}
        pick: all
        key: [/id]
        write:
          create: POST /planets/{code}/ships code=planets.code
`

// nestedPort is the API of nestedSpec: planets are seed, docks and ships
// are created below a planet; a ship's home dock must be a dock of its
// planet.
type nestedPort struct {
	mu     sync.Mutex
	next   int
	docks  map[string][]map[string]any
	ships  map[string][]map[string]any
	planet []map[string]any
}

func newNestedPort(filled bool) *nestedPort {
	p := &nestedPort{next: 100, docks: map[string][]map[string]any{}, ships: map[string][]map[string]any{},
		planet: []map[string]any{{"code": "mars", "name": "Mars"}, {"code": "venus", "name": "Venus"}}}
	if filled {
		p.docks["mars"] = []map[string]any{{"id": 1, "name": "North"}, {"id": 2, "name": "South"}}
		p.docks["venus"] = []map[string]any{{"id": 3, "name": "Cloud"}}
		p.ships["mars"] = []map[string]any{{"id": 11, "name": "Comet", "homeDockId": 2}, {"id": 12, "name": "Dust", "homeDockId": 1}}
		p.ships["venus"] = []map[string]any{{"id": 13, "name": "Haze", "homeDockId": 3}}
	}
	return p
}

func (p *nestedPort) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	answer := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/"), "/")
	if len(parts) == 1 && parts[0] == "planets" && r.Method == http.MethodGet {
		answer(http.StatusOK, p.planet)
		return
	}
	if len(parts) < 3 || parts[0] != "planets" {
		http.NotFound(w, r)
		return
	}
	code, kind := parts[1], parts[2]
	list := map[string]map[string][]map[string]any{"docks": p.docks, "ships": p.ships}[kind]
	if list == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 3 && r.Method == http.MethodGet:
		out := list[code]
		if out == nil {
			out = []map[string]any{}
		}
		answer(http.StatusOK, out)
	case len(parts) == 3 && r.Method == http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			answer(http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if kind == "ships" {
			id, _ := body["homeDockId"].(float64)
			if !p.has(p.docks[code], int(id)) {
				answer(http.StatusUnprocessableEntity, map[string]any{"error": "no such dock"})
				return
			}
			body["homeDockId"] = int(id)
		}
		p.next++
		body["id"] = p.next
		list[code] = append(list[code], body)
		answer(http.StatusCreated, body)
	case len(parts) == 4 && r.Method == http.MethodPut && kind == "ships":
		id, _ := strconv.Atoi(parts[3])
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			answer(http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		dock, _ := body["homeDockId"].(float64)
		if !p.has(p.docks[code], int(dock)) {
			answer(http.StatusUnprocessableEntity, map[string]any{"error": "no such dock"})
			return
		}
		for _, rec := range list[code] {
			if rec["id"] == id {
				rec["name"], rec["homeDockId"] = body["name"], int(dock)
				answer(http.StatusOK, rec)
				return
			}
		}
		answer(http.StatusNotFound, map[string]any{"error": "not found"})
	case len(parts) == 4 && r.Method == http.MethodGet:
		id, _ := strconv.Atoi(parts[3])
		for _, rec := range list[code] {
			if rec["id"] == id {
				answer(http.StatusOK, rec)
				return
			}
		}
		answer(http.StatusNotFound, map[string]any{"error": "not found"})
	default:
		http.NotFound(w, r)
	}
}

func (p *nestedPort) has(list []map[string]any, id int) bool {
	for _, rec := range list {
		if rec["id"] == id {
			return true
		}
	}
	return false
}

// loadNested writes nestedSpec and loads it.
func loadNested(t *testing.T) (*spec.Spec, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested.yaml")
	if err := os.WriteFile(path, []byte(nestedSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// fixUntilClean runs the loop of -fix as the command does: repair, fetch,
// build, make the fixes of the build and fetch again, until the build has
// no fix left; it fails after rounds rounds.
func fixUntilClean(t *testing.T, s *spec.Spec, file, base string, rounds int) (*Tree, *Built) {
	t.Helper()
	tr, _, err := Repair(s, parse(t, file))
	if err != nil {
		t.Fatal(err)
	}
	for range rounds {
		b, _ := tr.Bytes()
		var d *Data
		tr, d = fetch(t, s, string(b), base)
		built, err := Build(s, tr, d)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, b)
		}
		if len(built.Fixes) == 0 {
			return tr, built
		}
		if tr, _, err = ApplyFixes(tr, built.Fixes); err != nil {
			t.Fatalf("fixes: %v", err)
		}
		if tr, _, err = Repair(s, tr); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := tr.Bytes()
	t.Fatalf("still fixes after %d rounds:\n%s", rounds, b)
	return nil, nil
}

// TestFixNestedSource: the home dock of a ship is a key of the stage; the
// docks are read only below their planet (two path values). -fix adds
// below the ships the read of the dock with the planet from the node
// above and the dock from the field, gives it as the source in order, and
// after the fetch the test sends the dock it created and runs on an empty
// instance.
func TestFixNestedSource(t *testing.T) {
	s, path := loadNested(t)
	hs := httptest.NewServer(newNestedPort(true))
	t.Cleanup(hs.Close)
	base := hs.URL + "/api"
	tr, built := fixUntilClean(t, s, nestedDockTree, base, 4)
	b, _ := tr.Bytes()
	if !strings.Contains(string(b), "GET /planets/{code}/docks/{dockId} code=planets.code dockId=.homeDockId") {
		t.Errorf("the read of the dock below the ships:\n%s", b)
	}
	if v := tr.Entry("ships").Values["homeDockId"]; v == nil || !strings.HasSuffix(text(v), ".id") {
		t.Errorf("the source in order: %v\n%s", v, b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Errorf("no error left: %+v", errs)
	}
	creates := 0
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /planets/{code}/ships" {
			creates++
			if !strings.HasPrefix(text(st.Body.(map[string]any)["homeDockId"]), "{{") {
				t.Errorf("%s sends the dock the test created: %v", st.ID, st.Body)
			}
		}
	}
	if creates != 2 {
		t.Errorf("both ships of mars are created: %d", creates)
	}
	runNested(t, path, built)
}

// TestFixSchemaValues: values of the stage that do not fit the request (a
// name the pattern does not allow, a call sign too long) are errors; -fix
// gives the node of the ships values that fit in fields, and built again
// nothing is left. fields: {x: null} leaves a field out of the body.
func TestFixSchemaValues(t *testing.T) {
	src := strings.NewReplacer(
		"    ShipWrite:\n      type: object\n      required: [name, homeDockId]\n      additionalProperties: false\n      properties:\n        name: {type: string, minLength: 1}\n",
		"    ShipWrite:\n      type: object\n      required: [name, homeDockId]\n      additionalProperties: false\n      properties:\n        name: {type: string, pattern: '^[A-Z]{2}-[0-9]{3}$'}\n        callSign: {type: string, maxLength: 3}\n",
		"        name: {type: string}\n        homeDockId: {type: integer}\n",
		"        name: {type: string}\n        callSign: {type: string}\n        homeDockId: {type: integer}\n",
	).Replace(nestedSpec)
	if strings.Count(src, "callSign") != 2 {
		t.Fatal("the nested spec changed")
	}
	path := filepath.Join(t.TempDir(), "nested.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	port := newNestedPort(true)
	for _, sh := range port.ships["mars"] {
		sh["callSign"] = "ALPHA"
	}
	hs := httptest.NewServer(port)
	t.Cleanup(hs.Close)
	base := hs.URL + "/api"
	_, first := buildFrom(t, s, nestedDockTree, base)
	kinds := map[string]bool{}
	for _, n := range first.Errors() {
		if strings.HasSuffix(n.What, "does not fit the schema") {
			kinds[strings.SplitN(n.What, " ", 2)[0]] = true
		}
	}
	if !kinds["name"] || !kinds["callSign"] {
		t.Fatalf("both fields are errors: %+v", first.Errors())
	}
	tr, built := fixUntilClean(t, s, nestedDockTree, base, 5)
	b, _ := tr.Bytes()
	ships := tr.Node("ships")
	if ships.Fields["name"] == nil || ships.Fields["callSign"] == nil {
		t.Fatalf("fields of the ships give both:\n%s", b)
	}
	if !strings.Contains(string(b), "(-fix)") {
		t.Errorf("the comment says why:\n%s", b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Errorf("no error left: %+v\n%s", errs, b)
	}
	// null leaves the field out
	file := strings.Replace(string(b), "      callSign: ", "      callSign: null # out\n      x: ", 1)
	file = strings.Replace(file, "      x: ", "      #", 1)
	tr, d := fetch(t, s, file, base)
	if v, has := tr.Node("ships").Fields["callSign"]; !has || v != nil {
		t.Fatalf("callSign: null in the file:\n%s", file)
	}
	built, err = Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /planets/{code}/ships" {
			if _, has := st.Body.(map[string]any)["callSign"]; has {
				t.Errorf("%s leaves callSign out: %v", st.ID, st.Body)
			}
		}
	}
}

// TestFixLostSourceBecomesSeed: every ship names a dock of the other
// planet, which the read below the planet cannot give: the node -fix adds
// leaves the ships without records. -fix takes it out again and sets the
// creates of the docks off (the instance holds them, seed); the ships are
// fetched again and send the docks of the stage, no error is left.
func TestFixLostSourceBecomesSeed(t *testing.T) {
	s, _ := loadNested(t)
	port := newNestedPort(true)
	port.ships["mars"] = []map[string]any{{"id": 11, "name": "Comet", "homeDockId": 3}}
	port.ships["venus"] = []map[string]any{{"id": 13, "name": "Haze", "homeDockId": 1}}
	hs := httptest.NewServer(port)
	t.Cleanup(hs.Close)
	file := strings.Replace(nestedDockTree, "pick: [mars]", "pick: [mars, venus]", 1)
	tr, built := fixUntilClean(t, s, file, hs.URL+"/api", 5)
	b, _ := tr.Bytes()
	if strings.Contains(string(b), "added by -fix") || tr.Entry("ships").Values["homeDockId"] != nil {
		t.Errorf("the read of the dock is gone again:\n%s", b)
	}
	if w := tr.Node("docks").Write[WriteCreate]; w == nil || !w.Off {
		t.Errorf("the docks are seed:\n%s", b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Errorf("no error left: %+v\n%s", errs, b)
	}
	ships := 0
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /planets/{code}/ships" {
			ships++
		}
	}
	if ships != 2 {
		t.Errorf("both ships are created: %d\n%s", ships, b)
	}
}

// runNested runs a built test on an empty nestedPort.
func runNested(t *testing.T, specPath string, built *Built) {
	t.Helper()
	text, err := built.Scenario.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tree-test_gen.yaml")
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: specPath, Handler: newNestedPort(false)},
		Scenario: path,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed {
		t.Errorf("the scenario failed on an empty instance:\n%s\n%s", out.String(), text)
	}
}

// TestClosestOfSeveralKeys: a value that matches the keys of several
// records takes the one nearest in the tree (the dock of the ship's
// planet); keys of one field of one resource give one fix, keys of
// different resources none.
func TestClosestOfSeveralKeys(t *testing.T) {
	s, _ := loadNested(t)
	tr := parse(t, nestedDockTree)
	b := &builder{s: s, t: tr, m: modelOf(s), d: &Data{}, canon: map[*Record]*Record{}}
	mars := &Record{ID: "planets1", Node: tr.Node("planets")}
	venus := &Record{ID: "planets2", Node: tr.Node("planets")}
	far := &Record{ID: "docks3", Node: tr.Node("docks"), Parent: venus}
	near := &Record{ID: "docks1", Node: tr.Node("docks"), Parent: mars}
	ship := &Record{ID: "ships1", Node: tr.Node("ships"), Parent: mars}
	list := []keyRef{{rec: far, field: "id"}, {rec: near, field: "id"}}
	if got := b.closest(list, ship); got.rec != near {
		t.Errorf("the dock of mars: %s", got.rec.ID)
	}
	if got := b.closest(list, nil); got.rec != far {
		t.Errorf("without a record the first: %s", got.rec.ID)
	}
	if !b.sameKindOf(list) {
		t.Error("two docks by id are one kind")
	}
	other := &Record{ID: "ships2", Node: tr.Node("ships"), Parent: mars}
	if b.sameKindOf(append(list, keyRef{rec: other, field: "id"})) {
		t.Error("a dock and a ship are not")
	}
	// two fields of one record hold the value (its code and its label):
	// one record, the field the name says
	both := []keyRef{{rec: near, field: "Label"}, {rec: near, field: "DockCode"}}
	if !b.oneRecord(both) || b.oneRecord(list) {
		t.Error("one record: the fields of the dock, not two docks")
	}
	if got := byName(both, []string{"dockCode"}); got.field != "DockCode" {
		t.Errorf("the field the name says: %s", got.field)
	}
	if got := byName(both, []string{"code"}); got.field != "DockCode" {
		t.Errorf("a name the field ends in: %s", got.field)
	}
}

// TestFixPlainValue: dockCount holds 2, which is also the id of a dock the
// test creates; its name says it is no reference. -fix keeps the value of
// the record (fields: {dockCount: .dockCount}) instead of a source; built
// again the body sends 2 and no error is left.
func TestFixPlainValue(t *testing.T) {
	src := strings.NewReplacer(
		"        name: {type: string}\n        homeDockId: {type: integer}\n",
		"        name: {type: string}\n        homeDockId: {type: integer}\n        dockCount: {type: integer}\n",
		"        name: {type: string, minLength: 1}\n        homeDockId: {type: integer}\n",
		"        name: {type: string, minLength: 1}\n        homeDockId: {type: integer}\n        dockCount: {type: integer}\n",
	).Replace(nestedSpec)
	if strings.Count(src, "dockCount") != 2 {
		t.Fatal("the nested spec changed")
	}
	path := filepath.Join(t.TempDir(), "nested.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	port := newNestedPort(true)
	port.ships["mars"] = port.ships["mars"][:1]
	port.ships["mars"][0]["dockCount"] = 2
	hs := httptest.NewServer(port)
	t.Cleanup(hs.Close)
	tr, built := fixUntilClean(t, s, nestedDockTree, hs.URL+"/api", 5)
	b, _ := tr.Bytes()
	if v := tr.Node("ships").Fields["dockCount"]; v != ".dockCount" {
		t.Errorf("fields keeps the value of the record: %v\n%s", v, b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Errorf("no error left: %+v\n%s", errs, b)
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /planets/{code}/ships" && text(st.Body.(map[string]any)["dockCount"]) != "2" {
			t.Errorf("%s sends dockCount 2: %v", st.ID, st.Body)
		}
	}
}

// TestLooksLikeRef: a field names a record by a key, or the resource's name
// followed by a key or a word like id; a count or a size does not.
func TestLooksLikeRef(t *testing.T) {
	s, _ := loadNested(t)
	m := modelOf(s)
	var dock *model.Resource
	for _, r := range m.Resources {
		if r.Name == "Dock" {
			dock = r
		}
	}
	if dock == nil {
		t.Fatal("no resource Dock")
	}
	for field, want := range map[string]bool{
		"homeDockId": true, "dockId": true, "dockIds": true, "id": true, "route.dockId": true, "dockNo": true,
		"dockCount": false, "size": false, "dockName": false, "rows": false,
	} {
		if got := looksLikeRef(field, dock, "id"); got != want {
			t.Errorf("%s: %v, want %v", field, got, want)
		}
	}
}

// seedShipsTree: the ships are seed (the API holds them), each reads its
// home dock below it, which the test creates anew; the ships are updated.
const seedShipsTree = `order:
  - homeDock
tree:
  planets:
    call: GET /planets
    pick: [mars]
    key: [/code]
    write: {create: none}
    each:
      ships:
        call: GET /planets/{code}/ships
        path: {code: .code}
        pick: all
        key: [/id]
        write:
          create: none
          update:
            call: PUT /planets/{code}/ships/{shipId}
            path: {code: planets.code, shipId: .id}
            onSeed: true
        each:
          homeDock:
            call: GET /planets/{code}/docks/{dockId}
            path: {code: planets.code, dockId: .homeDockId}
            key: [/id]
            write:
              create: POST /planets/{code}/docks code=planets.code
`

// TestFixSeedRecordSendsCreatedKey: a ship of the seed names its home dock,
// which the test creates anew. The read of the dock takes the key of its
// create (no error), and the update of the ship (onSeed), which has no line
// in order, gets the source in fields: -fix writes {homeDockId: homeDock.id}
// at the ships; built again the update sends the dock the test created,
// and the test runs on an instance that holds only the ships.
func TestFixSeedRecordSendsCreatedKey(t *testing.T) {
	s, path := loadNested(t)
	hs := httptest.NewServer(newNestedPort(true))
	t.Cleanup(hs.Close)
	base := hs.URL + "/api"
	_, first := buildFrom(t, s, seedShipsTree, base)
	for _, n := range first.Errors() {
		if strings.HasSuffix(n.Step, ".read") {
			t.Errorf("the read of the dock the test created takes its new key: %+v", n)
		}
	}
	if len(first.Fixes) == 0 {
		t.Fatalf("the update gets a fix: %+v", first.Errors())
	}
	tr, built := fixUntilClean(t, s, seedShipsTree, base, 4)
	b, _ := tr.Bytes()
	if v := tr.Node("ships").Fields["homeDockId"]; v != "homeDock.id" {
		t.Errorf("fields of the ships: %v\n%s", v, b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Errorf("no error left: %+v\n%s", errs, b)
	}
	updates := 0
	for _, st := range built.Scenario.Steps {
		if st.Call == "PUT /planets/{code}/ships/{shipId}" {
			updates++
			if !strings.HasPrefix(text(st.Body.(map[string]any)["homeDockId"]), "{{homeDock") {
				t.Errorf("%s sends the dock the test created: %v", st.ID, st.Body)
			}
		}
	}
	if updates == 0 {
		t.Error("a ship of the seed is updated (onSeed)")
	}
	text, err := built.Scenario.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	scenarioPath := filepath.Join(t.TempDir(), "tree-test_gen.yaml")
	if err := os.WriteFile(scenarioPath, text, 0o644); err != nil {
		t.Fatal(err)
	}
	port := newNestedPort(true)
	port.docks = map[string][]map[string]any{}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: path, Handler: port},
		Scenario: scenarioPath,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed {
		t.Errorf("the scenario failed on an instance with the ships:\n%s\n%s", out.String(), text)
	}
}

// TestFixSideRef: below planets, the docks read the planet code of the
// node beside it (allPlanets, its first planet), not of the planet they
// hang below. -fix names the node above, a default that is no problem of
// the check.
func TestFixSideRef(t *testing.T) {
	s, _ := loadNested(t)
	file := `tree:
  allPlanets:
    call: GET /planets
    pick: 1
    key: [/code]
  planets:
    call: GET /planets
    pick: [mars]
    key: [/code]
    each:
      docks:
        call: GET /planets/{code}/docks
        path: {code: allPlanets.code}
        pick: all
        key: [/id]
        write:
          create: POST /planets/{code}/docks code=allPlanets.code
`
	tr := parse(t, file)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("no problem of the check: %v", problems)
	}
	fixed, done, err := Repair(s, tr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	if len(done) != 1 || strings.Contains(string(b), "allPlanets.code") || fixed.Node("docks").Path["code"] != "planets.code" {
		t.Errorf("the planet above: %v\n%s", done, b)
	}
}

// TestFixSeedUpdateOfOtherRead: the ships are listed twice; the first node
// (shipList) stands for the records, the second (ships) has the update
// and the home dock below it. The source goes into the fields of ships,
// whose write sends the body, and the build takes the dock under ships.
func TestFixSeedUpdateOfOtherRead(t *testing.T) {
	s, _ := loadNested(t)
	hs := httptest.NewServer(newNestedPort(true))
	t.Cleanup(hs.Close)
	file := strings.Replace(seedShipsTree, "    each:\n      ships:\n",
		"    each:\n      shipList:\n        call: GET /planets/{code}/ships\n        path: {code: .code}\n        pick: all\n        key: [/id]\n      ships:\n", 1)
	if !strings.Contains(file, "shipList:") {
		t.Fatal("the tree of the test changed")
	}
	tr, built := fixUntilClean(t, s, file, hs.URL+"/api", 4)
	b, _ := tr.Bytes()
	if v := tr.Node("ships").Fields["homeDockId"]; v != "homeDock.id" {
		t.Errorf("fields of the ships, the node with the write: %v\n%s", v, b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Errorf("no error left: %+v\n%s", errs, b)
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "PUT /planets/{code}/ships/{shipId}" && !strings.HasPrefix(text(st.Body.(map[string]any)["homeDockId"]), "{{homeDock") {
			t.Errorf("%s sends the dock the test created: %v", st.ID, st.Body)
		}
	}
}
