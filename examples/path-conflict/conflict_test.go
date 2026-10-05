//go:build conflict

package pathconflict_test

import (
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/examples/path-conflict/server"
)

// TestConflict runs the spec with /book/{id} and /book/{class}. The server,
// like any router, serves only one of them; the class cases fail on
// purpose. Run it with: go test -tags conflict -run TestConflict -v .
func TestConflict(t *testing.T) {
	apitest.Run(t, apitest.Config{
		SpecPath:    "openapi.yaml",
		Handler:     server.New(),
		Token:       apitest.StaticToken(server.Token),
		NumberCases: true,
		ReportPath:  "apitest-report/conflict.md",
	})
}
