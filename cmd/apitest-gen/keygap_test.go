package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// leadTree reads the pilot of a ship by leadPilotId, which the model takes
// for no reference: order gives it no source.
const leadTree = `order:
  - pilots
  - docks
  - ships
tree:
  pilots:
    call: GET /pilots
    pick: 1
    off: true
    key: [/id]
    write:
      create: POST /pilots
  planets:
    call: GET /planets
    pick: [mars]
    key: [/code]
    write: {create: none}
    each:
      docks:
        call: GET /planets/{code}/docks
        path: {code: .code}
        pick: {first: 1}
        key: [/id]
        write:
          create: POST /planets/{code}/docks code=planets.code
        each:
          ships:
            call: GET /docks/{dockId}/ships
            path: {dockId: .id}
            pick: all
            key: [/id]
            write:
              create: POST /docks/{dockId}/ships dockId=docks.id
            each:
              shipPilot: GET /pilots/{pilotId} pilotId=.leadPilotId
`

// TestTreeFixKeyOfTheStage: a body field that sends a key of the stage
// (leadPilotId) is named after the build with the hint to -fix; -fix gives
// it its source in order and builds again from the same records.
func TestTreeFixKeyOfTheStage(t *testing.T) {
	specPath, file := treeProject(t)
	b, _ := os.ReadFile(specPath)
	src := strings.NewReplacer(
		"required: [name, pilotId]", "required: [name, leadPilotId]",
		"        name: {type: string, minLength: 1}\n        pilotId: {type: integer}", "        name: {type: string, minLength: 1}\n        leadPilotId: {type: integer}",
		"required: [id, dockId, name, pilotId]", "required: [id, dockId, name, leadPilotId]",
		"        name: {type: string}\n        pilotId: {type: integer}", "        name: {type: string}\n        leadPilotId: {type: integer}",
	).Replace(string(b))
	if strings.Count(src, "leadPilotId") != 4 {
		t.Fatal("the Starport spec changed: the ships keep pilotId")
	}
	if err := os.WriteFile(specPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(strings.TrimSuffix(file, "tree.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(leadTree), 0o644); err != nil {
		t.Fatal(err)
	}
	st := testserver.NewStarport(starportToken, 100)
	st.PilotField = "leadPilotId"
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)

	code, out, errOut := cli("tree", "-spec", specPath, "-file", file, "-base-url", hs.URL+"/api", "-token", starportToken)
	if code != 0 || !strings.Contains(out, "1 body fields send a key of the stage of a record the test creates") || !strings.Contains(out, "add -fix to the same command") {
		t.Fatalf("build: %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = cli("tree", "-spec", specPath, "-file", file, "-fix")
	if code != 0 || !strings.Contains(out, ": - ships → - ships: {…, leadPilotId: shipPilot.id}") || !strings.Contains(out, "built again") ||
		strings.Contains(out, "body fields send a key") {
		t.Fatalf("-fix: %d\n%s%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "  - ships: {leadPilotId: shipPilot.id}\n") {
		t.Errorf("the source in order:\n%s", b)
	}
	test, _ := os.ReadFile(strings.TrimSuffix(file, "tree.yaml") + "tree-test_gen.yaml")
	if !strings.Contains(string(test), "leadPilotId: '{{shipPilot") {
		t.Errorf("the create sends the pilot the test creates:\n%s", test)
	}
}
