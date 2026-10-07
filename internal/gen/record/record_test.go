package record

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

const specFile = "../../../testdata/gen/record.yaml"

// starport is an empty instance of testdata/gen/record.yaml: every table
// counts its ids from 1, a dock name is unique, a ship needs its dock and a
// booking its ship.
type starport struct {
	mu       sync.Mutex
	docks    map[int]map[string]any
	ships    map[int]map[string]any
	bookings map[int]map[string]any
	next     map[string]int
	clock    int
	sent     []string
	// location answers a POST of a ship with a Location header only
	location bool
}

func newStarport() *starport {
	return &starport{docks: map[int]map[string]any{}, ships: map[int]map[string]any{}, bookings: map[int]map[string]any{}, next: map[string]int{}}
}

func (sp *starport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.sent = append(sp.sent, r.Method+" "+r.URL.RequestURI())
	var body map[string]any
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		_ = dec.Decode(&body)
	}
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	fail := func(status int, msg string) { answer(status, map[string]any{"message": msg}) }
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	table := map[string]map[int]map[string]any{"docks": sp.docks, "ships": sp.ships, "bookings": sp.bookings}[segs[0]]
	if table == nil {
		fail(404, "no such path")
		return
	}
	if len(segs) == 1 {
		switch r.Method {
		case http.MethodGet:
			var out []any
			for i := 1; i <= sp.next[segs[0]]; i++ {
				if rec := table[i]; rec != nil {
					out = append(out, rec)
				}
			}
			answer(200, out)
		case http.MethodPost:
			switch segs[0] {
			case "docks":
				for _, d := range sp.docks {
					if d["name"] == body["name"] {
						fail(409, fmt.Sprintf("a dock named %v exists", body["name"]))
						return
					}
				}
			case "ships":
				if sp.docks[num(body["dockId"])] == nil {
					fail(422, fmt.Sprintf("dock %v does not exist", body["dockId"]))
					return
				}
			case "bookings":
				route, _ := body["route"].(map[string]any)
				if sp.ships[num(body["shipId"])] == nil || route == nil || sp.docks[num(route["originDockId"])] == nil {
					fail(422, "ship or dock does not exist")
					return
				}
				sp.clock++
				body["createdAt"] = time.Date(2027, 1, 15, 10, 0, sp.clock, 0, time.UTC).Format(time.RFC3339)
			}
			sp.next[segs[0]]++
			id := sp.next[segs[0]]
			body["id"] = id
			table[id] = body
			if segs[0] == "ships" && sp.location {
				w.Header().Set("Location", fmt.Sprintf("/ships/%d", id))
				w.WriteHeader(201)
				return
			}
			answer(201, body)
		default:
			fail(405, "method not allowed")
		}
		return
	}
	id, _ := strconv.Atoi(segs[1])
	rec := table[id]
	if rec == nil {
		fail(404, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		answer(200, rec)
	case http.MethodPut:
		body["id"] = id
		table[id] = body
		answer(200, body)
	case http.MethodDelete:
		delete(table, id)
		w.WriteHeader(204)
	default:
		fail(405, "method not allowed")
	}
}

func num(v any) int {
	n, _ := strconv.Atoi(fmt.Sprint(v))
	return n
}

// workspace copies the spec into a temporary directory.
func workspace(t *testing.T) (specPath, filePath string) {
	t.Helper()
	return workspaceOf(t, specFile)
}

// workspaceOf copies a spec into a temporary directory.
func workspaceOf(t *testing.T, src string) (specPath, filePath string) {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	specPath = filepath.Join(dir, "starport.yaml")
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return specPath, filepath.Join(dir, DefaultFile)
}

