// Package bind decides which parameters get their value from an earlier
// response (FR-PARAM-01 rank 1, FR-PARAM-03/05/06) and extracts those values.
//
// Sources, in order of precedence:
//  1. x-apitest-bind at the parameter,
//  2. OpenAPI links in a 2xx response of the producing operation,
//  3. a heuristic for path parameters within the same resource group.
package bind

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Kind is how a binding was declared.
type Kind int

// Binding kinds.
const (
	Explicit  Kind = iota // x-apitest-bind
	Link                  // OpenAPI links
	Heuristic             // FR-PARAM-06
)

func (k Kind) String() string {
	switch k {
	case Explicit:
		return "x-apitest-bind"
	case Link:
		return "links"
	default:
		return "heuristic"
	}
}

// Source is where a bound value comes from.
type Source struct {
	// Pointer is a JSON pointer into the response body (or the request body
	// if FromRequest is set), e.g. "/id".
	Pointer string
	// Header is a response header. For "Location" the last path segment is
	// used (FR-PARAM-03).
	Header      string
	FromRequest bool
	// Const is a constant value from a link.
	Const    any
	HasConst bool
}

func (s Source) String() string {
	switch {
	case s.HasConst:
		return fmt.Sprintf("constant %v", s.Const)
	case s.Header != "":
		return "header " + s.Header
	case s.FromRequest:
		return "request body " + s.Pointer
	default:
		return "body " + s.Pointer
	}
}

// Binding fills one parameter of a consumer operation from a producer.
type Binding struct {
	Consumer *spec.Operation
	Param    *openapi3.Parameter
	Producer *spec.Operation
	Source   Source
	Kind     Kind
}

// Key identifies the value a binding reads. Bindings with the same key share
// one value, so an update after PUT affects all of them (FR-PARAM-04).
func (b *Binding) Key() string {
	return b.Producer.ID + "|" + b.Source.String()
}

// CrossGroup reports whether producer and consumer are in different groups.
func (b *Binding) CrossGroup() bool {
	return b.Producer.Group() != b.Consumer.Group()
}

// Set holds all bindings of a spec.
type Set struct {
	byParam  map[string]*Binding
	Findings []spec.Finding
}

func paramKey(opID string, p *openapi3.Parameter) string {
	return opID + "\x00" + p.In + "\x00" + p.Name
}

// For returns the binding of parameter p of operation op, or nil.
func (set *Set) For(op *spec.Operation, p *openapi3.Parameter) *Binding {
	return set.byParam[paramKey(op.ID, p)]
}

// Of returns the bindings of op, sorted by parameter.
func (set *Set) Of(op *spec.Operation) []*Binding {
	var out []*Binding
	for _, p := range op.Params {
		if b := set.For(op, p); b != nil {
			out = append(out, b)
		}
	}
	return out
}

// By returns all bindings whose producer is op.
func (set *Set) By(op *spec.Operation) []*Binding {
	var out []*Binding
	for _, b := range set.byParam {
		if b.Producer == op {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key()+out[i].Consumer.ID < out[j].Key()+out[j].Consumer.ID })
	return out
}

