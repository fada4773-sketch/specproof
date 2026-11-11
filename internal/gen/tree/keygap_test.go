package tree

import (
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// stage serves a filled Starport of the variant.
func (v listStarport) stage(t *testing.T) string {
	t.Helper()
	st := v.server(100)
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)
	return hs.URL + "/api"
}

// buildFrom fetches the records of a tree file from the stage and builds the
// test.
func buildFrom(t *testing.T, s *spec.Spec, text, base string) (*Tree, *Built) {
	t.Helper()
	tr, d := fetch(t, s, text, base)
	b, err := Build(s, tr, d)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, text)
	}
	return tr, b
}

// TestListRefsDetails: an element of a list that names its record twice,
// by its key and in the embedded record whose key carries the resource
// name (crewDetails: [{pilotId, pilot: {pilotId}}]): the check names both
// fields, -fix adds one node that reads the pilots and gives both their
// source; the create sets both in each element, the test runs on an empty
// instance.
func TestListRefsDetails(t *testing.T) {
	v := detailsStarport
	s, path := v.load(t)
	tr := parse(t, v.withoutSource())
	problems := Check(s, tr)
	for _, f := range []string{"crewDetails.pilotId", "crewDetails.pilot.pilotId"} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "order: ships."+f+" holds the id of a pilot") }) {
			t.Errorf("the check names %s: %v", f, problems)
		}
	}
	fixed, _, err := Repair(s, tr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	if left := Check(s, fixed); len(left) > 0 {
		t.Fatalf("-fix left %v\n%s", left, b)
	}
	ships := fixed.Node("ships")
	if len(ships.Each) != 1 {
		t.Errorf("one node reads the pilots of both fields:\n%s", b)
	}
	e := fixed.Entry("ships")
	if e.Values["crewDetails.pilotId"] == nil || e.Values["crewDetails.pilotId"] != e.Values["crewDetails.pilot.pilotId"] {
		t.Errorf("both fields from that node: %v", e.Values)
	}
	_, built := buildFrom(t, s, string(b), v.stage(t))
	creates := 0
	for _, st := range built.Scenario.Steps {
		if st.Call != "POST /docks/{dockId}/ships" {
			continue
		}
		creates++
		for _, f := range []string{"crewDetails.pilotId", "crewDetails.pilot.pilotId"} {
			got, _ := Field(st.Body, f)
			l, _ := got.([]any)
			if len(l) != 2 || slices.ContainsFunc(l, func(x any) bool { return !strings.HasPrefix(text(x), "{{") }) {
				t.Errorf("%s: %s holds the placeholders of the pilots: %v", st.ID, f, st.Body)
			}
		}
	}
	if creates == 0 {
		t.Fatal("no create of a ship")
	}
	if len(built.Fixes) > 0 {
		t.Errorf("nothing left to fix: %v", built.Fixes)
	}
	if res, log := runEmptyAt(t, path, v.server(1), built); res.Failed {
		t.Errorf("the scenario failed on an empty instance:\n%s", log)
	}
}

// leadTree reads the pilot of a ship by leadPilotId, without a source in
// order: the model takes leadPilotId for no reference, the check says
// nothing.
var leadTree = strings.NewReplacer(
	"  - ships: {pilotId: shipPilot.id}\n", "  - ships\n",
	"pilotId=.pilotId", "pilotId=.leadPilotId",
).Replace(orderTree)

