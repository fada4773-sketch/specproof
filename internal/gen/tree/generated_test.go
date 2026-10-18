package tree

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// bookingSpec: the bookings of a dock are known by their slot; the
// instance gives each an id, which the create answers in Ack (Id, the
// spelling differs) and the receipt of a booking is read by.
const bookingSpec = `
openapi: 3.0.3
info: {title: bookings, version: "1"}
paths:
  /docks:
    get:
      operationId: listDocks
      responses: {"200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/Dock"}}}}}}
  /docks/{code}/bookings:
    parameters: [{name: code, in: path, required: true, schema: {type: string}}]
    get:
      operationId: listBookings
      responses: {"200": {description: ok, content: {application/json: {schema: {type: array, items: {$ref: "#/components/schemas/Booking"}}}}}}
    post:
      operationId: createBooking
      requestBody: {required: true, content: {application/json: {schema: {$ref: "#/components/schemas/Booking"}}}}
      responses: {"201": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Ack"}}}}}
  /docks/{code}/bookings/{slot}:
    parameters:
      - {name: code, in: path, required: true, schema: {type: string}}
      - {name: slot, in: path, required: true, schema: {type: string}}
    delete:
      operationId: deleteBooking
      responses: {"204": {description: gone}}
  /bookings/{bookingId}/receipt:
    parameters: [{name: bookingId, in: path, required: true, schema: {type: integer}}]
    get:
      operationId: getReceipt
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Receipt"}}}}
        "404": {description: no such booking}
components:
  schemas:
    Dock:
      type: object
      properties: {id: {type: integer}, code: {type: string}}
    Booking:
      type: object
      required: [slot, pilot]
      properties: {id: {type: integer}, slot: {type: string}, pilot: {type: string}}
    Ack:
      type: object
      required: [Message]
      properties: {Id: {type: integer}, Message: {type: string}}
    Receipt:
      type: object
      properties: {bookingId: {type: integer}, total: {type: integer}}
`

// bookingTree reads the receipt of each booking by .id; key lacks /id.
const bookingTree = `
order:
  - bookings
tree:
  docks:
    call: GET /docks
    pick: all
    key: [/code]
    write: {create: none}
    each:
      bookings:
        call: GET /docks/{code}/bookings
        path: {code: .code}
        pick: all
        key: [/slot]
        write:
          create: POST /docks/{code}/bookings code=docks.code
          delete: DELETE /docks/{code}/bookings/{slot} code=docks.code slot=.slot
        each:
          receipt: GET /bookings/{bookingId}/receipt bookingId=.id
`

// bookingServer serves docks north and south and their bookings; ids
// start at first. It ignores an id in the body of a create, as an
// instance does that gives ids; ack false answers without Id.
func bookingServer(first int, ack bool) http.Handler {
	type booking struct {
		ID         int    `json:"id"`
		Slot       string `json:"slot"`
		Pilot      string `json:"pilot"`
		dock, gone string
	}
	var mu sync.Mutex
	var all []*booking
	next := first
	if first > 1 {
		all = []*booking{{ID: first, Slot: "A1", Pilot: "Vega", dock: "north"}, {ID: first + 1, Slot: "B2", Pilot: "Rigel", dock: "south"}}
		next = first + 2
	}
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /docks", func(w http.ResponseWriter, _ *http.Request) {
		write(w, 200, []map[string]any{{"id": 1, "code": "north"}, {"id": 2, "code": "south"}})
	})
	mux.HandleFunc("GET /docks/{code}/bookings", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		out := []*booking{}
		for _, b := range all {
			if b.dock == r.PathValue("code") && b.gone == "" {
				out = append(out, b)
			}
		}
		write(w, 200, out)
	})
	mux.HandleFunc("POST /docks/{code}/bookings", func(w http.ResponseWriter, r *http.Request) {
		var in booking
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Slot == "" {
			write(w, 400, map[string]any{"Message": "slot and pilot"})
			return
		}
		mu.Lock()
		b := &booking{ID: next, Slot: in.Slot, Pilot: in.Pilot, dock: r.PathValue("code")}
		next++
		all = append(all, b)
		mu.Unlock()
		if ack {
			write(w, 201, map[string]any{"Id": b.ID, "Message": "created"})
			return
		}
		write(w, 201, map[string]any{"Message": "created"})
	})
	mux.HandleFunc("DELETE /docks/{code}/bookings/{slot}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for _, b := range all {
			if b.dock == r.PathValue("code") && b.Slot == r.PathValue("slot") && b.gone == "" {
				b.gone = "yes"
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("GET /bookings/{bookingId}/receipt", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("bookingId"))
		mu.Lock()
		defer mu.Unlock()
		for _, b := range all {
			if b.ID == id && b.gone == "" {
				write(w, 200, map[string]any{"bookingId": b.ID, "total": 40})
				return
			}
		}
		write(w, 404, map[string]any{"Message": "no booking " + r.PathValue("bookingId")})
	})
	return mux
}