func loadSpec(t *testing.T, path string) *spec.Spec {
	t.Helper()
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// analyse runs -analyse on the spec and saves the record file.
func analyse(t *testing.T, specPath, filePath string) *Analysis {
	t.Helper()
	f, err := Load(filePath)
	if err != nil {
		t.Fatal(err)
	}
	an, err := Analyse(loadSpec(t, specPath), f, defaults.Run{}, 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Save(filePath); err != nil {
		t.Fatal(err)
	}
	return an
}

// record runs the record file against srv (nil: no instance), saves the
// file and the spec, and returns the result.
func record(t *testing.T, specPath, filePath string, srv *httptest.Server, refresh ...string) *Result {
	t.Helper()
	f, err := Load(filePath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load(specPath)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Spec: loadSpec(t, specPath), Doc: doc, File: f, Refresh: refresh}
	if srv != nil {
		in.Client = &Client{Opt: discover.Options{BaseURL: srv.URL}}
	}
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.FileChanged {
		if err := f.Save(filePath); err != nil {
			t.Fatal(err)
		}
	}
	if len(res.Problems) == 0 && res.SpecChanged {
		if err := doc.Save(specPath); err != nil {
			t.Fatal(err)
		}
	}
	return res
}

func notes(ns []Note) string {
	var b strings.Builder
	for _, n := range ns {
		b.WriteString(n.String() + "\n")
	}
	return b.String()
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// step finds the entry of an endpoint in a record file.
func step(t *testing.T, f *File, key string) *Step {
	t.Helper()
	for _, st := range f.Steps {
		if st.Key == key {
			return st
		}
	}
	t.Fatalf("no entry %q", key)
	return nil
}

// edit loads the record file, changes it and saves it.
func edit(t *testing.T, filePath string, change func(f *File)) {
	t.Helper()
	f, err := Load(filePath)
	if err != nil {
		t.Fatal(err)
	}
	change(f)
	if err := f.Save(filePath); err != nil {
		t.Fatal(err)
	}
}

func states(res *Result) string {
	var parts []string
	for _, sr := range res.Steps {
		parts = append(parts, sr.Step.String()+"="+sr.State)
	}
	return strings.Join(parts, "\n")
}

// -analyse orders the entries so every body finds its records: Ship after
// Dock, Booking after both, the DELETE last; it links the body fields to
// the values the entries before save, and a second run adds nothing.
func TestAnalyseOrdersAndLinks(t *testing.T) {
	specPath, filePath := workspace(t)
	an := analyse(t, specPath, filePath)
	if len(an.Added) != 9 || an.Complete != 0 || len(an.Notes) != 0 {
		t.Fatalf("added %d, complete %d, notes:\n%s", len(an.Added), an.Complete, notes(an.Notes))
	}
	if an.Run == nil || strings.Join(an.Run.Tags, ",") != "Dock,Ship,Booking" || !an.Run.DeleteLast {
		t.Errorf("suggested order: %+v", an.Run)
	}
	text := read(t, filePath)
	for _, want := range []string{
		"# Examples for apitest",
		"Dock:\n  - POST /docks:\n      status: new\n      body: {capacity: 19, name: Where Team}\n      save: {dockId: /id}\n      response:\n",
		"  - POST /ships:\n      status: new\n      body: {dockId: '{{dockId}}', name: Either Group}\n      save: {shipId: /id}",
		"route: {departure: \"2026-06-01T23:39:00Z\", originDockId: '{{dockId}}'}",
		"shipId: '{{shipId}}'",
		"cleanup:\n  - DELETE /docks/{dockId}:\n      status: new\n      path: {dockId: '{{dockId}}'}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("record file misses %q:\n%s", want, text)
		}
	}
	at := func(s string) int { return strings.Index(text, "\n"+s+":") }
	if at("Dock") > at("Ship") || at("Ship") > at("Booking") || at("Booking") > at("cleanup") {
		t.Errorf("sections out of order:\n%s", text)
	}
	again := analyse(t, specPath, filePath)
	if len(again.Added) != 0 || read(t, filePath) != text {
		t.Errorf("second analyse added %d entries", len(again.Added))
	}
}

// The examples record writes pass apitest in a new empty instance, in the
// order -analyse proposes.
func TestRecordPassesApitest(t *testing.T) {
	specPath, filePath := workspace(t)
	an := analyse(t, specPath, filePath)
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Recorded != 9 || res.Sent != 9 || !res.SpecChanged || res.Examples == 0 {
		t.Fatalf("recorded %d, sent %d, problems:\n%s", res.Recorded, res.Sent, notes(res.Problems))
	}
	text := read(t, filePath)
	for _, want := range []string{"      response:\n        status: 201\n        body: {capacity: 19, id: 1, name: Where Team}",
		"      response:\n        status: 204\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("record file misses %q:\n%s", want, text)
		}
	}
	order, err := CheckOrder(loadSpec(t, specPath), mustLoad(t, filePath), *an.Run)
	if err != nil || len(order) != 0 {
		t.Errorf("order: %v\n%s", err, notes(order))
	}
	fresh := httptest.NewServer(newStarport())
	defer fresh.Close()
	apitest.Run(t, apitest.Config{SpecPath: specPath, BaseURL: fresh.URL, Tags: an.Run.Tags, DeleteLast: true, DisableReports: true})
}

func mustLoad(t *testing.T, path string) *File {
	t.Helper()
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Without an instance record writes the stored answers into the spec and
// sends nothing; a second run changes nothing.
func TestRecordWritesStoredAnswers(t *testing.T) {
	specPath, filePath := workspace(t)
	analyse(t, specPath, filePath)
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	record(t, specPath, filePath, srv)
	stored := read(t, filePath)
	// a spec without examples, as it comes from the server code again
	b, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	res := record(t, specPath, filePath, nil)
	if len(res.Problems) > 0 || res.Sent != 0 || res.Recorded != 0 || !res.SpecChanged || res.FileChanged {
		t.Fatalf("offline run: sent %d, problems:\n%s", res.Sent, notes(res.Problems))
	}
	for _, sr := range res.Steps {
		if sr.State != StateKept {
			t.Errorf("%s: %s", sr.Step, sr.State)
		}
	}
	if !strings.Contains(read(t, specPath), "example: {dockId: 1, id: 1, name: Either Group}") {
		t.Errorf("spec without the stored answer:\n%s", read(t, specPath))
	}
	res = record(t, specPath, filePath, nil)
	if res.SpecChanged || res.Examples != 0 || read(t, filePath) != stored {
		t.Errorf("second offline run changed something: %d examples", res.Examples)
	}
}

// Only entries with status new or repeat are sent; a recorded one becomes
// approved and is not sent again, its values come from the stored answer.
// An ignored entry is neither sent nor written into the spec.
func TestRecordStatus(t *testing.T) {
	specPath, filePath := workspace(t)
	analyse(t, specPath, filePath)
	if n := strings.Count(read(t, filePath), "status: new"); n != 9 {
		t.Fatalf("%d entries with status new:\n%s", n, read(t, filePath))
	}
	sp := newStarport()
	srv := httptest.NewServer(sp)
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Recorded != 9 || strings.Count(read(t, filePath), "status: approved") != 9 {
		t.Fatalf("recorded %d, problems:\n%s\n%s", res.Recorded, notes(res.Problems), read(t, filePath))
	}
	// again: nothing is sent
	res = record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Sent != 0 || res.FileChanged {
		t.Fatalf("second run sent %d:\n%s", res.Sent, notes(res.Problems))
	}
	edit(t, filePath, func(f *File) {
		step(t, f, "GET /ships/{shipId}").setStatus(StatusRepeat)
		step(t, f, "GET /docks").setStatus(StatusIgnore)
	})
	before := len(sp.sent)
	res = record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Recorded != 1 || res.Sent != 1 || len(sp.sent) != before+1 || sp.sent[before] != "GET /ships/1" {
		t.Fatalf("recorded %d, sent %v, problems:\n%s", res.Recorded, sp.sent[before:], notes(res.Problems))
	}
	want := "POST /docks=kept\nGET /docks/{dockId}=kept\nGET /docks=ignored\nPUT /docks/{dockId}=kept\nPOST /ships=kept\nGET /ships/{shipId}=recorded\n" +
		"POST /bookings=kept\nGET /bookings/{bookingId}=kept\nDELETE /docks/{dockId}=kept"
	if got := states(res); got != want {
		t.Errorf("states:\n%s\nwant:\n%s", got, want)
	}
	if res.Steps[5].Why != "status repeat" || res.Steps[5].URL != "/ships/1" || res.Steps[5].Status != 200 {
		t.Errorf("recorded step: %+v", res.Steps[5])
	}
	text := read(t, filePath)
	if !strings.Contains(text, "  - GET /ships/{shipId}:\n      status: approved\n") || !strings.Contains(text, "  - GET /docks:\n      status: ignore\n") {
		t.Errorf("statuses:\n%s", text)
	}
	// an ignored entry is not written into the spec
	b, _ := os.ReadFile(specFile)
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	record(t, specPath, filePath, nil)
	if strings.Contains(read(t, specPath), "- {capacity: 14, id: 1, name: How Life}") {
		t.Errorf("the ignored list has an example:\n%s", read(t, specPath))
	}
	// approved without answer
	edit(t, filePath, func(f *File) { yamldoc.Delete(step(t, f, "GET /ships/{shipId}").node, keyResponse) })
	res = record(t, specPath, filePath, nil)
	if len(res.Problems) != 1 || res.Problems[0].Code != CodeApproved {
		t.Errorf("problems:\n%s", notes(res.Problems))
	}
	if _, err := Parse([]byte("Dock:\n  - GET /docks:\n      status: done\n")); err == nil || !strings.Contains(err.Error(), `status of "GET /docks" is "done"`) {
		t.Errorf("unknown status: %v", err)
	}
}

// -refresh sends approved entries again.
func TestRecordRefresh(t *testing.T) {
	specPath, filePath := workspace(t)
	analyse(t, specPath, filePath)
	// the dock stays in the instance for the entries sent again
	edit(t, filePath, func(f *File) { step(t, f, "DELETE /docks/{dockId}").setStatus(StatusIgnore) })
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	record(t, specPath, filePath, srv)
	res := record(t, specPath, filePath, srv, "Ship")
	if len(res.Problems) > 0 || res.Recorded != 2 || res.Steps[4].Why != "-refresh" || res.Steps[5].URL != "/ships/2" {
		t.Fatalf("recorded %d, problems:\n%s\n%s\n%s", res.Recorded, notes(res.Problems), states(res), read(t, filePath))
	}
	for _, r := range []string{"all", "createShip", "post /ships", "POST /ships", "Ship"} {
		if !refreshed(step(t, mustBound(t, specPath, filePath), "POST /ships"), []string{r}) {
			t.Errorf("-refresh %q does not name POST /ships", r)
		}
	}
	if refreshed(step(t, mustBound(t, specPath, filePath), "POST /ships"), []string{"Dock"}) {
		t.Error("-refresh Dock names POST /ships")
	}
}

func mustBound(t *testing.T, specPath, filePath string) *File {
	t.Helper()
	f := mustLoad(t, filePath)
	res := &Result{}
	f.bind(loadSpec(t, specPath), res)
	if len(res.Problems) > 0 {
		t.Fatal(notes(res.Problems))
	}
	return f
}

// A stored answer that no longer fits the schema is recorded again; without
// an instance it is reported and still written.
func TestRecordSchemaChanged(t *testing.T) {
	specPath, filePath := workspace(t)
	analyse(t, specPath, filePath)
	// the dock stays in the instance for the entries sent again
	edit(t, filePath, func(f *File) { step(t, f, "DELETE /docks/{dockId}").setStatus(StatusIgnore) })
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	record(t, specPath, filePath, srv)
	// the ships get a required field the stored answers lack
	b := strings.Replace(read(t, specPath), "    Ship:\n      allOf:\n        - $ref: \"#/components/schemas/ShipWrite\"\n        - type: object\n          required: [id]",
		"    Ship:\n      allOf:\n        - $ref: \"#/components/schemas/ShipWrite\"\n        - type: object\n          required: [id, registry]", 1)
	if err := os.WriteFile(specPath, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	res := record(t, specPath, filePath, nil)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res.Problems))
	}
	stale := 0
	for _, n := range res.Notes {
		if n.Code == CodeStale {
			stale++
		}
	}
	if stale != 2 || !strings.Contains(states(res), "POST /ships=stale") {
		t.Errorf("notes:\n%s\nstates:\n%s", notes(res.Notes), states(res))
	}
	if !strings.Contains(notes(res.Notes), `set "status: repeat" to send it again`) {
		t.Errorf("notes:\n%s", notes(res.Notes))
	}
	// approved: sent again only with status repeat
	if res = record(t, specPath, filePath, srv); res.Sent != 0 {
		t.Errorf("an approved entry was sent: %d", res.Sent)
	}
	edit(t, filePath, func(f *File) {
		step(t, f, "POST /ships").setStatus(StatusRepeat)
		step(t, f, "GET /ships/{shipId}").setStatus(StatusRepeat)
	})
	res = record(t, specPath, filePath, srv)
	if res.Recorded != 2 || res.Steps[4].Why != "status repeat" {
		t.Errorf("recorded %d:\n%s", res.Recorded, states(res))
	}
	// the instance still lacks the field: apitest will report it
	if !strings.Contains(notes(res.Notes), CodeSchema) {
		t.Errorf("notes:\n%s", notes(res.Notes))
	}
}

