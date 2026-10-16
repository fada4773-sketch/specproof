package tree

import (
	"slices"
	"strings"
	"testing"
)

// orderTree is a Starport tree with an order: pilots, docks, then ships,
// whose pilot comes from the pilot read below each ship.
const orderTree = `
order:
  - pilots
  - docks
  - ships: {pilotId: shipPilot.id}
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
              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId
`

// TestOrderProposal: the proposal orders the creates by what they take
// (pilots and docks before the ships) and names the source of the ship's
// pilot; the file round-trips.
func TestOrderProposal(t *testing.T) {
	s := loadStarport(t)
	b, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	tr := parse(t, out)
	if !tr.Strict() {
		t.Fatalf("a proposal has an order:\n%s", out)
	}
	var names []string
	for _, e := range tr.Order {
		names = append(names, e.Node)
	}
	if i, j, k := slices.Index(names, "pilots"), slices.Index(names, "docks"), slices.Index(names, "ships"); i < 0 || j < 0 || k < 0 || i > k || j > k {
		t.Errorf("order: %v\n%s", names, out)
	}
	if e := tr.Entry("ships"); e == nil || e.Values["pilotId"] != "shipPilot.id" {
		t.Errorf("the source of the pilot of a ship:\n%s", out)
	}
	if !strings.Contains(out, "order:\n  - ") || strings.Index(out, "order:") > strings.Index(out, "tree:") {
		t.Errorf("order at the top:\n%s", out)
	}
	if problems := Check(s, tr); len(problems) > 0 {
		t.Errorf("the proposal must pass its own check: %v", problems)
	}
}

// TestOrderCheck: every way a user's order can be wrong is named, with the
// line, before anything is fetched.
func TestOrderCheck(t *testing.T) {
	s := loadStarport(t)
	if problems := Check(s, parse(t, orderTree)); len(problems) > 0 {
		t.Fatalf("a right order: %v", problems)
	}
	for name, c := range map[string]struct{ from, to, want string }{
		"after what it takes": {"  - pilots\n  - docks\n  - ships: {pilotId: shipPilot.id}\n", "  - pilots\n  - ships: {pilotId: shipPilot.id}\n  - docks\n",
			"order: ships comes before docks, but the create of ships takes a value of docks; move docks above ships"},
		"the source's create too": {"  - pilots\n  - docks\n  - ships: {pilotId: shipPilot.id}\n", "  - docks\n  - ships: {pilotId: shipPilot.id}\n  - pilots\n",
			"order: ships comes before pilots, but the create of ships takes a value of shipPilot (created like pilots); move pilots above ships"},
		"no node":       {"  - pilots\n", "  - pilots\n  - moons\n", "order: moons is no node of the tree"},
		"twice":         {"  - pilots\n", "  - pilots\n  - pilots\n", "order: pilots is listed twice"},
		"no create":     {"  - pilots\n", "  - pilots\n  - planets\n", "order: planets creates nothing"},
		"no place":      {"  - pilots\n", "", "pilots: its create has no place in order"},
		"no body field": {"{pilotId: shipPilot.id}", "{pilotId: shipPilot.id, captain: shipPilot.id}", "order: ships: the body of POST /docks/{dockId}/ships has no field captain"},
		"no reference":  {"{pilotId: shipPilot.id}", "{pilotId: 7}", "order: ships.pilotId: 7 is no reference to a node"},
		"no relation":   {"{pilotId: shipPilot.id}", "{pilotId: pilots.id}", "order: ships.pilotId: pilots is neither below nor above ships"},
		"no field":      {"{pilotId: shipPilot.id}", "{pilotId: shipPilot.rank}", "order: ships.pilotId: the records of shipPilot have no field rank"},
	} {
		tr := parse(t, strings.Replace(orderTree, c.from, c.to, 1))
		problems := Check(s, tr)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, "line ") && strings.Contains(p, c.want) }) {
			t.Errorf("%s: want %q, got %v", name, c.want, problems)
		}
	}
	// without order the file is as before: nothing to check
	if problems := Check(s, parse(t, withoutOrder(orderTree))); len(problems) > 0 {
		t.Errorf("no order: %v", problems)
	}
}