// Resolve determines the bindings of all operations. Errors in explicit
// declarations are returned before any request is sent (NFR-08).
func Resolve(s *spec.Spec) (*Set, error) {
	set := &Set{byParam: map[string]*Binding{}}
	var errs []string

	// 1. x-apitest-bind
	for _, op := range s.Ops {
		for _, p := range op.Params {
			raw, ok := p.Extensions["x-apitest-bind"]
			if !ok {
				continue
			}
			b, err := explicit(s, op, p, raw)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s.parameters[%s].x-apitest-bind: %v", op.Where, p.Name, err))
				continue
			}
			set.byParam[paramKey(op.ID, p)] = b
		}
	}

	// 2. links
	for _, producer := range s.Ops {
		if producer.Op.Responses == nil {
			continue
		}
		for _, code := range sortedCodes(producer.Op.Responses.Map()) {
			if code[0] != '2' {
				continue
			}
			r := producer.Op.Responses.Map()[code].Value
			if r == nil {
				continue
			}
			for _, name := range sortedLinks(r.Links) {
				set.addLink(s, producer, code, name, r.Links[name].Value)
			}
		}
	}

	// 3. heuristic for path parameters without explicit binding
	for _, op := range s.Ops {
		for _, p := range op.Params {
			if p.In != openapi3.ParameterInPath || set.byParam[paramKey(op.ID, p)] != nil {
				continue
			}
			if b := heuristic(s, op, p, func(producer *spec.Operation) bool { return set.acceptable(producer, op) }); b != nil {
				set.byParam[paramKey(op.ID, p)] = b
				set.Findings = append(set.Findings, spec.Finding{
					Kind:  spec.FindingHeuristic,
					Where: fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name),
					Message: fmt.Sprintf("parameter %q is resolved heuristically from %s (%s); make it explicit with x-apitest-bind or links",
						p.Name, b.Producer.ID, b.Source),
				})
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("invalid bindings:\n  %s", strings.Join(errs, "\n  "))
	}
	sort.SliceStable(set.Findings, func(i, j int) bool { return set.Findings[i].Where < set.Findings[j].Where })
	return set, nil
}

// acceptable reports whether consumer may take a heuristic value from
// producer without closing a cycle between cases or resource groups.
func (set *Set) acceptable(producer, consumer *spec.Operation) bool {
	if set.dependsOn(producer, consumer) {
		return false
	}
	return producer.Group() == consumer.Group() || !set.groupDependsOn(producer.Group(), consumer.Group())
}