// A request the instance rejects stops the run: the answers recorded before
// it stay in the file, the spec stays unchanged.
func TestRecordFailedRequest(t *testing.T) {
	specPath, filePath := workspace(t)
	analyse(t, specPath, filePath)
	edit(t, filePath, func(f *File) {
		st := step(t, f, "POST /ships")
		_ = yamldoc.SetNode(st.node, keyBody, valueNode(map[string]any{"name": "Comet", "dockId": 99}))
	})
	before := read(t, specPath)
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) != 1 || res.Problems[0].Code != CodeFailed || !strings.Contains(res.Problems[0].Message, "answered 422: {\"message\":\"dock 99 does not exist\"}") ||
		!strings.Contains(res.Problems[0].Message, "sent: {\"dockId\":99,\"name\":\"Comet\"}") {
		t.Fatalf("problems:\n%s", notes(res.Problems))
	}
	if res.Recorded != 4 || res.SpecChanged || read(t, specPath) != before {
		t.Errorf("recorded %d, spec changed %v", res.Recorded, res.SpecChanged)
	}
	if !strings.Contains(states(res), "POST /ships=failed\nGET /ships/{shipId}=not sent") || res.Steps[4].Answer == "" {
		t.Errorf("states:\n%s", states(res))
	}
	if !strings.Contains(read(t, filePath), "status: 201\n        body: {capacity: 19, id: 1, name: Where Team}") {
		t.Error("the answers recorded before the failure were not saved")
	}
}

