package record

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/scenario"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Analysis is what Analyse added to the record file.
type Analysis struct {
	Added []*Step
	// Complete are the added entries the spec had examples for already,
	// answers included: they need no request.
	Complete int
	// Saves are the saved values added to link the entries.
	Saves int
	// Run is the order apitest has to run the cases in so that every
	// entry finds the data its body refers to; nil if apitest's order as
	// given fits. Its Tags and DeleteLast go into "$apitest" of the
	// defaults file and into the apitest.Config of the test.
	Run   *defaults.Run
	Notes []Note
}

// Cleanup is the section of the cases apitest runs after all tags: the
// DELETEs with DeleteLast.
const Cleanup = "cleanup"

// genericParams are path parameter names that do not say which resource
// they address.
var genericParams = []string{"id", "uuid", "key", "guid"}

// Analyse adds an entry for every case apitest runs that the record file
// has none for, in apitest's order: per tag, create, read, list, update,
// delete. Path parameters apitest binds to an earlier answer get
// "{{name}}" and the entry that creates the value a "save"; the other
// values and the bodies come from the examples of the spec, else they are
// generated from the schema. Answers the spec holds are taken over. The
// entries already in the file stay as they are.
func Analyse(s *spec.Spec, f *File, run defaults.Run, seed uint64) (*Analysis, error) {
	order, binds, err := scenario.Order(s, run)
	if err != nil {
		return nil, err
	}
	an := &Analysis{}
	res := &Result{}
	f.bind(s, res)
	an.Notes = append(an.Notes, res.Problems...)
	a := &analyser{f: f, s: s, binds: binds, seed: seed, an: an, index: map[*Step]int{}, saved: map[string]string{}}
	a.producible()
	if better, ok := a.suggest(order, run); ok {
		if order, _, err = scenario.Order(s, better); err != nil {
			return nil, err
		}
		an.Run = &better
	}
	have := map[string]*Step{}
	for _, st := range f.Steps {
		if st.Op != nil {
			have[st.Op.ID+"/"+st.Example()] = st
		}
		for _, sv := range st.Save {
			a.saved[sv.Name] = st.Key + " " + sv.From
		}
	}
	// a case apitest runs after the cases of other tags have run (a DELETE
	// with DeleteLast) goes into the last section, "cleanup"
	closed := map[string]bool{}
	cur := ""
	for i, c := range order {
		section := c.Group
		if c.Group != cur {
			if closed[c.Group] {
				section = Cleanup
			} else {
				closed[cur], cur = true, c.Group
			}
		}
		if c.Kind != cases.Positive || c.Skip != "" {
			continue
		}
		if st := have[c.Op.ID+"/"+c.Example]; st != nil {
			a.index[st] = i
			continue
		}
		if c.NotBuildable != "" && !strings.HasPrefix(c.NotBuildable, "required body without example") {
			an.Notes = append(an.Notes, Note{CodeSkipped, method(c.Op), "left out: " + c.NotBuildable})
			continue
		}
		st := a.entry(c, section)
		a.index[st] = i
		a.place(st, i)
		have[c.Op.ID+"/"+c.Example] = st
		an.Added = append(an.Added, st)
		if st.Response != nil {
			an.Complete++
		}
	}
	return an, nil
}

// produced is a value apitest binds a path parameter to: the source in
// the answer of the producing operation, and the name a body field that
// refers to it has.
type produced struct {
	name  string // "shipId"
	op    *spec.Operation
	from  string // "/id" or "header Location"
	param string
}

// producible collects the values apitest binds parameters to. A body
// field named like one of them ("shipId", "originDockId" for "dockId")
// refers to the record its producer creates.
func (a *analyser) producible() {
	seen := map[string]bool{}
	for _, op := range a.s.Ops {
		for _, b := range a.binds.By(op) {
			if b.Source.HasConst || b.Source.FromRequest {
				continue
			}
			from := b.Source.Pointer
			if b.Source.Header != "" {
				from = "header " + b.Source.Header
			} else if from == "" {
				from = "/"
			}
			name := b.Param.Name
			if slices.Contains(genericParams, strings.ToLower(name)) {
				name = lowerFirst(word(singular(b.Producer.Group()))) + "Id"
			}
			if seen[strings.ToLower(name)] {
				continue
			}
			seen[strings.ToLower(name)] = true
			a.products = append(a.products, produced{name: name, op: b.Producer, from: from, param: b.Param.Name})
		}
	}
	// longer names first: "originDockId" is a dock id, not an "id"
	slices.SortStableFunc(a.products, func(x, y produced) int { return len(y.name) - len(x.name) })
}

// refers returns what a body field refers to, if anything.
func (a *analyser) refers(field string) *produced {
	for i, p := range a.products {
		if matches(field, p.name) {
			return &a.products[i]
		}
	}
	return nil
}

// matches reports whether a field is named like a value: "dockId", or
// ends in it as a word: "originDockId".
func matches(field, name string) bool {
	if strings.EqualFold(field, name) {
		return true
	}
	if len(field) <= len(name) || !strings.EqualFold(field[len(field)-len(name):], name) {
		return false
	}
	r := []rune(field[len(field)-len(name):])
	return unicode.IsUpper(r[0]) || field[len(field)-len(name)-1] == '_'
}

