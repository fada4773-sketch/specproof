package tree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// droppedSpec: the create takes allOf [ShipRead, own fields]; ShipRead has
// a readOnly id and status.
const droppedSpec = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /ships:
    post:
      operationId: createShip
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ShipWrite'}
      responses:
        "201": {description: ok}
components:
  schemas:
    ShipRead:
      type: object
      properties:
        id: {type: integer, readOnly: true}
        status: {type: string, readOnly: true}
        name: {type: string}
        callsign: {type: string, nullable: true}
        pilot:
          type: object
          properties:
            name: {type: string}
        crew:
          type: array
          items:
            type: object
            properties:
              role: {type: string}
    ShipWrite:
      allOf:
        - $ref: '#/components/schemas/ShipRead'
        - type: object
          properties:
            dockId: {type: integer}
`

func newProjection() *projection {
	return &projection{root: "ships.ShipWrite", drop: map[string]string{}, fixed: map[string]bool{}}
}

// TestBodyDroppedNote: a body built from a record notes every field of the
// record it leaves out, with the reason, also inside objects and lists;
// the fields the request takes stay.
func TestBodyDroppedNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(path, []byte(droppedSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{"id": 7, "status": "docked", "name": "Comet", "callsign": nil, "dockId": 3,
		"pilot":     map[string]any{"name": "Vega", "rank": 2},
		"crew":      []any{map[string]any{"role": "cook", "since": 1}, map[string]any{"role": "medic", "since": 2}},
		"createdAt": "2026-01-01"}
	b := &builder{out: &Built{}}
	pj := newProjection()
	ref := requestRef(lookupOp(s, "createShip"))
	out, _ := b.project("ships.create", rec, ref.Value, pj.root, "ShipWrite", 0, pj).(map[string]any)
	b.noteDropped("ships.create", "ShipWrite", pj)

	for _, k := range []string{"name", "dockId", "pilot", "crew"} {
		if _, ok := out[k]; !ok {
			t.Errorf("the body lacks %s: %v", k, out)
		}
	}
	if len(b.out.Notes) != 1 {
		t.Fatalf("one note for the body: %v", b.out.Notes)
	}
	n := b.out.Notes[0]
	if n.Step != "ships.create" {
		t.Errorf("the note stands at the create: %q", n.Step)
	}
	want := "6 fields of the record left out: callsign, createdAt, crew[].since, id, pilot.rank, status"
	if !strings.HasPrefix(n.What, want) {
		t.Errorf("what:\n got %q\nwant %q", n.What, want)
	}
	for _, w := range []string{"readOnly of ShipWrite: id, status", "null in the record: callsign", "not in the schema of ShipWrite: createdAt, crew[].since, pilot.rank"} {
		if !strings.Contains(n.Why, w) {
			t.Errorf("why lacks %q: %q", w, n.Why)
		}
	}

	b = &builder{out: &Built{}}
	pj = newProjection()
	b.project("ships.create", map[string]any{"name": "Comet"}, ref.Value, pj.root, "ShipWrite", 0, pj)
	b.noteDropped("ships.create", "ShipWrite", pj)
	if len(b.out.Notes) != 0 {
		t.Errorf("nothing left out, no note: %v", b.out.Notes)
	}
}