// Problems of the file stop the run before anything is sent.
func TestRecordProblems(t *testing.T) {
	for _, tc := range []struct{ name, file, code, msg string }{
		{"unknown endpoint", "Dock:\n  - POST /harbours:\n      body: {}\n", CodeUnknownOp, `no operation "POST /harbours"`},
		{"placeholder", "Ship:\n  - GET /ships/{shipId}:\n      path: {shipId: '{{shipId}}'}\n", CodePlaceholder, "{{shipId}}: no entry above saves"},
		{"twice without name", "Dock:\n  - GET /docks\n  - GET /docks\n", CodeDuplicate, "appears 2 times"},
		{"name without place", "Dock:\n  - GET /docks:\n      name: all\n", CodeDuplicate, "a named example needs"},
		{"same name", "Dock:\n  - POST /docks:\n      name: a\n      body: {name: A, capacity: 1}\n  - POST /docks:\n      name: a\n      body: {name: B, capacity: 1}\n", CodeDuplicate, `the name "a" is used twice`},
		{"path parameter missing", "Dock:\n  - GET /docks/{dockId}\n", CodeParam, "{dockId} has no value"},
		{"unknown parameter", "Dock:\n  - GET /docks:\n      query: {zone: north}\n", CodeParam, `no query parameter "zone" (it has: none)`},
		{"body missing", "Dock:\n  - POST /docks\n", CodeBody, "requires a request body"},
		{"body not taken", "Dock:\n  - GET /docks:\n      body: {name: A}\n", CodeBody, "takes no request body"},
		{"request invalid", "Dock:\n  - POST /docks:\n      body: {name: A, capacity: 99}\n", CodeRequest, "body /capacity:"},
		{"parameter invalid", "Dock:\n  - GET /docks/{dockId}:\n      path: {dockId: abc}\n", CodeRequest, `path parameter "dockId"`},
		{"no instance", "Dock:\n  - GET /docks\n", CodeNeedsURL, "1 entries are to be sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specPath, filePath := workspace(t)
			if err := os.WriteFile(filePath, []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			res := record(t, specPath, filePath, nil)
			if len(res.Problems) == 0 || res.Problems[0].Code != tc.code || !strings.Contains(res.Problems[0].Message, tc.msg) {
				t.Errorf("problems:\n%s", notes(res.Problems))
			}
			if res.Sent != 0 || res.SpecChanged {
				t.Errorf("sent %d, spec changed %v", res.Sent, res.SpecChanged)
			}
		})
	}
}

// An endpoint that appears more than once gets named examples; a parameter
// the path shares gets a copy in the operation that needs another value.
func TestRecordNamedExamples(t *testing.T) {
	specPath, filePath := workspace(t)
	file := `Dock:
  - POST /docks:
      name: north
      body: {name: North, capacity: 4}
      save: {dockId: /id}
  - POST /docks:
      name: south
      body: {name: South, capacity: 6}
      save: {southId: /id}
  - GET /docks/{dockId}:
      path: {dockId: "{{dockId}}"}
cleanup:
  - DELETE /docks/{dockId}:
      name: south
      path: {dockId: "{{southId}}"}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Recorded != 4 {
		t.Fatalf("recorded %d, problems:\n%s", res.Recorded, notes(res.Problems))
	}
	s := loadSpec(t, specPath)
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range all {
		if c.Kind == cases.Positive {
			names = append(names, c.Name)
		}
	}
	got := strings.Join(names, " ")
	for _, want := range []string{"Dock/createDock/north", "Dock/createDock/south", "Dock/getDock/default", "Dock/deleteDock/south"} {
		if !strings.Contains(got, want) {
			t.Errorf("cases %s miss %s", got, want)
		}
	}
	if strings.Contains(got, "getDock/south") || strings.Contains(got, "updateDock/south") {
		t.Errorf("the named example of the DELETE reached other operations: %s", got)
	}
	text := read(t, specPath)
	for _, want := range []string{
		"            examples:\n              north:\n                value: {name: North, capacity: 4}\n              south:\n                value: {name: South, capacity: 6}",
		"              examples:\n                north:\n                  value: {capacity: 4, id: 1, name: North}",
		"      parameters:\n        - name: dockId\n          in: path\n          required: true\n          schema:\n            type: integer\n          examples:\n            south:\n              value: 2",
		"    DockId:\n      name: dockId\n      in: path\n      required: true\n      schema:\n        type: integer\n      example: 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("spec misses %q:\n%s", want, text)
		}
	}
}

// A value saved from a header is stored with the answer, so a run without
// instance finds it too.
func TestRecordSaveFromHeader(t *testing.T) {
	specPath, filePath := workspace(t)
	file := `Dock:
  - POST /docks:
      body: {name: North, capacity: 4}
      save: {dockId: /id}
Ship:
  - POST /ships:
      body: {name: Comet, dockId: "{{dockId}}"}
      save: {shipId: header Location}
  - GET /ships/{shipId}:
      path: {shipId: "{{shipId}}"}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	sp := newStarport()
	sp.location = true
	srv := httptest.NewServer(sp)
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Steps[2].URL != "/ships/1" {
		t.Fatalf("problems:\n%s\n%+v", notes(res.Problems), res.Steps)
	}
	if !strings.Contains(read(t, filePath), "status: 201\n        headers: {Location: /ships/1}") {
		t.Errorf("record file:\n%s", read(t, filePath))
	}
	res = record(t, specPath, filePath, nil)
	if len(res.Problems) > 0 || !strings.Contains(read(t, specPath), "          schema:\n            type: integer\n          example: 1") {
		t.Errorf("problems:\n%s\n%s", notes(res.Problems), read(t, specPath))
	}
}

