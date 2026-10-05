package apitest

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/plan"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// largeSpecLimit is NFR-04: 1,000 operations load and plan in under 5 s.
const largeSpecLimit = 5 * time.Second

// TestTPL2_25_LargeSpecLoadsAndPlans loads and plans the GitHub REST API
// description (1,231 operations) without sending requests (NFR-04, AK-10).
// The time limit is checked without the race detector (make perf); with
// it, the duration is only logged.
func TestTPL2_25_LargeSpecLoadsAndPlans(t *testing.T) {
	path := unzip(t, "../testdata/large/api.github.com.json.gz")

	start := time.Now()
	s, err := spec.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	set, err := bind.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(all, func(*cases.Case) bool { return true }, set, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	t.Logf("%d operations, %d cases, %d spec findings, %d binding findings: %s", len(s.Ops), len(p.Cases()), len(s.Findings), len(set.Findings), d)

	if len(s.Ops) != 1231 {
		t.Errorf("operations: got %d, want 1231", len(s.Ops))
	}
	for _, c := range p.Cases() {
		if c.NotBuildable != "" && len(c.NotBuildable) < 10 {
			t.Errorf("%s: NOT_BUILDABLE without a useful reason: %q", c.Name, c.NotBuildable)
		}
	}
	if raceEnabled {
		t.Logf("race detector on: limit %s not checked (run make perf)", largeSpecLimit)
		return
	}
	if d > largeSpecLimit {
		t.Errorf("load and plan took %s, limit %s (NFR-04)", d, largeSpecLimit)
	}
}

func unzip(t *testing.T, gz string) string {
	t.Helper()
	f, err := os.Open(gz)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "spec.json")
	w, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, zr); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}