// groupDependsOn reports whether group from needs values from group target,
// directly or indirectly.
func (set *Set) groupDependsOn(from, target string) bool {
	seen := map[string]bool{}
	var walk func(g string) bool
	walk = func(g string) bool {
		if g == target {
			return true
		}
		if seen[g] {
			return false
		}
		seen[g] = true
		for _, b := range set.byParam {
			if b.Consumer.Group() == g && b.Producer.Group() != g && walk(b.Producer.Group()) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

// dependsOn reports whether op needs a value from target, directly or
// through other bindings. A heuristic binding that would close such a cycle
// is not added: guessing must never create a dependency cycle.
func (set *Set) dependsOn(op, target *spec.Operation) bool {
	seen := map[*spec.Operation]bool{}
	var walk func(o *spec.Operation) bool
	walk = func(o *spec.Operation) bool {
		if o == target {
			return true
		}
		if seen[o] {
			return false
		}
		seen[o] = true
		for _, b := range set.byParam {
			if b.Consumer == o && walk(b.Producer) {
				return true
			}
		}
		return false
	}
	return walk(op)
}

// explicit parses x-apitest-bind: {from: <operationId>, pointer: /id} or
// {from: <operationId>, header: Location}; "source: request" reads the
// request body of the producer instead of its response.
func explicit(s *spec.Spec, op *spec.Operation, p *openapi3.Parameter, raw any) (*Binding, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected an object with from and pointer or header")
	}
	from, _ := m["from"].(string)
	if from == "" {
		return nil, fmt.Errorf("from is missing (operationId of the producing operation)")
	}
	producer := s.Op(from)
	if producer == nil {
		return nil, fmt.Errorf("operation %q does not exist", from)
	}
	if producer == op {
		return nil, fmt.Errorf("an operation cannot bind to itself")
	}
	src := Source{}
	src.Pointer, _ = m["pointer"].(string)
	src.Header, _ = m["header"].(string)
	if v, _ := m["source"].(string); v == "request" {
		src.FromRequest = true
	}
	switch {
	case src.Pointer == "" && src.Header == "":
		return nil, fmt.Errorf("set pointer (e.g. /id) or header (e.g. Location)")
	case src.Pointer != "" && src.Header != "":
		return nil, fmt.Errorf("set either pointer or header, not both")
	case src.Pointer != "" && !strings.HasPrefix(src.Pointer, "/"):
		return nil, fmt.Errorf("pointer %q must start with / (JSON pointer)", src.Pointer)
	case src.Header != "" && src.FromRequest:
		return nil, fmt.Errorf("source: request requires pointer")
	}
	return &Binding{Consumer: op, Param: p, Producer: producer, Source: src, Kind: Explicit}, nil
}

func (set *Set) addLink(s *spec.Spec, producer *spec.Operation, code, name string, l *openapi3.Link) {
	where := fmt.Sprintf("%s.responses.%s.links.%s", producer.Where, code, name)
	if l == nil {
		return
	}
	if l.OperationID == "" {
		set.Findings = append(set.Findings, spec.Finding{Kind: spec.FindingBinding, Where: where, Message: "link without operationId is ignored (operationRef is not supported)"})
		return
	}
	consumer := s.Op(l.OperationID)
	if consumer == nil {
		set.Findings = append(set.Findings, spec.Finding{Kind: spec.FindingBinding, Where: where, Message: fmt.Sprintf("link points to unknown operation %q", l.OperationID)})
		return
	}
	keys := make([]string, 0, len(l.Parameters))
	for k := range l.Parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		in, pname := "", k
		if i := strings.Index(k, "."); i > 0 {
			in, pname = k[:i], k[i+1:]
		}
		p := findParam(consumer, in, pname)
		if p == nil {
			set.Findings = append(set.Findings, spec.Finding{Kind: spec.FindingBinding, Where: where, Message: fmt.Sprintf("parameter %q does not exist in %s", k, consumer.ID)})
			continue
		}
		src, err := parseExpression(l.Parameters[k])
		if err != nil {
			set.Findings = append(set.Findings, spec.Finding{Kind: spec.FindingBinding, Where: where + ".parameters." + k, Message: err.Error()})
			continue
		}
		key := paramKey(consumer.ID, p)
		if set.byParam[key] != nil {
			continue // x-apitest-bind or an earlier link wins
		}
		set.byParam[key] = &Binding{Consumer: consumer, Param: p, Producer: producer, Source: src, Kind: Link}
	}
}

func findParam(op *spec.Operation, in, name string) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.Name == name && (in == "" || p.In == in) {
			return p
		}
	}
	return nil
}

// parseExpression understands the runtime expressions apitest supports:
// $response.body#/ptr, $response.header.Name, $request.body#/ptr and
// constants.
func parseExpression(v any) (Source, error) {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "$") {
		return Source{Const: v, HasConst: true}, nil
	}
	switch {
	case strings.HasPrefix(s, "$response.body#"):
		return Source{Pointer: strings.TrimPrefix(s, "$response.body#")}, nil
	case s == "$response.body":
		return Source{Pointer: ""}, nil
	case strings.HasPrefix(s, "$response.header."):
		return Source{Header: strings.TrimPrefix(s, "$response.header.")}, nil
	case strings.HasPrefix(s, "$request.body#"):
		return Source{Pointer: strings.TrimPrefix(s, "$request.body#"), FromRequest: true}, nil
	}
	return Source{}, fmt.Errorf("expression %q is not supported (allowed: $response.body#/..., $response.header..., $request.body#/...)", s)
}