// CheckOrder reports an entry apitest runs before an entry above it, and the
// cases the file lacks.
func TestCheckOrder(t *testing.T) {
	specPath, filePath := workspace(t)
	file := `Dock:
  - GET /docks
  - POST /docks:
      body: {name: North, capacity: 4}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	ns, err := CheckOrder(loadSpec(t, specPath), mustLoad(t, filePath), defaults.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 || ns[0].Code != CodeOrder || ns[0].Where != "line 3 POST /docks" || !strings.Contains(ns[0].Message, "before GET /docks (line 2)") ||
		ns[1].Code != CodeNotInFile || !strings.Contains(ns[1].Message, "7 cases") {
		t.Errorf("notes:\n%s", notes(ns))
	}
	ns, _ = CheckOrder(loadSpec(t, specPath), mustLoad(t, filePath), defaults.Run{ExcludeOps: []string{"listDocks"}})
	if len(ns) == 0 || ns[0].Code != CodeNotRun {
		t.Errorf("notes:\n%s", notes(ns))
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct{ name, file, err string }{
		{"json", `{"Dock": []}`, "the record file is YAML"},
		{"no list", "Dock: {}\n", `tag "Dock" must hold a list`},
		{"two keys", "Dock:\n  - GET /docks:\n    POST /docks:\n", "an entry is"},
		{"settings no mapping", "Dock:\n  - GET /docks: [1]\n", "must be a mapping"},
		{"unknown setting", "Dock:\n  - GET /docks:\n      header: {}\n", `unknown setting "header"`},
		{"path no mapping", "Dock:\n  - GET /docks:\n      path: [1]\n", "must map parameter names"},
		{"save no mapping", "Dock:\n  - GET /docks:\n      save: /id\n", "save of"},
		{"save name", "Dock:\n  - GET /docks:\n      save: {1x: /id}\n", "no name for a saved value"},
		{"save source", "Dock:\n  - GET /docks:\n      save: {x: id}\n", `"id" is no source`},
		{"save request", "Dock:\n  - GET /docks:\n      save: {x: request body}\n", `"request body" is no source`},
		{"filter no mapping", "Dock:\n  - GET /docks:\n      filter: [1]\n", "filter of"},
		{"filter empty", "Dock:\n  - GET /docks:\n      filter: {}\n", "filter of"},
		{"ignore no list", "Dock:\n  - GET /docks:\n      ignore: x\n", "must be a list of field names"},
		{"response no mapping", "Dock:\n  - GET /docks:\n      response: 200\n", "must have status and body"},
		{"status", "Dock:\n  - GET /docks:\n      response: {status: ok}\n", "must be an HTTP status"},
		{"no status", "Dock:\n  - GET /docks:\n      response: {body: {}}\n", "has no status"},
		{"response setting", "Dock:\n  - GET /docks:\n      response: {status: 200, time: 1}\n", `unknown setting "time" in the response`},
		{"broken", "Dock: [\n", "did not find"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.file))
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("error %v, want %q", err, tc.err)
			}
		})
	}
	f, err := Parse([]byte("# only a comment\n"))
	if err != nil || len(f.Steps) != 0 {
		t.Fatalf("comment only: %v", err)
	}
	f, err = Parse([]byte("Dock:\nShip:\n  - GET /ships/{shipId}:\n      ignore: [createdAt]\n      save:\n      response:\n        status: 200\n        headers: {location: /x}\n"))
	if err != nil || len(f.Steps) != 1 || f.Steps[0].Ignore[0] != "createdAt" || f.Steps[0].Response.Headers["Location"] != "/x" {
		t.Fatalf("parse: %v %+v", err, f)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err != nil {
		t.Errorf("a missing file: %v", err)
	}
}

func TestFillAndCompact(t *testing.T) {
	n := valueNode(map[string]any{"id": "{{shipId}}", "label": "ship-{{shipId}}-{{dockId}}", "list": []any{"{{missing}}"}})
	got, missing := fill(n, map[string]any{"shipId": json.Number("7"), "dockId": "D1"})
	v := decode(got)
	if text(v) != `{"id":7,"label":"ship-7-D1","list":["{{missing}}"]}` || strings.Join(missing, ",") != "missing" {
		t.Errorf("fill: %s, missing %v", text(v), missing)
	}
	if strings.Join(names(n), ",") != "shipId,shipId,dockId,missing" {
		t.Errorf("names: %v", names(n))
	}
	node, err := jsonNode([]byte(`{"z": 1, "a": [true, null, 1.5, "x"], "long": "` + strings.Repeat("x", 80) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	compact(node)
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}}
	b, _ := yaml.Marshal(doc)
	if !strings.HasPrefix(string(b), "z: 1\na: [true, null, 1.5, x]\nlong: ") {
		t.Errorf("compact:\n%s", b)
	}
	if _, err := jsonNode([]byte(`{} {}`)); err == nil {
		t.Error("two values are no JSON")
	}
	for _, tc := range []struct {
		field, name string
		want        bool
	}{{"dockId", "dockId", true}, {"originDockId", "dockId", true}, {"origin_dockId", "dockId", true}, {"undockId", "dockId", false}, {"id", "dockId", false}} {
		if matches(tc.field, tc.name) != tc.want {
			t.Errorf("matches(%q, %q) != %v", tc.field, tc.name, tc.want)
		}
	}
	if word("ship-dock bay") != "shipDockBay" || word("--") != "value" || singular("Companies") != "Company" || lowerFirst("Dock") != "dock" {
		t.Error("word, singular or lowerFirst")
	}
}

const rosterFile = "../../../testdata/gen/record-roster.yaml"

// roster is an empty instance of testdata/gen/record-roster.yaml: the
// client chooses the pilot codes, a POST of a pilot answers without body,
// a mission needs its pilot.
type roster struct {
	mu       sync.Mutex
	pilots   []map[string]any
	missions []map[string]any
}

