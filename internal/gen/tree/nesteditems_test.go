package tree

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// overviewSpec: the overview of the docks answers per dock its ships; the
// ships themselves are created and updated at /ships.
const overviewSpec = `
openapi: 3.0.3
info: {title: overview, version: "1"}
paths:
  /docks/overview:
    get:
      operationId: listDockOverview
      responses: {"200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/DockOverview"}}}}}}
  /docks/overview/{dockName}:
    parameters: [{name: dockName, in: path, required: true, schema: {type: string}}]
    get:
      operationId: getDockOverview
      responses: {"200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/DockOverview"}}}}}}
  /ships:
    post:
      operationId: createShip
      requestBody: {required: true, content: {application/json: {schema: {$ref: "#/components/schemas/ShipWrite"}}}}
      responses: {"201": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Ship"}}}}}
  /ships/{shipId}:
    parameters: [{name: shipId, in: path, required: true, schema: {type: integer}}]
    put:
      operationId: updateShip
      requestBody: {required: true, content: {application/json: {schema: {$ref: "#/components/schemas/ShipWrite"}}}}
      responses: {"200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Ship"}}}}}
components:
  schemas:
    DockOverview:
      type: object
      properties:
        dockName: {type: string}
        ships: {type: array, items: {$ref: "#/components/schemas/Ship"}}
    ShipWrite:
      type: object
      required: [name, dockName, class]
      properties: {name: {type: string}, dockName: {type: string}, class: {type: string}}
    Ship:
      type: object
      required: [id, name, dockName, class]
      properties: {id: {type: integer}, name: {type: string}, dockName: {type: string}, class: {type: string}}
`

// overviewTree creates and updates the freighters of dock north, read
// from the ships of its overview.
const overviewTree = `
order:
  - ships
tree:
  overview:
    call: GET /docks/overview
    key: [/dockName]
    pick: [north]
    each:
      ships:
        call: GET /docks/overview/{dockName}
        path: {dockName: .dockName}
        items: /0/ships
        key: [/id]
        pick: all
        where: {class: freighter}
        write:
          create:
            call: POST /ships
            body: {name: .name, dockName: .dockName, class: .class}
          update:
            call: PUT /ships/{shipId}
            path: {shipId: .id}
            body: {name: .name, dockName: .dockName, class: .class}
`

// overviewServer serves ships grouped by dock; ids start at first, and
// a server with first > 1 holds the stage: two freighters and a shuttle
// at north, a freighter at south.
func overviewServer(first int) http.Handler {
	type ship struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		DockName string `json:"dockName"`
		Class    string `json:"class"`
	}
	var mu sync.Mutex
	var all []*ship
	next := first
	if first > 1 {
		for _, s := range []ship{{Name: "Vega", DockName: "north", Class: "freighter"}, {Name: "Rigel", DockName: "north", Class: "shuttle"},
			{Name: "Deneb", DockName: "north", Class: "freighter"}, {Name: "Altair", DockName: "south", Class: "freighter"}} {
			s.ID = next
			next++
			all = append(all, &s)
		}
	}
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	overview := func(dock string) []map[string]any {
		byDock := map[string][]*ship{}
		for _, s := range all {
			if dock == "" || s.DockName == dock {
				byDock[s.DockName] = append(byDock[s.DockName], s)
			}
		}
		var names []string
		for n := range byDock {
			names = append(names, n)
		}
		sort.Strings(names)
		out := []map[string]any{}
		for _, n := range names {
			out = append(out, map[string]any{"dockName": n, "ships": byDock[n]})
		}
		return out
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /docks/overview", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		write(w, 200, overview(""))
	})
	mux.HandleFunc("GET /docks/overview/{dockName}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		write(w, 200, overview(r.PathValue("dockName")))
	})
	mux.HandleFunc("POST /ships", func(w http.ResponseWriter, r *http.Request) {
		var in ship
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || in.DockName == "" || in.Class == "" {
			write(w, 400, map[string]any{"message": "name, dockName and class"})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		in.ID = next
		next++
		all = append(all, &in)
		write(w, 201, in)
	})
	mux.HandleFunc("PUT /ships/{shipId}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("shipId"))
		var in ship
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || in.DockName == "" || in.Class == "" {
			write(w, 400, map[string]any{"message": "name, dockName and class"})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, s := range all {
			if s.ID == id {
				s.Name, s.DockName, s.Class = in.Name, in.DockName, in.Class
				write(w, 200, s)
				return
			}
		}
		write(w, 404, map[string]any{"message": "no ship " + r.PathValue("shipId")})
	})
	return mux
}

// TestItemsInElement: the records of a node come from a list inside an
// element of the answer (items: /0/ships). where picks among them, the
// list step checks the created records in that list, and the node above
// (the overview, the same request's schema) takes no create from it.
func TestItemsInElement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overview.yaml")
	if err := os.WriteFile(path, []byte(overviewSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	tr := parse(t, overviewTree)
	srv := httptest.NewServer(overviewServer(100))
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: srv.URL})
	srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range d.Records {
		if r.Node.Name == "ships" {
			names = append(names, text(r.Value.(map[string]any)["name"]))
		}
	}
	if strings.Join(names, ",") != "Vega,Deneb" {
		t.Errorf("where picks the freighters of north: %v", names)
	}
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := built.Scenario.Encode(" test")
	var list bool
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /ships" && !strings.HasPrefix(st.ID, "ships") {
			t.Errorf("%s: the overview takes no create of the ships:\n%s", st.ID, text)
		}
		if st.Call == "GET /docks/overview/{dockName}" {
			list = true
			if st.Contains == nil || st.Contains.List != "/0/ships" || st.Contains.Key != "/id" {
				t.Errorf("%s: contains in /0/ships by /id: %+v\n%s", st.ID, st.Contains, text)
			}
		}
	}
	if !list {
		t.Fatalf("no list step of the ships:\n%s", text)
	}
	if !strings.Contains(string(text), "list: /0/ships") {
		t.Errorf("the scenario names the list:\n%s", text)
	}
	file := filepath.Join(t.TempDir(), "tree-test_gen.yaml")
	if err := os.WriteFile(file, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: path, Handler: overviewServer(1)},
		Scenario: file,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil || res.Failed {
		t.Errorf("the run in an empty instance: %v\n%s\n%s", err, out.String(), text)
	}
}