// suggest finds the order apitest has to run the tags in: a tag whose
// bodies refer to the records of another tag comes after it, and since
// those records must still exist then, every DELETE runs last. It reports
// whether that order differs from the one given.
func (a *analyser) suggest(order []*cases.Case, run defaults.Run) (defaults.Run, bool) {
	var groups []string
	deps := map[string][]string{}
	for _, c := range order {
		if !slices.Contains(groups, c.Group) {
			groups = append(groups, c.Group)
		}
		if c.Kind != cases.Positive || requestMedia(c.Op) == nil {
			continue
		}
		body, ok := c.Body, c.HasBody
		if !ok {
			body, ok = a.generate(requestMedia(c.Op).Schema, "body."+c.Op.ID)
		}
		if !ok {
			continue
		}
		walkFields(body, func(k string) {
			if p := a.refers(k); p != nil && p.op.Group() != c.Group && !slices.Contains(deps[c.Group], p.op.Group()) {
				deps[c.Group] = append(deps[c.Group], p.op.Group())
			}
		})
	}
	if len(deps) == 0 {
		return run, false
	}
	var sorted []string
	placed := map[string]bool{}
	for len(sorted) < len(groups) {
		next := ""
		for _, g := range groups {
			if placed[g] {
				continue
			}
			ready := true
			for _, d := range deps[g] {
				if !placed[d] && slices.Contains(groups, d) {
					ready = false
				}
			}
			if ready {
				next = g
				break
			}
		}
		if next == "" { // a cycle: keep the rest as it is
			for _, g := range groups {
				if !placed[g] {
					next = g
					break
				}
			}
		}
		placed[next] = true
		sorted = append(sorted, next)
	}
	better := run
	better.Tags, better.DeleteLast = sorted, true
	if slices.Equal(sorted, groups) && run.DeleteLast {
		return run, false
	}
	return better, true
}

// walkFields calls fn with every field name of a value whose value is
// plain.
func walkFields(v any, fn func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if scalar(c) {
				fn(k)
			}
			walkFields(c, fn)
		}
	case []any:
		for _, c := range x {
			walkFields(c, fn)
		}
	}
}

type analyser struct {
	f     *File
	s     *spec.Spec
	binds *bind.Set
	seed  uint64
	an    *Analysis
	// index is the position of a step's case in apitest's order
	index map[*Step]int
	// saved maps the name of a saved value to its source, to keep the
	// names unique
	saved    map[string]string
	products []produced
}

// entry builds the entry of a case.
func (a *analyser) entry(c *cases.Case, section string) *Step {
	op := c.Op
	st := &Step{Tag: section, Key: method(op), Op: op, node: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}}
	if c.Example != cases.DefaultExample {
		st.Name = c.Example
		st.node.Content = append(st.node.Content, scalarNode(keyName), scalarNode(c.Example))
	}
	for _, in := range []string{openapi3.ParameterInPath, openapi3.ParameterInQuery} {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		for _, p := range op.Params {
			if p.In != in || (in == openapi3.ParameterInQuery && !p.Required) {
				continue
			}
			m.Content = append(m.Content, scalarNode(p.Name), a.paramValue(c, p))
		}
		if len(m.Content) == 0 {
			continue
		}
		key := keyPath
		if in == openapi3.ParameterInQuery {
			key, st.Query = keyQuery, m
		} else {
			st.Path = m
		}
		st.node.Content = append(st.node.Content, scalarNode(key), m)
	}
	if m := requestMedia(op); m != nil {
		body, ok := c.Body, c.HasBody
		if !ok {
			body, ok = a.generate(m.Schema, "body."+op.ID)
		}
		if ok {
			n := valueNode(a.link(body))
			compact(n)
			st.Body = n
			st.node.Content = append(st.node.Content, scalarNode(keyBody), n)
		}
	}
	if c.Expect.HasExample {
		status := c.Expect.Status
		if status == 0 {
			status = 200
		}
		n := valueNode(c.Expect.Example)
		compact(n)
		st.node.Content = append(st.node.Content, scalarNode(keyResponse), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"})
		st.setResponse(&Response{Status: status, Body: n})
	} else {
		st.node.Content = append(st.node.Content, scalarNode(keyResponse), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"})
	}
	return st
}

// paramValue is the value of a parameter in a new entry: "{{name}}" when
// apitest binds it to an earlier answer, else its example or a generated
// value.
func (a *analyser) paramValue(c *cases.Case, p *openapi3.Parameter) *yaml.Node {
	if b := a.binds.For(c.Op, p); b != nil && !b.Source.HasConst && !b.Source.FromRequest {
		if prod := a.producer(b.Producer); prod != nil {
			from := b.Source.Pointer
			if b.Source.Header != "" {
				from = "header " + b.Source.Header
			}
			if b.Source.Pointer == "" && b.Source.Header == "" {
				from = "/"
			}
			return scalarNode("{{" + a.save(prod, from, p.Name) + "}}")
		}
	}
	if v, ok := params.Resolve(p, params.Inputs{CaseName: c.Example, OpID: c.Op.ID}); ok {
		return valueNode(v.V)
	}
	if v, ok := a.generate(p.Schema, "param."+c.Op.ID+"."+p.Name); ok {
		return valueNode(v)
	}
	return scalarNode("")
}

// producer is the entry of the operation a binding reads from: its first
// entry in the file.
func (a *analyser) producer(op *spec.Operation) *Step {
	for _, st := range a.f.Steps {
		if st.Op == op {
			return st
		}
	}
	return nil
}

// save returns the name a step saves a value under, adding the save if
// the step has none for that source yet.
func (a *analyser) save(st *Step, from, param string) string {
	for _, sv := range st.Save {
		if sv.From == from {
			return sv.Name
		}
	}
	base := param
	if slices.Contains(genericParams, strings.ToLower(param)) {
		base = lowerFirst(word(singular(st.Op.Group()))) + "Id"
	}
	name := base
	for i := 2; a.saved[name] != "" && a.saved[name] != st.Key+" "+from; i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	a.saved[name] = st.Key + " " + from
	st.addSave(Save{Name: name, From: from})
	a.an.Saves++
	return name
}

// link replaces the fields of a body that refer to a record by the value
// its entry saves: "dockId" and "originDockId" in the body of a ship refer
// to the dock, so they get "{{dockId}}" and the entry of the dock saves it.
func (a *analyser) link(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, c := range x {
			if scalar(c) {
				if name := a.linked(k); name != "" {
					out[k] = "{{" + name + "}}"
					continue
				}
			}
			out[k] = a.link(c)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = a.link(c)
		}
		return out
	}
	return v
}