func (ro *roster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ro.mu.Lock()
	defer ro.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	pilot := func(code string) map[string]any {
		for _, p := range ro.pilots {
			if p["pilotCode"] == code {
				return p
			}
		}
		return nil
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/pilots":
		if pilot(fmt.Sprint(body["pilotCode"])) != nil {
			answer(409, map[string]any{"message": "pilot exists"})
			return
		}
		ro.pilots = append(ro.pilots, body)
		w.WriteHeader(201)
	case r.Method == http.MethodGet && r.URL.Path == "/pilots":
		answer(200, append([]map[string]any{}, ro.pilots...))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/pilots/"):
		if p := pilot(strings.TrimPrefix(r.URL.Path, "/pilots/")); p != nil {
			answer(200, p)
			return
		}
		answer(404, map[string]any{"message": "no such pilot"})
	case r.Method == http.MethodPost && r.URL.Path == "/missions":
		if pilot(fmt.Sprint(body["pilotCode"])) == nil {
			answer(422, map[string]any{"message": "no such pilot"})
			return
		}
		body["id"] = len(ro.missions) + 1
		ro.missions = append(ro.missions, body)
		answer(201, body)
	case r.Method == http.MethodGet && r.URL.Path == "/missions":
		items := []map[string]any{}
		for _, m := range ro.missions {
			if m["pilotCode"] == r.URL.Query().Get("pilotCode") {
				items = append(items, m)
			}
		}
		answer(200, map[string]any{"total": len(items), "items": items})
	default:
		answer(404, map[string]any{"message": "no such path"})
	}
}

// -analyse saves what later entries need: the pilot code apitest binds from
// the request of the POST, and a query parameter no binding names, found
// by its name; a mission body refers to the pilot, so Pilot runs first.
func TestAnalyseSavesFromRequest(t *testing.T) {
	specPath, filePath := workspaceOf(t, rosterFile)
	an := analyse(t, specPath, filePath)
	if an.Run == nil || strings.Join(an.Run.Tags, ",") != "Pilot,Mission" {
		t.Errorf("suggested order: %+v", an.Run)
	}
	text := read(t, filePath)
	for _, want := range []string{
		"Pilot:\n  - POST /pilots:\n      status: new\n      body:",
		"      save: {pilotCode: request /pilotCode}",
		"  - GET /pilots/{pilotCode}:\n      status: new\n      path: {pilotCode: '{{pilotCode}}'}",
		"  - POST /missions:\n      status: new\n      body: {pilotCode: '{{pilotCode}}', title:",
		"  - GET /missions:\n      status: new\n      query: {pilotCode: '{{pilotCode}}'}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("record file misses %q:\n%s", want, text)
		}
	}
	srv := httptest.NewServer(&roster{})
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Recorded != 5 {
		t.Fatalf("recorded %d, problems:\n%s", res.Recorded, notes(res.Problems))
	}
	fresh := httptest.NewServer(&roster{})
	defer fresh.Close()
	apitest.Run(t, apitest.Config{SpecPath: specPath, BaseURL: fresh.URL, Tags: an.Run.Tags, DeleteLast: true, DisableReports: true})
}

// "save" takes values from the request too; "filter" keeps only the list
// elements that match, and apitest finds them in the list in any order.
func TestRecordFilterAndRequestValues(t *testing.T) {
	specPath, filePath := workspaceOf(t, rosterFile)
	file := `Pilot:
  - POST /pilots:
      body: {pilotCode: P001, name: Ada}
      save: {adaCode: request /pilotCode, adaBody: request /}
  - GET /pilots:
      filter: {pilotCode: '{{adaCode}}'}
      save: {listed: /0/name}
  - GET /pilots/{pilotCode}:
      path: {pilotCode: '{{adaCode}}'}
      save: {sentCode: request path pilotCode}
Mission:
  - POST /missions:
      name: first
      body: {pilotCode: '{{sentCode}}', title: 'Flight of {{listed}}'}
  - POST /missions:
      name: second
      body: {pilotCode: '{{adaCode}}', title: Return}
      save: {secondId: /id}
  - GET /missions:
      query: {pilotCode: '{{adaCode}}'}
      filter: {/id: '{{secondId}}'}
      save: {queried: request query pilotCode}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&roster{})
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || res.Recorded != 6 {
		t.Fatalf("recorded %d, problems:\n%s\nnotes:\n%s", res.Recorded, notes(res.Problems), notes(res.Notes))
	}
	text := read(t, filePath)
	for _, want := range []string{
		"  - GET /pilots:\n      status: approved\n      filter: {pilotCode: '{{adaCode}}'}\n      save: {listed: /0/name}\n      response:\n        status: 200\n        body:\n          - {name: Ada, pilotCode: P001}\n",
		"body: {id: 1, pilotCode: P001, title: Flight of Ada}",
		"        body:\n          items:\n            - {id: 2, pilotCode: P001, title: Return}\n          total: 2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("record file misses %q:\n%s", want, text)
		}
	}
	spec := read(t, specPath)
	if strings.Count(spec, "x-apitest-compare-unordered: true") != 2 {
		t.Errorf("spec without x-apitest-compare-unordered:\n%s", spec)
	}
	// stored answers give the same values without instance
	res = record(t, specPath, filePath, nil)
	if len(res.Problems) > 0 || res.SpecChanged {
		t.Errorf("offline: changed %v, problems:\n%s", res.SpecChanged, notes(res.Problems))
	}
	// the filtered list sent again: the saves read the filtered answer
	res = record(t, specPath, filePath, srv, "listMissions")
	if len(res.Problems) > 0 || res.Recorded != 1 {
		t.Errorf("refresh:\n%s\n%s%s", states(res), notes(res.Problems), notes(res.Notes))
	}
	final := httptest.NewServer(&roster{})
	defer final.Close()
	apitest.Run(t, apitest.Config{SpecPath: specPath, BaseURL: final.URL, Tags: []string{"Pilot", "Mission"}, DeleteLast: true, DisableReports: true})
}

// A filter on an answer without list, or one no element matches.
func TestRecordFilterProblems(t *testing.T) {
	specPath, filePath := workspaceOf(t, rosterFile)
	file := `Pilot:
  - POST /pilots:
      body: {pilotCode: P001, name: Ada}
  - GET /pilots/{pilotCode}:
      path: {pilotCode: P001}
      filter: {pilotCode: P001}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&roster{})
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) != 1 || res.Problems[0].Code != CodeFilter || !strings.Contains(res.Problems[0].Message, "holds no list") {
		t.Errorf("problems:\n%s", notes(res.Problems))
	}
	file = `Pilot:
  - GET /pilots:
      filter: {pilotCode: P999}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	res = record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || len(res.Notes) == 0 || res.Notes[0].Code != CodeFilter || !strings.Contains(read(t, filePath), "body: []") {
		t.Errorf("problems:\n%s\nnotes:\n%s\n%s", notes(res.Problems), notes(res.Notes), read(t, filePath))
	}
	file = `Pilot:
  - GET /pilots:
      filter: {pilotCode: '{{nothing}}'}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	res = record(t, specPath, filePath, srv)
	if len(res.Problems) != 1 || res.Problems[0].Code != CodePlaceholder {
		t.Errorf("problems:\n%s", notes(res.Problems))
	}
}

