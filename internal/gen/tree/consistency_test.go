package tree

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// repairAll runs -fix on a file and fails when a problem is left: every
// problem these tests make has a fix.
func repairAll(t *testing.T, name, file string) *Tree {
	t.Helper()
	s := loadStarport(t)
	tr := parse(t, file)
	fixed, done, err := Repair(s, tr)
	if err != nil {
		t.Fatalf("%s: -fix: %v (made %v)", name, err, done)
	}
	if left := Check(s, fixed); len(left) > 0 {
		b, _ := fixed.Bytes()
		t.Fatalf("%s: -fix made %v, left %v\n%s", name, done, left, b)
	}
	return fixed
}

// TestScopeCheck: what the file still sends although the scope excludes it
// is named, and -fix sets off: true (a require goes); a source that reads
// an excluded call is replaced by a read the scope allows (the list of the
// pilots with a where on the key), so the ship still sends the pilot the
// test creates.
func TestScopeCheck(t *testing.T) {
	s := loadStarport(t)
	// getPilot excluded: the pilot of a ship comes from the list of pilots
	file := "scope:\n  exclude:\n    ops: [getPilot]\n" + orderTree
	problems := Check(s, parse(t, file))
	if !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "shipPilot: GET /pilots/{pilotId} is excluded by scope (operation getPilot), but the tree still fetches it") &&
			strings.Contains(p, "shipPilot: add off: true # excluded by scope (operation getPilot) (-fix does it)")
	}) {
		t.Errorf("excluded read: %v", problems)
	}
	fixed := repairAll(t, "getPilot", file)
	if n := fixed.Node("shipPilot"); n == nil || !n.Off {
		t.Errorf("shipPilot is off")
	}
	e := fixed.Entry("ships")
	src, _ := e.Values["pilotId"].(string)
	name, _, _ := strings.Cut(src, ".")
	if n := fixed.Node(name); n == nil || n.Call != "GET /pilots" || n.Parent.Name != "ships" || len(n.Where) != 1 || n.Where[0].Test != "ships.pilotId" {
		b, _ := fixed.Bytes()
		t.Errorf("the pilot of a ship from the list:\n%s", b)
	}
	// the tag Pilot excluded: no pilot is created or read, the ship sends
	// the pilot of the stage (seed); the create, its line and the source go
	file = "scope:\n  exclude:\n    tags: [Pilot]\n" + orderTree
	fixed = repairAll(t, "tag Pilot", file)
	if fixed.Entry("pilots") != nil || len(fixed.Entry("ships").Values) > 0 || !fixed.Node("pilots").Write[WriteCreate].Off {
		b, _ := fixed.Bytes()
		t.Errorf("no pilots:\n%s", b)
	}
	// no read of pilots in scope: the test cannot take the pilot of a
	// ship; the create of the pilots goes off (seed), the source goes
	file = "scope:\n  exclude:\n    ops: [getPilot, listPilots]\n" + orderTree
	problems = Check(s, parse(t, file))
	fixed = repairAll(t, "no read of pilots", file)
	if w := fixed.Node("pilots").Write[WriteCreate]; w == nil || !w.Off || len(fixed.Entry("ships").Values) > 0 {
		b, _ := fixed.Bytes()
		t.Errorf("pilots are seed:\n%s\nproblems before: %v", b, problems)
	}
	// a write and a require the scope excludes
	file = "scope:\n  exclude:\n    ops: [deleteShip, listShips]\n" + strings.NewReplacer(
		"              create: POST /docks/{dockId}/ships dockId=docks.id\n",
		"              create: POST /docks/{dockId}/ships dockId=docks.id\n              delete: DELETE /ships/{shipId}\n",
		"        pick: {first: 1}\n",
		"        pick: {first: 1}\n        require:\n          ships: GET /docks/{dockId}/ships dockId=.id\n").Replace(orderTree)
	problems = Check(s, parse(t, file))
	for _, want := range []string{"ships.write.delete: DELETE /ships/{shipId} is excluded by scope (operation deleteShip), but the test still sends it",
		"docks.require.ships: GET /docks/{dockId}/ships is excluded by scope (operation listShips)"} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("want %q: %v", want, problems)
		}
	}
	fixed = repairAll(t, "write and require", file)
	if w := fixed.Node("ships").Write["delete"]; w == nil || !w.Off || len(fixed.Node("docks").Require) != 0 {
		b, _ := fixed.Bytes()
		t.Errorf("delete off, require gone:\n%s", b)
	}
}

