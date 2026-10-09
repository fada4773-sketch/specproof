# specproof

Spec-driven integration tests for HTTP APIs in Go, and a generator that makes any OpenAPI spec testable.

- **`apitest`** (library, `import "github.com/fada4773-sketch/specproof/apitest"`) runs the tests.
- **`apitest-gen`** (command line tool) writes the examples apitest needs into your spec. See [apitest-gen](#apitest-gen-examples-for-your-spec).

`apitest` reads your OpenAPI document, turns its **examples** into test cases, sends them to your running API and checks every response against the spec: status code, schema and expected example. Writes are read back with GET to prove the data was stored. Authentication is tested without and with manipulated tokens. Every case is a normal Go subtest, and every run writes a Markdown report (unless you turn it off).

```go
func TestAPI(t *testing.T) {
	apitest.Run(t, apitest.Config{
		SpecPath: "../../api/openapi.yaml",
		Handler:  router, // your http.Handler, e.g. a Gin engine
		Token:    apitest.StaticToken(token),
		Strict:   true,
	})
}
```

```
--- FAIL: TestAPI (0.21s)
    --- PASS: TestAPI/Organization/createOrganization/acme (0.01s)
    --- PASS: TestAPI/Organization/getOrganizationById/default (0.00s)
    --- FAIL: TestAPI/Organization/updateOrganization/default (0.01s)
        DATA_MISMATCH: data was not stored as sent: GET /organizations/1f0c…/ differs (1 difference, first at /name)
    --- PASS: TestAPI/Organization/deleteOrganization/unauthorized (0.00s)
```

The idea: the examples in a spec are documentation and test cases at the same time. If the API does not behave as its own documentation shows, either the code or the documentation is wrong, and both should be noticed.

All examples below use one API: an *Enterprise Orchestration System* with organizations, workflows and deployments, deeply nested DTOs and full CRUD.

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Examples](#examples)
- [Configuration reference](#configuration-reference)
- [Spec extensions](#spec-extensions)
- [Results and report](#results-and-report)
- [Running in CI](#running-in-ci)
- [apitest-gen: examples for your spec](#apitest-gen-examples-for-your-spec)
- [Example project: Swagger Petstore](#example-project-swagger-petstore)
- [Limits](#limits)
- [Development](#development)

## Install

```bash
go get github.com/fada4773-sketch/specproof/apitest@latest       # the library
go install github.com/fada4773-sketch/specproof/cmd/apitest-gen@latest  # the generator
```

Requires Go 1.26 or later. The library depends on [kin-openapi](https://github.com/getkin/kin-openapi), a YAML parser and [regexp2](https://github.com/dlclark/regexp2) for ECMA-262 `pattern`s that Go's `regexp` cannot evaluate; the generator additionally uses `yaml.v3` and [gofakeit](https://github.com/brianvoe/gofakeit), which are not compiled into code that only imports the library. apitest never starts containers and never fetches tokens itself: that stays in your project, so you keep full control over infrastructure and credentials.

## Quick start

### API in the same process

The fastest way: pass your router as `Handler`. apitest serves it with `httptest` under the base path of the spec's `servers` entry (here `/v1`).

```yaml
# api/openapi.yaml
servers:
  - url: https://api.enterprise.orchestration.io/v1
security:
  - bearerAuth: []
components:
  securitySchemes:
    bearerAuth: { type: http, scheme: bearer }
```

```go
package api_test

import (
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"

	"example.com/orchestration/internal/app"
)

func TestAPI(t *testing.T) {
	apitest.Run(t, apitest.Config{
		SpecPath: "../api/openapi.yaml",
		Handler:  app.NewRouter(app.NewStore()),
		Token:    apitest.StaticToken("mock-token-admin"),
	})
}
```

```bash
go test ./... -run TestAPI -v
```

### Running API, e.g. in a container

```go
//go:build integration

func TestAPI(t *testing.T) {
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, "ghcr.io/acme/orchestration:1.4.2",
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/health").WithPort("8080/tcp")),
	)
	if err != nil {
		t.Skipf("no container runtime: %v", err)
	}
	testcontainers.CleanupContainer(t, ctr)
	endpoint, err := ctr.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}

	apitest.Run(t, apitest.Config{
		SpecPath:   "../../api/openapi.yaml",
		BaseURL:    endpoint + "/v1", // must contain the servers' base path
		HealthPath: "/health",        // waited for before the first case
		Token:      apitest.StaticToken(os.Getenv("API_TOKEN")),
	})
}
```

Writing requests are only sent to `localhost` and loopback addresses. For a test environment on another host, list it in `AllowedHosts` (or set `AllowRemoteWrites`).

## How it works

1. **Load** the spec (OpenAPI 3.0, 3.1, or Swagger 2.0, which is converted). Invalid specs stop the run before the first request, with the location in the spec. Examples that do not match their own schema are reported as spec findings.
2. **Derive cases** from examples. Each case is named `<Tag>/<operationId>/<example>`.
3. **Resolve parameters** in this order: value from an earlier response (binding) → `Config.Params` → named parameter example → parameter `example` → schema `example` → `default` → first `enum` value. Optional query parameters are only set from the first four sources.
4. **Order** the cases per resource group (the first tag): create → read → list → update → 4xx examples → authentication cases → delete. Groups follow the dependency graph of their bindings; DELETEs of groups that others depend on run last, in reverse order.
5. **Check** every response in three stages: status code, schema (types, required fields, formats, `additionalProperties`, headers, content type), and the expected example (subset by default). Undocumented status codes fail, even 2xx.
6. **Verify writes**: after POST, PUT and PATCH the resource is read back with GET and compared with what was sent; after DELETE, GET must return 404.
7. **Test authentication** for operations with `security`: without token (`unauthorized`, expects 401), with a manipulated token (`invalid-token`, expects 401 or 403), and optionally with a valid token that lacks rights (`forbidden`, expects 403).

## Examples

### A case from the schema example

Without named examples, one `default` case is built from the request body's `example`, the schema's `example`, or the examples of the single properties. `OrganizationCreateDto` has a complete schema example, so it is sent as it is:

```yaml
/organizations:
  post:
    tags: [Organization]
    operationId: createOrganization
    requestBody:
      required: true
      content:
        application/json:
          schema: { $ref: '#/components/schemas/OrganizationCreateDto' }
    responses:
      '201':
        description: Organization created successfully
        content:
          application/json:
            schema: { $ref: '#/components/schemas/OrganizationResponseDto' }

components:
  schemas:
    OrganizationCreateDto:
      type: object
      required: [name, legalEntity, settings]
      example:
        name: "Acme Corp International"
        legalEntity:
          taxId: "DE123456789"
          registeredAddress:
            street: "Friedrichstraße 42"
            city: "Berlin"
            country: "DE"
            geoCoordinates: { latitude: 52.520008, longitude: 13.404954 }
        settings: { … }
```

Result: the case `Organization/createOrganization/default` expects 201, validates the body against `OrganizationResponseDto`, and then reads the organization back with `GET /organizations/{orgId}` and compares every sent field. Without `tags`, the group is `untagged`.

If a required value has no example anywhere, the case is `NOT_BUILDABLE` with the name of the missing field; apitest never sends placeholder values.

### Named examples, expected responses and negative tests

Each named request example becomes its own case. A response example **with the same name** is the expected result, and a pairing with a 4xx response makes it a negative test:

```yaml
/organizations:
  post:
    tags: [Organization]
    operationId: createOrganization
    requestBody:
      content:
        application/json:
          schema: { $ref: '#/components/schemas/OrganizationCreateDto' }
          examples:
            acme:
              value:
                name: "Acme Corp International"
                legalEntity: { taxId: "DE123456789", registeredAddress: { … } }
                settings: { … }
            missing-legal-entity:
              value:
                name: "Acme Corp International"
                settings: { … }
    responses:
      '201':
        content:
          application/json:
            schema: { $ref: '#/components/schemas/OrganizationResponseDto' }
            examples:
              acme:
                value:
                  id: "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
                  createdAt: "2026-01-15T08:30:00Z"
                  name: "Acme Corp International"
      '422':
        content:
          application/json:
            schema: { $ref: '#/components/schemas/ValidationErrorResponseDto' }
            examples:
              missing-legal-entity:
                value:
                  code: "ERR_VALIDATION_FAILED"
                  fieldErrors: [{ fieldPath: "legalEntity", error: "Must not be empty" }]
```

| Case | Expectation |
|---|---|
| `Organization/createOrganization/acme` | 201, body contains all fields of the `acme` response example |
| `Organization/createOrganization/missing-legal-entity` | 422, body contains `code` and `fieldErrors` of the example |

`id` (`format: uuid`) and `createdAt` (`format: date-time`) are generated by the server, so only their presence and format are checked, not the values from the example. Fields marked `readOnly` are treated the same way. Request examples paired with a 4xx response are exempt from the check that examples match their schema; they are invalid on purpose. A request example paired with a **5xx** response the same way tests that error (`Dock/undockDock/overload` → 500).

### Error cases: 404, 409, 5xx

With `ErrorCases: true` apitest provokes two errors itself, for every operation that documents them:

| Case | When | Request | Expected |
|---|---|---|---|
| `<Tag>/<op>/not-found` | the operation has a path parameter and documents `404` | the last path parameter gets a key no record has: `999999999` for integers, a UUID, `apitest-not-found` for strings (within `minimum`/`maximum`/`maxLength`), or `x-apitest-not-found` of the parameter; all other values as in the regular case, and a required parameter without value (no example, or its producer failed) gets one generated from its schema, so the case is sent anyway | 404, body checked against the schema |
| `<Tag>/<op>/conflict` | a POST with a body that documents `409` | the body of the regular case a second time, right after it | 409, body checked against the schema |

5xx and other errors come from named examples (see above). With `TolerateErrorCases: true` an error case that gets another answer (`204` instead of `404`, `500` instead of `409`) is `TOLERATED` instead of failing the test; this covers the generated cases and named examples that expect 404, 409 or 5xx, while other 4xx examples (400, 422) still fail. The report analyses every error case: a matrix *expected → received* and per case the request, both statuses and the answer.

```go
apitest.Run(t, apitest.Config{
	// ...
	ErrorCases:         true,
	TolerateErrorCases: true, // report wrong error answers, do not fail on them (yet)
})
```

### Ignoring server fields

`workflows` get a `version` that the server increments, and `deployments` a `status` the server sets. Exclude such fields from the value comparison, globally or per operation:

```go
apitest.Run(t, apitest.Config{
	// ...
	IgnoreFields: []string{"createdAt", "version", "status"}, // field names, at every level
})
```

```yaml
/workflows/{workflowId}:
  put:
    operationId: updateWorkflow
    x-apitest-ignore: [/version, /rootStep/nextSteps/*/stepId]   # JSON pointers, * = any index
```

### Path parameters from earlier responses

`GET /organizations/{orgId}` needs the ID the POST returned. apitest resolves it in this order:

```yaml
# 1. explicit binding at the parameter
/organizations/{orgId}:
  parameters:
    - name: orgId
      in: path
      required: true
      schema: { type: string, format: uuid }
      x-apitest-bind: { from: createOrganization, pointer: /id }

# 2. OpenAPI links in the producer's response
/deployments:
  post:
    operationId: createDeployment
    responses:
      '201':
        content: { … }
        links:
          GetDeployment:
            operationId: getDeploymentById
            parameters: { deploymentId: '$response.body#/deploymentId' }
          DeleteDeployment:
            operationId: deleteDeployment
            parameters: { deploymentId: '$response.body#/deploymentId' }
```

3. Without either, a heuristic looks for the producing POST of the same tag: the collection in front of the parameter (`POST /workflows` for `/workflows/{workflowId}` and `/workflows/by-id/{workflowId}`), a POST named after the resource (`POST /workflow` for `workflowCode`), or a POST whose body has a field with the parameter's name. From that POST it takes a response field with the parameter's name, otherwise `id`, otherwise the `Location` header, otherwise the field from the sent body. Field names match case-insensitively (`WorkflowId` in the body, `workflowId` in the path). Each heuristic resolution is listed as a spec finding, so you can make it explicit. If a heuristic source turns out empty (the producer succeeded, but its body has no such field) and `Config.Params` has a value for the parameter, that value is used with a warning instead of skipping the dependent cases.

Headers work too: `x-apitest-bind: { from: createOrganization, header: Location }` takes the last path segment of `Location: /v1/organizations/a0ee…`. If the producer fails, the dependent cases are `SKIPPED` with the cause instead of failing on their own. When a PUT changes a bound key, the following cases use the value that was actually stored.

Across tags the heuristic only uses a POST named after the resource (`POST /organizations` for `organizationId`), and never a binding that would create a cycle between cases or tags; anything else across tags must be explicit (`x-apitest-bind` or `links`). Bindings also decide the order of the groups: if deployments reference organizations, all organization cases run first, and `deleteOrganization` runs after `deleteDeployment`.

### Query parameters and fixed values

```yaml
/organizations:
  get:
    operationId: listOrganizations
    parameters:
      - name: limit
        in: query
        schema: { type: integer, default: 20, example: 10 }   # sent: ?limit=10
      - name: offset
        in: query
        schema: { type: integer, default: 0 }                 # not sent: optional, only a default
```

Values that must match your seed data, or that the spec has no example for, come from `Config.Params` by parameter name:

```go
Params: map[string]string{"limit": "50", "tenant": "t-42"},
```

A generic name like `id` usually means a different resource in every operation. Prefix the key with the `operationId` to set it for one operation only; the prefixed key wins over the plain name, and a prefixed key that matches no parameter is reported as a warning:

```go
Params: map[string]string{
	"getOrganization.id": "org-1",
	"getDeployment.id":   "dep-7",
},
```

### Comparison modes and arrays

The expected example is compared as a **subset** by default: every field of the example must be present with the same value, additional fields are fine. Change it globally with `CompareMode`, or per operation, response or example:

```yaml
/workflows/{workflowId}:
  get:
    operationId: getWorkflowById
    x-apitest-compare: exact     # no additional fields allowed
```

Strings are compared case-sensitively. If the API changes the case of values or field names (`"NORD"` for `"Nord"`), set `CaseInsensitive: true`.

Arrays are compared in order. For arrays whose order does not matter, mark the schema:

```yaml
ClusterSpecDto:
  properties:
    nodePools:
      type: array
      x-apitest-compare-unordered: true
      items: { … }
```

### Asynchronous writes

A deployment is created with status `INITIATED` and becomes readable a moment later. Let the GET check repeat until it succeeds, or turn it off:

```yaml
/deployments:
  post:
    operationId: createDeployment
    x-apitest-verify: { poll: true, timeout: 30s }

/deployments/{deploymentId}:
  delete:
    operationId: deleteDeployment
    x-apitest-verify: false      # teardown takes minutes; do not wait for 404
```

### Authentication

For every operation with `security` that documents 401, apitest adds two cases: `unauthorized` (no token) and `invalid-token` (by default the first 100 characters of the real token; `TamperSignature` keeps the form and only breaks the signature). Operations with `x-apitest-forbidden: true` and a documented 403 also get a `forbidden` case, sent with a valid token that lacks rights:

```yaml
/organizations/{orgId}:
  delete:
    operationId: deleteOrganization
    x-apitest-forbidden: true
    responses:
      '204': { description: Organization deleted successfully }
      '401': { $ref: '#/components/responses/401Unauthorized' }
      '403': { $ref: '#/components/responses/403Forbidden' }
      '404': { $ref: '#/components/responses/404NotFound' }
```

```go
apitest.Run(t, apitest.Config{
	// ...
	Token:          apitest.StaticToken("mock-token-admin"),  // read and write
	ForbiddenToken: apitest.StaticToken("mock-token-reader"), // read only
})
```

By default the manipulated token is the first 100 characters of the real one: a JWT loses its signature and the end of its payload, so neither a verifier nor an API that only decodes tokens (behind a validating gateway) can accept it. To test the signature check itself, set `TamperToken: apitest.TamperSignature`; the token then keeps header and claims and only the signature is broken. Both tokens are redacted to `***` in the report. If the API accepts the manipulated token, the case fails with a hint saying what was sent. That is a finding about the API, not a token the library forgot to change.

Authentication cases run while the resource still exists, right before the DELETE. If the API accepts an unauthorized DELETE, that case fails and the regular DELETE is skipped with a reference to it. Operations that require a token but document neither 401 nor 403 get no authentication cases; the report lists them.

Tokens that expire can be fetched on demand, e.g. from the login endpoint of the API:

```go
Token: apitest.TokenFunc(func(ctx context.Context) (string, error) {
	return login(ctx, baseURL, "it-user", os.Getenv("IT_PASSWORD")) // cache it yourself if needed
}),
```

If the spec declares no `security` at all (neither globally nor on any operation), `Config.Token` is still sent as a bearer token to every operation, with a spec finding and a warning; declare `securitySchemes` and `security` to make this explicit and to get authentication cases. Whitespace, line breaks and a leading `Bearer ` in the token are removed before sending, since a pasted token often contains them.

Leave out authentication cases per environment with `SkipAuthCases`; they are then not run at all and do not show up as subtests. Headers for single cases, such as `Accept` for the error responses, are set in `Hooks.BeforeRequest`:

```go
cfg.SkipAuthCases = []string{apitest.AuthInvalidToken} // e.g. locally, without the token-checking proxy
cfg.Hooks.BeforeRequest = func(_ context.Context, c *apitest.Case, req *http.Request) error {
	if c.Example == apitest.AuthUnauthorized || c.Example == apitest.AuthInvalidToken {
		req.Header.Set("Accept", "text/plain")
	}
	return nil
}
```

A static JWT is checked at the start: an expired token aborts the run with one clear error instead of a 401 for every case.

### Skipping and ordering

```yaml
/deployments/{deploymentId}:
  delete:
    operationId: deleteDeployment
    x-apitest-skip: "tears down real cloud infrastructure"

/workflows:
  get:
    operationId: listWorkflows
    x-apitest-order: 1           # among operations of the same kind
```

### Resetting state and extra checks

```go
apitest.Run(t, apitest.Config{
	// ...
	Hooks: apitest.Hooks{
		// Before each resource group, e.g. truncate tables.
		BeforeGroup: func(ctx context.Context, group string) error {
			return db.Reset(ctx)
		},
		// After the built-in checks passed: look into the database.
		AfterResponse: func(ctx context.Context, c *apitest.Case, resp *apitest.Response) error {
			if c.OperationID != "createOrganization" {
				return nil
			}
			var n int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM organizations WHERE tax_id = 'DE123456789'`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("expected 1 organization in the database, found %d", n)
			}
			return nil
		},
		// Before sending, e.g. an idempotency key.
		BeforeRequest: func(ctx context.Context, c *apitest.Case, req *http.Request) error {
			req.Header.Set("Idempotency-Key", c.Name)
			return nil
		},
	},
})
```

A hook that returns an error fails its case; a panic in a hook only affects its case.

### Selecting what runs

```go
apitest.Run(t, apitest.Config{
	// ...
	Tags:       []string{"Organization", "Workflow"}, // only these groups, in this order
	ExcludeOps: []string{"deleteDeployment"},          // operationId or "DELETE /deployments/{deploymentId}"
})
```

```bash
# a single case; the producers it needs (createOrganization) run as preconditions
go test ./... -run 'TestAPI/Organization/updateOrganization/default'

# everything except the authentication cases ("/" separates the name levels)
go test ./... -run TestAPI -skip 'TestAPI/.*/.*/^(unauthorized|invalid-token|forbidden)$'
```

### Accepted deviations

Sometimes an API deviates from its spec on purpose or for a while. Instead of global tolerances, each deviation is allowed individually, with a reason and an expiry date:

```yaml
# apitest_deviations.yaml
- case: Organization/listOrganizations/default   # "*" matches within one name element
  expected: 200                                  # status code the spec expects
  actual: 206                                    # status code the API returns
  reason: "pagination answers 206 until API-431 is released"
  ticket: API-431                                # optional
  expires: 2026-12-31

- case: Workflow/*/default
  status: EXAMPLE_MISMATCH                       # for deviations that are not a status code
  pointer: /rootStep/action/handler              # optional: only differences at this location
  reason: "handler names are normalized to lower case"
  expires: 2026-11-30
```

```go
DeviationsPath: "apitest_deviations.yaml",
```

A matching case becomes `DEVIATION`. Entries without `reason` or `expires` are rejected. Expired entries fail a strict run; entries that expire within 14 days and entries that were not used are reported.

### Strict mode and reports

```go
apitest.Run(t, apitest.Config{
	// ...
	Strict: os.Getenv("CI") == "true", // NOT_BUILDABLE and expired deviations fail in CI

	// Reports (choose one):
	ReportPath: "apitest-report/orchestration.md", // without ReportPath no report files are written
	ReportJSON: true,                              // additionally orchestration.json
	// orchestration.html (dashboard) is written by default; DisableHTMLReport: true leaves it out
	OmitBodies: true,                              // no request/response bodies in the report
	Redact:     []string{"taxId", "x509PublicKey"}, // more fields to replace by ***

	// or no report files at all; results only via go test
	// DisableReports: true,
})
```

## Configuration reference

| Field | Meaning |
|---|---|
| `SpecPath` | OpenAPI file (YAML/JSON). Relative paths are relative to the test package directory. **Required.** |
| `BaseURL` | Base URL of the running API, including the servers' base path, e.g. `http://127.0.0.1:8080/v1`. |
| `Handler` | Alternative to `BaseURL`: an `http.Handler` served in-process via `httptest`. Set exactly one. |
| `Token` | `apitest.StaticToken(t)` or `apitest.TokenFunc(fn)`. Required if the spec uses `securitySchemes`; sent as bearer token everywhere if the spec declares no security. Asked before every request. |
| `ForbiddenToken` | Valid token with too few rights, for `forbidden` cases. Without it those cases are skipped. |
| `Tags`, `IncludeOps`, `ExcludeOps` | Select groups (in this order) and operations (`operationId` or `"POST /path"`). Producers of selected cases always run as preconditions. |
| `MethodOrder`, `DeleteLast` | Order of the regular cases within a group by method; DELETEs of all groups at the very end. |
| `LastInTag` | Operations (`operationId` or `"POST /path"`) whose cases run after all other cases of their tag, whatever the method, in the order listed; only the tag's DELETEs follow. |
| `NumberCases` | Prefix subtest names with their position (`07_Organization/…`) to show the execution order. |
| `SkipAuthCases` | Authentication cases left out completely: `apitest.AuthUnauthorized`, `AuthInvalidToken`, `AuthForbidden`. |
| `TamperToken` | Token of the `invalid-token` case: `apitest.TruncateToken` (default, first 100 characters), `apitest.TamperSignature` or your own function. |
| `Params`, `Headers` | Fixed parameter values by name (or `"<operationId>.<name>"` for one operation), extra headers for every request. |
| `IgnoreFields` | Field names or JSON pointers (`/items/*/id`) excluded from value comparison. |
| `CompareMode` | `subset` (default), `exact` or `schema`. |
| `CaseInsensitive` | Compare strings and field names of the example without regard to case (`"Nord"` matches `"nord"`, `Name` matches `name`); also the read-back after writes and `IgnoreFields`. The schema check stays case-sensitive. |
| `Strict` | `NOT_BUILDABLE` cases and expired deviations fail the test. apitest does not detect CI itself. |
| `DeviationsPath` | File with accepted deviations. |
| `RequestTimeout` | Per request, default 10 s. Requests are never retried. |
| `HealthPath` | Checked before the first case (2xx required). Without it any response on `/` counts as ready. |
| `AllowedHosts`, `AllowRemoteWrites` | Writing requests are refused unless the host is `localhost`/loopback, listed, or remote writes are allowed. |
| `HTTPClient` | Custom client, e.g. for mTLS. |
| `ReportPassedDetails`, `DisableWarnings` | Details for passed cases in the report; no warnings and no spec findings, neither in the `go test` output nor in the report. |
| `DisableReports` | Write no report files. |
| `ReportPath`, `ReportJSON`, `OmitBodies`, `Redact` | Report location, additional JSON report, leave out bodies, extra field names to redact. |
| `DisableHTMLReport` | Writes no HTML dashboard next to the Markdown report. |
| `ErrorCases`, `TolerateErrorCases` | Generate `not-found` (404) and `conflict` (409) cases; report wrong error answers as `TOLERATED` instead of failing. |
| `Hooks` | `BeforeGroup`, `AfterGroup`, `BeforeRequest`, `AfterResponse`. |

## Spec extensions

All extensions are optional. A spec without any extension works as long as it has examples.

| Extension | Where | Meaning |
|---|---|---|
| `x-apitest-skip: "<reason>"` | operation, named example | Skip the case; the reason is shown in the report. |
| `x-apitest-bind: {from, pointer \| header}` | parameter | Take the value from an earlier response. |
| `x-apitest-compare: schema \| subset \| exact` | operation, response, example | Override the comparison mode. |
| `x-apitest-ignore: [<pointer or field>]` | operation, response | Exclude fields from value comparison. |
| `x-apitest-compare-unordered: true` | response, array schema | Compare arrays independent of order. |
| `x-apitest-verify: false \| {poll: true, timeout: 10s}` | operation | Disable the GET check, or repeat it until it succeeds. |
| `x-apitest-order: <number>` | operation | Order operations of the same kind within a group. |
| `x-apitest-forbidden: true` | operation | Generate a `forbidden` case sent with `Config.ForbiddenToken`. |
| `x-apitest-not-found: <value>` | path parameter | The key the `not-found` case sends (`Config.ErrorCases`). |

Tips:

- Give every operation a tag; it becomes the resource group and the first part of the case name.
- Give request and response examples that belong together **the same name**.
- Describe error cases as named request examples paired with a 4xx or 5xx response of the same name; document 404 and 409 to get the generated `not-found` and `conflict` cases.
- Document 401 (and 403) for secured operations; otherwise authentication cannot be tested.

## Results and report

| Status | Meaning | Fails the test |
|---|---|---|
| `PASSED` | All checks passed. | no |
| `FAILED` | Unexpected status code. | yes |
| `SCHEMA_VIOLATION` | Response violates the schema. | yes |
| `EXAMPLE_MISMATCH` | Response differs from the expected example. | yes |
| `DATA_MISMATCH` | Write was not stored as sent, or DELETE did not delete. | yes |
| `ERROR` | Network error, timeout, refused write, panic in a hook. | yes |
| `DEVIATION` | Allowed by the deviations file. | only if expired and strict |
| `TOLERATED` | An error case got another answer than documented, with `TolerateErrorCases`. | no |
| `NOT_BUILDABLE` | No value for a required parameter or body. | only in strict mode |
| `SKIPPED` | Skipped on purpose or because a dependency failed. | no |

Reports are only written when `ReportPath` is set; without it the results go to `go test` and the returned `Result` only. With `ReportPath: "apitest-report/api.md"` apitest writes two reports with the same content, after every group, so an aborted run still leaves them:

- **`apitest-report/api.html`**, a self-contained dashboard (no external files, light and dark theme): key figures, cases by status, tags, response time histogram with avg/p50/p90/p95/p99/max, status codes, the slowest requests, the error case analysis, and every case in a table with search, status filters and expandable details.
- **`apitest-report/api.md`** for pull requests and CI artifacts, with the same figures as tables and a Mermaid chart.

The Markdown report contains a summary, coverage of operations and named examples, spec findings, every failed case with differences, schema errors, request/response bodies, the response headers and a `curl` command with the headers as sent, the deviations, and the passed cases. It is written after every group, so an aborted run still leaves a report. Tokens, `Authorization`/`Cookie` headers, API keys in query strings, `writeOnly` fields and fields named like `password`, `secret`, `token` or `apiKey` are replaced by `***`; `curl` commands use `$TOKEN` instead. `Run` also returns a `Result` with every case, for your own assertions:

```go
res := apitest.Run(t, cfg)
if res.Summary.OperationsCovered < res.Summary.Operations {
	t.Logf("%d operations were not called", res.Summary.Operations-res.Summary.OperationsCovered)
}
```

## Running in CI

apitest is plain `go test`. Set a timeout (the default of `go test` is 10 minutes), upload the report directory as an artifact, and pass secrets as environment variables that your test reads:

```yaml
# GitHub Actions
- uses: actions/setup-go@v5
  with: { go-version-file: go.mod }
- run: go test -tags=integration -timeout 20m ./test/integration/...
  env:
    API_TOKEN: ${{ secrets.API_TOKEN }}
- uses: actions/upload-artifact@v4
  if: always()
  with: { name: apitest-report, path: test/integration/apitest-report/ }
```

The report is Markdown, so it can also be shown in the job summary: `cat test/integration/apitest-report/*.md >> "$GITHUB_STEP_SUMMARY"`.

## apitest-gen: examples for your spec

apitest builds every request from the examples of the spec. `apitest-gen` writes the missing ones, so a spec without examples becomes testable. It is a command line tool and works for any OpenAPI 3.x file, whatever language the API is written in.

```sh
go install github.com/fada4773-sketch/specproof/cmd/apitest-gen@latest

apitest-gen -spec openapi.yaml -dict global-dict.json -defaults defaults.json -check
```

- **`global-dict.json`** is created on the first run: one value per DTO field and parameter, generated from the schema (formats, ranges, lengths, ECMA-262 patterns including lookaheads, readable values by field name). Later runs keep its values, so the examples stay stable.
- **`defaults.json`** holds values that must exist in your environment and win everywhere:

  ```json
  {
    "tenantId": "t-1001",
    "Planet.Name": "Mars",
    "getPlanet.id": 7,
    "planetId": 7,
    "updatePlanet.x-apitest-verify": false,
    "planetCode": {"from": "GET /planets", "pick": "/[active=true]/code"},
    "$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true}
  }
  ```

  Plain names apply everywhere; `Dto.field` and `operationId.field` narrow them. `/planets/{id}` takes `planetId`, derived from the path. `x-…` keys set extensions, and `from` fetches the value from a running environment (`-base-url`, `apitest-gen discover`). `$apitest` repeats the order settings of your `apitest.Config`.
- **Examples follow the data.** Every resource (`Planet`, `Moon`, …) gets records, fetched from a running instance with `-base-url` or generated. The cases are played in the order apitest runs them: an update sends new values, and every later GET, list and response example shows them. Path parameters, bodies and responses of one record always agree. No links or extensions are needed for this.
- The examples go **into the spec, in place** (or `-out`), where apitest reads them: parameter examples, request bodies, 2xx responses, at the `$ref` target of shared objects. Comments and key order stay; a second run changes nothing. A default that violates a schema stops the run before anything is written.
- **`-check`** (or `apitest-gen check`) then reports every case apitest could not send and every example that violates its schema, with exit code 1 for CI. It uses apitest's own case building, so it sees what a real run would see.
- Before anything is saved, the written spec is played again the way apitest will run it; an example that does not fit the data at its case stops the run, and nothing is written.
- **`apitest-gen record`** keeps the examples in one file, `examples.record.yaml`, for a test against an empty instance (a test container): per tag the requests in the order they run, each with its answer. `record -analyse` writes the entries, in apitest's order, with bodies from the schema and the ids of later requests linked to the answers of earlier ones (`save: { dockId: /id }`, `"{{dockId}}"`). `record -base-url <empty instance>` sends only the requests whose answer is missing or no longer fits the schema, stores the answers and writes every entry into the spec; without `-base-url` it only writes the stored answers, so everyone gets the same examples. Entries with `status: ignore` are never sent but get examples that fit the schema, generated where needed. Bindings apitest would only guess are written as `x-apitest-bind`, and a lint of the written spec lists everything `apitest.Run` would still warn about. See [docs/record.md](docs/record.md).
- **`apitest-gen review`** prints the resource model, evaluates what apitest would report as spec findings (missing 401/403, invalid examples, …), the cases it cannot send and the values the generator cannot create, and writes a fix for each into `defaults.json`. You check that file; the next `apitest-gen` run takes the entries into the spec.

The flags you need most often:

| Flag | Meaning |
|---|---|
| `-spec` | the OpenAPI file (required) |
| `-dict` | the dictionary, default `global-dict.json` |
| `-defaults` | one or more defaults files, comma-separated |
| `-out` | write the spec there instead of in place |
| `-base-url` | running instance: the records come from there (GET only) |
| `-check` | check the written spec, exit code 1 on problems |
| `-dry-run` | show what would change, write nothing |
| `-v` | verbose: also list every value and example written and how often each default was used; without it only problems are listed |

`apitest-gen help` prints all commands and flags.

With the examples removed from the Bookstore test spec, apitest can send 32 of 55 cases; after `apitest-gen`, all 55 run green.

## Example project: Swagger Petstore

[`examples/petstore`](examples/petstore) tests the public Swagger Petstore v3, started with Testcontainers, against its frozen spec. It is a separate module that uses apitest exactly like an external project.

```bash
make test-public      # needs Docker or Podman
```

It found six real deviations between the Petstore and its own spec, classified in [`findings.md`](examples/petstore/findings.md) and accepted in [`apitest_deviations.yaml`](examples/petstore/apitest_deviations.yaml).

With Podman, point Testcontainers to the Podman socket:

```bash
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
export TESTCONTAINERS_RYUK_DISABLED=true   # only if the Ryuk container does not start
```

## Limits

- Request bodies: JSON and `application/x-www-form-urlencoded`. `multipart/form-data` and binary uploads are `NOT_BUILDABLE`.
- Authentication: HTTP bearer, OAuth2/OpenID Connect (sent as bearer), API keys in header, query or cookie. HTTP basic and mutual TLS are not supported; such operations are `NOT_BUILDABLE`.
- List endpoints: only the first page is checked.
- No load tests, no fuzzing, no random data: everything comes from the spec's examples, so runs are deterministic.
- `$ref` to remote URLs is not loaded; references to local files are.

## Development

```bash
make test         # unit and component tests with the race detector
make check        # vet, golangci-lint, govulncheck, dependency check, tests, coverage >= 85 %
make perf         # load and plan the GitHub REST spec (1,231 operations) without the race detector
make test-public  # Petstore integration test (Docker/Podman)
make stability    # 20 Petstore runs with fresh containers, results must not change
```

Project layout:

| Path | Content |
|---|---|
| `apitest/` | the library: `Run`, `Config`, hooks, results, reports, component tests |
| `cmd/apitest-gen/` | the generator command |
| `internal/` | spec loading, cases, bindings, planning, requests, comparison, reports, and `internal/gen/` for the generator |
| `testdata/` | specs and golden files shared by the tests |
| `examples/petstore/` | reference test against the Swagger Petstore in a container (own module) |

The component tests run against a built-in test API (`internal/testserver`) with switches that build in exactly one kind of error each (wrong status, schema violation, lost update, no-op delete, ignored authentication, …); each switch must change exactly the expected cases.

## License

[MIT](LICENSE)
