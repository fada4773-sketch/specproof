package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/apply"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

func review(t *testing.T, defaultsJSON string) *Result {
	t.Helper()
	const path = "../../../testdata/gen/review.yaml"
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d, notes, _ := dict.Build(s, nil, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(defaultsJSON))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"id"}
	res := apply.Apply(doc, s, d, defs, apply.Options{Seed: 1, GenericIDs: ids})
	return Run(Input{Spec: s, Dict: d, DictNotes: notes, Defaults: defs, GenericIDs: ids, Apply: res})
}

func find(r *Result, action, contains string) *Suggestion {
	for i, s := range r.Suggestions {
		if s.Action == action && strings.Contains(s.Key+" "+s.Where+" "+s.Message+" "+s.Fix, contains) {
			return &r.Suggestions[i]
		}
	}
	return nil
}

func TestReviewProposesFixes(t *testing.T) {
	r := review(t, `{"Dock": {"DockCode": "x"}}`)
	for _, c := range []struct{ action, contains, key string }{
		{ActionDefault, "getDock.x-apitest-forbidden", "getDock.x-apitest-forbidden"}, // no 403
		{ActionChoose, "listDocks.zone", "listDocks.zone"},                            // NOT_BUILDABLE, pattern unsolved
		{ActionChoose, "/pilots/{id}", "/pilots/{id}"},                                // generic id without producer
		{ActionChoose, "Dock.Sealed", "Dock.Sealed"},                                  // NO_VALUE
		{ActionSpec, `did you mean "getDock"`, ""},                                    // link to an unknown operation
		{ActionSpec, "neither 401 nor 403", ""},                                       // getPilot
		{ActionSpec, "examples.broken", ""},                                           // curated named example
		{ActionApply, "paths./docks/{dockCode}.get.responses.200", ""},                // invalid example
		{ActionEdit, "#/components/schemas/Dock", "Dock"},                             // a DTO name as key
	} {
		s := find(r, c.action, c.contains)
		if s == nil {
			t.Errorf("no %s suggestion with %q", c.action, c.contains)
			continue
		}
		if s.Key != c.key {
			t.Errorf("%s %q: key %q, want %q", c.action, c.contains, s.Key, c.key)
		}
	}
	// a heuristic binding needs no entry: the examples follow it
	if b := find(r, ActionDefault, "getDock.dockCode"); b != nil {
		t.Errorf("heuristic binding proposed: %+v", b)
	}
	if n := len(r.Suggestions); n > 10 {
		for _, s := range r.Suggestions {
			t.Logf("%s %s %s: %s", s.Action, s.Key, s.Where, s.Message)
		}
		t.Errorf("%d suggestions, duplicates?", n)
	}
}

// Keys the defaults already have are decided and not proposed again.
func TestReviewSkipsKnownKeys(t *testing.T) {
	r := review(t, `{"getDock.dockCode": {"bind": "createDock", "pointer": "/DockCode"}, "/pilots/{id}": 3}`)
	if s := find(r, ActionDefault, "getDock.dockCode"); s != nil {
		t.Errorf("proposed again: %+v", s)
	}
	if s := find(r, ActionChoose, "/pilots/{id}"); s != nil {
		t.Errorf("proposed again: %+v", s)
	}
}

// review writes data only into defaults.json: proposed values, and for
// values only the user knows the value used so far. No comments, no null,
// no bindings. A second run proposes nothing twice.
func TestReviewUpdatesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.json")
	r := review(t, `{}`)
	if changed, err := defaults.Update(path, r.Changes(), nil); err != nil || !changed {
		t.Fatalf("update: %v %v", changed, err)
	}
	b, _ := os.ReadFile(path)
	defs, err := defaults.Parse(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if strings.Contains(string(b), "$") || len(defs.Todos()) > 0 {
		t.Errorf("comments or null written:\n%s", b)
	}
	if defs.Plain("/pilots/{id}") == nil {
		t.Errorf("the generated id is not written:\n%s", b)
	}
	if len(defs.Bindings()) > 0 || defs.Plain("getDock.x-apitest-forbidden") == nil {
		t.Errorf("a binding written or the extension missing:\n%s", b)
	}
	if defs.IsTodo("listDocks.zone") || strings.Contains(string(b), "listDocks.zone") {
		t.Errorf("a value nobody knows is written:\n%s", b)
	}

	again := review(t, string(b))
	if n := len(again.Changes()); n != 0 {
		t.Errorf("proposed again: %d", n)
	}
}

