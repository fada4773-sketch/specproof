package tree

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

const registrySpec = "../../../testdata/gen/tree-garden.yaml"

// gardenScope is the scope of the garden tests: the kinds of a garden are
// left out (their operations), the kind embedded in a garden stays part of
// it.
const gardenScope = "scope:\n  exclude:\n    tags: [GardenKind]\n"

func loadRegistry(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Load(context.Background(), registrySpec)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// gardenStage serves a filled garden registry; the fetch must never close
// a garden of it (GET /close/gardens/{id} changes it).
func gardenStage(t *testing.T) string {
	t.Helper()
	g := testserver.NewGarden(100)
	g.Fill()
	hs := httptest.NewServer(g)
	t.Cleanup(func() {
		hs.Close()
		if g.Closed > 0 {
			t.Errorf("the fetch closed %d gardens of the stage", g.Closed)
		}
	})
	return hs.URL + "/api"
}

// proposeGarden is the file apitest-gen tree writes for the garden
// registry with the scope.
func proposeGarden(t *testing.T, s *spec.Spec) string {
	t.Helper()
	sc := parse(t, gardenScope).Scope
	tr := ProposeScope(s, sc)
	tr.AddScope(sc)
	b, err := tr.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixLoop runs -fix as the command does on an existing file: the fixes of
// the check, then fetch, build, the fixes of the build (or of the problems
// that stop it) and the check again, until a build has nothing to fix or
// rounds are used up. It returns the tree, the last build (nil when
// problems stop it) and those problems.
func fixLoop(t *testing.T, s *spec.Spec, file, base string, rounds int) (*Tree, *Built, []string) {
	t.Helper()
	tr, _, err := Repair(s, parse(t, file))
	if err != nil {
		t.Fatal(err)
	}
	for range rounds {
		b, _ := tr.Bytes()
		var d *Data
		tr, d = fetch(t, s, string(b), base)
		built, err := Build(s, tr, d)
		var fe *FindingError
		var fixes []Fix
		switch {
		case errors.As(err, &fe):
			fixes = fe.Fixes
		case err != nil:
			t.Fatalf("build: %v", err)
		default:
			fixes = built.Fixes
		}
		if len(fixes) == 0 {
			if fe != nil {
				return tr, nil, fe.Problems
			}
			return tr, built, nil
		}
		next, _, err := ApplyFixes(tr, fixes)
		if err != nil && next == tr {
			t.Fatalf("fixes: %v", err)
		}
		if tr, _, err = Repair(s, next); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := tr.Bytes()
	t.Fatalf("still fixes after %d rounds:\n%s", rounds, b)
	return nil, nil, nil
}

// runGarden runs a built test on an empty garden registry.
func runGarden(t *testing.T, built *Built) {
	t.Helper()
	text, err := built.Scenario.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tree-test_gen.yaml")
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: registrySpec, Handler: testserver.NewGarden(500)},
		Scenario: path,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed {
		t.Errorf("the test failed on an empty instance:\n%s\n%s", out.String(), text)
	}
}

// TestFixGardenFromProposal: the proposal for the garden registry, then
// -fix against a filled instance until nothing is left; the test it
// builds runs on an empty instance.
func TestFixGardenFromProposal(t *testing.T) {
	s := loadRegistry(t)
	checkGardenFix(t, s, proposeGarden(t, s))
}

// TestFixGardenExistingFile: the file an earlier apitest-gen proposed
// (conditions that must hold together, the moon read by its number with an
// id, the seeds of a person below the person of a garden): -fix makes the
// same test of it as of the proposal.
func TestFixGardenExistingFile(t *testing.T) {
	s := loadRegistry(t)
	b, err := os.ReadFile("../../../testdata/gen/tree-garden-old.yaml")
	if err != nil {
		t.Fatal(err)
	}
	checkGardenFix(t, s, string(b))
}

// checkGardenFix runs -fix on a file and checks the test it builds: every
// garden of the stage is created, each sends the seeds, the moon and the
// tenders the test created, a person's code as it is; the test runs on an
// empty instance.
func checkGardenFix(t *testing.T, s *spec.Spec, file string) {
	t.Helper()
	tr, built, problems := fixLoop(t, s, file, gardenStage(t), 10)
	b, _ := tr.Bytes()
	if len(problems) > 0 {
		t.Fatalf("problems left:\n%s\n%s", strings.Join(problems, "\n"), b)
	}
	if errs := built.Errors(); len(errs) > 0 {
		t.Fatalf("errors left: %+v\n%s", errs, b)
	}
	// every garden of the stage is created, each names the seeds, the
	// moon and the tenders the test created; a person's code stays
	gardens := map[string]map[string]any{}
	for _, st := range built.Scenario.Steps {
		if st.Call == "POST /gardens" {
			body := st.Body.(map[string]any)
			gardens[text(body["Slug"])] = body
		}
	}
	if len(gardens) != 3 {
		t.Fatalf("the three gardens are created: %v\n%s", gardens, b)
	}
	placeholder := func(v any) bool { return strings.HasPrefix(text(v), "{{") }
	for code, body := range gardens {
		seeds, _ := body["Seeds"].([]any)
		if len(seeds) == 0 {
			t.Errorf("%s sends its seeds: %v", code, body)
		}
		for _, s := range seeds {
			if !placeholder(s.(map[string]any)["Id"]) {
				t.Errorf("%s sends a seed the test created: %v", code, s)
			}
		}
	}
	if !placeholder(gardens["rose"]["MoonId"]) {
		t.Errorf("rose lies on a moon the test created: %v", gardens["rose"])
	}
	if kind, _ := gardens["rose"]["GardenKind"].(map[string]any); kind == nil || !placeholder(kind["Tenders"].([]any)[0].(map[string]any)["PersonId"]) {
		t.Errorf("rose is tended by a person the test created: %v", gardens["rose"])
	}
	// the GET that closes a garden is an action of the gardens, no read
	if n := tr.Node("gardens"); n.Write["close"] == nil || slices.ContainsFunc(n.Each, func(c *Node) bool { return c.Call == "GET /close/gardens/{id}" }) {
		t.Errorf("close is an action of the gardens:\n%s", b)
	}
	if !slices.ContainsFunc(built.Scenario.Steps, func(st *scenario.Step) bool { return st.Call == "GET /close/gardens/{id}" }) {
		t.Error("the test closes a garden it created")
	}
	if code := text(gardens["fern"]["PersonCode"]); code != "ada" && (!placeholder(code) || !strings.HasSuffix(code, ".Code}}")) {
		t.Errorf("fern names the code of its person as it is: %v", gardens["fern"])
	}
	runGarden(t, built)
}
