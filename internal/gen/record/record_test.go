package record

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// starport is the local instance of testdata/gen/record.yaml: data from
// earlier work, and a sequence per table.
type starport struct {
	mu       sync.Mutex
	planets  []map[string]any
	docks    []map[string]any
	configs  map[string]any // dockCode → config
	ships    []map[string]any
	nextShip int
	clock    int
	sent     []string
	gone     map[string]bool // GET paths that answer 404
}

func newStarport() *starport {
	n := func(i int) json.Number { return json.Number(strconv.Itoa(i)) }
	return &starport{
		planets: []map[string]any{
			{"id": n(7), "planetCode": "P1", "name": "Mars"},
			{"id": n(8), "planetCode": "P2", "name": "Venus"},
		},
		docks: []map[string]any{
			{"id": n(30), "dockCode": "D1", "planetId": n(7), "name": "North"},
			{"id": n(31), "dockCode": "D2", "planetId": n(7), "name": "South"},
			{"id": n(32), "dockCode": "D3", "planetId": n(8), "name": "East"},
		},
		configs: map[string]any{
			"D1": map[string]any{"settings": map[string]any{"mode": ""}},
			"D2": map[string]any{"settings": map[string]any{"mode": "auto"}},
		},
		ships: []map[string]any{
			{"id": n(100), "shipCode": "S2", "dockId": n(30), "name": "Comet"},
			{"id": n(101), "shipCode": "S1", "dockId": n(31), "name": "Falcon"},
			{"id": n(103), "shipCode": "S3", "dockId": n(31), "name": "Hawk"},
		},
		nextShip: 104,
	}
}

func (sp *starport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if r.Method != http.MethodGet {
		sp.sent = append(sp.sent, r.Method+" "+r.URL.Path)
	} else if sp.gone[r.URL.Path] {
		http.NotFound(w, r)
		return
	}
	seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	planet := func(code string) map[string]any {
		for _, p := range sp.planets {
			if p["planetCode"] == code {
				return p
			}
		}
		return nil
	}
	ship := func(o map[string]any) map[string]any {
		sp.clock++
		c := map[string]any{"updatedAt": fmt.Sprintf("2026-01-01T00:00:%02dZ", sp.clock%60)}
		for k, v := range o {
			c[k] = v
		}
		return c
	}
	switch {
	case len(seg) == 1 && seg[0] == "Planet":
		reply(200, sp.planets)
	case len(seg) == 2 && seg[0] == "Planet":
		if p := planet(seg[1]); p != nil {
			reply(200, p)
			return
		}
		reply(404, nil)
	case len(seg) == 3 && seg[2] == "Dock":
		p := planet(seg[1])
		out := []any{}
		for _, d := range sp.docks {
			if p != nil && d["planetId"] == p["id"] {
				out = append(out, d)
			}
		}
		reply(200, out)
	case len(seg) == 5 && seg[4] == "Config":
		if r.Method == http.MethodPut {
			sp.configs[seg[3]] = body
		}
		if c, ok := sp.configs[seg[3]]; ok {
			reply(200, c)
			return
		}
		reply(404, nil)
	case len(seg) == 2 && seg[1] == "search":
		out := []any{}
		for _, s := range sp.ships {
			out = append(out, ship(s))
		}
		reply(200, out)
	case len(seg) == 5 && seg[4] == "Ship" && r.Method == http.MethodGet:
		out := []any{}
		for _, d := range sp.docks {
			if d["dockCode"] != seg[3] {
				continue
			}
			for _, s := range sp.ships {
				if fmt.Sprint(s["dockId"]) == fmt.Sprint(d["id"]) {
					out = append(out, ship(s))
				}
			}
		}
		reply(200, out)
	case (len(seg) == 3 || len(seg) == 5) && seg[len(seg)-1] == "Ship" && r.Method == http.MethodPost:
		for _, s := range sp.ships {
			if s["shipCode"] == body["shipCode"] {
				reply(409, map[string]any{"message": "exists"})
				return
			}
		}
		s := map[string]any{"id": json.Number(strconv.Itoa(sp.nextShip)), "shipCode": body["shipCode"], "dockId": json.Number(fmt.Sprint(body["dockId"])), "name": body["name"]}
		sp.nextShip++
		sp.ships = append(sp.ships, s)
		reply(201, ship(s))
	case len(seg) == 3 && seg[2] == "Ship":
		out := []any{}
		for _, s := range sp.ships {
			out = append(out, ship(s))
		}
		reply(200, out)
	case len(seg) == 3 && seg[0] == "Ship":
		for i, s := range sp.ships {
			if fmt.Sprint(s["id"]) != seg[2] {
				continue
			}
			switch r.Method {
			case http.MethodDelete:
				sp.ships = append(sp.ships[:i], sp.ships[i+1:]...)
				reply(204, nil)
			case http.MethodPut:
				for k, v := range body {
					s[k] = json.Number(fmt.Sprint(v))
					if k == "name" {
						s[k] = v
					}
				}
				reply(200, ship(s))
			default:
				reply(200, ship(s))
			}
			return
		}
		reply(404, nil)
	default:
		reply(404, nil)
	}
}

const recordConfig = `{
  "params": {"planetCode": "P1"},
  "seed": ["Planet", "Dock"],
  "select": {"Dock": {"details": {"/Planet/{planetCode}/Dock/{dockCode}/Config": {"mandatory": ["settings.mode"]}}}},
  "$apitest": {"DeleteLast": true}
}`

func runRecord(t *testing.T, sp *starport, specPath string, old *Recorded, writes bool) (*Result, *yamldoc.Doc, *Client) {
	t.Helper()
	return runConfig(t, sp, specPath, recordConfig, old, writes)
}

