// Package spec loads an OpenAPI document and exposes its operations in a
// deterministic order. It encapsulates the OpenAPI library (kin-openapi) so the
// rest of apitest does not depend on its API directly.
package spec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/yaml"
)

// methodOrder fixes the iteration order of operations within a path.
var methodOrder = []string{
	http.MethodPost, http.MethodGet, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace,
}

// Spec is a loaded, validated OpenAPI document.
type Spec struct {
	Doc      *openapi3.T
	Path     string // absolute path of the spec file
	Version  string // value of the "openapi" field
	Ops      []*Operation
	Findings []Finding
}

// Operation is one path + method combination with its effective parameters
// and security requirements.
type Operation struct {
	// ID is the operationId, or the substitute "<METHOD> <path>" (FR-SPEC-06).
	ID             string
	HasOperationID bool
	Method         string
	Path           string
	Tags           []string
	Op             *openapi3.Operation
	// Params are path-level and operation-level parameters merged; operation
	// parameters override path parameters with the same name and location.
	Params []*openapi3.Parameter
	// Security is the effective security: the operation's own requirements
	// or, if unset, the document's. An empty slice means "no authentication".
	Security openapi3.SecurityRequirements
	// Where is the location of the operation in the spec, e.g. "paths./x.post".
	Where string
	// SecurityDeclared is set if the operation or the document declares
	// security, including an explicit "security: []".
	SecurityDeclared bool
}

// Finding is a problem in the spec that does not prevent loading.
type Finding struct {
	Kind    string // e.g. FindingExampleSchema
	Where   string // location in the spec
	Message string
}

// Finding kinds.
const (
	FindingExampleSchema = "example_schema"
	FindingHeuristic     = "heuristic"
	FindingBinding       = "binding"
	FindingValidation    = "validation"
	FindingAuth          = "auth"
)

// Load reads, resolves and validates the spec at path. Relative paths are
// resolved against the current working directory, as is usual in Go tests.
func Load(ctx context.Context, path string) (*Spec, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("spec path %q: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("spec file %s not readable: %w (paths are relative to the test package directory)", abs, err)
	}

	loader := openapi3.NewLoader()
	loader.Context = ctx
	// FR-SPEC-02: $ref to other local files is allowed, remote URLs are not.
	// localOnly also avoids kin-openapi's default reader, which keeps a
	// process-wide cache (FR-GO-04).
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = localOnly
	doc, converted, err := loadDocument(loader, abs)
	if err != nil {
		if where := diagnoseRefs(abs); where != "" {
			return nil, fmt.Errorf("spec %s could not be loaded: %s (%w)", abs, where, err)
		}
		return nil, fmt.Errorf("spec %s could not be loaded: %w", abs, err)
	}
	if doc.OpenAPI == "" {
		return nil, fmt.Errorf("spec %s: field \"openapi\" (or \"swagger\": \"2.0\") missing; this is not an OpenAPI document", abs)
	}
	findings, err := validate(ctx, doc)
	if err != nil {
		return nil, fmt.Errorf("spec %s is invalid: %w", abs, err)
	}
	if converted {
		findings = append(findings, Finding{Kind: FindingValidation, Where: "swagger", Message: "Swagger 2.0 was converted to OpenAPI " + doc.OpenAPI + " before testing (FR-SPEC-05)"})
	}

	s := &Spec{Doc: doc, Path: abs, Version: doc.OpenAPI, Findings: findings}
	if err := s.collectOperations(); err != nil {
		return nil, fmt.Errorf("Spec %s: %w", abs, err)
	}
	s.checkExamples()
	return s, nil
}

// localOnly allows $ref to local files but refuses remote URLs (FR-SPEC-02).
func localOnly(loader *openapi3.Loader, location *url.URL) ([]byte, error) {
	if location.Host != "" || (location.Scheme != "" && location.Scheme != "file") {
		return nil, fmt.Errorf("external reference %q is not loaded: only local files are allowed", location.String())
	}
	return openapi3.ReadFromFile(loader, location)
}