// Keys in "$rejected" are not proposed again.
func TestReviewSkipsRejected(t *testing.T) {
	r := review(t, `{"$rejected": ["getDock.dockCode"]}`)
	if s := find(r, ActionDefault, "getDock.dockCode"); s != nil {
		t.Errorf("rejected key proposed: %+v", s)
	}
}

func TestClosest(t *testing.T) {
	if c := closest("getDok", []string{"getDock", "listDocks"}); c != "getDock" {
		t.Errorf("closest: %q", c)
	}
	if c := closest("xyz", []string{"getDock"}); c != "" {
		t.Errorf("closest: %q", c)
	}
}

// With the resource model, path parameters that hold record keys get their
// examples from the records: no bindings, no ids to choose; "$snapshot"
// names the lists the records are fetched from.
func TestReviewModel(t *testing.T) {
	const path = "../../../testdata/gen/lists.yaml"
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d, notes, _ := dict.Build(s, nil, dict.Options{Seed: 1})
	defs := defaults.Empty()
	doc, err := yamldoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"id"}
	res := apply.Apply(doc, s, d, defs, apply.Options{Seed: 1, GenericIDs: ids})
	// the entries follow the order of the paths in the spec, not the names
	r := Run(Input{Spec: s, Dict: d, DictNotes: notes, Defaults: defs, GenericIDs: ids, Apply: res, Model: model.Detect(s, nil),
		PathOrder: yamldoc.Keys(yamldoc.Get(doc.Root, "paths"))})
	got := map[string]string{}
	for _, p := range r.Changes() {
		b, _ := json.Marshal(p.Value)
		got[p.Key] = string(b)
	}
	want := map[string]string{defaults.SnapshotKey: `{"Planet":{"from":"/Planet","count":1,"seed":[],"validation":{"mandatoryFields":[],"equalFields":{},"followingDetails":[]},"$comment":"listPlanets: /Planet"},"Moon":{"from":"/Planet/id/{id}/Moon","count":1,"seed":[],"validation":{"mandatoryFields":[],"equalFields":{},"followingDetails":[]},"$comment":"listMoonsOfPlanet: /Planet/id/{id}/Moon; {id} is the Id of the first Planet"}}`}
	if len(got) != len(want) || got[defaults.SnapshotKey] != want[defaults.SnapshotKey] {
		t.Errorf("got %v", got)
	}

	// with "$snapshot" in the defaults nothing is proposed
	defs, _ = defaults.Parse([]byte(`{"$snapshot": {"Planet": {"from": "listPlanets"}}}`))
	r = Run(Input{Spec: s, Dict: d, DictNotes: notes, Defaults: defs, GenericIDs: ids, Apply: res, Model: model.Detect(s, nil)})
	if n := len(r.Changes()); n != 0 {
		t.Errorf("%d changes", n)
	}
}

// A list below a path parameter of unknown meaning ({tier}): review writes
// the request with what it knows (dockCode from the Dock of the last run),
// keeps {tier} and shows the template in "$comment".
func TestReviewSnapshotURL(t *testing.T) {
	const path = "../../../testdata/gen/tiers.yaml"
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	old := dict.New()
	old.Records = map[string][]map[string]any{"Dock": {{"Code": "abc", "Name": "Moon Dock"}}}
	d, notes, _ := dict.Build(s, old, dict.Options{Seed: 1})
	defs := defaults.Empty()
	doc, err := yamldoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	res := apply.Apply(doc, s, d, defs, apply.Options{Seed: 1})
	r := Run(Input{Spec: s, Dict: d, DictNotes: notes, Defaults: defs, Apply: res, Model: model.Detect(s, nil)})
	sg := find(r, ActionDefault, defaults.SnapshotKey)
	if sg == nil {
		t.Fatalf("no $snapshot proposed: %+v", r.Suggestions)
	}
	got, _ := json.Marshal(sg.Value)
	want := `{"Dock":{"from":"/DockPreset/Tier/{tier}?dockCode=abc","count":1,"seed":[],"validation":{"mandatoryFields":[],"equalFields":{},"followingDetails":[]},"$comment":"GetDockPresets: /DockPreset/Tier/{tier}?dockCode={dockCode}&sectorCode={sectorCode}&pilotNumber={pilotNumber}; filled: dockCode = Code of the first Dock; replace {tier} with values that exist in the instance"}}`
	if g := strings.ReplaceAll(string(got), `\u0026`, "&"); g != want {
		t.Errorf("got  %s\nwant %s", g, want)
	}
	if !strings.Contains(sg.Fix, "set the placeholders") {
		t.Errorf("fix: %s", sg.Fix)
	}
}