// TestStaleWhere: a where kept for a node the tree no longer has only
// leaves records out; -fix removes it, and the source of order that read
// the node goes too (the check names the new one).
func TestStaleWhere(t *testing.T) {
	s := loadStarport(t)
	file := strings.NewReplacer(
		"            pick: all\n", "            pick: all\n            where:\n              pilotId: present # crew reads it\n",
		"{pilotId: shipPilot.id}", "{pilotId: crew.id}").Replace(orderTree)
	problems := Check(s, parse(t, file))
	if !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "ships.where.pilotId: present was added for crew, which the tree does not have: it only leaves records out") &&
			strings.Contains(p, "remove pilotId from the where of ships (-fix does it)")
	}) {
		t.Errorf("stale where: %v", problems)
	}
	fixed := repairAll(t, "stale where", file)
	if n := fixed.Node("ships"); len(n.Where) != 0 || fixed.Entry("ships").Values["pilotId"] != "shipPilot.id" {
		b, _ := fixed.Bytes()
		t.Errorf("where gone, source from the pilot below:\n%s", b)
	}
}

// TestScopeAfterOrder: a scope added to a file with order goes below
// order; the explanation at the top stays where it is, and the next merge
// changes nothing.
func TestScopeAfterOrder(t *testing.T) {
	s := loadStarport(t)
	b, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	tr := parse(t, string(b))
	tr.AddScope(Scope{ExcludeOps: []string{"getHealth"}})
	Merge(s, tr, ProposeScope(s, tr.Scope), "1.0")
	out, _ := tr.Bytes()
	if strings.Index(string(out), "scope:") < strings.Index(string(out), "order:") {
		t.Errorf("scope below order:\n%s", out)
	}
	again := parse(t, string(out))
	if changes := Merge(s, again, ProposeScope(s, again.Scope), "1.0"); len(changes) > 0 {
		t.Errorf("a second merge changes nothing: %+v", changes)
	}
}

// TestReferenceEndToEnd: a ship names a pilot; the file has neither the
// read of that pilot below the ships nor the source in order. The check
// stops the build, -fix adds both, the fetch reads the pilot of each ship,
// and the test sends the new key of the pilot it created to the create of
// the ship: the empty instance accepts it (it answers 422 for a pilot it
// does not have). Also with getPilot excluded: the pilot then comes from
// the list of pilots with a where on its key.
func TestReferenceEndToEnd(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	broken := strings.NewReplacer(
		"  - ships: {pilotId: shipPilot.id}\n", "  - ships\n",
		"              shipPilot: GET /pilots/{pilotId} pilotId=.pilotId\n", "").Replace(orderTree)
	if _, err := Build(s, parse(t, broken), &Data{}); err == nil || !strings.Contains(err.Error(), "ships.pilotId holds the id of a pilot the test creates") {
		t.Fatalf("the build stops: %v", err)
	}
	for name, file := range map[string]string{
		"read by key": broken,
		"from a list": "scope:\n  exclude:\n    ops: [getPilot]\n" + broken,
	} {
		fixed := repairAll(t, name, file)
		b, _ := fixed.Bytes()
		tr, d := fetch(t, s, string(b), base)
		built, err := Build(s, tr, d)
		if err != nil {
			t.Fatalf("%s: build: %v\n%s", name, err, b)
		}
		sent := 0
		for _, st := range built.Scenario.Steps {
			if st.Call != "POST /docks/{dockId}/ships" {
				continue
			}
			sent++
			if v := text(st.Body.(map[string]any)["pilotId"]); !strings.HasPrefix(v, "{{") {
				t.Errorf("%s: the ship sends the pilot of the stage: %v", name, v)
			}
		}
		if sent == 0 {
			t.Errorf("%s: no ship created:\n%s", name, b)
		}
		if res, log := runEmpty(t, built); res.Failed {
			t.Errorf("%s: the test failed on an empty instance:\n%s", name, log)
		}
	}
}

