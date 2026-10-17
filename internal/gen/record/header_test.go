package record

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/bind"
)

// headerPort serves testdata/gen/record-header.yaml: pilots count from 1,
// a ship answers with ticket 100+n in the body and its key n in X-Ship-Id,
// dock 7 is seed data.
type headerPort struct {
	mu            sync.Mutex
	pilots, ships int
}

func (hp *headerPort) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hp.mu.Lock()
	defer hp.mu.Unlock()
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	var id int
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/pilots":
		hp.pilots++
		answer(201, map[string]any{"id": hp.pilots, "name": body["name"]})
	case r.Method == http.MethodPost && r.URL.Path == "/ships":
		hp.ships++
		w.Header().Set("X-Ship-Id", fmt.Sprint(hp.ships))
		answer(201, map[string]any{"id": 100 + hp.ships, "name": body["name"]})
	case sscan(r.URL.Path, "/ship-pilots/%d", &id) && id >= 1 && id <= hp.pilots:
		answer(200, map[string]any{"id": id, "name": "Ada"})
	case sscan(r.URL.Path, "/ship-docks/%d", &id) && id == 7:
		answer(200, map[string]any{"id": 7, "name": "Seed"})
	case sscan(r.URL.Path, "/bookings/%d", &id) && id >= 1 && id <= hp.ships:
		answer(200, []any{})
	default:
		answer(404, map[string]any{"message": "not found"})
	}
}

func sscan(path, format string, id *int) bool {
	_, err := fmt.Sscanf(path, format, id)
	return err == nil
}

// apitest guesses {id} of getShipPilot and getShipDock and {shipId} of
// listShipBookings from createShip's body /id. The record file knows
// better: the pilot comes from createPilot, the dock is a fixed value, the
// ship key from the header. record writes what the record file says, and
// apitest passes a fresh instance.
func TestRecordBindingsFollowTheFile(t *testing.T) {
	specPath, filePath := workspaceOf(t, "../../../testdata/gen/record-header.yaml")
	file := `vars:
  dockId: 7
Pilot:
  - POST /pilots:
      body: {name: Ada}
      save: {pilotId: /id}
Ship:
  - POST /ships:
      body: {name: Comet}
      save: {shipId: header X-Ship-Id}
  - GET /ship-pilots/{id}:
      path: {id: '{{pilotId}}'}
  - GET /ship-docks/{id}:
      path: {id: '{{dockId}}'}
Booking:
  - GET /bookings/{shipId}:
      path: {shipId: '{{shipId}}'}
`
	if err := os.WriteFile(filePath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	if set, err := bind.Resolve(loadSpec(t, specPath)); err != nil || len(set.Findings) != 3 {
		t.Fatalf("guessed bindings before: %v %+v", err, set)
	}
	srv := httptest.NewServer(&headerPort{})
	defer srv.Close()
	res := record(t, specPath, filePath, srv)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res.Problems))
	}
	text := read(t, specPath)
	for _, want := range []string{
		"example: 1, x-apitest-bind: {from: createPilot, pointer: /id}}",
		"example: 7, x-apitest-bind: false}",
		"example: 1, x-apitest-bind: {from: createShip, header: X-Ship-Id}}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("spec misses %q:\n%s", want, text)
		}
	}
	got := notes(res.Notes)
	for _, want := range []string{
		"BIND GET /ship-pilots/{id}: {id} takes its value from createPilot (body /id), as the record file saves it; written as x-apitest-bind, apitest guessed createShip (body /id) before",
		"BIND GET /ship-docks/{id}: {id} has the fixed value 7 in the record file; written as x-apitest-bind: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notes miss %q:\n%s", want, got)
		}
	}
	set, err := bind.Resolve(loadSpec(t, specPath))
	if err != nil || len(set.Findings) != 0 {
		t.Errorf("guessed bindings after: %v %+v", err, set)
	}
	if again := record(t, specPath, filePath, nil); len(again.Problems) > 0 || again.Binds != 0 || read(t, specPath) != text {
		t.Errorf("a second run changes the spec: binds %d, problems:\n%s", again.Binds, notes(again.Problems))
	}
	fresh := httptest.NewServer(&headerPort{})
	defer fresh.Close()
	apitest.Run(t, apitest.Config{SpecPath: specPath, BaseURL: fresh.URL, Tags: []string{"Pilot", "Ship", "Booking"}, DisableReports: true})
}
