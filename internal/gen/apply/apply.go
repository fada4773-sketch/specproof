// Package apply writes examples into an OpenAPI file, at the places apitest
// reads them: parameter.example, requestBody content[mt].example and the
// examples of 2xx responses. Values come from, in this order:
//
//  1. defaults.json (wins over everything, also inside existing examples)
//  2. an existing example that fits its schema (unless Overwrite)
//  3. the dictionary
//
// A default that violates the schema of a place it applies to is fatal:
// nothing is written, so no environment-specific value ends up wrong.
package apply

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Codes of the notes.
const (
	CodeAdded         = "EXAMPLE_ADDED"
	CodeReplaced      = "EXAMPLE_REPLACED"   // the existing example did not fit its schema
	CodeDefaults      = "DEFAULTS_APPLIED"   // defaults were set inside an existing example
	CodeIncomplete    = "EXAMPLE_INCOMPLETE" // a required field has no value
	CodeExternalRef   = "EXTERNAL_REF"       // the target is in another file
	CodeSharedParam   = "SHARED_PARAM_CONFLICT"
	CodeGenericID     = "GENERIC_ID"            // where the value of {id} comes from
	CodeExtension     = "EXT_FROM_DEFAULTS"     // an extension was set on an operation
	CodeBind          = "BIND_WRITTEN"          // a binding from defaults.json was written
	CodeBindSkipped   = "BIND_NOT_WRITTEN"      // a binding could not be written
	CodeDefaultUnused = "DEFAULT_UNUSED"        // a defaults entry matched nothing
	CodeNamedInvalid  = "EXAMPLE_NAMED_INVALID" // a curated named example violates its schema
	CodeDictDefault   = "DICT_FROM_DEFAULTS"    // a default was kept in the dictionary
	CodeDefaultTodo   = "DEFAULT_TODO"          // a defaults entry is null: its value is still missing
)

// Options control Apply.
type Options struct {
	Seed uint64
	// Overwrite regenerates existing examples instead of keeping them.
	Overwrite bool
	// GenericIDs are path parameter names that mean another resource on
	// every path, by default id, uuid and key.
	GenericIDs []string
	// RecordKey reports whether a path parameter holds the key of a record
	// (internal/gen/scenario). Its defaults select the record, which gives
	// every path its value, so a shared parameter object is no conflict.
	RecordKey func(op *spec.Operation, param string) bool
}

// Note is one message.
type Note struct {
	Code    string
	Where   string // location in the spec, e.g. "paths./pilots/{id}.get.parameters[id]"
	Message string
}

// Stats counts what Apply did.
type Stats struct {
	Added, Replaced, DefaultsApplied, Kept, Incomplete int
	Extensions, Bindings                               int
	Dict                                               int // dictionary values taken from the defaults
}

// Result of Apply. With Fatal entries the document must not be saved.
type Result struct {
	Notes   []Note
	Fatal   []string
	Stats   Stats
	Changed bool
}

// Apply writes the examples into doc. s is the same file loaded by
// apitest's spec loader, d the dictionary (its Paths section may be
// extended), defs the defaults.
func Apply(doc *yamldoc.Doc, s *spec.Spec, d *dict.Dict, defs *defaults.Defaults, opt Options) *Result {
	if len(opt.GenericIDs) == 0 {
		opt.GenericIDs = []string{"id", "uuid", "key"}
	}
	a := &applier{doc: doc, s: s, d: d, defs: defs, opt: opt, v: spec.NewValidator(),
		res: &Result{}, written: map[*yaml.Node]bool{}, fatalSeen: map[string]bool{}}
	for _, op := range s.Ops {
		a.operation(op)
	}
	a.schemaExamples()
	for _, key := range defs.Todos() {
		a.note(CodeDefaultTodo, "defaults", fmt.Sprintf("%q is null; set a value that exists in the test environment", key))
	}
	for _, key := range defs.Unused() {
		msg := fmt.Sprintf("%q matched no field, parameter or operation; check the spelling", key)
		if dto := a.dtoName(key); dto != "" {
			msg = fmt.Sprintf("%q matched no field; to set the DTO %s itself use the key %q", key, dto, DTOKey(dto))
		}
		a.note(CodeDefaultUnused, "defaults", msg)
	}
	return a.res
}

// dtoName returns the DTO a key names, ignoring case, if it is one.
func (a *applier) dtoName(key string) string {
	if a.s.Doc.Components == nil {
		return ""
	}
	for name := range a.s.Doc.Components.Schemas {
		if strings.EqualFold(name, key) {
			return name
		}
	}
	return ""
}

type applier struct {
	doc       *yamldoc.Doc
	s         *spec.Spec
	d         *dict.Dict
	defs      *defaults.Defaults
	opt       Options
	v         *spec.Validator
	res       *Result
	written   map[*yaml.Node]bool // shared targets are handled once
	fatalSeen map[string]bool
}

func (a *applier) note(code, where, msg string) {
	a.res.Notes = append(a.res.Notes, Note{Code: code, Where: where, Message: msg})
}