func TestFilterList(t *testing.T) {
	list := valueNode([]any{map[string]any{"id": 1, "a": map[string]any{"b": "x"}}, map[string]any{"id": 2, "Code": "C2"}})
	for _, tc := range []struct {
		want map[string]any
		ids  string
	}{
		{map[string]any{"id": "2"}, "[2]"},
		{map[string]any{"code": "C2"}, "[2]"},
		{map[string]any{"/a/b": "x"}, "[1]"},
		{map[string]any{"id": 1, "/a/b": "y"}, "[]"},
	} {
		out, ok := filterList(list, tc.want)
		var ids []any
		for _, el := range decode(out).([]any) {
			ids = append(ids, el.(map[string]any)["id"])
		}
		if !ok || fmt.Sprint(ids) != tc.ids {
			t.Errorf("filter %v: %v %v", tc.want, ok, ids)
		}
	}
	if len(decode(list).([]any)) != 2 {
		t.Error("filterList changed its input")
	}
	if _, ok := filterList(valueNode(map[string]any{"a": []any{}, "b": []any{}}), map[string]any{"id": 1}); ok {
		t.Error("an object with two lists has no one list")
	}
	if _, ok := filterList(valueNode(map[string]any{"id": 1}), map[string]any{"id": 1}); ok {
		t.Error("an object without list")
	}
	if reference("id") || !reference("pilotCode") || !reference("dockId") || reference("name") {
		t.Error("reference")
	}
}

const permitsFile = "../../../testdata/gen/record-permits.yaml"

// The bindings of apitest decide the order of the tags; a body reference
// against them is reported instead of a Tags apitest refuses: Dock takes
// {dockPermitId} from DockPermit, so a permit body cannot refer to a dock.
func TestAnalyseBindingsWinOverBodies(t *testing.T) {
	specPath, filePath := workspaceOf(t, permitsFile)
	an := analyse(t, specPath, filePath)
	if an.Run != nil {
		t.Errorf("suggested order %+v", an.Run)
	}
	if len(an.Notes) != 1 || an.Notes[0].Code != CodeOrder || an.Notes[0].Where != "DockPermit" ||
		!strings.Contains(an.Notes[0].Message, "createDockPermit.dockId refers to records of Dock, but apitest must run DockPermit before Dock: getDockWithPermit takes {dockPermitId} from createDockPermit (heuristic binding)") {
		t.Errorf("notes:\n%s", notes(an.Notes))
	}
	text := read(t, filePath)
	if strings.Index(text, "\nDockPermit:") > strings.Index(text, "\nDock:") {
		t.Errorf("sections:\n%s", text)
	}
	// "$apitest".Tags against the bindings: the error names them
	want := "getDockWithPermit (Dock) takes {dockPermitId} from createDockPermit (DockPermit), heuristic binding"
	run := defaults.Run{Tags: []string{"Dock", "DockPermit"}}
	if _, err := Analyse(loadSpec(t, specPath), mustLoad(t, filePath), run, 1); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("analyse: %v", err)
	}
	if _, err := CheckOrder(loadSpec(t, specPath), mustLoad(t, filePath), run); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("check order: %v", err)
	}
	if err := orderError(loadSpec(t, specPath), defaults.Run{}, errTest); err.Error() != "test" {
		t.Errorf("without Tags: %v", err)
	}
}

var errTest = fmt.Errorf("test")

