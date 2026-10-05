package apitest_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/apitest"
)

// A typical integration test: the project starts its API (here in the same
// process), obtains a token and hands both to apitest.Run. In a real project
// the function is a test, e.g. func TestAPI(t *testing.T).
func Example() {
	testAPI := func(t *testing.T, router http.Handler, token string) {
		res := apitest.Run(t, apitest.Config{
			SpecPath:       "../../api/openapi.yaml",
			Handler:        router,
			Token:          apitest.StaticToken(token),
			IgnoreFields:   []string{"id", "createdAt", "updatedAt"},
			DeviationsPath: "apitest_deviations.yaml",
			Strict:         os.Getenv("CI") == "true",
			Hooks: apitest.Hooks{
				BeforeGroup: func(_ context.Context, _ string) error {
					return nil // e.g. truncate tables, flush caches
				},
			},
		})
		t.Logf("report: %s", res.Report)
	}
	_ = testAPI
}

// A running API, e.g. a container started with Testcontainers, is tested
// through its base URL. The token is fetched on demand, so long runs can
// refresh it.
func ExampleTokenFunc() {
	testAPI := func(t *testing.T, baseURL string, login func(context.Context) (string, error)) {
		apitest.Run(t, apitest.Config{
			SpecPath:       "spec/openapi.yaml",
			BaseURL:        baseURL + "/api/v3",
			HealthPath:     "/openapi.json",
			Token:          apitest.TokenFunc(login),
			RequestTimeout: 5 * time.Second,
			ReportJSON:     true,
		})
	}
	_ = testAPI
}