// TestKeyGapFix: a body field that sends the key of the stage of a record
// the test creates (leadPilotId = the id of a pilot) is noted at its step
// with the change; the build gives the fix, -fix makes it: the source in
// order from the node below that reads the pilot by the field. Built again,
// the create sends the placeholder and the test runs on an empty instance.
func TestKeyGapFix(t *testing.T) {
	v := leadStarport
	s, path := v.load(t)
	base := v.stage(t)
	tr := parse(t, leadTree)
	if problems := Check(s, tr); len(problems) > 0 {
		t.Fatalf("the check knows nothing of leadPilotId: %v", problems)
	}
	tr, built := buildFrom(t, s, leadTree, base)
	if len(built.Fixes) != 1 || built.Fixes[0].Change != "- ships → - ships: {…, leadPilotId: shipPilot.id}" || built.Fixes[0].NeedsFetch() {
		t.Fatalf("one fix, the source in order: %+v", built.Fixes)
	}
	noted := slices.ContainsFunc(built.Notes, func(n Note) bool {
		return strings.HasPrefix(n.Step, "ships") && strings.Contains(n.What, "leadPilotId = ") && strings.HasSuffix(n.What, " is a key of the stage") &&
			strings.Contains(n.Why, "the test creates shipPilot1 anew with another key") && strings.Contains(n.Why, "(-fix does it)")
	})
	if !noted {
		t.Errorf("the note at the create: %+v", built.Notes)
	}
	fixed, _, err := ApplyFixes(tr, built.Fixes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	_, built = buildFrom(t, s, string(b), base)
	if len(built.Fixes) > 0 {
		t.Errorf("nothing left to fix: %v", built.Fixes)
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /docks/{dockId}/ships" && !strings.HasPrefix(text(st.Body.(map[string]any)["leadPilotId"]), "{{") {
			t.Errorf("%s sends the pilot the test created: %v", st.ID, st.Body)
		}
	}
	if res, log := runEmptyAt(t, path, v.server(1), built); res.Failed {
		t.Errorf("the scenario failed on an empty instance:\n%s", log)
	}
}

// TestKeyGapFixAddsNode: without a node below the ship that reads its
// pilot, the fix adds the read by the key below the ships and the source;
// the records must be fetched again.
func TestKeyGapFixAddsNode(t *testing.T) {
	v := leadStarport
	s, _ := v.load(t)
	file := strings.NewReplacer(
		"    pick: 1\n    off: true\n", "    pick: all\n",
		"            each:\n              shipPilot: GET /pilots/{pilotId} pilotId=.leadPilotId\n", "",
	).Replace(leadTree)
	tr, built := buildFrom(t, s, file, v.stage(t))
	if len(built.Fixes) != 1 || !built.Fixes[0].NeedsFetch() ||
		!strings.Contains(built.Fixes[0].Change, "add below ships the node pilot: GET /pilots/{pilotId} pilotId=.leadPilotId, and - ships: {…, leadPilotId: pilot.id}") {
		t.Fatalf("the fix adds the read below the ships: %+v", built.Fixes)
	}
	fixed, _, err := ApplyFixes(tr, built.Fixes)
	if err != nil {
		t.Fatal(err)
	}
	if n := fixed.Node("pilot"); n == nil || n.Parent != fixed.Node("ships") || fixed.Entry("ships").Values["leadPilotId"] != "pilot.id" {
		b, _ := fixed.Bytes()
		t.Errorf("the node and the source:\n%s", b)
	}
}

// leadDetailsStarport names each pilot twice in an element of a list,
// under names the model takes for no reference: only the values tell.
var leadDetailsStarport = listStarport{name: "lead details", mode: "leadDetails", field: "leadDetails.leadPilotId",
	props: "        leadDetails:\n          type: array\n          items:\n            type: object\n            properties:\n              leadPilotId: {type: integer}\n              leadPilot:\n                type: object\n                properties:\n                  leadPilotId: {type: integer}\n                  name: {type: string}"}

// TestKeyGapFixesShareTheNode: two fields of one list element that send
// keys of the stage of the same records get one node: the first fix adds
// it, the second names it. Both are made (before, the second failed: the
// node was there), and after the fetch the test runs on an empty instance.
func TestKeyGapFixesShareTheNode(t *testing.T) {
	v := leadDetailsStarport
	s, path := v.load(t)
	base := v.stage(t)
	file := strings.NewReplacer("    pick: 1\n    off: true\n", "    pick: all\n").Replace(v.withoutSource())
	if problems := Check(s, parse(t, file)); len(problems) > 0 {
		t.Fatalf("the check knows nothing of leadDetails: %v", problems)
	}
	tr, built := buildFrom(t, s, file, base)
	if len(built.Fixes) != 2 || built.Fixes[0].adds == "" || built.Fixes[1].adds != "" || !strings.Contains(built.Fixes[1].Change, "the node the fix before adds") {
		t.Fatalf("one node, two sources: %+v", built.Fixes)
	}
	if len(built.Errors()) == 0 {
		t.Error("the keys of the stage are errors")
	}
	fixed, done, err := ApplyFixes(tr, built.Fixes)
	if err != nil || len(done) != 2 {
		t.Fatalf("both fixes: %v %v", done, err)
	}
	b, _ := fixed.Bytes()
	if n := len(fixed.Node("ships").Each); n != 1 {
		t.Errorf("one node below the ships, got %d:\n%s", n, b)
	}
	_, built = buildFrom(t, s, string(b), base)
	if len(built.Fixes) > 0 || len(built.Errors()) > 0 {
		t.Fatalf("nothing left: %v %v", built.Fixes, built.Errors())
	}
	if res, log := runEmptyAt(t, path, v.server(1), built); res.Failed {
		t.Errorf("the scenario failed on an empty instance:\n%s", log)
	}
}

// TestNoteLevels: each note has its level; in the timeline of the test the
// errors of a step come first, as "error: …" in the result and as notes
// with their level; the section of the test counts them.
func TestNoteLevels(t *testing.T) {
	v := leadStarport
	s, _ := v.load(t)
	_, built := buildFrom(t, s, leadTree, v.stage(t))
	if len(built.Errors()) == 0 {
		t.Fatal("the keys of the stage are errors")
	}
	for _, n := range built.Notes {
		if n.Level != LevelError && n.Level != LevelWarn && n.Level != LevelInfo {
			t.Errorf("a note without level: %+v", n)
		}
	}
	found := false
	for _, e := range TestTimeline(s, built) {
		if e.Step != "ships1.create" {
			continue
		}
		found = true
		if len(e.Notes) < 2 || e.Notes[0].Level != LevelError || !strings.Contains(e.Result, "error: leadPilotId = ") || !strings.Contains(e.Result, "info: ") {
			t.Errorf("the notes of the create, the error first: %+v", e)
		}
	}
	if !found {
		t.Fatal("no create of ships1 in the timeline")
	}
	if summary, _ := ScenarioSection(built); !strings.Contains(summary, "errors") {
		t.Errorf("the summary counts the errors: %s", summary)
	}
}

// otherReadTree lists the ships of a dock twice: ships (the first one,
// with the source of the pilot below it) and dockShips (all of them), both
// created by the create of ships; order names the source below ships.
var otherReadTree = strings.NewReplacer(
	"  - ships\n", "  - ships: {leadPilotId: shipPilot.id}\n",
	"            pick: all\n            key: [/id]\n            write:\n              create: POST /docks/{dockId}/ships dockId=docks.id\n",
	"            pick: {first: 1}\n            key: [/id]\n            write:\n              create: POST /docks/{dockId}/ships dockId=docks.id\n",
).Replace(leadTree) + "          dockShips:\n            call: GET /docks/{dockId}/ships\n            path: {dockId: .id}\n            pick: all\n            key: [/id]\n"

// TestSourceForOtherReads: records of another node of the same resource
// (dockShips) have no record of the source of order below them. The build
// takes the pilot each names by its value from the records the fetch took
// elsewhere; for the pilots it took nowhere, -fix adds a read below
// dockShips (order stays), and after the fetch of dockShips the test runs
// on an empty instance.
func TestSourceForOtherReads(t *testing.T) {
	v := leadStarport
	s, path := v.load(t)
	base := v.stage(t)
	if !strings.Contains(otherReadTree, "dockShips:") || !strings.Contains(otherReadTree, "pick: {first: 1}") {
		t.Fatal("the tree of the test changed")
	}
	tr, d := fetch(t, s, otherReadTree, base)
	_, err := Build(s, tr, d)
	var fe *FindingError
	if !errors.As(err, &fe) || len(fe.Fixes) != 1 || !strings.Contains(err.Error(), "the records are read by dockShips") ||
		!strings.Contains(fe.Fixes[0].Change, "below dockShips the node") || !slices.Equal(fe.Fixes[0].FetchNodes(), []string{"dockShips"}) {
		t.Fatalf("a read below dockShips: %v", err)
	}
	fixed, _, err := ApplyFixes(tr, fe.Fixes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	if e := fixed.Entry("ships"); e.Values["leadPilotId"] != "shipPilot.id" || len(fixed.Node("dockShips").Each) != 1 {
		t.Fatalf("order stays, a node below dockShips:\n%s", b)
	}
	tr, d = fetch(t, s, string(b), base)
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatalf("built again: %v", err)
	}
	if len(built.Errors()) > 0 {
		t.Errorf("no error left: %v", built.Errors())
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /docks/{dockId}/ships" && !strings.HasPrefix(text(st.Body.(map[string]any)["leadPilotId"]), "{{") {
			t.Errorf("%s sends the pilot the test created: %v", st.ID, st.Body)
		}
	}
	if res, log := runEmptyAt(t, path, v.server(1), built); res.Failed {
		t.Errorf("the scenario failed on an empty instance:\n%s", log)
	}
}

// TestSourceByValue: when the fetch took the pilots elsewhere (the list of
// pilots at the root), the records of dockShips take the pilot they name by
// its value: no problem, no fix, the placeholder of that pilot.
func TestSourceByValue(t *testing.T) {
	v := leadStarport
	s, _ := v.load(t)
	file := strings.Replace(otherReadTree, "    pick: 1\n    off: true\n", "    pick: all\n", 1)
	_, built := buildFrom(t, s, file, v.stage(t))
	if len(built.Fixes) > 0 || len(built.Errors()) > 0 {
		t.Fatalf("nothing to fix: %v %v", built.Fixes, built.Errors())
	}
	creates := 0
	for _, st := range built.Scenario.Steps {
		if st.Call != "POST /docks/{dockId}/ships" {
			continue
		}
		creates++
		if !strings.HasPrefix(text(st.Body.(map[string]any)["leadPilotId"]), "{{") {
			t.Errorf("%s sends the pilot the test created: %v", st.ID, st.Body)
		}
	}
	if creates < 2 {
		t.Errorf("the ships of both nodes are created: %d", creates)
	}
}

// TestFetchNamesNone: a path value whose field is empty in the record (null
// on the way) leaves the node out with a note that the record names none,
// not that the field is unknown.
func TestFetchNamesNone(t *testing.T) {
	for path, want := range map[string]bool{
		"crew.pilotId":   true,  // crew is null
		"route.dock.id":  true,  // route has no dock
		"tags.pilotId":   true,  // an empty list
		"name.first":     false, // a text has no fields
		"crwe.pilotId":   false, // a slip: the record has no crwe
		"route.originId": false, // route has it
	} {
		rec := map[string]any{"name": "Comet", "crew": nil, "route": map[string]any{"originId": 3}, "tags": []any{}}
		if _, got := emptyAlong(rec, path); got != want {
			t.Errorf("%s: empty %v, want %v", path, got, want)
		}
	}
}

// TestPathKeyFixSkipsExcluded: the fix of a path value that is a key of
// the stage names the node above that reads the record, but not one the
// scope excludes (or that is off): it reads no records.
func TestPathKeyFixSkipsExcluded(t *testing.T) {
	s := loadStarport(t)
	file := strings.Replace(orderTree, "              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n",
		"              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n              shipDock: GET /docks/{dockId} dockId=106\n", 1)
	pick := func(problems []string) string {
		for _, p := range problems {
			if strings.Contains(p, "shipDock: {dockId} = 106 is the id of a dock of the stage") {
				return p
			}
		}
		return ""
	}
	if p := pick(Check(s, parse(t, file))); !strings.Contains(p, "dockId=106 → dockId=docks.id (-fix does it)") {
		t.Fatalf("the docks above give it: %q", p)
	}
	excluded := "scope:\n  exclude:\n    ops: [listDocks]\n" + file
	if p := pick(Check(s, parse(t, excluded))); p == "" || strings.Contains(p, "docks.id") || strings.Contains(p, "(-fix does it)") {
		t.Errorf("an excluded node gives nothing: %q", p)
	}
}

// TestAddedNodeNamesItsTag: a node -fix adds says in its comment its
// operationId, its tag and that -fix added it.
func TestAddedNodeNamesItsTag(t *testing.T) {
	v := leadStarport
	s, _ := v.load(t)
	file := strings.NewReplacer(
		"    pick: 1\n    off: true\n", "    pick: all\n",
		"            each:\n              shipPilot: GET /pilots/{pilotId} pilotId=.leadPilotId\n", "",
	).Replace(leadTree)
	tr, built := buildFrom(t, s, file, v.stage(t))
	fixed, _, err := ApplyFixes(tr, built.Fixes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fixed.Bytes()
	if !strings.Contains(string(b), "# getPilot · tag Pilot · added by -fix") {
		t.Errorf("the comment of the added node:\n%s", b)
	}
}

// TestOwnKeyAndEmbedded: the key a record holds itself is no reference to
// a copy of it another node read; a field inside an embedded record of
// another resource is none either, except that record's key.
func TestOwnKeyAndEmbedded(t *testing.T) {
	s := loadStarport(t)
	tr := parse(t, orderTree)
	b := &builder{s: s, t: tr, m: modelOf(s), d: &Data{}, canon: map[*Record]*Record{}}
	pilot := &Record{ID: "pilots1", Node: tr.Node("pilots"), Value: map[string]any{"id": 5, "name": "Ada"}}
	copyOf := &Record{ID: "shipPilot1", Node: tr.Node("shipPilot"), Value: map[string]any{"id": 5, "name": "Ada"}}
	ship := &Record{ID: "ships1", Node: tr.Node("ships"), Value: map[string]any{"id": 9, "pilotId": 5}}
	kr := keyRef{rec: pilot, field: "id"}
	if !b.ownKey(copyOf, kr, []string{"id"}, 5) {
		t.Error("the id of the pilot read twice is its own")
	}
	if b.ownKey(ship, kr, []string{"pilotId"}, 5) {
		t.Error("the pilotId of a ship names the pilot")
	}
	for path, want := range map[string]bool{
		"dock.pilots.pilotId": true,  // inside the embedded dock
		"dock.id":             false, // the key of the dock
		"route.dock.id":       false, // the key of a dock inside route
		"crew.pilotId":        false, // crew is no resource
		"pilotId":             false,
	} {
		if got := b.inEmbedded(ship, path); got != want {
			t.Errorf("%s: embedded %v, want %v", path, got, want)
		}
	}
}

// TestThroughExcludedRecord: a node that reads through an embedded record
// of a resource the scope excludes (.pilot.pilotId with Pilot excluded) is
// named, and -fix sets it off; its own request (getLicense) is in scope.
func TestThroughExcludedRecord(t *testing.T) {
	s := loadStarport(t)
	file := "scope:\n  exclude:\n    tags: [Pilot]\n" + strings.Replace(orderTree,
		"              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n",
		"              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n              shipLicense: GET /licenses/{pilotId} pilotId=.pilot.pilotId\n", 1)
	problems := Check(s, parse(t, file))
	if !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "shipLicense: reads .pilot.pilotId through the embedded Pilot, which the scope excludes (tag Pilot)") && strings.Contains(p, "(-fix does it)")
	}) {
		t.Fatalf("the read through the excluded pilot: %v", problems)
	}
	fixed, _, err := Repair(s, parse(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if n := fixed.Node("shipLicense"); n == nil || !n.Off {
		b, _ := fixed.Bytes()
		t.Errorf("shipLicense is off:\n%s", b)
	}
	// .pilotId of the ship itself is no read through a pilot
	if _, res, _ := throughExcluded(s, fixed.Scope, &Node{Path: map[string]any{"pilotId": ".pilotId"}}); res != "" {
		t.Errorf("a field on top reads through nothing: %s", res)
	}
}
