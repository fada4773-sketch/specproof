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
	if code != 0 || !strings.Contains(out, ": - ships → - ships: {…, leadPilotId: shipPilot.id}") || !strings.Contains(out, "-fix, round 1:") ||
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

// TestTreeFixLoop: with -base-url and -fix one run fetches, builds, makes
// the changes the build finds (a node below the ships that reads the pilots
// both fields of an element name), fetches that node again and builds until
// no error is left; the review marks the notes with their level.
func TestTreeFixLoop(t *testing.T) {
	specPath, file := treeProject(t)
	b, _ := os.ReadFile(specPath)
	props := "        leadDetails:\n          type: array\n          items:\n            type: object\n            properties:\n              leadPilotId: {type: integer}\n              leadPilot:\n                type: object\n                properties:\n                  leadPilotId: {type: integer}\n                  name: {type: string}"
	src := strings.NewReplacer(
		"required: [name, pilotId]", "required: [name, leadDetails]",
		"        name: {type: string, minLength: 1}\n        pilotId: {type: integer}", "        name: {type: string, minLength: 1}\n"+props,
		"required: [id, dockId, name, pilotId]", "required: [id, dockId, name, leadDetails]",
		"        name: {type: string}\n        pilotId: {type: integer}", "        name: {type: string}\n"+props,
	).Replace(string(b))
	if strings.Count(src, "leadDetails:") != 2 {
		t.Fatal("the Starport spec changed: the ships keep pilotId")
	}
	if err := os.WriteFile(specPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimSuffix(file, "tree.yaml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file0 := strings.NewReplacer(
		"    pick: 1\n    off: true\n", "    pick: all\n",
		"            each:\n              shipPilot: GET /pilots/{pilotId} pilotId=.leadPilotId\n", "",
	).Replace(leadTree)
	if err := os.WriteFile(file, []byte(file0), 0o644); err != nil {
		t.Fatal(err)
	}
	st := testserver.NewStarport(starportToken, 100)
	st.PilotField = "leadDetails"
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)

	code, out, errOut := cli("tree", "-spec", specPath, "-file", file, "-base-url", hs.URL+"/api", "-token", starportToken, "-fix")
	if code != 0 || !strings.Contains(out, "-fix, round 1:") || !strings.Contains(out, "add below ships the node shipPilot") ||
		!strings.Contains(out, "the node the fix before adds") || strings.Contains(out, "-fix, round 2:") || strings.Contains(out, "body fields send a key") {
		t.Fatalf("one run: %d\n%s%s", code, out, errOut)
	}
	if strings.Count(out, "fetching from") != 2 {
		t.Errorf("the whole tree, then ships again:\n%s", out)
	}
	test, _ := os.ReadFile(dir + "tree-test_gen.yaml")
	if !strings.Contains(string(test), "leadPilotId: '{{pilots") {
		t.Errorf("the creates send the pilots the test creates:\n%s", test)
	}
	review, _ := os.ReadFile(dir + "tree-review_gen.md")
	if strings.Contains(string(review), "error: ") || !strings.Contains(string(review), "info: ") {
		t.Errorf("no error left, the notes marked:\n%s", review)
	}
}
