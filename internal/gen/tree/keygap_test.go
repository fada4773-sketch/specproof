package tree

import (
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
