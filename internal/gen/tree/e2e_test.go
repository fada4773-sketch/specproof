package tree

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/spec"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

const (
	treeToken    = "tree-token"
	starportSpec = "../../../testdata/gen/tree-starport.yaml"
)

func loadStarport(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Load(context.Background(), starportSpec)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// stage serves a Starport with records, as a stage would.
func stage(t *testing.T) (*testserver.Starport, string) {
	t.Helper()
	st := testserver.NewStarport(treeToken, 100)
	st.Fill()
	hs := httptest.NewServer(st)
	t.Cleanup(hs.Close)
	return st, hs.URL + "/api"
}

// runEmpty runs a scenario against an empty Starport (planets only).
func runEmpty(t *testing.T, b *Built) (*apitest.Result, string) {
	t.Helper()
	text, err := b.Scenario.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tree-scenario_gen.yaml")
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: starportSpec, Handler: testserver.NewStarport(treeToken, 1), Token: apitest.StaticToken(treeToken)},
		Scenario: path,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	return res, out.String() + "\n" + string(text)
}

func TestTreeRebuildsTheStageInAnEmptyInstance(t *testing.T) {
	s := loadStarport(t)
	srv, base := stage(t)
	text, err := Propose(s).Encode()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Fetch(context.Background(), s, tr, Options{BaseURL: base, Token: treeToken})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests["POST"] + srv.Requests["PUT"] + srv.Requests["PATCH"] + srv.Requests["DELETE"]; n != 0 {
		t.Fatalf("a fetch must only read: %v", srv.Requests)
	}
	b, err := Build(s, tr, d)
	if err != nil {
		t.Fatal(err)
	}
	res, log := runEmpty(t, b)
	if res.Failed {
		t.Fatalf("the scenario failed on an empty instance:\n%s\nnotes: %+v\ndata notes: %v", log, b.Notes, d.Notes)
	}
	t.Logf("%d steps\n%s", len(res.Cases), log)
}