// linked is the saved value a body field refers to; "" if none.
func (a *analyser) linked(field string) string {
	if p := a.refers(field); p != nil {
		if prod := a.producer(p.op); prod != nil {
			return a.save(prod, p.from, p.name)
		}
	}
	for name := range a.saved {
		if matches(field, name) {
			return name
		}
	}
	return ""
}

func scalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any, nil:
		return false
	}
	return true
}

// generate builds a value for a schema; a request body leaves out its
// readOnly fields.
func (a *analyser) generate(ref *openapi3.SchemaRef, path string) (any, bool) {
	if ref == nil || ref.Value == nil {
		return nil, false
	}
	r := value.Generate(ref.Value, value.Context{Seed: a.seed, Path: path})
	if !r.OK {
		return nil, false
	}
	return writable(spec.Normalize(r.Value), ref.Value, 0), true
}

// writable removes the readOnly fields of a value.
func writable(v any, s *openapi3.Schema, depth int) any {
	if s == nil || depth > 20 {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		props := properties(s)
		for k, c := range x {
			p := props[k]
			if p == nil || p.Value == nil {
				continue
			}
			if p.Value.ReadOnly {
				delete(x, k)
				continue
			}
			x[k] = writable(c, p.Value, depth+1)
		}
	case []any:
		if s.Items != nil {
			for i, c := range x {
				x[i] = writable(c, s.Items.Value, depth+1)
			}
		}
	}
	return v
}

// properties are the properties of a schema with those of its allOf parts.
func properties(s *openapi3.Schema) openapi3.Schemas {
	out := openapi3.Schemas{}
	var add func(x *openapi3.Schema, depth int)
	add = func(x *openapi3.Schema, depth int) {
		if x == nil || depth > 20 {
			return
		}
		for k, p := range x.Properties {
			if out[k] == nil {
				out[k] = p
			}
		}
		for _, part := range x.AllOf {
			add(part.Value, depth+1)
		}
		if len(x.Properties) == 0 && len(x.AllOf) == 0 {
			for _, part := range append(append(openapi3.SchemaRefs{}, x.OneOf...), x.AnyOf...) {
				add(part.Value, depth+1)
				break
			}
		}
	}
	add(s, 0)
	return out
}

// place puts a new step into the file: after the last step of its tag
// that apitest runs before it.
func (a *analyser) place(st *Step, idx int) {
	var before *Step
	for _, s := range a.f.Steps {
		if s.Tag != st.Tag {
			continue
		}
		if i, ok := a.index[s]; ok && i < idx {
			before = s
		}
	}
	pos := 0
	if before != nil {
		pos = a.f.position(before) + 1
	}
	a.f.insert(st, before, pos)
}

func singular(name string) string {
	switch {
	case strings.HasSuffix(name, "ies"):
		return strings.TrimSuffix(name, "ies") + "y"
	case strings.HasSuffix(name, "s") && !strings.HasSuffix(name, "ss"):
		return strings.TrimSuffix(name, "s")
	}
	return name
}

// word joins the letters and digits of a name in camel case: "ship-dock"
// becomes "shipDock".
func word(s string) string {
	var b strings.Builder
	upper := false
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			upper = b.Len() > 0
			continue
		}
		if upper {
			r = unicode.ToUpper(r)
			upper = false
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "value"
	}
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