func (s *Spec) collectOperations() error {
	paths := s.Doc.Paths.Map()
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)

	seen := map[string]string{}
	for _, p := range keys {
		item := paths[p]
		ops := item.Operations()
		for _, m := range methodOrder {
			op, ok := ops[m]
			if !ok {
				continue
			}
			where := fmt.Sprintf("paths.%s.%s", p, strings.ToLower(m))
			o := &Operation{
				ID:             op.OperationID,
				HasOperationID: op.OperationID != "",
				Method:         m,
				Path:           p,
				Tags:           op.Tags,
				Op:             op,
				Params:         mergeParams(item.Parameters, op.Parameters),
				Where:          where,
			}
			if !o.HasOperationID {
				o.ID = m + " " + p
			}
			if prev, dup := seen[o.ID]; dup {
				return fmt.Errorf("duplicate operationId %q (%s and %s); operationIds must be unique", o.ID, prev, where)
			}
			seen[o.ID] = where
			switch {
			case op.Security != nil:
				o.Security, o.SecurityDeclared = *op.Security, true
			case s.Doc.Security != nil:
				o.Security, o.SecurityDeclared = s.Doc.Security, true
			default:
				o.Security = openapi3.SecurityRequirements{}
			}
			s.Ops = append(s.Ops, o)
		}
	}
	return nil
}

func mergeParams(pathLevel, opLevel openapi3.Parameters) []*openapi3.Parameter {
	var out []*openapi3.Parameter
	key := func(p *openapi3.Parameter) string { return p.In + ":" + p.Name }
	override := map[string]bool{}
	for _, r := range opLevel {
		if r != nil && r.Value != nil {
			override[key(r.Value)] = true
		}
	}
	for _, r := range pathLevel {
		if r != nil && r.Value != nil && !override[key(r.Value)] {
			out = append(out, r.Value)
		}
	}
	for _, r := range opLevel {
		if r != nil && r.Value != nil {
			out = append(out, r.Value)
		}
	}
	return out
}

// Op returns the operation with the given ID (operationId or substitute ID).
func (s *Spec) Op(id string) *Operation {
	for _, o := range s.Ops {
		if o.Is(id) {
			return o
		}
	}
	return nil
}

// Untagged is the group of operations without tags.
const Untagged = "untagged"

// Group is the resource group of the operation: its first tag.
func (o *Operation) Group() string {
	if len(o.Tags) > 0 && o.Tags[0] != "" {
		return o.Tags[0]
	}
	return Untagged
}

// Is reports whether id names this operation: its operationId or
// "<METHOD> <path>", which is accepted for every operation.
func (o *Operation) Is(id string) bool {
	return id == o.ID || id == o.Method+" "+o.Path
}

// Is31 reports whether the document is OpenAPI 3.1.x or later.
func (s *Spec) Is31() bool {
	return !strings.HasPrefix(s.Version, "3.0")
}