// heuristic implements FR-PARAM-06 for path parameter p of op. Only POSTs
// of the same group are considered. In this order:
//
//  1. the POST on the collection in front of "{p}"; literal segments in
//     between, such as "id", "code" or "Crew" in "/Rocket/Crew/{crewName}",
//     are skipped, other parameters are not crossed;
//  2. a POST whose last literal path segment names the resource of p, e.g.
//     "POST /x/{a}/MissionPlan" for "{missionPlanId}";
//  3. a POST whose request or response has a field named like p.
//
// Field names are compared case-insensitively, because path parameters are
// often camelCase while bodies are PascalCase ("{callsign}" and "Callsign").
func heuristic(s *spec.Spec, op *spec.Operation, p *openapi3.Parameter, accept func(*spec.Operation) bool) *Binding {
	segs := strings.Split(strings.Trim(op.Path, "/"), "/")
	k := -1
	for i, seg := range segs {
		if seg == "{"+p.Name+"}" {
			k = i
			break
		}
	}
	if k <= 0 {
		return nil
	}
	posts := samePosts(s, op)
	bindTo := func(producer *spec.Operation) *Binding {
		if !accept(producer) {
			return nil
		}
		if src, ok := pick(producer, p.Name); ok {
			return &Binding{Consumer: op, Param: p, Producer: producer, Source: src, Kind: Heuristic}
		}
		return nil
	}

	// 1. collection in front of the parameter
	for end := k; end > 0; end-- {
		prefix := "/" + strings.Join(segs[:end], "/")
		for _, o := range posts {
			if o.Path == prefix {
				if b := bindTo(o); b != nil {
					return b
				}
			}
		}
		if isParam(segs[end-1]) {
			break
		}
	}
	// 2. a POST named after the resource of the parameter
	res := resourceOf(p.Name)
	named := func(list []*spec.Operation) *Binding {
		for _, o := range list {
			last := lastLiteral(o.Path)
			if strings.EqualFold(last, res) || strings.EqualFold(last, res+"s") {
				if b := bindTo(o); b != nil {
					return b
				}
			}
		}
		return nil
	}
	if res != "" {
		if b := named(posts); b != nil {
			return b
		}
	}
	// 3. a POST that has a field with the parameter's name
	for _, o := range posts {
		resp, _ := successSchema(o)
		_, inResp := propertyName(resp, p.Name)
		_, inReq := requestProperty(o, p.Name)
		if inResp || inReq {
			if b := bindTo(o); b != nil {
				return b
			}
		}
	}
	// 4. rule 2 across groups: the resource belongs to another tag
	if res != "" {
		return named(otherPosts(s, op))
	}
	return nil
}

// otherPosts returns the POSTs outside op's group, fewest path parameters
// first.
func otherPosts(s *spec.Spec, op *spec.Operation) []*spec.Operation {
	var out []*spec.Operation
	for _, o := range s.Ops {
		if o.Method == http.MethodPost && o.Group() != op.Group() {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i].Path, "{") < strings.Count(out[j].Path, "{")
	})
	return out
}

// samePosts returns the POSTs of op's group, fewest path parameters first.
func samePosts(s *spec.Spec, op *spec.Operation) []*spec.Operation {
	var out []*spec.Operation
	for _, o := range s.Ops {
		if o.Method == http.MethodPost && o.Group() == op.Group() && o != op {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i].Path, "{") < strings.Count(out[j].Path, "{")
	})
	return out
}

// pick chooses where the value of parameter name comes from in the
// producer's exchange.
func pick(producer *spec.Operation, name string) (Source, bool) {
	resp, hasBody := successSchema(producer)
	lower := strings.ToLower(name)
	idLike := lower == "id" || strings.HasSuffix(lower, "id")
	if field, ok := propertyName(resp, name); ok {
		return Source{Pointer: "/" + escape(field)}, true
	}
	if idLike {
		if field, ok := propertyName(resp, "id"); ok {
			return Source{Pointer: "/" + escape(field)}, true
		}
		if !hasBody {
			return Source{Header: "Location"}, true
		}
	}
	if field, ok := requestProperty(producer, name); ok {
		return Source{Pointer: "/" + escape(field), FromRequest: true}, true
	}
	return Source{}, false
}

// resourceOf derives the resource name from a parameter name:
// "missionPlanId" -> "missionPlan", "rocketNumber" -> "rocket".
func resourceOf(param string) string {
	lower := strings.ToLower(param)
	for _, suffix := range []string{"id", "code", "number", "key"} {
		if strings.HasSuffix(lower, suffix) && len(param) > len(suffix) {
			return param[:len(param)-len(suffix)]
		}
	}
	return ""
}