func bookingFiles(t *testing.T, ack bool) (*spec.Spec, string) {
	t.Helper()
	text := bookingSpec
	if !ack {
		text = strings.Replace(text, "properties: {Id: {type: integer}, Message: {type: string}}", "properties: {Message: {type: string}}", 1)
	}
	path := filepath.Join(t.TempDir(), "bookings.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// TestGeneratedIDCheck: a node whose records the test creates names the
// id the instance gives them in key; -fix adds it, the proposal has it.
func TestGeneratedIDCheck(t *testing.T) {
	s, _ := bookingFiles(t, true)
	tr := parse(t, bookingTree)
	want := "bookings: key lacks /id: the instance gives the id of a record it creates"
	change := "bookings: key: [/slot] → key: [/slot, /id] (-fix does it)"
	if problems := Check(s, tr); !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) && strings.Contains(p, change) }) {
		t.Errorf("want %q with %q, got %v", want, change, problems)
	}
	fixed, done, err := Repair(s, tr)
	if err != nil || len(done) == 0 {
		t.Fatalf("-fix: %v %v", done, err)
	}
	if b, _ := fixed.Bytes(); !strings.Contains(string(b), "key: [/slot, /id]") || len(Check(s, fixed)) > 0 {
		t.Errorf("after -fix: %v\n%s", Check(s, fixed), b)
	}
	// a create that requires the id: the client gives it
	required := strings.Replace(bookingSpec, "required: [slot, pilot]", "required: [id, slot, pilot]", 1)
	path := filepath.Join(t.TempDir(), "required.yaml")
	if err := os.WriteFile(path, []byte(required), 0o644); err != nil {
		t.Fatal(err)
	}
	given, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if problems := Check(given, tr); len(problems) > 0 {
		t.Errorf("an id the client gives: %v", problems)
	}
	proposed := Propose(s)
	var found bool
	proposed.Walk(func(n *Node) {
		if n.Call == "GET /docks/{code}/bookings" {
			found = true
			if !slices.Contains(n.Key, "/id") {
				t.Errorf("proposal: key of %s: %v", n.Name, n.Key)
			}
		}
	})
	if !found {
		t.Error("the proposal has no list of bookings")
	}
}

// TestGeneratedIDRebuild: the stage gave the bookings 456 and 457, the
// empty instance gives 1 and 2. The create leaves the id out and saves the
// one the instance gives: from Id of the answer, else by a step that
// finds the booking by its slot; the receipt is read by it.
func TestGeneratedIDRebuild(t *testing.T) {
	for _, ack := range []bool{true, false} {
		s, path := bookingFiles(t, ack)
		srv := httptest.NewServer(bookingServer(456, ack))
		tr := parse(t, strings.Replace(bookingTree, "key: [/slot]", "key: [/slot, /id]", 1))
		d, err := Fetch(context.Background(), s, tr, Options{BaseURL: srv.URL})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		built, err := Build(s, tr, d)
		if err != nil {
			t.Fatal(err)
		}
		text, _ := built.Scenario.Encode(" test")
		create, receipt := built.Scenario.Step("bookings1.create"), built.Scenario.Step("bookings1.receipt.read")
		if receipt == nil {
			for _, st := range built.Scenario.Steps {
				if st.Call == "GET /bookings/{bookingId}/receipt" {
					receipt = st
					break
				}
			}
		}
		if create == nil || receipt == nil || hasField(create.Body, "id") || receipt.Path["bookingId"] != "{{bookings1.id}}" {
			t.Fatalf("ack %v: no id in the create, the receipt by the saved id:\n%s", ack, text)
		}
		find := built.Scenario.Step("bookings1.find")
		switch {
		case ack && (create.Save["bookings1.id"] != "/Id" || find != nil):
			t.Errorf("the id from Id of the answer:\n%s", text)
		case !ack && (find == nil || find.Save["bookings1.id"] != "/[slot={{bookings1.slot}}]/id"):
			t.Errorf("a step finds the id by the slot:\n%s", text)
		}
		file := filepath.Join(t.TempDir(), "tree-test_gen.yaml")
		if err := os.WriteFile(file, text, 0o644); err != nil {
			t.Fatal(err)
		}
		// a run, -update, and a run that compares with the answers written
		for _, update := range []bool{false, true, false} {
			var out strings.Builder
			res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
				Config:   apitest.Config{SpecPath: path, Handler: bookingServer(1, ack)},
				Scenario: file,
				Update:   update,
			}, apitest.StandaloneOptions{Out: &out})
			if err != nil || res.Failed {
				written, _ := os.ReadFile(file)
				t.Errorf("ack %v, update %v: the run in an empty instance: %v\n%s\n%s", ack, update, err, out.String(), written)
			}
		}
	}
}
