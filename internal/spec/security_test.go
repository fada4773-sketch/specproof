package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// loadInline writes src to a temporary file and loads it.
func loadInline(t *testing.T, src string) *Spec {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(t.Context(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

const noSecurity = `
openapi: 3.0.3
info: { title: t, version: "1" }
paths:
  /notes:
    get:
      operationId: listNotes
      responses: { "200": { description: ok } }
    post:
      operationId: createNote
      %s
      responses: { "201": { description: created } }
%s`

func TestAssumeBearerWithoutSecurity(t *testing.T) {
	s := loadInline(t, fmt.Sprintf(noSecurity, "", ""))
	name, ok := s.AssumeBearer()
	if !ok || name != AssumedScheme {
		t.Fatalf("AssumeBearer = %q, %v", name, ok)
	}
	sc := s.Doc.Components.SecuritySchemes[name]
	if sc == nil || sc.Value.Type != "http" || sc.Value.Scheme != "bearer" {
		t.Fatalf("scheme = %+v", sc)
	}
	for _, o := range s.Ops {
		if len(o.Security) != 1 || o.Security[0][name] == nil {
			t.Errorf("%s: security = %v", o.ID, o.Security)
		}
	}
}

func TestAssumeBearerReusesSingleScheme(t *testing.T) {
	comps := `
components:
  securitySchemes:
    jwt: { type: http, scheme: bearer, bearerFormat: JWT }`
	s := loadInline(t, fmt.Sprintf(noSecurity, "", comps))
	if name, ok := s.AssumeBearer(); !ok || name != "jwt" {
		t.Fatalf("AssumeBearer = %q, %v", name, ok)
	}
	if len(s.Doc.Components.SecuritySchemes) != 1 {
		t.Errorf("schemes = %v", s.Doc.Components.SecuritySchemes)
	}
}

func TestAssumeBearerKeepsDeclaredSecurity(t *testing.T) {
	// "security: []" explicitly declares a public operation.
	s := loadInline(t, fmt.Sprintf(noSecurity, "security: []", ""))
	if _, ok := s.AssumeBearer(); ok {
		t.Fatal("AssumeBearer changed a spec that declares security")
	}
	for _, o := range s.Ops {
		if len(o.Security) != 0 {
			t.Errorf("%s: security = %v", o.ID, o.Security)
		}
	}
}

func TestECMAScriptPattern(t *testing.T) {
	// Lookahead is valid ECMA-262 but not supported by Go's regexp.
	s := loadInline(t, `
openapi: 3.0.3
info: { title: t, version: "1" }
paths:
  /users:
    post:
      operationId: createUser
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: { type: string, pattern: '^(?!admin$)[a-z]+$' }
      responses: { "201": { description: created } }
`)
	schema := s.Op("createUser").Op.RequestBody.Value.Content["application/json"].Schema.Value
	v := NewValidator()
	if errs := v.Validate(schema, map[string]any{"name": "bob"}, ModePlain); len(errs) != 0 {
		t.Errorf("bob: %v", errs)
	}
	if errs := v.Validate(schema, map[string]any{"name": "admin"}, ModePlain); len(errs) == 0 {
		t.Error("admin: no error")
	}
}
