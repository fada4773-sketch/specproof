package apitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/apply"
	"github.com/fada4773-sketch/specproof/internal/gen/check"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// End to end with apitest-gen: every example is removed from the Bookstore
// spec, the generator writes new ones, and apitest runs against the
// Bookstore server. The generated values are not the server's data, so
// examples are compared by schema; status codes, schemas, the GET check
// after writes, bindings and authentication are checked in full.
func TestGeneratedExamplesRunGreen(t *testing.T) {
	src, err := os.ReadFile(bookstoreSpec)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	removed := stripExamples(doc.Root)
	if removed == 0 {
		t.Fatal("the Bookstore spec has no examples to remove")
	}
	b, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bookstore.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	bare, err := spec.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	before := check.Run(bare, nil)
	if before.Ready == before.Cases {
		t.Fatalf("without examples every case is buildable; the test proves nothing")
	}

	// the generator
	d, _, _ := dict.Build(bare, nil, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res := apply.Apply(doc, bare, d, defs, apply.Options{Seed: 1})
	if len(res.Fatal) > 0 {
		t.Fatalf("fatal: %v", res.Fatal)
	}
	if b, err = doc.Bytes(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	generated, err := spec.Load(t.Context(), path)
	if err != nil {
		t.Fatalf("generated spec does not load: %v", err)
	}
	after := check.Run(generated, nil)
	if len(after.Problems) > 0 {
		var lines []string
		for _, p := range after.Problems {
			lines = append(lines, p.Kind+" "+p.Where+": "+p.Message)
		}
		t.Fatalf("check after generating (%d of %d ready before):\n%s", before.Ready, before.Cases, strings.Join(lines, "\n"))
	}

	// apitest against the Bookstore server
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.SpecPath = path
	cfg.CompareMode = CompareSchema
	cfg.DisableReports = true
	cfg.ReportPath = ""
	out := runFake(t, cfg)
	if out.res.Failed {
		t.Errorf("apitest run with generated examples failed:\n%s", out.ft.output())
	}
	for _, c := range out.res.Cases {
		if c.Status == StatusNotBuildable {
			t.Errorf("%s: NOT_BUILDABLE: %s", c.Name, c.Message)
		}
	}
	t.Logf("%d examples removed; before: %d of %d cases buildable; after: %d cases, %v",
		removed, before.Ready, before.Cases, out.res.Summary.Total, out.res.Summary.Counts)
}

// stripExamples removes every "example" and "examples" key outside of
// schema properties and returns how many were removed.
func stripExamples(n *yaml.Node) int {
	removed := 0
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); {
			k := n.Content[i].Value
			isProperties := k == "properties"
			if (k == "example" || k == "examples") && !isProperties {
				n.Content = append(n.Content[:i], n.Content[i+2:]...)
				removed++
				continue
			}
			if isProperties {
				// property names may be "example"; only look inside them
				for j := 1; j < len(n.Content[i+1].Content); j += 2 {
					removed += stripExamples(n.Content[i+1].Content[j])
				}
			} else {
				removed += stripExamples(n.Content[i+1])
			}
			i += 2
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			removed += stripExamples(c)
		}
	}
	return removed
}