func runConfig(t *testing.T, sp *starport, specPath, config string, old *Recorded, writes bool) (*Result, *yamldoc.Doc, *Client) {
	t.Helper()
	srv := httptest.NewServer(sp)
	t.Cleanup(srv.Close)
	s, err := spec.Load(context.Background(), specPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load(specPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Recorded = old
	prev, err := yamldoc.Load(specPath) // the spec is the output too
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	c := &Client{Opt: discover.Options{BaseURL: srv.URL}, Log: func(e Entry) { log = append(log, e.String()) }}
	res, err := Run(context.Background(), Input{Spec: s, Doc: doc, Config: cfg, Client: c, Writes: writes, Prev: prev})
	if err != nil {
		t.Fatal(err)
	}
	if testing.Verbose() {
		t.Logf("log:\n%s", strings.Join(log, "\n"))
	}
	return res, doc, c
}

// exampleAt decodes the example below a path of keys of the document.
func exampleAt(t *testing.T, doc *yamldoc.Doc, keys ...string) any {
	t.Helper()
	n := yamldoc.Path(doc.Root, append(keys, "example")...)
	if n == nil {
		t.Fatalf("no example at %v", keys)
	}
	v, err := yamldoc.Decode(n)
	if err != nil {
		t.Fatal(err)
	}
	return spec.Normalize(v)
}

func equal(t *testing.T, what string, got any, want string) {
	t.Helper()
	var w any
	if err := spec.DecodeJSON([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !compare.Equal(spec.Normalize(got), w) {
		b, _ := json.Marshal(got)
		t.Errorf("%s:\n got %s\nwant %s", what, b, want)
	}
}

func notes(res *Result) string {
	var b strings.Builder
	for _, n := range append(res.Notes, res.Problems...) {
		b.WriteString(n.String() + "\n")
	}
	return b.String()
}

// The examples show the empty environment: seed and created records with
// the ids its sequences assign, lists with only those records.
func TestRecord(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sp := newStarport()
	res, doc, c := runRecord(t, sp, specPath, nil, true)
	all := notes(res)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", all)
	}
	resp := func(path, method, code string) []string {
		return []string{"paths", path, method, "responses", code, "content", "application/json"}
	}
	// the seed: id 1 each; D1 has no mode in its config, so D2 is the dock
	equal(t, "GetPlanets", exampleAt(t, doc, resp("/Planet", "get", "200")...), `[{"id":1,"planetCode":"P1","name":"Mars"}]`)
	equal(t, "GetDocks", exampleAt(t, doc, resp("/Planet/{planetCode}/Dock", "get", "200")...), `[{"id":1,"dockCode":"D2","planetId":1,"name":"South"}]`)
	// the ships of the dock, created by the POSTs of the run in their order:
	// CreateDockShip first (id 1, its own list), then CreateShip (id 2)
	equal(t, "CreateDockShip body", exampleAt(t, doc, "paths", "/Planet/{planetCode}/Dock/{dockCode}/Ship", "post", "requestBody", "content", "application/json"),
		`{"shipCode":"S3","dockId":1,"name":"Hawk"}`)
	equal(t, "CreateShip body", exampleAt(t, doc, "paths", "/Planet/{planetCode}/Ship", "post", "requestBody", "content", "application/json"),
		`{"shipCode":"S1","dockId":1,"name":"Falcon"}`)
	created := exampleAt(t, doc, resp("/Planet/{planetCode}/Ship", "post", "201")...).(map[string]any)
	delete(created, "updatedAt")
	equal(t, "CreateShip answer", created, `{"id":2,"shipCode":"S1","dockId":1,"name":"Falcon"}`)
	// only the created ships, in the order the environment created them
	for _, op := range [][]string{resp("/Planet/{planetCode}/Ship", "get", "200"), resp("/Ship/search", "post", "200")} {
		var ids []string
		for _, s := range exampleAt(t, doc, op...).([]any) {
			ids = append(ids, fmt.Sprint(s.(map[string]any)["id"], s.(map[string]any)["shipCode"]))
		}
		if strings.Join(ids, ",") != "1S3,2S1" {
			t.Errorf("%s lists %v", op[1], ids)
		}
	}
	equal(t, "{id}", exampleAt(t, doc, "paths", "/Ship/id/{id}", "parameters", "0"), `2`)
	equal(t, "{planetCode}", exampleAt(t, doc, "components", "parameters", "PlanetCode"), `"P1"`)
	equal(t, "{dockCode}", exampleAt(t, doc, "paths", "/Planet/{planetCode}/Dock/{dockCode}/Config", "get", "parameters", "1"), `"D2"`)
	// the instance holds the ships again, with their next ids; nothing else
	// changed. CreateDockShip has no DELETE below its path: DeleteShip
	// removes its ship by id
	if strings.Join(sp.sent, ", ") != "PUT /Planet/P1/Dock/D2/Config, POST /Ship/search, PUT /Ship/id/101, DELETE /Ship/id/101, POST /Planet/P1/Ship, DELETE /Ship/id/103, POST /Planet/P1/Dock/D2/Ship" {
		t.Errorf("writes: %v", sp.sent)
	}
	if len(sp.ships) != 3 {
		t.Errorf("ships after the run: %v", sp.ships)
	}
	for i, want := range []string{"S1 104 Falcon", "S3 105 Hawk"} {
		if s := sp.ships[i+1]; fmt.Sprint(s["shipCode"], " ", s["id"], " ", s["name"]) != want {
			t.Errorf("ship after the run: %v, want %s", s, want)
		}
	}
	for _, want := range []string{
		"VOLATILE updatedAt",
		"NOT_IN_CONTAINER GetDockConfig: GET /Planet/P1/Dock/D2/Config runs before UpdateDockConfig writes it",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
	equal(t, "seed", res.Recorded.Seed, `{"Planet":{"id":1,"planetCode":"P1","name":"Mars"},"Dock":{"id":1,"dockCode":"D2","planetId":1,"name":"South"}}`)
	if c.Count[http.MethodPost] != 3 {
		t.Errorf("requests: %v", c.Count)
	}

	// the second run: the schemas did not change, so nothing is written and
	// nothing but GET is sent
	if err := doc.Save(specPath); err != nil {
		t.Fatal(err)
	}
	sp.sent = nil
	res2, _, c2 := runRecord(t, sp, specPath, res.Recorded, true)
	if res2.Changed || len(sp.sent) > 0 || res2.Stats.Unchanged != res.Stats.Ops {
		t.Errorf("second run: changed %v, writes %v, %d of %d unchanged\n%s", res2.Changed, sp.sent, res2.Stats.Unchanged, res.Stats.Ops, notes(res2))
	}
	if c2.Count[http.MethodPost] != 0 {
		t.Errorf("requests: %v", c2.Count)
	}

	// the last run created the ship as id 7: the unchanged examples get the
	// id the environment assigns now
	param := yamldoc.Path(doc.Root, "paths", "/Ship/id/{id}", "parameters", "0")
	if err := yamldoc.Set(param, "example", 7); err != nil {
		t.Fatal(err)
	}
	if err := doc.Save(specPath); err != nil {
		t.Fatal(err)
	}
	old := *res.Recorded
	old.Created = map[string]int{"CreateShip": 7}
	res3, doc3, _ := runRecord(t, sp, specPath, &old, true)
	equal(t, "{id} moved", exampleAt(t, doc3, "paths", "/Ship/id/{id}", "parameters", "0"), `2`)
	if res3.Stats.Remapped == 0 || !strings.Contains(notes(res3), "IDS_SHIFTED") {
		t.Errorf("third run:\n%s", notes(res3))
	}
}

// Without writes the examples of PUT, POST and DELETE come from the data the
// GETs read, and the instance stays untouched.
func TestRecordReadOnly(t *testing.T) {
	sp := newStarport()
	res, doc, c := runRecord(t, sp, "../../../testdata/gen/record.yaml", nil, false)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res))
	}
	if len(sp.sent) > 0 || c.Count[http.MethodGet] == 0 {
		t.Errorf("writes %v, requests %v", sp.sent, c.Count)
	}
	created := exampleAt(t, doc, "paths", "/Planet/{planetCode}/Ship", "post", "responses", "201", "content", "application/json").(map[string]any)
	delete(created, "updatedAt")
	equal(t, "CreateShip answer", created, `{"id":2,"shipCode":"S1","dockId":1,"name":"Falcon"}`)
}

// A dock without a passing config makes the run take the next one; none
// stops it with the reasons.
func TestRecordSelectNone(t *testing.T) {
	sp := newStarport()
	sp.configs["D2"] = map[string]any{"settings": map[string]any{}}
	res, _, _ := runRecord(t, sp, "../../../testdata/gen/record.yaml", nil, false)
	all := notes(res)
	for _, want := range []string{"SELECT_NONE GetDocks: no dock of #1 GET /Planet/P1/Dock passes", "settings.mode empty", "SEED_MISSING Dock"} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`{"params": {"planetCode": "P1", "GetShip.id": {"field": "Id"}}, "seed": ["Planet"], "$comment": "x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := c.param("GetShip", "ID"); !ok || p.Field != "Id" {
		t.Errorf("GetShip.id: %+v", p)
	}
	if p, ok := c.param("GetDock", "planetcode"); !ok || p.Value != "P1" {
		t.Errorf("planetCode: %+v", p)
	}
	for in, want := range map[string]string{
		`{"$snapshot": {}}`: "belongs to the format of",
		`{"pick": 1}`:       `unknown key "pick"`,
		`{"select": {"Dock": {"details": {"x": {}}}}}`:     "is no path",
		`{"params": {"a.b": {"value": 1}}}`:                "group parameters under the operationId alone",
		`{"$apitest": {"MethodOrder": ["DELETE", "GET"]}}`: "DELETE must be the last",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestTableName(t *testing.T) {
	for in, want := range map[string]string{"ShipRead": "ship", "ShipCreateRequest": "ship", "DockDetailRead": "dockdetail", "Planet": "planet", "Dto": "dto"} {
		if got := tableName(in); got != want {
			t.Errorf("tableName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"dockId": "dock", "planet_id": "planet", "id": "", "paid": ""} {
		if got := refTable(in); got != want {
			t.Errorf("refTable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPasses(t *testing.T) {
	v := map[string]any{"Settings": map[string]any{"Mode": "auto", "Tags": []any{}}, "Lines": []any{map[string]any{"qty": json.Number("0")}, map[string]any{"qty": json.Number("7")}}}
	for _, c := range []struct {
		equal     map[string]any
		mandatory []string
		want      string
	}{
		{nil, []string{"settings.mode"}, ""},
		{nil, []string{"settings.tags"}, "settings.tags empty"},
		{nil, []string{"settings.missing"}, "settings.missing empty"},
		{map[string]any{"lines.qty": "7"}, nil, ""},
		{map[string]any{"settings.mode": "manual"}, nil, `settings.mode is not "manual"`},
	} {
		if got := passes(v, c.equal, c.mandatory); got != c.want {
			t.Errorf("passes(%v, %v) = %q, want %q", c.equal, c.mandatory, got, c.want)
		}
	}
}

// An instance that does not answer stops the run before anything is read.
func TestRecordInstanceDown(t *testing.T) {
	srv := httptest.NewServer(newStarport())
	srv.Close()
	s, err := spec.Load(context.Background(), "../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := Parse([]byte(recordConfig))
	_, err = Run(context.Background(), Input{Spec: s, Doc: doc, Config: cfg, Client: &Client{Opt: discover.Options{BaseURL: srv.URL}}})
	if err == nil || !strings.Contains(err.Error(), "the instance does not answer") {
		t.Errorf("err = %v", err)
	}
}

// "$apitest".Tags runs only these tags, in this order: the run reads,
// writes and gets examples for nothing else.
func TestRecordTags(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"params": {"planetCode": "P1"}, "seed": ["Planet", "Dock"], "$apitest": {"Tags": ["Dock", "Planet"]}}`
	sp := newStarport()
	res, doc, c := runConfig(t, sp, specPath, config, nil, true)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res))
	}
	if res.Stats.Ops != 5 || c.Count[http.MethodPost] != 0 || strings.Join(sp.sent, ", ") != "PUT /Planet/P1/Dock/D1/Config" {
		t.Errorf("%d operations, requests %v, writes %v", res.Stats.Ops, c.Count, sp.sent)
	}
	if yamldoc.Path(doc.Root, "paths", "/Planet/{planetCode}/Ship", "get", "responses", "200", "content", "application/json", "example") != nil {
		t.Error("the Ship tag got examples")
	}
	exampleAt(t, doc, "paths", "/Planet/{planetCode}/Dock", "get", "responses", "200", "content", "application/json")

	s, err := spec.Load(context.Background(), specPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := Parse([]byte(`{"$apitest": {"Tags": ["Dock", "Moon"]}}`))
	if _, err := Run(context.Background(), Input{Spec: s, Doc: doc, Config: cfg, Client: c}); err == nil || !strings.Contains(err.Error(), "Moon") {
		t.Errorf("unknown tag: %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing file: %v", err)
	}
	if _, err := Parse([]byte("{\"seed\": [\"Planet\"]}\n\napitest-gen record -spec x\n")); err == nil || !strings.Contains(err.Error(), "text after the JSON object (line 1)") {
		t.Errorf("text after the JSON: %v", err)
	}
}