func (a *applier) fatal(msg string) {
	if !a.fatalSeen[msg] {
		a.fatalSeen[msg] = true
		a.res.Fatal = append(a.res.Fatal, msg)
	}
}

// opName is the operationId for qualified defaults; operations without one
// cannot be addressed there.
func opName(op *spec.Operation) string {
	if op.HasOperationID {
		return op.ID
	}
	return ""
}

func (a *applier) operation(op *spec.Operation) {
	itemY := yamldoc.Path(a.doc.Root, "paths", op.Path)
	opY := yamldoc.Get(itemY, strings.ToLower(op.Method))
	if opY == nil {
		return
	}
	where := fmt.Sprintf("paths.%s.%s", op.Path, strings.ToLower(op.Method))
	item := a.s.Doc.Paths.Value(op.Path)

	// parameters: operation level overrides path level with the same in+name
	own := map[string]bool{}
	for _, p := range op.Op.Parameters {
		if p.Value != nil {
			own[p.Value.In+"."+p.Value.Name] = true
		}
	}
	if item != nil {
		a.parameters(op, item.Parameters, yamldoc.Get(itemY, "parameters"), true, own, where)
	}
	a.parameters(op, op.Op.Parameters, yamldoc.Get(opY, "parameters"), false, nil, where)

	// request body
	if rb := op.Op.RequestBody; rb != nil && rb.Value != nil {
		if target := a.target(yamldoc.Get(opY, "requestBody"), where+".requestBody"); target != nil {
			a.content(op, rb.Value.Content, yamldoc.Get(target, "content"), spec.ModeRequest, where+".requestBody")
		}
	}

	// responses: 2xx and default get examples, the others only have
	// invalid ones replaced, because apitest validates every example
	if op.Op.Responses != nil {
		respY := yamldoc.Get(opY, "responses")
		for _, code := range sortedKeys(op.Op.Responses.Map()) {
			ref := op.Op.Responses.Map()[code]
			if ref.Value == nil {
				continue
			}
			success := (len(code) == 3 && code[0] == '2') || code == "default"
			w := where + ".responses." + code
			if target := a.target(yamldoc.Get(respY, code), w); target != nil {
				a.contentFor(op, ref.Value.Content, yamldoc.Get(target, "content"), spec.ModeResponse, w, !success)
			}
		}
	}

	a.extensions(op, opY, where)
}

// target resolves a node that may be a local $ref. Shared targets are
// returned only the first time.
func (a *applier) target(n *yaml.Node, where string) *yaml.Node {
	if n == nil {
		return nil
	}
	t, err := a.doc.Resolve(n)
	if err != nil {
		a.note(CodeExternalRef, where, err.Error()+"; examples there are not written")
		return nil
	}
	if yamldoc.Ref(n) != "" {
		if a.written[t] {
			return nil
		}
		a.written[t] = true
	}
	return t
}

// ---------------------------------------------------------------------
// Parameters

func (a *applier) parameters(op *spec.Operation, params openapi3.Parameters, listY *yaml.Node, pathLevel bool, overridden map[string]bool, where string) {
	if listY == nil || listY.Kind != yaml.SequenceNode {
		return
	}
	for i, ref := range params {
		if i >= len(listY.Content) || ref == nil || ref.Value == nil {
			continue
		}
		p := ref.Value
		if overridden[p.In+"."+p.Name] || p.Schema == nil || p.Schema.Value == nil {
			continue
		}
		py := listY.Content[i]
		w := fmt.Sprintf("%s.parameters[%s]", where, p.Name)
		shared := pathLevel || yamldoc.Ref(py) != ""
		target, err := a.doc.Resolve(py)
		if err != nil {
			a.note(CodeExternalRef, w, err.Error())
			continue
		}
		a.bindParam(op, p, py, shared, w)
		scopeOp := opName(op)
		if shared {
			// a shared object holds one value for all operations
			scopeOp = ""
			if e := a.defs.Scoped(opName(op), p.Name); e != nil {
				a.defs.Use(e) // it matched, the conflict is reported instead
				if a.recordKey(op, p) {
					a.note(CodeGenericID, w, fmt.Sprintf("%q selects the record; the records give this path its value", e.Key))
				} else {
					a.note(CodeSharedParam, w, fmt.Sprintf("%q needs its own value, but the parameter object is shared with other operations; the shared value is used", e.Key))
				}
			}
			if a.written[target] {
				a.sharedGenericID(op, p, target, w)
				continue
			}
		}
		a.written[target] = true
		a.parameter(op, p, target, scopeOp, w)
	}
}