// TestGlobalWhereEveryLevel: the where at the top of the file holds for
// every record of its resource at every level: the lists, the reads of one
// record (by id, by name), the records a field names (the person of a
// garden, the moon of a garden), the searches and the beds below the
// gardens; a garden whose person the where leaves out goes too.
func TestGlobalWhereEveryLevel(t *testing.T) {
	s := loadColony(t)
	st := testserver.NewColony(treeToken, 500)
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)
	b, err := ProposeScope(s, Scope{ExcludeTags: []string{"Admin"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]struct {
		field string
		ok    []string
	}{"Person": {"name", []string{"Ada"}}, "Moon": {"name", []string{"Phobos", "Deimos"}}, "Bed": {"rows", []string{"2"}}}
	file := string(b) + "where:\n  Person.name: [Ada]\n  Moon.name: [Phobos, Deimos]\n  Bed.rows: [2]\n"
	tr := parse(t, file)
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: hs.URL + "/api", Token: treeToken})
	if err != nil {
		t.Fatal(err)
	}
	m := modelOf(s)
	seen := map[string]int{}
	for _, r := range d.Records {
		op := lookupOp(s, r.Node.Call)
		res := resourceOf(m, op)
		if res == nil || len(res.Schemas) == 0 || res.Schemas[0] != itemDTO(op) {
			continue
		}
		w, ok := where[res.Name]
		if !ok {
			continue
		}
		seen[res.Name]++
		if v := text(r.Value.(map[string]any)[w.field]); !slices.Contains(w.ok, v) {
			t.Errorf("%s (%s, %s): %s %s = %s", r.ID, r.Node.Call, res.Name, res.Name, w.field, v)
		}
	}
	for _, name := range []string{"Person", "Moon", "Bed"} {
		if seen[name] == 0 {
			t.Errorf("no record of %s fetched: %v", name, seen)
		}
	}
	for _, r := range d.Of(tr.Node("gardens")) {
		if g := text(r.Value.(map[string]any)["name"]); g == "Sage" {
			t.Errorf("Sage belongs to Grace, whom the where leaves out")
		}
	}
	if _, err := Build(s, tr, d); err != nil {
		t.Errorf("build: %v", err)
	}
}

// TestGlobalWhereSource: a ship whose pilot the where at the top leaves out
// cannot be rebuilt (its create takes the pilot's key): the fetch leaves the
// ship out and takes the next one, with a note; the test runs on an empty
// instance. Also with the pilot read from the list (getPilot excluded).
func TestGlobalWhereSource(t *testing.T) {
	s := loadStarport(t)
	_, base := stage(t)
	for name, file := range map[string]string{
		"by key":    orderTree + "where:\n  Pilot.name: [Ada]\n",
		"from list": "scope:\n  exclude:\n    ops: [getPilot]\n" + orderTree + "where:\n  Pilot.name: [Ada]\n",
	} {
		fixed := repairAll(t, name, file)
		b, _ := fixed.Bytes()
		tr, d := fetch(t, s, string(b), base)
		ships := d.Of(tr.Node("ships"))
		if len(ships) == 0 {
			t.Fatalf("%s: no ship", name)
		}
		for _, sh := range ships {
			below := slices.ContainsFunc(d.Records, func(r *Record) bool {
				return r.Parent == sh && text(r.Value.(map[string]any)["name"]) == "Ada"
			})
			if !below {
				t.Errorf("%s: %s has no pilot Ada below it", name, sh.ID)
			}
		}
		if !slices.ContainsFunc(d.Notes, func(n string) bool {
			return strings.Contains(n, "without a record of") && strings.Contains(n, "left out by where")
		}) {
			t.Errorf("%s: notes: %v", name, d.Notes)
		}
		// the note of the pilot under the ship left out goes with it
		if slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, " under ships") && strings.Contains(n, ": 0 of") }) {
			t.Errorf("%s: a note of a ship left out: %v", name, d.Notes)
		}
		built, err := Build(s, tr, d)
		if err != nil {
			t.Fatalf("%s: build: %v", name, err)
		}
		if res, log := runEmpty(t, built); res.Failed {
			t.Errorf("%s: the test failed on an empty instance:\n%s", name, log)
		}
	}
}