// With a filter, save reads the element the filter kept: "/name" is the
// name of the matching pilot, "/0/name" and "/" work on the filtered list
// and the element. A filter added after recording filters the stored
// answer, so the saves read the right element without sending.
func TestRecordFilterThenSave(t *testing.T) {
	specPath, filePath := workspaceOf(t, rosterFile)
	file := `Pilot:
  - POST /pilots:
      name: ada
      body: {pilotCode: P001, name: Ada}
  - POST /pilots:
      name: bob
      body: {pilotCode: P002, name: Bob}
  - GET /pilots:
      filter: {pilotCode: P002}
      save: {byName: /name, byIndex: /0/name, whole: /}
Mission:
  - POST /missions:
      name: one
      body: {pilotCode: P002, title: '{{byName}}-{{byIndex}}'}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&roster{})
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 || !strings.Contains(read(t, filePath), "body: {id: 1, pilotCode: P002, title: Bob-Bob}") {
		t.Fatalf("problems:\n%s\n%s", notes(res.Problems), read(t, filePath))
	}
	vars := map[string]any{}
	f := mustBound(t, specPath, filePath)
	st := step(t, f, "GET /pilots")
	r, _ := st.request(vars)
	st.saveFrom(st.Response, nil, r, vars)
	if text(vars["whole"]) != `{"name":"Bob","pilotCode":"P002"}` {
		t.Errorf("whole: %s", text(vars["whole"]))
	}
	// the filter is added after the list was recorded unfiltered
	edit(t, filePath, func(f *File) {
		st := step(t, f, "GET /pilots")
		yamldoc.Delete(st.node, keyFilter)
		st.Filter = nil
		st.setResponse(&Response{Status: 200, Body: valueNode([]any{
			map[string]any{"pilotCode": "P001", "name": "Ada"}, map[string]any{"pilotCode": "P002", "name": "Bob"}})})
	})
	edit(t, filePath, func(f *File) {
		st := step(t, f, "GET /pilots")
		n := valueNode(map[string]any{"pilotCode": "P002"})
		insertBefore(st.node, keyFilter, n, keySave)
	})
	res = record(t, specPath, filePath, nil)
	if len(res.Problems) > 0 || !res.FileChanged || !strings.Contains(notes(res.Notes), "the stored answer is filtered now") {
		t.Fatalf("problems:\n%s\nnotes:\n%s", notes(res.Problems), notes(res.Notes))
	}
	if got := text(decode(step(t, mustLoad(t, filePath), "GET /pilots").Response.Body)); got != `[{"name":"Bob","pilotCode":"P002"}]` {
		t.Errorf("stored list %s", got)
	}
}

// -analyse keeps the entries of the file as they are, with comments and
// own values, adds the missing ones and a status to every entry: approved
// with an answer, else new.
func TestAnalyseKeepsEntriesAndAddsStatus(t *testing.T) {
	specPath, filePath := workspace(t)
	file := `# my notes
Dock:
  - POST /docks:
      body: {name: "Harbour One", capacity: 7} # chosen by hand
      save: {dockId: /id}
      response:
        status: 201
        body: {id: 1, name: Harbour One, capacity: 7}
  - GET /docks/{dockId}:
      status: repeat
      path: {dockId: "{{dockId}}"}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	an := analyse(t, specPath, filePath)
	if len(an.Added) != 7 || an.Statuses != 8 {
		t.Errorf("added %d, statuses %d", len(an.Added), an.Statuses)
	}
	text := read(t, filePath)
	for _, want := range []string{"# my notes\nDock:\n  - POST /docks:\n      status: approved\n      body: {name: \"Harbour One\", capacity: 7} # chosen by hand\n      save: {dockId: /id}\n      response:\n        status: 201\n        body: {id: 1, name: Harbour One, capacity: 7}\n",
		"  - GET /docks/{dockId}:\n      status: repeat\n      path: {dockId: \"{{dockId}}\"}\n", "  - GET /docks:\n      status: new\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("record file misses %q:\n%s", want, text)
		}
	}
	if again := analyse(t, specPath, filePath); len(again.Added) != 0 || again.Statuses != 0 || read(t, filePath) != text {
		t.Errorf("second analyse changed the file: %d added, %d statuses", len(again.Added), again.Statuses)
	}
}

// save reads deeply nested values of the request and of the answer, list
// elements and escaped field names included.
func TestSaveDeepValues(t *testing.T) {
	specPath, filePath := workspace(t)
	file := `Dock:
  - POST /docks:
      body: {name: North, capacity: 4}
      save: {dockId: /id}
Ship:
  - POST /ships:
      body: {name: Comet, dockId: '{{dockId}}'}
      save: {shipId: /id}
Booking:
  - POST /bookings:
      body:
        shipId: '{{shipId}}'
        route: {originDockId: '{{dockId}}', departure: "2027-01-15T08:00:00Z"}
        crew:
          - {pilotName: Ada, role: CAPTAIN}
          - {pilotName: Bob, role: NAVIGATOR}
      save:
        sentDock: request /route/originDockId
        secondPilot: request /crew/1/pilotName
        firstRole: request /crew/0/role
        crew: request /crew
        answerPilot: /crew/1/pilotName
  - GET /bookings/{bookingId}:
      path: {bookingId: 1}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newStarport())
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res.Problems))
	}
	vars := map[string]any{}
	f := mustBound(t, specPath, filePath)
	for _, st := range f.Steps {
		r, _ := st.request(vars)
		if st.Response != nil {
			if miss := st.saveFrom(st.Response, nil, r, vars); len(miss) > 0 {
				t.Errorf("%s: missing %v", st, miss)
			}
		}
	}
	if text(vars["sentDock"]) != "1" || vars["secondPilot"] != "Bob" || vars["firstRole"] != "CAPTAIN" || vars["answerPilot"] != "Bob" ||
		text(vars["crew"]) != `[{"pilotName":"Ada","role":"CAPTAIN"},{"pilotName":"Bob","role":"NAVIGATOR"}]` {
		t.Errorf("vars: %s", text(vars))
	}
	st := &Step{Save: []Save{{"a", "request /x~1y/0/z"}, {"b", "request /missing/deep"}}}
	got := map[string]any{}
	miss := st.saveFrom(&Response{}, nil, &request{body: map[string]any{"x/y": []any{map[string]any{"z": "ok"}}}, hasBody: true}, got)
	if got["a"] != "ok" || len(miss) != 1 || miss[0] != "request /missing/deep" {
		t.Errorf("escaped: %v, missing %v", got, miss)
	}
}

// -analyse finds a value no binding names at any depth of an earlier
// answer or request.
func TestFieldPointer(t *testing.T) {
	s := loadSpec(t, specFile)
	book := requestMedia(s.Op("createBooking")).Schema.Value
	for name, want := range map[string]string{"shipId": "/shipId", "originDockId": "/route/originDockId", "PILOTNAME": "/crew/0/pilotName"} {
		if got, ok := fieldPointer(book, name); !ok || got != want {
			t.Errorf("fieldPointer(%s) = %q %v, want %q", name, got, ok, want)
		}
	}
	if _, ok := fieldPointer(book, "nothing"); ok {
		t.Error("a field that is not there")
	}
	if escapePointer("a/b~c") != "a~1b~0c" {
		t.Error("escapePointer")
	}
}