// BasePath returns the path component of the first server, or "" for "/".
func (s *Spec) BasePath() string {
	p, err := s.Doc.Servers.BasePath()
	if err != nil || p == "/" {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

// checkExamples validates every example against its schema (FR-SPEC-04).
func (s *Spec) checkExamples() {
	v := NewValidator()
	for _, o := range s.Ops {
		for _, p := range o.Params {
			where := fmt.Sprintf("%s.parameters[%s]", o.Where, p.Name)
			if p.Schema == nil || p.Schema.Value == nil {
				continue
			}
			if p.Example != nil {
				s.checkOne(v, where+".example", p.Schema.Value, p.Example, ModePlain)
			}
			for _, name := range sortedKeys(p.Examples) {
				if ex := p.Examples[name]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
					s.checkOne(v, where+".examples."+name, p.Schema.Value, ex.Value.Value, ModePlain)
				}
			}
		}
		if rb := o.Op.RequestBody; rb != nil && rb.Value != nil {
			// Request examples paired with a 4xx response of the same name are
			// negative tests (FR-CASE-05) and invalid on purpose.
			s.checkContent(v, o.Where+".requestBody", rb.Value.Content, ModeRequest, negativeExamples(o.Op))
		}
		if o.Op.Responses != nil {
			for code, r := range o.Op.Responses.Map() {
				if r != nil && r.Value != nil {
					s.checkContent(v, fmt.Sprintf("%s.responses.%s", o.Where, code), r.Value.Content, ModeResponse, nil)
				}
			}
		}
	}
	if s.Doc.Components != nil {
		for _, name := range sortedKeys(s.Doc.Components.Schemas) {
			ref := s.Doc.Components.Schemas[name]
			if ref == nil || ref.Value == nil {
				continue
			}
			where := "components.schemas." + name
			if ref.Value.Example != nil {
				s.checkOne(v, where+".example", ref.Value, ref.Value.Example, ModePlain)
			}
			for i, ex := range ref.Value.Examples {
				s.checkOne(v, fmt.Sprintf("%s.examples[%d]", where, i), ref.Value, ex, ModePlain)
			}
		}
	}
	sort.SliceStable(s.Findings, func(i, j int) bool { return s.Findings[i].Where < s.Findings[j].Where })
}

func (s *Spec) checkContent(v *Validator, where string, content openapi3.Content, mode Mode, skip map[string]bool) {
	for _, mt := range sortedKeys(content) {
		m := content[mt]
		if m == nil || m.Schema == nil || m.Schema.Value == nil || !IsJSON(mt) {
			continue
		}
		base := fmt.Sprintf("%s.content[%s]", where, mt)
		if m.Example != nil {
			s.checkOne(v, base+".example", m.Schema.Value, m.Example, mode)
		}
		for _, name := range sortedKeys(m.Examples) {
			if skip[name] {
				continue
			}
			if ex := m.Examples[name]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
				s.checkOne(v, base+".examples."+name, m.Schema.Value, ex.Value.Value, mode)
			}
		}
	}
}

// negativeExamples returns the names of examples used by 4xx responses.
func negativeExamples(op *openapi3.Operation) map[string]bool {
	out := map[string]bool{}
	if op.Responses == nil {
		return out
	}
	for code, r := range op.Responses.Map() {
		if len(code) != 3 || code[0] != '4' || r == nil || r.Value == nil {
			continue
		}
		for _, m := range r.Value.Content {
			if m == nil {
				continue
			}
			for name := range m.Examples {
				out[name] = true
			}
		}
	}
	return out
}

func (s *Spec) checkOne(v *Validator, where string, schema *openapi3.Schema, value any, mode Mode) {
	errs := v.Validate(schema, Normalize(value), mode)
	for _, e := range errs {
		s.Findings = append(s.Findings, Finding{
			Kind:    FindingExampleSchema,
			Where:   where,
			Message: fmt.Sprintf("example does not match the schema: %s", e),
		})
	}
}

// IsJSON reports whether a media type is JSON (application/json, application/*+json).
func IsJSON(mediaType string) bool {
	mt := strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	return mt == "application/json" || (strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json"))
}

// Normalize converts a value from YAML/JSON decoding into the canonical form
// used everywhere in apitest: maps, slices, strings, bools, nil and
// json.Number for numbers.
func Normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := DecodeJSON(b, &out); err != nil {
		return v
	}
	return out
}

