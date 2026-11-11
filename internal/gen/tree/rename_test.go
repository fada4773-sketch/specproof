package tree

import (
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// renameTree has docks with an update in the short form (with comment)
// and a patch.
const renameTree = `
order:
  - docks
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
        pick: {first: 1}
        key: [/id]
        write:
          create: POST /planets/{code}/docks code=planets.code
          update: PUT /docks/{dockId} # updateDock
          patch: PATCH /docks/{dockId}
`

// TestRenameDefault: -fix gives an update without change the change of its
// name ("-neu" after it); the short form becomes a mapping with its
// comment, a second -fix adds nothing, the patch keeps auto. The scenario
// sends the new name and the read after it holds it.
func TestRenameDefault(t *testing.T) {
	s := loadStarport(t)
	fixed, done, err := Repair(s, parse(t, renameTree))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	out := string(b)
	if len(done) != 1 || !strings.Contains(done[0], "docks.write.update: add change: {name: '{{.name}}-neu'}") {
		t.Errorf("done: %v", done)
	}
	if !strings.Contains(out, "          update:\n            call: PUT /docks/{dockId} # updateDock\n            change: {name: '{{.name}}-neu'}\n") {
		t.Errorf("the update after -fix:\n%s", out)
	}
	if w := fixed.Node("docks").Write["patch"]; w.Change != nil {
		t.Errorf("the patch keeps auto: %v", w.Change)
	}
	if _, again, err := Repair(s, fixed); err != nil || len(again) != 0 {
		t.Errorf("a second -fix: %v %v", again, err)
	}
	_, base := stage(t)
	tr, d := fetch(t, s, out, base)
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	var st *scenario.Step
	for _, x := range built.Scenario.Steps {
		if x.ID == "docks1.update" {
			st = x
		}
	}
	if st == nil {
		t.Fatalf("no update of docks1:\n%s", out)
	}
	m, _ := st.Body.(map[string]any)
	if !strings.HasSuffix(text(m["name"]), "-neu") || len(m) < 2 || !strings.Contains(st.Why, "changes name") {
		t.Errorf("the update sends the whole body with the new name: %v (%s)", st.Body, st.Why)
	}
	if res, log := runEmpty(t, built); res.Failed {
		t.Errorf("the scenario failed on an empty instance:\n%s", log)
	}
}

// TestRenameDefaultLeaves: -fix leaves an update with change (also auto)
// or body, and one whose record is read or written by its name.
func TestRenameDefaultLeaves(t *testing.T) {
	s := loadStarport(t)
	for name, file := range map[string]string{
		"auto":          strings.Replace(renameTree, "update: PUT /docks/{dockId} # updateDock", "update:\n            call: PUT /docks/{dockId}\n            change: auto", 1),
		"change":        strings.Replace(renameTree, "update: PUT /docks/{dockId} # updateDock", "update:\n            call: PUT /docks/{dockId}\n            change: {capacity: 9}", 1),
		"body":          strings.Replace(renameTree, "update: PUT /docks/{dockId} # updateDock", "update:\n            call: PUT /docks/{dockId}\n            body: {name: N, capacity: 2}", 1),
		"key":           strings.Replace(renameTree, "        key: [/id]\n        write:\n          create: POST /planets", "        key: [/id, /name]\n        write:\n          create: POST /planets", 1),
		"read by name":  renameTree + "        each:\n          named:\n            call: GET /planets/{code}/docks\n            path: {code: planets.code}\n            query: {name: .name}\n",
		"other by name": renameTree + "  report: GET /reports/{reportNo} reportNo=docks.name\n",
	} {
		_, done, err := Repair(s, parse(t, file))
		if err != nil || len(done) != 0 {
			t.Errorf("%s: -fix made %v (%v)", name, done, err)
		}
	}
}
