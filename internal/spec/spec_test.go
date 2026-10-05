package spec

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testdata = "../../testdata/specs/"

type opSummary struct {
	ID, Method, Path string
	Tags             []string
}

func summarize(s *Spec) []opSummary {
	var out []opSummary
	for _, o := range s.Ops {
		out = append(out, opSummary{o.ID, o.Method, o.Path, o.Tags})
	}
	return out
}

func mustLoad(t *testing.T, file string) *Spec {
	t.Helper()
	s, err := Load(t.Context(), testdata+file)
	if err != nil {
		t.Fatalf("Load(%s): %v", file, err)
	}
	return s
}

func TestTPL1_01_LoadVersionsAndFormats(t *testing.T) {
	want := []opSummary{
		{"createPlanet", "POST", "/planets", []string{"Planet"}},
		{"listPlanets", "GET", "/planets", []string{"Planet"}},
	}
	for _, file := range []string{"basic30.yaml", "basic30.json", "basic31.yaml"} {
		t.Run(file, func(t *testing.T) {
			s := mustLoad(t, file)
			if got := summarize(s); !reflect.DeepEqual(got, want) {
				t.Errorf("operations: got %+v, want %+v", got, want)
			}
			// nullable (3.0) and type: [string, "null"] (3.1) must behave the same.
			schema := s.Doc.Components.Schemas["Planet"].Value
			v := NewValidator()
			if errs := v.Validate(schema, Normalize(map[string]any{"code": "l", "name": nil}), ModePlain); errs != nil {
				t.Errorf("name=null: got %v, want no errors", errs)
			}
			if errs := v.Validate(schema, Normalize(map[string]any{"code": "l", "name": 5}), ModePlain); len(errs) != 1 || errs[0].Pointer != "/name" {
				t.Errorf("name=5: got %v, want one error at /name", errs)
			}
			if len(s.Findings) != 0 {
				t.Errorf("findings: got %v, want none", s.Findings)
			}
		})
	}
	if !mustLoad(t, "basic31.yaml").Is31() || mustLoad(t, "basic30.yaml").Is31() {
		t.Error("Is31 reports the wrong version")
	}
}

func TestTPL1_02_CrossFileRefs(t *testing.T) {
	s := mustLoad(t, "multi/main.yaml")
	op := s.Op("getBook")
	if op == nil {
		t.Fatal("getBook not found")
	}
	if len(op.Params) != 1 || op.Params[0].Name != "id" {
		t.Fatalf("params: got %+v, want id from params.yaml", op.Params)
	}
	schema := op.Op.Responses.Status(200).Value.Content["application/json"].Schema.Value
	author := schema.Properties["author"]
	if author == nil || author.Value == nil || author.Value.Properties["name"] == nil {
		t.Fatalf("nested $ref to Author not resolved: %+v", author)
	}
}

func TestTPL1_02_ExternalURLRefused(t *testing.T) {
	_, err := Load(t.Context(), testdata+"external_url.yaml")
	if err == nil {
		t.Fatal("got nil error, want refusal of remote $ref")
	}
	if !strings.Contains(err.Error(), "https://example.com/remote.yaml") {
		t.Errorf("error should name the URL: %v", err)
	}
}

func TestTPL1_03_InvalidSpecs(t *testing.T) {
	abs, _ := filepath.Abs(testdata)
	tests := []struct {
		file string
		want string
	}{
		{"broken_syntax.yaml", "broken_syntax.yaml"},
		{"broken_ref.yaml", "Missing"},
		{"does_not_exist.yaml", "not readable"},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			_, err := Load(t.Context(), testdata+tc.file)
			if err == nil {
				t.Fatal("got nil error, want load error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q should contain %q", msg, tc.want)
			}
			if !strings.Contains(msg, abs) {
				t.Errorf("error %q should contain the absolute path %q (NFR-08)", msg, abs)
			}
		})
	}
}