// DecodeJSON decodes JSON with numbers kept as json.Number.
func DecodeJSON(b []byte, out any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("extra data after the JSON value")
	}
	return nil
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// validate checks the document structure (FR-SPEC-03). Conflicting path
// templates ("/a/{x}" and "/a/{y}") are reported as a finding instead of an
// error: they are invalid OpenAPI, but apitest addresses operations directly
// and is not affected. Real-world specs such as GitHub's REST description
// contain them.
func validate(ctx context.Context, doc *openapi3.T) ([]Finding, error) {
	err := doc.Validate(ctx, openapi3.DisableExamplesValidation(), openapi3.EnableMultiError(), openapi3.SetRegexCompiler(compileRegex))
	if err == nil {
		return nil, nil
	}
	var findings []Finding
	var fatal []error
	for _, e := range flatten(err) {
		var conflict *openapi3.ConflictingPathsError
		if errors.As(e, &conflict) {
			findings = append(findings, Finding{
				Kind:    FindingValidation,
				Where:   "paths." + conflict.Path2,
				Message: fmt.Sprintf("path %q conflicts with %q (same template); servers cannot route both", conflict.Path2, conflict.Path1),
			})
			continue
		}
		fatal = append(fatal, e)
	}
	if len(fatal) > 0 {
		return nil, errors.Join(fatal...)
	}
	return findings, nil
}

func flatten(err error) []error {
	var multi openapi3.MultiError
	if errors.As(err, &multi) {
		var out []error
		for _, e := range multi {
			out = append(out, flatten(e)...)
		}
		return out
	}
	return []error{err}
}

// loadDocument loads an OpenAPI 3 document, or converts a Swagger 2.0
// document to OpenAPI 3 (FR-SPEC-05).
func loadDocument(loader *openapi3.Loader, abs string) (*openapi3.T, bool, error) {
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, false, err
	}
	js, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, false, err
	}
	var head struct {
		Swagger string `json:"swagger"`
	}
	if err := json.Unmarshal(js, &head); err != nil || !strings.HasPrefix(head.Swagger, "2.") {
		doc, err := loader.LoadFromFile(abs)
		return doc, false, err
	}
	var doc2 openapi2.T
	if err := json.Unmarshal(js, &doc2); err != nil {
		return nil, true, fmt.Errorf("Swagger 2.0: %w", err)
	}
	doc, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return nil, true, fmt.Errorf("convert Swagger 2.0 to OpenAPI 3: %w", err)
	}
	// Without "host", kin-openapi drops basePath; keep it as a relative server.
	if len(doc.Servers) == 0 && doc2.BasePath != "" && doc2.BasePath != "/" {
		doc.Servers = openapi3.Servers{{URL: doc2.BasePath}}
	}
	if err := loader.ResolveRefsIn(doc, &url.URL{Path: filepath.ToSlash(abs)}); err != nil {
		return nil, true, fmt.Errorf("resolve $ref after converting Swagger 2.0: %w", err)
	}
	return doc, true, nil
}

// AssumedScheme is the security scheme apitest adds when a spec declares no
// security at all (see AssumeBearer).
const AssumedScheme = "apitestBearer"

// AssumeBearer handles specs that declare no security anywhere although the
// API needs a token: every operation without declared security requires a
// bearer token. It changes nothing if any operation or the document declares
// security (also "security: []"). With exactly one defined security scheme,
// that scheme is used, otherwise an http bearer scheme is added. It returns
// the scheme name and whether the spec was changed.
func (s *Spec) AssumeBearer() (string, bool) {
	for _, o := range s.Ops {
		if o.SecurityDeclared {
			return "", false
		}
	}
	if s.Doc.Components == nil {
		s.Doc.Components = &openapi3.Components{}
	}
	if s.Doc.Components.SecuritySchemes == nil {
		s.Doc.Components.SecuritySchemes = openapi3.SecuritySchemes{}
	}
	name := AssumedScheme
	if len(s.Doc.Components.SecuritySchemes) == 1 {
		for n := range s.Doc.Components.SecuritySchemes {
			name = n
		}
	} else {
		s.Doc.Components.SecuritySchemes[name] = &openapi3.SecuritySchemeRef{Value: openapi3.NewJWTSecurityScheme()}
	}
	for _, o := range s.Ops {
		o.Security = openapi3.SecurityRequirements{{name: []string{}}}
	}
	return name, true
}