// sharedGenericID reports a path default for a generic id that cannot be
// written, because the parameter object is shared by several paths and
// already has the value of another one.
func (a *applier) sharedGenericID(op *spec.Operation, p *openapi3.Parameter, target *yaml.Node, where string) {
	if !a.isGeneric(p) {
		return
	}
	bound := op.HasOperationID && a.defs.Binding(op.ID, p.Name) != nil
	keys := append([]string{op.Path}, resourceKeys(op.Path, p.Name)...)
	for _, k := range keys {
		e := a.defs.Plain(k)
		if e == nil {
			continue
		}
		a.defs.Use(e)
		if bound {
			a.note(CodeGenericID, where, fmt.Sprintf("%q is not needed: the parameter is bound (%q), apitest takes the value at run time", e.Key, op.ID+"."+p.Name))
			return
		}
		if a.recordKey(op, p) {
			a.note(CodeGenericID, where, fmt.Sprintf("%q selects the record; the records give this path its value", e.Key))
			return
		}
		current := "none"
		if ex := yamldoc.Get(target, "example"); ex != nil {
			if v, err := yamldoc.Decode(ex); err == nil {
				current = compact(v)
			}
		}
		if current == compact(e.Value) {
			return // the same value: nothing is lost
		}
		a.note(CodeSharedParam, where, fmt.Sprintf("%q = %s is not written: the parameter %q is shared by several paths and has one example (%s); bind it instead (apitest-gen review proposes a binding), or define the parameter in the operation",
			e.Key, compact(e.Value), p.Name, current))
		return
	}
}

func (a *applier) parameter(op *spec.Operation, p *openapi3.Parameter, target *yaml.Node, scopeOp, where string) {
	s := p.Schema.Value
	var existing any
	if ex := yamldoc.Get(target, "example"); ex != nil {
		existing, _ = yamldoc.Decode(ex)
	}

	var v any
	switch {
	case a.isGeneric(p):
		v = a.genericID(op, p, scopeOp, where)
	default:
		if e := a.defs.Field("", scopeOp, p.Name); e != nil {
			v = a.useDefault(e, s, spec.ModePlain, where)
			if v != nil {
				a.remember(a.d.Parameters[p.In+"."+p.Name], e, v, "parameters."+p.In+"."+p.Name)
			}
		}
	}
	if v == nil && existing != nil && !a.opt.Overwrite && a.valid(s, existing, spec.ModePlain) {
		a.res.Stats.Kept++
		return
	}
	if v == nil {
		if n := a.d.Parameters[p.In+"."+p.Name]; n != nil && n.Value != nil && a.valid(s, n.Value, spec.ModePlain) {
			v = n.Value
		}
	}
	if v == nil {
		if existing == nil && p.Required {
			a.res.Stats.Incomplete++
			a.note(CodeIncomplete, where, "required parameter without value; set it in defaults.json")
		}
		return
	}
	a.write(target, "example", existing, v, where, s, spec.ModePlain)
}

func (a *applier) recordKey(op *spec.Operation, p *openapi3.Parameter) bool {
	return a.opt.RecordKey != nil && p.In == openapi3.ParameterInPath && a.opt.RecordKey(op, p.Name)
}

func (a *applier) isGeneric(p *openapi3.Parameter) bool {
	return p.In == openapi3.ParameterInPath && slices.ContainsFunc(a.opt.GenericIDs, func(g string) bool { return strings.EqualFold(g, p.Name) })
}

// genericID resolves a path parameter like {id}: operation key, path key,
// the resource named by the path segment (/pilots/{id} → pilotId), the
// response DTO (App.id), the plain name, and finally a value kept per path
// in the dictionary.
func (a *applier) genericID(op *spec.Operation, p *openapi3.Parameter, scopeOp, where string) any {
	s := p.Schema.Value
	candidates := []*defaults.Entry{a.defs.Scoped(scopeOp, p.Name), a.defs.Plain(op.Path)}
	for _, k := range resourceKeys(op.Path, p.Name) {
		candidates = append(candidates, a.defs.Plain(k))
	}
	if dto := responseDTO(op); dto != "" {
		candidates = append(candidates, a.defs.Plain(dto+"."+p.Name))
	}
	candidates = append(candidates, a.defs.Plain(p.Name))
	for _, e := range candidates {
		if e != nil {
			a.note(CodeGenericID, where, fmt.Sprintf("from defaults %q", e.Key))
			return a.useDefault(e, s, spec.ModePlain, where)
		}
	}
	if v, ok := a.d.Paths[op.Path]; ok && a.valid(s, v, spec.ModePlain) {
		return v
	}
	r := value.Generate(s, value.Context{Seed: a.opt.Seed, Path: "paths." + op.Path, Name: p.Name})
	if !r.OK || !a.valid(s, r.Value, spec.ModePlain) {
		return nil
	}
	v := spec.Normalize(r.Value)
	a.d.Paths[op.Path] = v
	a.note(CodeGenericID, where, fmt.Sprintf("no default; generated %v and kept it in the dictionary under %q; set a real id in defaults.json with that key", v, op.Path))
	return v
}

// resourceKeys derives default keys from the segment in front of a path
// parameter: /app-versions/{id} → appVersionId, appVersionsId.
func resourceKeys(path, param string) []string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	i := slices.Index(segs, "{"+param+"}")
	if i <= 0 || strings.HasPrefix(segs[i-1], "{") {
		return nil
	}
	seg := segs[i-1]
	var out []string
	for _, word := range singulars(seg) {
		out = append(out, camel(word)+"Id")
	}
	return out
}