func TestTPL1_04_ExampleNotMatchingSchema(t *testing.T) {
	s := mustLoad(t, "bad_example.yaml")
	var wheres []string
	for _, f := range s.Findings {
		if f.Kind != FindingExampleSchema {
			t.Errorf("unexpected finding kind %q", f.Kind)
		}
		wheres = append(wheres, f.Where)
	}
	want := []string{
		"components.schemas.Book.example",
		"paths./books.post.requestBody.content[application/json].examples.wrong-type",
	}
	if !reflect.DeepEqual(wheres, want) {
		t.Errorf("findings at %v, want %v", wheres, want)
	}
}

func TestTPL1_30_SubstituteIDsAndMergedParams(t *testing.T) {
	s := mustLoad(t, "ids.yaml")
	get := s.Op("GET /things/{id}")
	if get == nil || get.HasOperationID {
		t.Fatalf("substitute ID for GET /things/{id} missing: %+v", get)
	}
	if len(get.Params) != 2 || get.Params[0].Description != "op-level" {
		t.Errorf("op-level parameter must override path-level one: %+v", get.Params)
	}
	if len(get.Security) != 1 {
		t.Errorf("GET inherits document security: got %v", get.Security)
	}
	del := s.Op("deleteThing")
	if del == nil || len(del.Security) != 0 {
		t.Errorf("security: [] must mean no authentication: %+v", del)
	}
	if s.BasePath() != "" {
		t.Errorf("BasePath without servers: got %q, want empty", s.BasePath())
	}
	if got := mustLoad(t, "basic30.yaml").BasePath(); got != "/api/v1" {
		t.Errorf("BasePath: got %q, want /api/v1", got)
	}
}

func TestTPL1_30_DuplicateOperationID(t *testing.T) {
	_, err := Load(t.Context(), testdata+"dup_ids.yaml")
	if err == nil || !strings.Contains(err.Error(), `"same"`) {
		t.Fatalf("got %v, want duplicate operationId error", err)
	}
}

func TestIsJSON(t *testing.T) {
	for mt, want := range map[string]bool{
		"application/json":                true,
		"application/json; charset=utf-8": true,
		"application/problem+json":        true,
		"application/xml":                 false,
		"text/plain":                      false,
	} {
		if got := IsJSON(mt); got != want {
			t.Errorf("IsJSON(%q): got %v, want %v", mt, got, want)
		}
	}
}

func TestConflictingPathsAreAFinding(t *testing.T) {
	s := mustLoad(t, "conflict.yaml")
	if len(s.Ops) != 2 || len(s.Findings) != 1 || s.Findings[0].Kind != FindingValidation {
		t.Fatalf("ops %d, findings %+v", len(s.Ops), s.Findings)
	}
	if !strings.Contains(s.Findings[0].Message, "conflicts with") {
		t.Errorf("message: %s", s.Findings[0].Message)
	}
}

func TestSwagger2IsConverted(t *testing.T) {
	s := mustLoad(t, "swagger2.yaml")
	if !strings.HasPrefix(s.Version, "3.") || len(s.Ops) != 2 || s.BasePath() != "/api" {
		t.Fatalf("version %s, %d ops, base %q", s.Version, len(s.Ops), s.BasePath())
	}
	create := s.Op("createPet")
	rb := create.Op.RequestBody.Value.Content["application/json"]
	if rb == nil || rb.Schema.Value == nil || rb.Schema.Value.Properties["name"] == nil {
		t.Fatalf("request body schema not converted: %+v", create.Op.RequestBody.Value)
	}
	if s.Doc.Components.SecuritySchemes["key"] == nil {
		t.Error("security definitions must be converted")
	}
	found := false
	for _, f := range s.Findings {
		found = found || strings.Contains(f.Message, "Swagger 2.0 was converted")
	}
	if !found {
		t.Error("the conversion must be reported")
	}
}