// A DTO named after a resource of a path, followed by Read or Part, is a
// view of that resource.
func TestNamer(t *testing.T) {
	n := namer{"planet": true, "dock": true, "dockgroup": true}
	for in, want := range map[string]string{
		"DockReadConfiguration": "dock", "DockPartRead": "dock", "DockGroupReadDetail": "dockgroup",
		"DockDetailRead": "dockdetail", "DockReadiness": "dockreadiness", "PlanetRead": "planet", "MoonRead": "moon",
	} {
		if got := n.table(in); got != want {
			t.Errorf("table(%q) = %q, want %q", in, got, want)
		}
	}
	for path, want := range map[string]bool{"/Planet/{code}/Dock": true, "/Dock/Planet/{code}": true, "/Dock/{id}/clone": false, "/docks/create": false} {
		s := &spec.Operation{Path: path}
		if got := recreates(s, "dock"); got != want {
			t.Errorf("recreates(%s) = %v", path, got)
		}
	}
}

// Elements of a list must fit the selected records; a single object read by
// its path is not compared with them.
func TestCheckRelations(t *testing.T) {
	rd := &reader{cfg: &Config{}, k: newKnown()}
	rd.k.add("ship", map[string]any{"id": json.Number("1"), "shipCode": "S1"})
	o := map[string]any{"shipId": json.Number("2"), "mode": "auto"}
	if r := rd.check("dockconfig", o, rd.k, rd.k.tables, 8); !strings.Contains(r, "shipId is 2, the selected ship has 1") {
		t.Errorf("list element: %q", r)
	}
	if r := rd.check("dockconfig", o, rd.k, nil, 8); r != "" {
		t.Errorf("single object: %q", r)
	}
}