// TestOrderStrictBuild: with an order the test creates in its order, the
// body takes the values order names, a value of the stage that is a key
// of a created record stays (a hint), and a missing source stops the
// build.
func TestOrderStrictBuild(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	tr, d := fetch(t, s, orderTree, base)
	built, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	var creates []string
	for _, st := range built.Scenario.Steps {
		if strings.HasSuffix(st.ID, ".create") {
			creates = append(creates, st.ID)
		}
	}
	if len(creates) < 3 || !strings.HasPrefix(creates[0], "shipPilot") || !strings.HasPrefix(creates[len(creates)-1], "ships") {
		t.Errorf("pilots, docks, ships: %v", creates)
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /docks/{dockId}/ships" && !strings.HasPrefix(text(st.Body.(map[string]any)["pilotId"]), "{{shipPilot") {
			t.Errorf("the pilot from order: %+v", st.Body)
		}
	}
	if res, log := runEmpty(t, built); res.Failed {
		t.Fatalf("the scenario failed:\n%s", log)
	}
	// without the source the ship keeps the pilot of the stage: a hint
	plain := parse(t, strings.Replace(orderTree, "  - ships: {pilotId: shipPilot.id}\n", "  - ships\n", 1))
	built, err = Build(s, plain, d)
	if err != nil {
		t.Fatal(err)
	}
	hint := false
	for _, n := range built.Notes {
		hint = hint || strings.Contains(n.Why, "give the source in order (pilotId: <node>.id)")
	}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /docks/{dockId}/ships" && strings.HasPrefix(text(st.Body.(map[string]any)["pilotId"]), "{{") {
			t.Errorf("nothing is guessed with an order: %+v", st.Body)
		}
	}
	if !hint {
		t.Errorf("the hint names the source: %+v", built.Notes)
	}
	// a source no record has stops the build
	gone := parse(t, strings.Replace(orderTree, "              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n",
		"              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n              crew: GET /pilots/{pilotId} pilotId=.pilotId\n", 1))
	gone.Order[2].Values = map[string]any{"pilotId": "crew.id"}
	if _, err := Build(s, gone, d); err == nil || !strings.Contains(err.Error(), "has no record of crew below or above it") {
		t.Errorf("a missing source: %v", err)
	}
}

// TestOrderMerge: a file without order gets the whole order; a file with
// one keeps it and gets a place for a new create, marked new, after what
// it takes.
func TestOrderMerge(t *testing.T) {
	s := loadStarport(t)
	tr := parse(t, withoutOrder(orderTree))
	changes := Merge(s, tr, Propose(s), "1.0")
	if !tr.Strict() || tr.OrderIndex("ships") < tr.OrderIndex("docks") || tr.OrderIndex("ships") < tr.OrderIndex("pilots") {
		b, _ := tr.Bytes()
		t.Fatalf("the whole order: %+v\n%s", tr.Order, b)
	}
	if !slices.ContainsFunc(changes, func(c Change) bool { return c.Where == "order" && strings.HasPrefix(c.What, "proposed") }) {
		t.Errorf("changes: %+v", changes)
	}
	// the user's order stays; the create of the docks it lacks comes after
	// the planets' children it takes nothing from, before the ships
	mine := parse(t, strings.Replace(orderTree, "  - docks\n", "", 1))
	changes = Merge(s, mine, Propose(s), "1.0")
	b, _ := mine.Bytes()
	out := string(b)
	if !strings.Contains(out, "  - docks # "+noteNew) {
		t.Errorf("docks marked new:\n%s", out)
	}
	if mine.OrderIndex("docks") > mine.OrderIndex("ships") || mine.OrderIndex("pilots") != 0 {
		t.Errorf("place of docks: %+v", mine.Order)
	}
	if !slices.ContainsFunc(changes, func(c Change) bool { return c.Where == "order.docks" }) {
		t.Errorf("changes: %+v", changes)
	}
	if problems := Check(s, parse(t, out)); len(problems) > 0 {
		t.Errorf("check after merge: %v\n%s", problems, out)
	}
	// a second merge changes nothing
	again := parse(t, out)
	if ch := Merge(s, again, Propose(s), "1.0"); slices.ContainsFunc(ch, func(c Change) bool { return strings.HasPrefix(c.Where, "order") }) {
		t.Errorf("a second merge: %+v", ch)
	}
}

// withoutOrder is a tree file without its order section.
func withoutOrder(file string) string {
	return "tree:" + strings.SplitN(file, "tree:", 2)[1]
}
