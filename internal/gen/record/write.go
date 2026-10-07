package record

import (
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// writer writes the entries of the record file into the spec, at the
// places apitest reads the examples from.
type writer struct {
	doc     *yamldoc.Doc
	res     *Result
	changed bool
	// done are the examples written per place and example name, to find a
	// place several operations share and need different examples at
	done map[*yaml.Node]map[string]written
}

type written struct {
	value any
	by    string
}

func newWriter(doc *yamldoc.Doc, res *Result) *writer {
	return &writer{doc: doc, res: res, done: map[*yaml.Node]map[string]written{}}
}

// write writes every step whose answer is stored, with the saved values
// of the stored answers filled in.
func (w *writer) write(f *File, v *spec.Validator) {
	count := map[string]int{}
	for _, st := range f.Steps {
		count[st.Op.ID]++
	}
	ignore := map[string][]string{}
	var ops []*spec.Operation
	vars := map[string]any{}
	for _, st := range f.Steps {
		named := st.Name != "" || count[st.Op.ID] > 1
		r, _ := st.request(vars)
		w.step(st, r, named, v)
		if st.Response != nil {
			st.saveStored(vars)
		}
		if !slices.Contains(ops, st.Op) {
			ops = append(ops, st.Op)
		}
		for _, f := range st.Ignore {
			if !slices.Contains(ignore[st.Op.ID], f) {
				ignore[st.Op.ID] = append(ignore[st.Op.ID], f)
			}
		}
	}
	for _, op := range ops {
		if len(ignore[op.ID]) > 0 {
			w.ignore(op, ignore[op.ID])
		}
	}
}

// step writes the examples of one step: its parameters, its body and its
// answer.
func (w *writer) step(st *Step, r *request, named bool, v *spec.Validator) {
	op := st.Op
	name := st.Example()
	for _, in := range []string{openapi3.ParameterInPath, openapi3.ParameterInQuery} {
		nodes := r.pathNodes
		if in == openapi3.ParameterInQuery {
			nodes = r.queryNodes
		}
		for _, k := range sortedKeys(nodes) {
			w.param(st, param(op, k, in), r, named)
		}
	}
	if r.hasBody {
		if pl := w.request(op); pl != nil {
			w.place(st, pl, name, r.bodyNode, named, func() *place { return w.ownRequest(op) })
		}
	}
	if st.Response == nil {
		return
	}
	code := responseCode(op, st.Response.Status)
	if code == "" {
		w.res.problem(CodeStatus, st.where(), "the answer has status %d, which the spec does not document; document it or check the request", st.Response.Status)
		return
	}
	if !named && code != lowestSuccess(op) {
		w.res.problem(CodeStatus, st.where(), "the instance answers %d, but for an entry without name apitest expects the lowest documented 2xx (%s); give the entry a \"name\" or fix the spec",
			st.Response.Status, lowestSuccess(op))
		return
	}
	if st.Response.Body == nil {
		return
	}
	pl := w.response(op, code)
	if pl == nil {
		return
	}
	if msg := firstViolation(v, pl.schema.Value, decode(st.Response.Body), spec.ModeResponse); msg != "" {
		w.res.note(CodeSchema, st.where(), "the stored answer violates the schema of the response %s (%s); apitest will report it", code, msg)
	}
	w.place(st, pl, name, st.Response.Body, named, func() *place { return w.ownResponse(op, code) })
}

// place is a media type of the spec that holds an example.
type place struct {
	node   *yaml.Node
	schema *openapi3.SchemaRef
	shared bool // reached through a $ref, other operations may use it
}

// operation is the node of an operation.
func (w *writer) operation(op *spec.Operation) *yaml.Node {
	return yamldoc.Get(yamldoc.Path(w.doc.Root, "paths", op.Path), strings.ToLower(op.Method))
}

func (w *writer) mediaPlace(holder *yaml.Node, m *media) *place {
	if holder == nil || m == nil || m.Schema == nil || m.Schema.Value == nil {
		return nil
	}
	target, err := w.doc.Resolve(holder)
	if err != nil {
		return nil
	}
	node, err := w.doc.Resolve(yamldoc.Get(yamldoc.Get(target, "content"), m.Type))
	if err != nil || node == nil {
		return nil
	}
	return &place{node: node, schema: m.Schema, shared: yamldoc.Ref(holder) != ""}
}

func (w *writer) request(op *spec.Operation) *place {
	return w.mediaPlace(yamldoc.Get(w.operation(op), "requestBody"), requestMedia(op))
}

func (w *writer) response(op *spec.Operation, code string) *place {
	return w.mediaPlace(yamldoc.Get(yamldoc.Get(w.operation(op), "responses"), code), responseMedia(op, code))
}

// ownRequest gives an operation its own copy of a shared request body.
func (w *writer) ownRequest(op *spec.Operation) *place {
	opNode := w.operation(op)
	if !w.inline(opNode, "requestBody") {
		return nil
	}
	return w.request(op)
}

// ownResponse gives an operation its own copy of a shared response.
func (w *writer) ownResponse(op *spec.Operation, code string) *place {
	if !w.inline(yamldoc.Get(w.operation(op), "responses"), code) {
		return nil
	}
	return w.response(op, code)
}

// inline replaces the $ref at key of a mapping by a copy of its target.
func (w *writer) inline(n *yaml.Node, key string) bool {
	holder := yamldoc.Get(n, key)
	if yamldoc.Ref(holder) == "" {
		return false
	}
	target, err := w.doc.Resolve(holder)
	if err != nil {
		return false
	}
	return yamldoc.SetNode(n, key, clone(target)) == nil
}

// place writes an example at a media type: "example" for a step without
// name, else "examples.<name>.value". The spec may not hold both, so the
// other form goes. A place several operations share gets a copy for this
// operation when they need different examples, before it gets a named
// example, which would give the others a case of that name too, and
// before the other form is removed.
func (w *writer) place(st *Step, pl *place, name string, value *yaml.Node, named bool, own func() *place) {
	v := decode(value)
	prev, ok := w.done[pl.node][name]
	conflict := ok && prev.by != st.Op.ID && !compare.Equal(spec.Normalize(prev.value), spec.Normalize(v))
	if pl.shared && (conflict || named || other(pl.node, named)) {
		c := own()
		if c == nil {
			w.res.problem(CodeShared, st.where(), "%s shares this place of the spec with other operations and cannot get its own copy; declare it in the operation", method(st.Op))
			return
		}
		pl = c
	}
	if w.done[pl.node] == nil {
		w.done[pl.node] = map[string]written{}
	}
	w.done[pl.node][name] = written{v, st.Op.ID}
	w.set(pl.node, name, value, named)
}

// other reports whether a node holds the other form of example: "examples"
// when a step without name writes "example", and the other way round.
func other(n *yaml.Node, named bool) bool {
	if named {
		return yamldoc.Get(n, "example") != nil
	}
	return yamldoc.Get(n, "examples") != nil
}

// set writes a value as the example of a node.
func (w *writer) set(node *yaml.Node, name string, value *yaml.Node, named bool) {
	value = clone(value)
	drop := "example"
	if !named {
		drop = "examples"
	}
	if yamldoc.Delete(node, drop) {
		w.changed = true
	}
	if !named {
		if cur := yamldoc.Get(node, "example"); cur == nil || !compare.Equal(decode(cur), decode(value)) {
			_ = yamldoc.SetNode(node, "example", value)
			w.changed = true
			w.res.Examples++
		}
		return
	}
	exs := yamldoc.Get(node, "examples")
	if exs == nil || exs.Kind != yaml.MappingNode {
		exs = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		_ = yamldoc.SetNode(node, "examples", exs)
	}
	ex := yamldoc.Get(exs, name)
	if ex == nil || ex.Kind != yaml.MappingNode || yamldoc.Ref(ex) != "" {
		ex = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		_ = yamldoc.SetNode(exs, name, ex)
	}
	if cur := yamldoc.Get(ex, "value"); cur == nil || !compare.Equal(decode(cur), decode(value)) {
		_ = yamldoc.SetNode(ex, "value", value)
		w.changed = true
		w.res.Examples++
	}
}

// param writes the example of a parameter. A parameter several
// operations share (at the path, or a $ref) is copied into the operation
// when they need different values.
func (w *writer) param(st *Step, p *openapi3.Parameter, r *request, named bool) {
	if p == nil {
		return
	}
	values := r.path
	if p.In == openapi3.ParameterInQuery {
		values = r.query
	}
	v := values[p.Name]
	value := valueNode(v)
	e := w.paramEntry(st.Op, p.Name, p.In)
	if e == nil {
		return
	}
	name := st.Example()
	prev, ok := w.done[e.target][name]
	conflict := ok && prev.by != st.Op.ID && !compare.Equal(spec.Normalize(prev.value), spec.Normalize(v))
	// named examples of a parameter make cases of every operation that has
	// it; the other form of example goes in set
	if e.shared() && (conflict || named || other(e.target, named)) {
		e = w.ownParam(st.Op, e)
	}
	if w.done[e.target] == nil {
		w.done[e.target] = map[string]written{}
	}
	w.done[e.target][name] = written{v, st.Op.ID}
	w.set(e.target, name, value, named)
}

// paramEntry is a parameter in a parameters list of the document.
type paramEntry struct {
	list   *yaml.Node
	index  int
	target *yaml.Node
	atPath bool // declared for the path, not for the operation
}

// shared reports whether other operations may use the parameter too: it
// is declared for the path or is a $ref.
func (e *paramEntry) shared() bool {
	return e.atPath || yamldoc.Ref(e.list.Content[e.index]) != ""
}

// paramEntry finds a parameter of an operation: in its own parameters,
// else in those of its path.
func (w *writer) paramEntry(op *spec.Operation, name, in string) *paramEntry {
	for i, holder := range []*yaml.Node{w.operation(op), yamldoc.Path(w.doc.Root, "paths", op.Path)} {
		list := yamldoc.Get(holder, "parameters")
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		for j, entry := range list.Content {
			target, err := w.doc.Resolve(entry)
			if err != nil {
				continue
			}
			n, at := yamldoc.Get(target, "name"), yamldoc.Get(target, "in")
			if n != nil && at != nil && n.Value == name && at.Value == in {
				return &paramEntry{list: list, index: j, target: target, atPath: i == 1}
			}
		}
	}
	return nil
}

// ownParam gives an operation its own copy of a shared parameter: a $ref
// in its list is replaced, a parameter of the path is declared again in
// the operation, where it overrides the one of the path.
func (w *writer) ownParam(op *spec.Operation, e *paramEntry) *paramEntry {
	c := clone(e.target)
	if !e.atPath {
		e.list.Content[e.index] = c
		return &paramEntry{list: e.list, index: e.index, target: c}
	}
	opNode := w.operation(op)
	list := yamldoc.Get(opNode, "parameters")
	if list == nil || list.Kind != yaml.SequenceNode {
		list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		_ = yamldoc.SetNode(opNode, "parameters", list)
	}
	list.Content = append(list.Content, c)
	w.changed = true
	return &paramEntry{list: list, index: len(list.Content) - 1, target: c}
}

// ignore adds fields to x-apitest-ignore of an operation; the fields it
// lists already stay.
func (w *writer) ignore(op *spec.Operation, fields []string) {
	opNode := w.operation(op)
	var cur []string
	if n := yamldoc.Get(opNode, "x-apitest-ignore"); n != nil && n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			cur = append(cur, c.Value)
		}
	}
	all := slices.Clone(cur)
	for _, f := range fields {
		if !slices.Contains(all, f) {
			all = append(all, f)
		}
	}
	if len(all) == len(cur) {
		return
	}
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, f := range all {
		list.Content = append(list.Content, scalarNode(f))
	}
	if err := yamldoc.SetNode(opNode, "x-apitest-ignore", list); err != nil {
		w.res.problem(CodeShared, method(op), "x-apitest-ignore cannot be written: %v", err)
		return
	}
	w.changed = true
}

func clone(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = clone(child)
	}
	return &c
}
