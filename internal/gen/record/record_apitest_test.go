package record

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
)

// emptyStarport is the environment of the test: only the seed, ids from 1.
func emptyStarport() *starport {
	return &starport{
		planets:  []map[string]any{{"id": json.Number("1"), "planetCode": "P1", "name": "Mars"}},
		docks:    []map[string]any{{"id": json.Number("1"), "dockCode": "D2", "planetId": json.Number("1"), "name": "South"}},
		configs:  map[string]any{},
		nextShip: 1,
	}
}

// The examples record writes from the instance pass apitest in an empty
// environment that holds only the seed.
func TestRecordPassesApitest(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "record.yaml")
	b, err := os.ReadFile("../../../testdata/gen/record.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	// the config is written before it is read
	config := `{
	  "params": {"planetCode": "P1"},
	  "seed": ["Planet", "Dock"],
	  "select": {"Dock": {"details": {"/Planet/{planetCode}/Dock/{dockCode}/Config": {"mandatory": ["settings.mode"]}}}},
	  "$apitest": {"DeleteLast": true, "MethodOrder": ["POST", "PUT", "PATCH", "GET", "DELETE"], "IgnoreFields": ["updatedAt"]}
	}`
	res, doc, _ := runConfig(t, newStarport(), specPath, config, nil, true)
	if len(res.Problems) > 0 {
		t.Fatalf("problems:\n%s", notes(res))
	}
	for _, n := range res.Notes {
		if n.Code == CodeContainer {
			t.Errorf("%s", n)
		}
	}
	if err := doc.Save(specPath); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(emptyStarport())
	defer srv.Close()
	apitest.Run(t, apitest.Config{SpecPath: specPath, BaseURL: srv.URL, DeleteLast: true,
		MethodOrder: []string{"POST", "PUT", "PATCH", "GET", "DELETE"}, IgnoreFields: []string{"updatedAt"}, DisableReports: true})
}
