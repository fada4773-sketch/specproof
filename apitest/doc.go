// Package apitest runs spec-driven integration tests against an HTTP API.
//
// It reads an OpenAPI 3.0/3.1 document, derives test cases from the examples
// in the spec, sends them to the API with a token and checks status code,
// schema and expected example of every response. Each case runs as a subtest
// named "<Tag>/<operationId>/<example>", so "go test -run" can select single
// cases. A Markdown report is written after every resource group and at the
// end of the run.
//
// Starting containers, preparing databases and obtaining tokens is left to
// the calling project. A typical integration test looks like this:
//
//	func TestAPI(t *testing.T) {
//		apitest.Run(t, apitest.Config{
//			SpecPath: "../../api/openapi.yaml",
//			BaseURL:  baseURL, // e.g. from a Testcontainers container
//			Token:    apitest.StaticToken(token),
//			Strict:   true,
//		})
//	}
package apitest