// A list the instance returns in another order is no change of its fields.
func TestSortLists(t *testing.T) {
	a := []any{map[string]any{"id": json.Number("2"), "name": "b"}, map[string]any{"id": json.Number("1"), "name": "a"}}
	b := []any{a[1], a[0]}
	if d := diffFields(sortLists(a), sortLists(b), ""); len(d) > 0 {
		t.Errorf("differences: %v", d)
	}
	c := []any{map[string]any{"name": "y"}, map[string]any{"name": "x"}}
	if d := diffFields(sortLists(c), sortLists([]any{c[1], c[0]}), ""); len(d) > 0 {
		t.Errorf("without ids: %v", d)
	}
}

// "params" can group the parameters of one operation; the config is checked
// against the spec before anything is read.
func TestConfigCheck(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n := newNamer(s)
	c, err := Parse([]byte(`{"params": {"$comment": "x", "planetCode": "P1", "GetShip": {"id": 7, "$comment": "y"}, "UpdateShip.id": {"field": "Id"}},
	  "select": {"$comment": "w", "Ship": {"from": "GetDockShips", "$comment": "z"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := c.param("GetShip", "id"); !ok || p.Value != json.Number("7") {
		t.Errorf("GetShip.id: %+v", p)
	}
	if err := c.check(s, n); err != nil {
		t.Errorf("check: %v", err)
	}
	c, err = Parse([]byte(`{"params": {"shipCode": 1, "GetShipz": {"id": 1}, "GetShip": {"code": 1}}, "select": {"Ship": {"from": "GetShip"}, "Dock": {"from": "Nope"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	err = c.check(s, n)
	for _, want := range []string{
		`"params".shipCode: no operation has a parameter "shipCode"`,
		`"params".GetShipz.id: no operation "GetShipz"`,
		`"params".GetShip.code: GetShip has no parameter "code" (it has: id)`,
		`"select".Ship.from: GetShip does not return a list of Ship; name the GET that lists them (e.g. GetDockShips, GetShips)`,
		`"select".Dock.from: no GET "Nope"`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("check lacks %q:\n%v", want, err)
		}
	}
}

// "from" takes the record from that list, and the list is read even if
// its tag does not run.
func TestRecordFrom(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(`{"params": {"planetCode": "P1"}, "select": {"Dock": {"equal": {"dockCode": "D2"}}, "Ship": {"from": "GetDockShips"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	r := &reader{n: newNamer(s), ctx: context.Background(), cfg: cfg, s: s, c: &Client{Opt: discover.Options{BaseURL: srv.URL}}, res: &Result{},
		ops: map[string]bool{"GetPlanets": true, "GetDocks": true, "GetShips": true}, cache: map[string]response{}, recs: map[string]*rec{}, all: map[string][]*rec{},
		creates: map[string]string{}, forPath: map[string]*rec{}, k: newKnown(), gets: map[string]*fetched{}, failed: map[string]string{}}
	r.read()
	if ship := r.recs["ship"]; ship == nil || !strings.HasPrefix(ship.from, "GetDockShips ") || ship.data["shipCode"] != "S1" {
		t.Errorf("ship: %+v", ship)
	}
}

// The DELETE of a record is its own; else the one "select" names in
// "delete", else any DELETE of its table its fields fill.
func TestRemover(t *testing.T) {
	r := &rec{table: "ship"}
	op := func(id string) *cases.Case {
		return &cases.Case{Op: &spec.Operation{ID: id}, Example: cases.DefaultExample}
	}
	byID := &wop{c: op("DeleteShip"), kind: kindDelete, table: "ship", rec: r, url: "/Ship/id/1", resolved: true}
	byCode := &wop{c: op("DeleteShipByCode"), kind: kindDelete, table: "ship", rec: r, url: "/Ship/code/S1", resolved: true}
	dock := &wop{c: op("DeleteDock"), kind: kindDelete, table: "dock", rec: &rec{table: "dock"}, url: "/Dock/1", resolved: true}
	w := &writes{rd: &reader{cfg: &Config{}}, ops: []*wop{dock, byID, byCode}}
	if d, u, _ := w.remover("ship", r); d != byID || u != "/Ship/id/1" {
		t.Errorf("first own DELETE: %v %q", d, u)
	}
	w.ops = []*wop{dock}
	if d, _, _ := w.remover("ship", r); d != nil {
		t.Errorf("a DELETE of another table: %v", d)
	}
}

// An update below the path of a POST with a list of its own writes the
// record of that list, not the first one of the table.
func TestRecFor(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	first, own := &rec{table: "ship"}, &rec{table: "ship"}
	rd := &reader{s: s, recs: map[string]*rec{"ship": first}, forPath: map[string]*rec{},
		creates: map[string]string{"/Planet/{planetCode}/Ship": "ship", "/Planet/{planetCode}/Dock/{dockCode}/Ship": "ship"}}
	w := &writes{rd: rd}
	below := &spec.Operation{Path: "/Planet/{planetCode}/Dock/{dockCode}/Ship/{shipCode}"}
	if r, why := w.recFor(below, "ship"); r != nil || !strings.Contains(why, "the list GET /Planet/{planetCode}/Dock/{dockCode}/Ship holds no ship of its own") {
		t.Errorf("without a record of its own: %v %q", r, why)
	}
	rd.forPath["/Planet/{planetCode}/Dock/{dockCode}/Ship"] = own
	if r, _ := w.recFor(below, "ship"); r != own {
		t.Error("the update does not write the ship of the dock")
	}
	if r, _ := w.recFor(&spec.Operation{Path: "/Ship/id/{id}"}, "ship"); r != first {
		t.Error("an update by id does not write the first ship")
	}
}

func TestSetKey(t *testing.T) {
	in := "{\n  \"seed\": [\"Planet\"],\n  \"$comment\": \"keep\"\n}\n"
	out, err := setKey([]byte(in), "$recorded", []byte(`{"a": 1}`))
	if err != nil || string(out) != "{\n  \"seed\": [\"Planet\"],\n  \"$comment\": \"keep\",\n  \"$recorded\": {\"a\": 1}\n}\n" {
		t.Fatalf("add: %q %v", out, err)
	}
	out, err = setKey(out, "$recorded", []byte(`{"b": 2}`))
	if err != nil || string(out) != "{\n  \"seed\": [\"Planet\"],\n  \"$comment\": \"keep\",\n  \"$recorded\": {\"b\": 2}\n}\n" {
		t.Errorf("replace: %q %v", out, err)
	}
	if out, err := setKey([]byte("{}"), "k", []byte("1")); err != nil || string(out) != "{\n  \"k\": 1\n}" {
		t.Errorf("empty: %q %v", out, err)
	}
	if _, err := setKey([]byte("[1]"), "k", []byte("1")); err == nil {
		t.Error("no object")
	}
}

// A violation names the request the data came from, the value and the
// rule of the schema.
func TestViolation(t *testing.T) {
	maxLen := uint64(3)
	s := &openapi3.Schema{Type: &openapi3.Types{"object"}, Required: []string{"Message"}, Properties: openapi3.Schemas{
		"Code": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: `^\w{3}$`, MaxLength: &maxLen}},
	}}
	v := map[string]any{"Code": "ab"}
	msg := violation("#12 GET /Dock/D2 (GetDock)", v, spec.SchemaError{Pointer: "/Code", Reason: "no match"}, s)
	for _, want := range []string{"/Code: no match", "data from: #12 GET /Dock/D2 (GetDock)", `value:     "ab"`, `schema:    type string, pattern ^\w{3}$, maxLength 3`} {
		if !strings.Contains(msg, want) {
			t.Errorf("violation lacks %q:\n%s", want, msg)
		}
	}
	msg = violation("#3 PUT /Dock/D2", v, spec.SchemaError{Pointer: "/Message", Reason: "missing"}, s)
	if !strings.Contains(msg, "value:     (missing; the object has Code)") || !strings.Contains(msg, "schema:    required: Message") {
		t.Errorf("missing field:\n%s", msg)
	}
	e := Entry{N: 7, Tag: "Dock", Why: "update UpdateDock", Method: "PUT", URL: "/Dock/D2", Body: map[string]any{"a": 1}, Status: 404, Resp: map[string]any{"Message": "no dock"}}
	if got := e.String(); !strings.Contains(got, "#007 [Dock]         PUT    /Dock/D2 → 404  update UpdateDock") || !strings.Contains(got, `sent:   {"a":1}`) || !strings.Contains(got, `answer: {"Message":"no dock"}`) {
		t.Errorf("entry:\n%s", got)
	}
}

// A second run takes the records of "$recorded" and sends no GET for the
// unchanged operations, even when the instance changed meanwhile; a DTO
// that changed is read again, and the same record is taken.
func TestRecordReuse(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sp := newStarport()
	res, doc, _ := runRecord(t, sp, specPath, nil, true)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res))
	}
	if err := doc.Save(specPath); err != nil {
		t.Fatal(err)
	}
	// through the file, as the next run reads it
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(recordConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveRecorded(defs, res.Recorded); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(defs)
	if err != nil {
		t.Fatal(err)
	}
	old := cfg.Recorded
	if len(old.Records) != 5 {
		t.Fatalf("records: %+v", old.Records)
	}

	sp.planets[0]["name"] = "Mars Two"
	sp.sent = nil
	res2, _, c2 := runRecord(t, sp, specPath, old, true)
	if c2.Count[http.MethodGet] != 0 || len(sp.sent) > 0 || res2.Changed {
		t.Errorf("second run: requests %v, writes %v, changed %v\n%s", c2.Count, sp.sent, res2.Changed, notes(res2))
	}
	if res2.Stats.Reused != 5 {
		t.Errorf("reused %d records", res2.Stats.Reused)
	}
	equal(t, "seed", res2.Recorded.Seed, `{"Planet":{"id":1,"planetCode":"P1","name":"Mars"},"Dock":{"id":1,"dockCode":"D2","planetId":1,"name":"South"}}`)

	// a new field in PlanetRead: the planet is read again, the same one
	b = []byte(strings.Replace(string(b), "        name: {type: string}\n    DockRead:", "        name: {type: string}\n        moons: {type: integer}\n    DockRead:", 1))
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	res3, _, c3 := runRecord(t, sp, specPath, old, true)
	if c3.Count[http.MethodGet] == 0 || res3.Stats.Reused != 4 {
		t.Errorf("changed DTO: requests %v, reused %d\n%s", c3.Count, res3.Stats.Reused, notes(res3))
	}
	equal(t, "seed after the change", res3.Recorded.Seed, `{"Planet":{"id":1,"planetCode":"P1","name":"Mars Two"},"Dock":{"id":1,"dockCode":"D2","planetId":1,"name":"South"}}`)

	// other "params" select every record again
	other := *old
	other.Params = "x"
	if res4, _, _ := runRecord(t, sp, specPath, &other, true); res4.Stats.Reused != 0 {
		t.Errorf("other params: reused %d", res4.Stats.Reused)
	}
}

// project builds a body of a composed DTO at every level: arrays of allOf
// elements, nested objects and their readOnly fields.
func TestProjectNested(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData([]byte(`
openapi: 3.0.3
info: {title: t, version: "1"}
paths: {}
components:
  schemas:
    ShipUpsert:
      type: object
      required: [Code]
      properties:
        Code: {type: string}
        Crew:
          type: array
          nullable: true
          items:
            allOf:
            - $ref: '#/components/schemas/PilotRead'
            - x-go-type: PilotRead
        Dock:
          $ref: '#/components/schemas/Dock'
    PilotUpsert:
      type: object
      properties:
        Name: {type: string}
    PilotRead:
      type: object
      allOf:
      - $ref: '#/components/schemas/PilotUpsert'
      - properties:
          Id: {type: integer, readOnly: true}
          ShipId: {type: integer}
    Dock:
      type: object
      properties:
        Name: {type: string}
`))
	if err != nil {
		t.Fatal(err)
	}
	var src any
	if err := spec.DecodeJSON([]byte(`{"Id": 4, "code": "S1", "Extra": 1,
		"Crew": [{"Id": 9, "Name": "Ann", "ShipId": 4, "Rank": "x"}, {"name": "Bo"}],
		"Dock": {"Name": "North", "Planet": {"Name": "Mars"}}}`), &src); err != nil {
		t.Fatal(err)
	}
	ref := &openapi3.SchemaRef{Ref: "#/components/schemas/ShipUpsert", Value: doc.Components.Schemas["ShipUpsert"].Value}
	equal(t, "request", project(src, ref, spec.ModeRequest),
		`{"Code":"S1","Crew":[{"Name":"Ann","ShipId":4},{"Name":"Bo"}],"Dock":{"Name":"North"}}`)
	equal(t, "response", project(src, ref, spec.ModeResponse),
		`{"Code":"S1","Crew":[{"Id":9,"Name":"Ann","ShipId":4},{"Name":"Bo"}],"Dock":{"Name":"North"}}`)
	var null any
	if err := spec.DecodeJSON([]byte(`{"Code": "S2", "Crew": null}`), &null); err != nil {
		t.Fatal(err)
	}
	equal(t, "null list", project(null, ref, spec.ModeRequest), `{"Code":"S2","Crew":null}`)
}

// A record created again gets a new id; the "id" of another DTO with the
// same number is not that record and stays, so the GETs after the writes
// compare equal.
func TestRecordVerifyOtherIDs(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sp := newStarport()
	sp.planets[1]["id"] = json.Number("101") // the id of the ship S1, which the run creates again
	res, _, _ := runRecord(t, sp, specPath, nil, true)
	if s := sp.ships[1]; s["shipCode"] != "S1" || s["id"] != json.Number("104") {
		t.Fatalf("S1 was not created again: %v\n%s", sp.ships, notes(res))
	}
	if all := notes(res); strings.Contains(all, CodeChanged) {
		t.Errorf("the planet with id 101 counts as changed:\n%s", all)
	}
}

// An update whose parameter needs a record of a later tag waits for the
// late reads and is sent then, with "format" taking the value.
func TestRecordLateUpdate(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"params": {"planetCode": "P1", "UpdateShip.id": {"format": "9{Dock.id}"}}, "seed": ["Planet", "Dock"],
	  "$apitest": {"Tags": ["Ship", "Dock"]}}`
	sp := newStarport()
	sp.ships = append(sp.ships, map[string]any{"id": json.Number("930"), "shipCode": "S9", "dockId": json.Number("30"), "name": "Nova"})
	res, _, _ := runConfig(t, sp, specPath, config, nil, true)
	all := notes(res)
	dock := slices.Index(sp.sent, "PUT /Planet/P1/Dock/D1/Config")
	ship := slices.Index(sp.sent, "PUT /Ship/id/930")
	if dock < 0 || ship < dock {
		t.Errorf("UpdateShip was not sent after the Dock tag: %v\n%s", sp.sent, all)
	}
	if strings.Contains(all, "NOT_EXECUTED UpdateShip") || strings.Contains(all, "no  was read") {
		t.Errorf("notes:\n%s", all)
	}
}

// "format" puts a parameter together from fields of several records; in
// the examples every id is the one of the environment.
func TestParamFormat(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n := newNamer(s)
	k := newKnown()
	k.add("planet", map[string]any{"id": json.Number("7"), "planetCode": "P1"})
	k.add("dock", map[string]any{"id": json.Number("30"), "dockCode": "D1"})
	cfg, err := Parse([]byte(`{"params": {"GetShip.id": {"format": "{Planet.id}-{Dock.id}-{dockCode}"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rd := &reader{n: n, cfg: cfg}
	var p *openapi3.Parameter
	for _, x := range s.Op("GetShip").Params {
		p = x
	}
	v, ok := rd.value(s.Op("GetShip"), p, k)
	if !ok || v.v != "7-30-D1" || v.table != "dock" {
		t.Fatalf("value %+v, %v", v, ok)
	}
	if _, ok := rd.format("{Planet.id}-{Ship.id}", k); ok {
		t.Error("a format with a field no record has has a value")
	}
	sm := &sim{rd: rd, ids: map[string]map[string]int{"planet": {"7": 1}, "dock": {"30": 2}}, tables: map[string]bool{"planet": true, "dock": true}}
	if got := sm.param(v); got != "1-2-D1" {
		t.Errorf("in the environment: %v", got)
	}

	for in, want := range map[string]string{
		`{"format": "P"}`:                     "has no {<field>}",
		`{"format": "{Planet.id"}`:            `"{" without "}"`,
		`{"format": "Planet.id}"}`:            `"}" without "{"`,
		`{"format": "{}-{id}"}`:               `empty "{}"`,
		`{"format": "{a}", "field": "id"}`:    "a parameter is a value",
		`{"Op": {"x": {"format": "{a}"}}}`:    "",
		`{"Op.x": {"format": "{a}"}}`:         "",
		`{"Op.x": {"format": "{a}", "b": 1}}`: "group parameters under the operationId alone",
	} {
		params := `{"x": ` + in + `}`
		if strings.HasPrefix(in, `{"Op`) {
			params = in
		}
		_, err := parseParams([]byte(params))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", in, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

// No example of -spec reaches the output, in the first run nor in the next
// one: the first run writes only what the instance answers, the next one
// takes the examples of unchanged operations from -out.
func TestRecordNoSpecExamples(t *testing.T) {
	dir := t.TempDir()
	specPath, outPath := filepath.Join(dir, "record.yaml"), filepath.Join(dir, "out.yaml")
	src, err := yamldoc.Load("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range [][]string{
		{"components", "parameters", "PlanetCode"},
		{"paths", "/Planet", "get", "responses", "200", "content", "application/json"},
		{"paths", "/Planet/{planetCode}/Dock/{dockCode}/Ship", "get", "responses", "200", "content", "application/json"},
	} {
		if err := yamldoc.Set(yamldoc.Path(src.Root, at...), "example", "OLD"); err != nil {
			t.Fatal(at, err)
		}
	}
	named := yamldoc.Path(src.Root, "paths", "/Ship/id/{id}", "get", "responses", "200", "content", "application/json")
	if err := yamldoc.Set(named, "examples", map[string]any{"old": map[string]any{"value": "OLD"}}); err != nil {
		t.Fatal(err)
	}
	if err := src.Save(specPath); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newStarport())
	t.Cleanup(srv.Close)
	run := func(old *Recorded, prev *yamldoc.Doc) (*Result, *yamldoc.Doc) {
		s, err := spec.Load(context.Background(), specPath)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := yamldoc.Load(specPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := Parse([]byte(recordConfig))
		cfg.Recorded = old
		c := &Client{Opt: discover.Options{BaseURL: srv.URL}, Log: func(Entry) {}}
		res, err := Run(context.Background(), Input{Spec: s, Doc: doc, Config: cfg, Client: c, Writes: true, Prev: prev})
		if err != nil {
			t.Fatal(err)
		}
		return res, doc
	}
	res, doc := run(nil, nil)
	b, _ := doc.Bytes()
	if strings.Contains(string(b), "OLD") {
		t.Fatalf("the first run keeps examples of -spec:\n%s", b)
	}
	if err := doc.Save(outPath); err != nil {
		t.Fatal(err)
	}
	out, err := yamldoc.Load(outPath)
	if err != nil {
		t.Fatal(err)
	}
	res2, doc2 := run(res.Recorded, out)
	b2, _ := doc2.Bytes()
	if strings.Contains(string(b2), "OLD") {
		t.Fatalf("the second run takes examples of -spec:\n%s", b2)
	}
	if res2.Changed || res2.Stats.Examples != 0 || string(b2) != string(b) {
		t.Errorf("second run: changed %v, %d examples\n%s", res2.Changed, res2.Stats.Examples, notes(res2))
	}
}

// Without any DELETE of its table a POST is not sent: its body is the
// answer of the GET, and nothing is NOT_EXECUTED.
func TestRecordCreateWithoutDelete(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"params": {"planetCode": "P1"}, "seed": ["Planet", "Dock"], "$apitest": {"ExcludeOps": ["DeleteShip"]}}`
	sp := newStarport()
	res, doc, _ := runConfig(t, sp, specPath, config, nil, true)
	all := notes(res)
	for _, s := range sp.sent {
		if strings.HasPrefix(s, "POST /Planet/") || strings.HasPrefix(s, "DELETE") {
			t.Errorf("sent %s", s)
		}
	}
	if strings.Contains(all, CodeNotExecuted) || !strings.Contains(all, "BUILT CreateShip: POST /Planet/{planetCode}/Ship is not sent: no DELETE of the spec removes a ship") {
		t.Errorf("notes:\n%s", all)
	}
	equal(t, "CreateShip body", exampleAt(t, doc, "paths", "/Planet/{planetCode}/Ship", "post", "requestBody", "content", "application/json"),
		`{"shipCode":"S2","dockId":1,"name":"Comet"}`)
}

// A DELETE without a POST of its own path is followed by a POST of its
// table that creates the record again.
func TestCreator(t *testing.T) {
	r, other := &rec{table: "ship"}, &rec{table: "ship", path: "/Planet/{p}/Dock/{d}/Ship"}
	op := func(id, path string) *cases.Case {
		return &cases.Case{Op: &spec.Operation{ID: id, Path: path}, Example: cases.DefaultExample}
	}
	own := &wop{c: op("CreateDockShip", "/Dock/Ship"), kind: kindCreate, table: "ship", rec: other, resolved: true}
	plain := &wop{c: op("CreateShip", "/Ship"), kind: kindCreate, table: "ship", rec: r, resolved: true}
	rd := &reader{cfg: &Config{}, recs: map[string]*rec{"ship": r}, forPath: map[string]*rec{"/Dock/Ship": other}, k: newKnown(),
		s: &spec.Spec{}}
	w := &writes{rd: rd, ops: []*wop{own, plain}}
	if got := w.creator("ship", r); got != plain {
		t.Errorf("the POST of the record: %v", got)
	}
	if got := w.creator("ship", other); got != own {
		t.Errorf("the POST of the list of the record: %v", got)
	}
	w.ops = []*wop{own}
	if got := w.creator("ship", r); got != nil {
		t.Errorf("a POST at the list of another record: %v", got.c.Op.ID)
	}
	w.ops = []*wop{{c: op("CreateShipAgain", "/Fleet/Ship"), kind: kindCreate, table: "ship", resolved: true}}
	if got := w.creator("ship", r); got == nil {
		t.Error("another POST of the table is not taken")
	}
}

// NO_DATA and NOT_IN_CONTAINER come with what to set in the defaults file:
// "select" details for a GET the selected record has no data at, "params"
// for a parameter without a value, "$apitest" for a GET before its write.
func TestRecordSuggestions(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"params": {"planetCode": "P1", "UpdateShip.id": {"field": "registry"}}, "seed": ["Planet", "Dock"],
	  "select": {"Dock": {"details": {"/Planet/{planetCode}/Dock/{dockCode}/Config": {"mandatory": ["settings.mode"]}}}}}`
	sp := newStarport()
	sp.gone = map[string]bool{"/Ship/id/101": true}
	res, _, _ := runConfig(t, sp, specPath, config, nil, true)
	got := map[string]string{}
	for _, s := range res.Suggestions {
		got[s.Where+" "+s.Code] = text(s.Fix)
	}
	for key, want := range map[string]string{
		"GetShip NO_DATA":                `{"select":{"ShipRead":{"details":{"/Ship/id/{id}":{}}}}}`,
		"UpdateShip NO_DATA":             `{"params":{"UpdateShip.id":{"field":"\u003cfield of a selected record that holds it\u003e"}}}`,
		"GetDockConfig NOT_IN_CONTAINER": `{"$apitest":{"MethodOrder":["POST","PUT","PATCH","GET"]}}`,
	} {
		if got[key] != want {
			t.Errorf("%s: %s, want %s\n%s", key, got[key], want, notes(res))
		}
	}

	path := filepath.Join(dir, "defaults.json")
	orig := "{\n  \"seed\": [\"Planet\"],\n  \"$recorded\": {}\n}\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSuggestions(path, res.Suggestions); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if _, err := Parse(b); err != nil || !strings.Contains(string(b), `"$suggestions": {`) || !strings.Contains(string(b), `"field": "<field of a selected record that holds it>"`) {
		t.Fatalf("with suggestions: %v\n%s", err, b)
	}
	if err := SaveSuggestions(path, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Errorf("without suggestions:\n%s", b)
	}
}

func TestDeleteKey(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1, "x": {"b": 2}, "c": 3}`: `{"a": 1, "c": 3}`,
		`{"x": [1], "c": 3}`:              `{ "c": 3}`,
		`{"a": 1, "x": 2}`:                `{"a": 1}`,
		`{"a": 1}`:                        `{"a": 1}`,
	} {
		got, err := deleteKey([]byte(in), "x")
		if err != nil || string(got) != want {
			t.Errorf("%s: %s %v, want %s", in, got, err, want)
		}
	}
	if _, err := deleteKey([]byte(`[1]`), "x"); err == nil {
		t.Error("no error for an array")
	}
}
