package pathconflict_test

import (
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/examples/path-conflict/server"
)

// TestFixed runs the spec with the class operations at /…/class/{class}.
func TestFixed(t *testing.T) {
	apitest.Run(t, apitest.Config{
		SpecPath:    "openapi.fixed.yaml",
		Handler:     server.NewFixed(),
		Token:       apitest.StaticToken(server.Token),
		NumberCases: true,
		ReportPath:  "apitest-report/fixed.md",
	})
}