func lastLiteral(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if !isParam(segs[i]) {
			return segs[i]
		}
	}
	return ""
}

func isParam(seg string) bool { return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") }

// successSchema returns the JSON schema of the lowest 2xx response and
// whether that response has a body at all.
func successSchema(op *spec.Operation) (*openapi3.Schema, bool) {
	if op.Op.Responses == nil {
		return nil, false
	}
	for _, code := range sortedCodes(op.Op.Responses.Map()) {
		if code[0] != '2' {
			continue
		}
		r := op.Op.Responses.Map()[code].Value
		if r == nil || len(r.Content) == 0 {
			return nil, false
		}
		for mt, m := range r.Content {
			if spec.IsJSON(mt) && m.Schema != nil && m.Schema.Value != nil {
				return m.Schema.Value, true
			}
		}
		return nil, true
	}
	return nil, false
}

// requestProperty returns the actual name of the request body field that
// matches name case-insensitively.
func requestProperty(op *spec.Operation, name string) (string, bool) {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return "", false
	}
	for mt, m := range rb.Value.Content {
		if spec.IsJSON(mt) && m.Schema != nil {
			if field, ok := propertyName(m.Schema.Value, name); ok {
				return field, true
			}
		}
	}
	return "", false
}

// propertyName finds a property of s (including allOf/oneOf/anyOf) by name.
// An exact match wins over a case-insensitive one.
func propertyName(s *openapi3.Schema, name string) (string, bool) {
	if s == nil {
		return "", false
	}
	if s.Properties[name] != nil {
		return name, true
	}
	for _, field := range sortedProps(s.Properties) {
		if strings.EqualFold(field, name) {
			return field, true
		}
	}
	for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.OneOf, s.AnyOf} {
		for _, r := range refs {
			if r != nil {
				if field, ok := propertyName(r.Value, name); ok {
					return field, true
				}
			}
		}
	}
	return "", false
}

func sortedProps(m openapi3.Schemas) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Extract reads the value of src from an exchange. body is the decoded
// response body (nil if none), reqBody the decoded request body.
func Extract(src Source, kind Kind, header http.Header, body, reqBody any) (any, bool) {
	switch {
	case src.HasConst:
		return src.Const, true
	case src.Header != "":
		v := header.Get(src.Header)
		if v == "" {
			return nil, false
		}
		if strings.EqualFold(src.Header, "Location") {
			return LastSegment(v), true
		}
		return v, true
	case src.FromRequest:
		return Pointer(reqBody, src.Pointer)
	}
	if v, ok := Pointer(body, src.Pointer); ok {
		return v, true
	}
	// The heuristic cannot know whether the server answers with the new
	// resource, so it also tries the Location header and the request body.
	if kind == Heuristic {
		if loc := header.Get("Location"); loc != "" && src.Pointer == "/id" {
			return LastSegment(loc), true
		}
		return Pointer(reqBody, src.Pointer)
	}
	return nil, false
}

// LastSegment returns the last path segment of a URL or path, e.g. "42" for
// "/books/42" or "http://h/books/42?x=1".
func LastSegment(loc string) string {
	if u, err := url.Parse(loc); err == nil {
		loc = u.Path
	}
	loc = strings.TrimSuffix(loc, "/")
	seg := path.Base(loc)
	if s, err := url.PathUnescape(seg); err == nil {
		return s
	}
	return seg
}

// Pointer resolves a JSON pointer in a decoded value.
func Pointer(v any, ptr string) (any, bool) {
	if ptr == "" {
		return v, v != nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, false
	}
	cur := v
	for _, part := range strings.Split(ptr[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func sortedCodes(m map[string]*openapi3.ResponseRef) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func sortedLinks(m openapi3.Links) []string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
