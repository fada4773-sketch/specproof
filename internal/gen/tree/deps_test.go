package tree

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// depsSpec: docks of a planet name their pilot (pilotId), pilots have a
// list and a create but no read by id; a booking lists its docks by code
// (docks: [D-1]); a search of ships filters by the planet's code (planet).
// The planets come first in the spec, the pilots last.
const depsSpec = `openapi: 3.0.3
info: {title: Deps, version: "1"}
paths:
  /planets:
    get:
      operationId: listPlanets
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Planet'}}}}}
  /planets/{code}/bookings:
    parameters: [{name: code, in: path, required: true, schema: {type: string}}]
    get:
      operationId: listBookings
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Booking'}}}}}
    post:
      operationId: createBooking
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Booking'}}}}
      responses:
        '201': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Booking'}}}}
  /planets/{code}/docks:
    parameters: [{name: code, in: path, required: true, schema: {type: string}}]
    get:
      operationId: listDocks
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Dock'}}}}}
    post:
      operationId: createDock
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Dock'}}}}
      responses:
        '201': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Dock'}}}}
  /docks/{dockCode}:
    parameters: [{name: dockCode, in: path, required: true, schema: {type: string}}]
    get:
      operationId: getDock
      responses:
        '200': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Dock'}}}}
  /ship-search:
    post:
      operationId: searchShips
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/ShipFilter'}}}}
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Ship'}}}}}
  /pilots:
    get:
      operationId: listPilots
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: array, items: {$ref: '#/components/schemas/Pilot'}}}}}
    post:
      operationId: createPilot
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Pilot'}}}}
      responses:
        '201': {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Pilot'}}}}
components:
  schemas:
    Planet:
      type: object
      properties: {code: {type: string}, name: {type: string}}
    Pilot:
      type: object
      properties: {id: {type: integer}, name: {type: string}}
    Dock:
      type: object
      properties: {dockCode: {type: string}, name: {type: string}, pilotId: {type: integer}}
    Booking:
      type: object
      properties: {id: {type: integer}, docks: {type: array, items: {type: string}}}
    Ship:
      type: object
      properties: {id: {type: integer}, name: {type: string}}
    ShipFilter:
      type: object
      properties: {planet: {type: string}, name: {type: string}, pageSize: {type: integer}}
`

// depsStage serves the records of depsSpec; it notes the bodies of the
// ship searches.
func depsStage(t *testing.T) (string, func() []string) {
	t.Helper()
	data := map[string]any{
		"GET /planets":               []any{map[string]any{"code": "mars", "name": "Mars"}},
		"GET /pilots":                []any{map[string]any{"id": 1, "name": "Ada"}, map[string]any{"id": 2, "name": "Lin"}, map[string]any{"id": 3, "name": "Kai"}},
		"GET /planets/mars/docks":    []any{map[string]any{"dockCode": "D-1", "name": "North", "pilotId": 2}},
		"GET /docks/D-1":             map[string]any{"dockCode": "D-1", "name": "North", "pilotId": 2},
		"GET /planets/mars/bookings": []any{map[string]any{"id": 7, "docks": []any{"D-1"}}},
		"POST /ship-search":          []any{map[string]any{"id": 5, "name": "Comet"}},
	}
	var mu sync.Mutex
	var searches []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			searches = append(searches, strings.TrimSpace(string(b)))
			mu.Unlock()
		}
		v, ok := data[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(searches)
	}
}

// TestReferencedRecordsAndOrder: a record the scenario creates brings the
// records it refers to into the fetch (the pilot of a dock from the list of
// pilots by a where on its id; the docks a booking lists, one read per
// code), the nodes come in the order of what they need (pilots before
// planets, docks before bookings), a search takes the filter its body
// names from the node above (planet: .code), and the scenario creates the
// records after those they refer to.
func TestReferencedRecordsAndOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deps.yaml")
	if err := os.WriteFile(path, []byte(depsSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("check: %v\n%s", problems, out)
	}
	var roots []string
	for _, n := range tr.Roots {
		roots = append(roots, n.Name)
	}
	if i, j := slices.Index(roots, "pilots"), slices.Index(roots, "planets"); i < 0 || j < 0 || i > j {
		t.Errorf("the pilots docks name come first: %v\n%s", roots, out)
	}
	planets := tr.Node("planets")
	var below []string
	for _, n := range planets.Each {
		below = append(below, n.Name)
	}
	if i, j := slices.Index(below, "docks"), slices.Index(below, "bookings"); i < 0 || j < 0 || i > j {
		t.Errorf("the docks bookings list come first: %v\n%s", below, out)
	}
	var pilot, dock, search *Node
	tr.Walk(func(n *Node) {
		switch {
		case n.Parent != nil && n.Parent.Name == "docks" && n.Call == "GET /pilots":
			pilot = n
		case n.Parent != nil && n.Parent.Name == "bookings" && n.Call == "GET /docks/{dockCode}":
			dock = n
		case n.Call == "POST /ship-search":
			search = n
		}
	})
	if pilot == nil || len(pilot.Where) != 1 || pilot.Where[0].Field != "id" || pilot.Where[0].Test != "docks.pilotId" || len(pilot.Require) != 0 {
		t.Errorf("the pilot of a dock by a where on its id:\n%s", out)
	}
	if dock == nil || dock.Path["dockCode"] != ".docks" || !dock.Pick.All {
		t.Errorf("the docks of a booking, one read per code:\n%s", out)
	}
	if search == nil || search.Parent != planets || !sameValues(search.Body, map[string]any{"planet": ".code"}) {
		t.Errorf("the search below the planets, filtered by their code:\n%s", out)
	}
	if w := tr.Node("pilots").Write[WriteCreate]; w == nil {
		t.Errorf("the create of pilots stays at their list:\n%s", out)
	}

	base, searches := depsStage(t)
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Of(pilot); len(got) != 1 || text(got[0].Value.(map[string]any)["id"]) != "2" {
		t.Errorf("the pilot the dock names: %v %v", ids(got), d.Notes)
	}
	if got := d.Of(dock); len(got) != 1 || !strings.HasSuffix(got[0].Request, "/docks/D-1") {
		t.Errorf("the dock the booking lists: %v %v", ids(got), d.Notes)
	}
	if got := searches(); !slices.Contains(got, `{"planet":"mars"}`) {
		t.Errorf("search bodies: %v", got)
	}

	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, st := range built.Scenario.Steps {
		if strings.HasSuffix(st.ID, ".create") {
			pos[st.Call] = i
		}
		if st.Call == "POST /planets/{code}/bookings" {
			if docks, _ := st.Body.(map[string]any)["docks"].([]any); len(docks) != 1 || !strings.HasPrefix(text(docks[0]), "{{") {
				t.Errorf("the booking names the dock the scenario created: %+v", st.Body)
			}
		}
		if st.Call == "POST /planets/{code}/docks" {
			if v := text(st.Body.(map[string]any)["pilotId"]); !strings.HasPrefix(v, "{{") {
				t.Errorf("the dock names the pilot the scenario created: %+v", st.Body)
			}
		}
	}
	p, k, bk := pos["POST /pilots"], pos["POST /planets/{code}/docks"], pos["POST /planets/{code}/bookings"]
	if len(pos) != 3 || p > k || k > bk {
		enc, _ := built.Scenario.Encode("")
		t.Errorf("creates: pilot, dock, booking: %v\n%s", pos, enc)
	}
}

func sameValues(v any, b map[string]any) bool {
	a, _ := v.(map[string]any)
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}