func singulars(w string) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	switch {
	case strings.HasSuffix(w, "ies"):
		add(strings.TrimSuffix(w, "ies") + "y")
	case strings.HasSuffix(w, "es"):
		add(strings.TrimSuffix(w, "s"))
		add(strings.TrimSuffix(w, "es"))
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		add(strings.TrimSuffix(w, "s"))
	}
	add(w)
	return out
}

func camel(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' })
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// responseDTO is the DTO of the lowest 2xx JSON response.
func responseDTO(op *spec.Operation) string {
	if op.Op.Responses == nil {
		return ""
	}
	codes := make([]string, 0)
	for code := range op.Op.Responses.Map() {
		if len(code) == 3 && code[0] == '2' {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	for _, code := range codes {
		r := op.Op.Responses.Map()[code].Value
		if r == nil {
			continue
		}
		for mt, m := range r.Content {
			if spec.IsJSON(mt) && m.Schema != nil {
				return dict.DTORef(m.Schema)
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------
// Bodies

func (a *applier) content(op *spec.Operation, content openapi3.Content, contentY *yaml.Node, mode spec.Mode, where string) {
	a.contentFor(op, content, contentY, mode, where, false)
}

// contentFor handles the media types of a body. With replaceOnly an
// existing invalid example is replaced, but a missing one is not added.
func (a *applier) contentFor(op *spec.Operation, content openapi3.Content, contentY *yaml.Node, mode spec.Mode, where string, replaceOnly bool) {
	for _, mt := range sortedKeys(content) {
		if !spec.IsJSON(mt) && !strings.EqualFold(strings.Split(mt, ";")[0], "application/x-www-form-urlencoded") {
			continue
		}
		media := content[mt]
		mediaY := yamldoc.Get(contentY, mt)
		if media == nil || media.Schema == nil || mediaY == nil {
			continue
		}
		w := fmt.Sprintf("%s.content[%s]", where, mt)
		a.media(op, media, mediaY, mode, w, replaceOnly)
	}
}

func (a *applier) media(op *spec.Operation, media *openapi3.MediaType, mediaY *yaml.Node, mode spec.Mode, where string, replaceOnly bool) {
	schema := media.Schema
	var existing any
	if ex := yamldoc.Get(mediaY, "example"); ex != nil {
		existing, _ = yamldoc.Decode(ex)
	}

	// named examples are curated test cases: only defaults go into them
	if exsY := yamldoc.Get(mediaY, "examples"); exsY != nil && exsY.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(exsY.Content); i += 2 {
			name := exsY.Content[i].Value
			exY, err := a.doc.Resolve(exsY.Content[i+1])
			if err != nil || a.written[exY] {
				continue
			}
			a.written[exY] = true
			if off := yamldoc.Get(exY, "x-example-defaults"); off != nil && off.Value == "false" {
				continue
			}
			valY := yamldoc.Get(exY, "value")
			if valY == nil {
				continue
			}
			v, err := yamldoc.Decode(valY)
			if err != nil {
				continue
			}
			// curated test cases are never replaced, but an invalid one is
			// named; request examples of a 4xx response are invalid on purpose
			negativeTest := mode == spec.ModeRequest && negative(op)[name]
			if !negativeTest && !a.valid(schema.Value, v, mode) {
				a.note(CodeNamedInvalid, where+".examples."+name, "the named example does not fit its schema; apitest-gen keeps curated examples, fix it by hand")
			}
			if nv, changed := a.withDefaults(v, schema, "", nil, opName(op), mode, where+".examples."+name); changed {
				if err := yamldoc.Set(exY, "value", nv); err == nil {
					a.res.Changed = true
					a.res.Stats.DefaultsApplied++
					a.note(CodeDefaults, where+".examples."+name, "defaults set in the named example")
				}
			}
		}
	}

	// "example" and "examples" exclude each other; with named examples
	// apitest builds one case per name and needs no "example"
	if exsY := yamldoc.Get(mediaY, "examples"); exsY != nil && len(exsY.Content) > 0 {
		return
	}
	if replaceOnly && (existing == nil || a.valid(schema.Value, existing, mode)) {
		return
	}
	if existing != nil && !a.opt.Overwrite && a.valid(schema.Value, existing, mode) {
		nv, changed := a.withDefaults(existing, schema, "", nil, opName(op), mode, where)
		if changed {
			a.write(mediaY, "example", existing, nv, where, schema.Value, mode)
			return
		}
		a.res.Stats.Kept++
		return
	}
	v, ok, missing := a.build(schema, "", nil, "", opName(op), mode, 0, nil)
	if !ok {
		a.res.Stats.Incomplete++
		a.note(CodeIncomplete, where, fmt.Sprintf("no example: %s has no value; set it in defaults.json or the dictionary", missing))
		return
	}
	a.write(mediaY, "example", existing, v, where, schema.Value, mode)
}

// build composes an example from the dictionary and the defaults. dto is
// the enclosing DTO, dn its dictionary node at this place.
func (a *applier) build(ref *openapi3.SchemaRef, dto string, dn *dict.Node, name, op string, mode spec.Mode, depth int, stack []string) (any, bool, string) {
	if ref == nil || ref.Value == nil {
		return nil, false, name
	}
	if name != "" && !isPlainSchema(ref) {
		if e := a.defs.Field(dto, op, name); e != nil {
			gen, ok, _ := a.dtoValue(ref, dto, dn, name, op, mode, depth, stack)
			if !ok {
				gen = nil // the default may provide what is missing
			}
			if v := a.objectDefault(e, gen, ref, mode, dtoWhere(dto, name)); v != nil {
				return v, true, ""
			}
			return nil, false, dtoWhere(dto, name)
		}
	}
	return a.dtoValue(ref, dto, dn, name, op, mode, depth, stack)
}

// dtoValue composes the value of a place and lays the default of its DTO
// ("#/components/schemas/<Dto>") over it.
func (a *applier) dtoValue(ref *openapi3.SchemaRef, dto string, dn *dict.Node, name, op string, mode spec.Mode, depth int, stack []string) (any, bool, string) {
	gen, ok, missing := a.compose(ref, dto, dn, name, op, mode, depth, stack)
	e, where := a.dtoDefault(ref)
	if e == nil {
		return gen, ok, missing
	}
	if !ok {
		gen = nil // the default may provide what is missing
	}
	if v := a.objectDefault(e, gen, ref, mode, where); v != nil {
		return v, true, ""
	}
	return nil, false, where
}

// dtoDefault returns the default for the DTO a place refers to.
func (a *applier) dtoDefault(ref *openapi3.SchemaRef) (*defaults.Entry, string) {
	target := dict.DTORef(ref)
	if target == "" {
		return nil, ""
	}
	e := a.defs.Plain(DTOKey(target))
	if e != nil {
		a.remember(a.d.Schemas[target], e, coerce(e.Value, ref.Value), "schemas."+target)
	}
	return e, "components.schemas." + target
}

// dictField returns the dictionary node of a field of a DTO.
func (a *applier) dictField(dto, name string) *dict.Node {
	n := a.d.Schemas[dto]
	for i := 0; n != nil && n.Ref != "" && i < 30; i++ { // alias
		n = a.d.Schemas[n.Ref]
	}
	if n == nil {
		return nil
	}
	return n.Properties[name]
}

// remember keeps a default in the dictionary, so the dictionary shows the
// values the spec uses. A leaf takes the value; a DTO passes the fields of
// an object default on to its own leaves, but not into other DTOs, which
// the default does not set everywhere. Operation-scoped defaults are not
// kept: a node is shared by every operation.
func (a *applier) remember(n *dict.Node, e *defaults.Entry, v any, where string) {
	if n == nil || a.operationScoped(e) {
		return
	}
	a.keep(n, v, where, 0)
}

func (a *applier) keep(n *dict.Node, v any, where string, depth int) {
	if n == nil || depth > 20 || n.Ref != "" {
		return
	}
	if n.Leaf() {
		if v == nil || reflect.DeepEqual(spec.Normalize(n.Value), spec.Normalize(v)) {
			return
		}
		n.Value = v
		a.res.Stats.Dict++
		a.note(CodeDictDefault, where, compact(v))
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			a.keep(n.Properties[k], x[k], where+"."+k, depth+1)
		}
	case []any:
		if len(x) > 0 {
			a.keep(n.Items, x[0], where+"[]", depth+1)
		}
	}
}

// operationScoped reports whether a key is "<operationId>.<name>".
func (a *applier) operationScoped(e *defaults.Entry) bool {
	prefix, _, found := strings.Cut(e.Key, ".")
	return found && a.s.Op(prefix) != nil && a.d.Schemas[prefix] == nil
}

// DTOKey is the defaults.json key for a whole DTO.
func DTOKey(name string) string { return "#/components/schemas/" + name }

// compose builds the value of one place without a default for the place
// itself; defaults of nested fields are used.
func (a *applier) compose(ref *openapi3.SchemaRef, dto string, dn *dict.Node, name, op string, mode spec.Mode, depth int, stack []string) (any, bool, string) {
	if depth > 20 {
		return nil, false, name + " (nesting too deep)"
	}
	if target := dict.DTORef(ref); target != "" {
		if slices.Contains(stack, target) {
			return nil, false, name + " (cycle)"
		}
		stack = append(stack, target)
		dto, dn = target, a.d.Schemas[target]
		for dn != nil && dn.Ref != "" && len(stack) < 30 { // alias
			dn = a.d.Schemas[dn.Ref]
		}
	}
	s := ref.Value
	props, required := dict.Properties(s)
	if len(props) == 0 && len(s.AllOf) == 0 {
		switch {
		case len(s.OneOf) > 0:
			return a.build(s.OneOf[0], dto, nil, name, op, mode, depth+1, stack)
		case len(s.AnyOf) > 0:
			return a.build(s.AnyOf[0], dto, nil, name, op, mode, depth+1, stack)
		}
	}
	if len(props) > 0 {
		obj := map[string]any{}
		for _, k := range sortedKeys(props) {
			c := props[k]
			if c.Value == nil || (mode == spec.ModeRequest && c.Value.ReadOnly) || (mode == spec.ModeResponse && c.Value.WriteOnly) {
				continue
			}
			var cn *dict.Node
			if dn != nil {
				cn = dn.Properties[k]
			}
			v, ok, missing := a.build(c, dto, cn, k, op, mode, depth+1, stack)
			if !ok {
				if slices.Contains(required, k) {
					return nil, false, missing
				}
				continue
			}
			obj[k] = v
		}
		return obj, true, ""
	}
	if value.Type(s) == "array" && s.Items != nil && s.Items.Value != nil {
		if p, _ := dict.Properties(s.Items.Value); len(p) > 0 || dict.DTORef(s.Items) != "" {
			var in *dict.Node
			if dn != nil {
				in = dn.Items
			}
			item, ok, missing := a.build(s.Items, dto, in, name, op, mode, depth+1, stack)
			if !ok {
				return nil, false, missing
			}
			return []any{item}, true, ""
		}
	}
	return a.leaf(s, dto, dn, name, op)
}

func (a *applier) leaf(s *openapi3.Schema, dto string, dn *dict.Node, name, op string) (any, bool, string) {
	where := name
	if dto != "" {
		where = dto + "." + name
	}
	if name != "" {
		if e := a.defs.Field(dto, op, name); e != nil {
			if v := a.useDefault(e, s, spec.ModePlain, where); v != nil {
				a.remember(dn, e, v, "schemas."+where)
				return v, true, ""
			}
			return nil, false, where
		}
	}
	if dn != nil && dn.Value != nil && a.valid(s, dn.Value, spec.ModePlain) {
		return dn.Value, true, ""
	}
	if dn == nil && name != "" {
		// an inline schema outside any DTO has no dictionary node; its value
		// is generated here, seeded by its place so it stays the same
		r := value.Generate(s, value.Context{Seed: a.opt.Seed, Path: "inline." + op + "." + where, Name: name, Parent: dto})
		if r.OK && a.valid(s, r.Value, spec.ModePlain) {
			return r.Value, true, ""
		}
	}
	return nil, false, where
}

// withDefaults sets defaults in the fields an existing example already has
// and lays the default of its DTO over it.
func (a *applier) withDefaults(v any, ref *openapi3.SchemaRef, dto string, stack []string, op string, mode spec.Mode, where string) (any, bool) {
	nv, changed := a.fieldDefaults(v, ref, dto, stack, op, mode, where)
	if e, at := a.dtoDefault(ref); e != nil && len(stack) <= 20 {
		if ov := a.objectDefault(e, nv, ref, mode, at); ov != nil && !reflect.DeepEqual(spec.Normalize(ov), spec.Normalize(v)) {
			return ov, true
		}
	}
	return nv, changed
}

// fieldDefaults sets the defaults of the fields an existing example
// already has; it never adds fields.
func (a *applier) fieldDefaults(v any, ref *openapi3.SchemaRef, dto string, stack []string, op string, mode spec.Mode, where string) (any, bool) {
	if ref == nil || ref.Value == nil || len(stack) > 20 {
		return v, false
	}
	if target := dict.DTORef(ref); target != "" {
		if slices.Contains(stack, target) {
			return v, false
		}
		dto, stack = target, append(stack, target)
	}
	s := ref.Value
	switch x := v.(type) {
	case map[string]any:
		props, _ := dict.Properties(s)
		out := make(map[string]any, len(x))
		changed := false
		for k, cv := range x {
			out[k] = cv
			c := props[k]
			if c == nil || c.Value == nil {
				continue
			}
			if isPlainSchema(c) {
				if e := a.defs.Field(dto, op, k); e != nil {
					nv := a.useDefault(e, c.Value, spec.ModePlain, dtoWhere(dto, k))
					if nv != nil && dto != "" {
						a.remember(a.dictField(dto, k), e, nv, "schemas."+dtoWhere(dto, k))
					}
					if nv != nil && !reflect.DeepEqual(spec.Normalize(nv), spec.Normalize(cv)) {
						out[k], changed = nv, true
					}
				}
				continue
			}
			nv, ch := a.withDefaults(cv, c, dto, stack, op, mode, where)
			if e := a.defs.Field(dto, op, k); e != nil {
				if ov := a.objectDefault(e, nv, c, mode, dtoWhere(dto, k)); ov != nil && !reflect.DeepEqual(spec.Normalize(ov), spec.Normalize(cv)) {
					nv, ch = ov, true
				}
			}
			if ch {
				out[k], changed = nv, true
			}
		}
		return out, changed
	case []any:
		if s.Items == nil {
			return v, false
		}
		out := make([]any, len(x))
		changed := false
		for i, item := range x {
			nv, ch := a.withDefaults(item, s.Items, dto, stack, op, mode, where)
			out[i], changed = nv, changed || ch
		}
		return out, changed
	}
	return v, false
}

func isPlainSchema(ref *openapi3.SchemaRef) bool {
	if dict.DTORef(ref) != "" {
		return false
	}
	s := ref.Value
	if p, _ := dict.Properties(s); len(p) > 0 {
		return false
	}
	if value.Type(s) == "array" && s.Items != nil && s.Items.Value != nil {
		return isPlainSchema(s.Items)
	}
	return true
}

func dtoWhere(dto, name string) string {
	if dto == "" {
		return name
	}
	return dto + "." + name
}

// useDefault returns the default for a place, adjusted to the type of the
// schema; a value that still violates the schema is fatal.
func (a *applier) useDefault(e *defaults.Entry, s *openapi3.Schema, mode spec.Mode, where string) any {
	v := coerce(e.Value, s)
	if errs := a.v.Validate(s, spec.Normalize(v), mode); len(errs) > 0 {
		a.fatal(fmt.Sprintf("DEFAULT_INVALID %s: %q = %s violates the schema: %s", where, e.Key, compact(e.Value), errs[0].Reason))
		return nil
	}
	a.defs.Use(e)
	return v
}

// objectDefault lays a default for a place that holds a DTO, an object or a
// list of them over the value built for it (base, nil if none): fields of
// the default win, the other fields keep their value. Fields the mode must
// not contain (readOnly in a request, writeOnly in a response) are left out.
// A result that violates the schema is fatal.
func (a *applier) objectDefault(e *defaults.Entry, base any, ref *openapi3.SchemaRef, mode spec.Mode, where string) any {
	v := overlay(base, visible(coerce(e.Value, ref.Value), ref, mode, 0))
	if errs := a.v.Validate(ref.Value, spec.Normalize(v), mode); len(errs) > 0 {
		at := ""
		if errs[0].Pointer != "" {
			at = errs[0].Pointer + ": "
		}
		a.fatal(fmt.Sprintf("DEFAULT_INVALID %s: %q = %s gives a value that violates the schema: %s%s", where, e.Key, compact(e.Value), at, errs[0].Reason))
		return nil
	}
	a.defs.Use(e)
	return v
}

// overlay lays top over base: object fields of top win and nested objects
// are merged; a list in top sets the length, and each of its elements is
// laid over the element of base at the same index, or over the first one.
func overlay(base, top any) any {
	switch t := top.(type) {
	case map[string]any:
		b, ok := base.(map[string]any)
		if !ok {
			return t
		}
		out := make(map[string]any, len(b)+len(t))
		for k, v := range b {
			out[k] = v
		}
		for k, v := range t {
			out[k] = overlay(b[k], v)
		}
		return out
	case []any:
		b, _ := base.([]any)
		out := make([]any, len(t))
		for i, v := range t {
			var tmpl any
			switch {
			case i < len(b):
				tmpl = b[i]
			case len(b) > 0:
				tmpl = b[0]
			}
			out[i] = overlay(tmpl, v)
		}
		return out
	}
	return top
}

// visible removes the fields a mode must not contain from a default:
// readOnly fields from a request, writeOnly fields from a response.
func visible(v any, ref *openapi3.SchemaRef, mode spec.Mode, depth int) any {
	if ref == nil || ref.Value == nil || depth > 20 || mode == spec.ModePlain {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := dict.Properties(ref.Value)
		out := make(map[string]any, len(x))
		for k, cv := range x {
			if c := props[k]; c != nil && c.Value != nil {
				if (mode == spec.ModeRequest && c.Value.ReadOnly) || (mode == spec.ModeResponse && c.Value.WriteOnly) {
					continue
				}
				cv = visible(cv, c, mode, depth+1)
			}
			out[k] = cv
		}
		return out
	case []any:
		if ref.Value.Items == nil {
			return v
		}
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = visible(item, ref.Value.Items, mode, depth+1)
		}
		return out
	}
	return v
}

// coerce adapts a default to the schema type: a scalar for an array field
// becomes a one-element array, an array for a scalar field its first
// element, numeric strings numbers and numbers strings.
func coerce(v any, s *openapi3.Schema) any {
	t := value.Type(s)
	if p, ok := dict.Primitive(s); ok {
		t = p
	}
	switch x := v.(type) {
	case map[string]any:
		if t == "array" {
			return []any{x}
		}
	case []any:
		if t != "array" && len(x) > 0 {
			return coerce(x[0], s)
		}
	case string:
		switch t {
		case "integer", "number":
			if _, err := strconv.ParseFloat(x, 64); err == nil {
				return json.Number(x)
			}
		case "array":
			return []any{x}
		}
	case json.Number:
		switch t {
		case "string":
			return x.String()
		case "array":
			return []any{x}
		}
	case bool:
		switch t {
		case "string":
			return strconv.FormatBool(x)
		case "array":
			return []any{x}
		}
	}
	return v
}

// write sets key to v in mapping n, after validating it; unchanged values
// are not rewritten, so the file does not change without reason.
func (a *applier) write(n *yaml.Node, key string, existing, v any, where string, s *openapi3.Schema, mode spec.Mode) {
	if errs := a.v.Validate(s, spec.Normalize(v), mode); len(errs) > 0 {
		a.res.Stats.Incomplete++
		a.note(CodeIncomplete, where, fmt.Sprintf("the composed example does not fit the schema (%s: %s); nothing written", errs[0].Pointer, errs[0].Reason))
		return
	}
	if existing != nil && reflect.DeepEqual(spec.Normalize(existing), spec.Normalize(v)) {
		a.res.Stats.Kept++
		return
	}
	if err := yamldoc.Set(n, key, v); err != nil {
		a.note(CodeIncomplete, where, err.Error())
		return
	}
	a.res.Changed = true
	switch {
	case existing == nil:
		a.res.Stats.Added++
		a.note(CodeAdded, where, compact(v))
	case a.valid(s, existing, mode):
		a.res.Stats.DefaultsApplied++
		a.note(CodeDefaults, where, compact(v))
	default:
		a.res.Stats.Replaced++
		a.note(CodeReplaced, where, "the old example did not fit its schema")
	}
}

func (a *applier) valid(s *openapi3.Schema, v any, mode spec.Mode) bool {
	return len(a.v.Validate(s, spec.Normalize(v), mode)) == 0
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// schemaExamples replaces invalid examples of components.schemas, which
// apitest validates as well. Missing ones are not added: apitest builds
// request bodies from the examples of the media types.
func (a *applier) schemaExamples() {
	if a.s.Doc.Components == nil {
		return
	}
	for _, name := range sortedKeys(a.s.Doc.Components.Schemas) {
		ref := a.s.Doc.Components.Schemas[name]
		node := yamldoc.Path(a.doc.Root, "components", "schemas", name)
		if ref == nil || ref.Value == nil || node == nil || yamldoc.Ref(node) != "" {
			continue
		}
		s := ref.Value
		where := "components.schemas." + name
		self := &openapi3.SchemaRef{Ref: "#/components/schemas/" + name, Value: s}
		build := func() (any, bool) {
			v, ok, missing := a.build(self, "", nil, "", "", spec.ModePlain, 0, nil)
			if !ok {
				a.res.Stats.Incomplete++
				a.note(CodeIncomplete, where, fmt.Sprintf("the example does not fit its schema and %s has no value to replace it", missing))
			}
			return v, ok
		}
		// a valid example keeps its values; only the defaults are set
		fill := func(old any) (any, bool) {
			if a.valid(s, old, spec.ModePlain) {
				return a.withDefaults(old, self, "", nil, "", spec.ModePlain, where)
			}
			return build()
		}
		if exY := yamldoc.Get(node, "example"); exY != nil {
			if old, err := yamldoc.Decode(exY); err == nil {
				if v, ok := fill(old); ok && v != nil {
					a.write(node, "example", old, v, where+".example", s, spec.ModePlain)
				}
			}
		}
		// OpenAPI 3.1: a list of examples on the schema
		if listY := yamldoc.Get(node, "examples"); listY != nil && listY.Kind == yaml.SequenceNode {
			list, err := yamldoc.Decode(listY)
			items, _ := list.([]any)
			if err != nil {
				continue
			}
			changed := false
			for i, item := range items {
				if v, ok := fill(item); ok && v != nil && !reflect.DeepEqual(spec.Normalize(v), spec.Normalize(item)) {
					items[i], changed = v, true
				}
			}
			if changed && yamldoc.Set(node, "examples", items) == nil {
				a.res.Changed = true
				a.res.Stats.Replaced++
				a.note(CodeReplaced, where+".examples", "examples were replaced or got their defaults")
			}
		}
	}
}

// negative returns the names of request examples paired with a 4xx
// response; they are invalid on purpose.
func negative(op *spec.Operation) map[string]bool {
	out := map[string]bool{}
	if op.Op.Responses == nil {
		return out
	}
	for code, r := range op.Op.Responses.Map() {
		if len(code) != 3 || code[0] != '4' || r.Value == nil {
			continue
		}
		for _, m := range r.Value.Content {
			for name := range m.Examples {
				out[name] = true
			}
		}
	}
	return out
}

// GenericIDKeys returns the defaults keys that can set the generic path
// parameter p of op, in the order apply looks them up.
func GenericIDKeys(op *spec.Operation, p *openapi3.Parameter) []string {
	var out []string
	if op.HasOperationID {
		out = append(out, op.ID+"."+p.Name)
	}
	out = append(out, op.Path)
	out = append(out, resourceKeys(op.Path, p.Name)...)
	if dto := responseDTO(op); dto != "" {
		out = append(out, dto+"."+p.Name)
	}
	return append(out, p.Name)
}
