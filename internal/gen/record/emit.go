package record

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// emitter writes the examples into the document.
type emitter struct {
	doc     *yamldoc.Doc
	res     *Result
	ignore  bool
	v       *spec.Validator
	params  map[*yaml.Node]written
	media   map[*yaml.Node]written
	changed bool
	bad     map[string]bool // operations an example of which was not written
	// base are the examples of the last run at the nodes of this document;
	// only an example that differs from it counts as written
	base map[*yaml.Node]any
}

type written struct {
	value any
	by    string
}

func newEmitter(doc *yamldoc.Doc, res *Result, ignore bool) *emitter {
	return &emitter{doc: doc, res: res, ignore: ignore, v: spec.NewValidator(), params: map[*yaml.Node]written{}, media: map[*yaml.Node]written{},
		bad: map[string]bool{}, base: map[*yaml.Node]any{}}
}

// emit writes the example of one case: its parameters, its request body and
// the answer at the response of its status. It reports whether all of it
// was written.
func (em *emitter) emit(c *cases.Case, e *example) bool {
	op := c.Op
	for _, name := range sortedKeys(e.params) {
		em.param(op, name, e.params[name], e.src)
	}
	if e.hasBody {
		if pl := em.request(op); pl != nil {
			em.write(pl, e.body, spec.ModeRequest, op.ID, e.src+", the body sent")
		}
	}
	if !e.hasResp {
		return !em.bad[op.ID]
	}
	pl := em.response(op, strconv.Itoa(e.status))
	if pl == nil {
		pl = em.response(op, c.Expect.Code)
		if pl != nil {
			em.res.note(CodeStatus, op.ID, "the instance answers %d, the spec documents %s; the example is written at %s", e.status, c.Expect.Code, pl.code)
		}
	}
	if pl != nil {
		em.write(pl, e.resp, spec.ModeResponse, op.ID, e.src+", the answer")
	}
	return !em.bad[op.ID]
}

// violation describes a schema error of an example: where its data came
// from, the value at that place and the rule of the schema.
func violation(src string, v any, e spec.SchemaError, s *openapi3.Schema) string {
	val := at(v, e.Pointer)
	shown := ""
	if m, ok := val.(missing); ok {
		shown = string(m)
	} else {
		b, _ := json.Marshal(val)
		shown = clip(string(b))
	}
	msg := fmt.Sprintf("%s: %s\n  data from: %s\n  value:     %s", e.Pointer, e.Reason, src, shown)
	if rule := ruleAt(s, e.Pointer); rule != "" {
		msg += "\n  schema:    " + rule
	}
	return msg + "\n  fix the data of the instance, the spec, or pass -ignorelinting to write it anyway"
}

// missing is the value at a pointer that has none.
type missing string

// at returns the value at a JSON pointer, or a missing.
func at(v any, pointer string) any {
	cur := v
	for _, seg := range strings.Split(strings.Trim(pointer, "/"), "/") {
		if seg == "" {
			continue
		}
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				return missing("(missing; the object has " + strings.Join(sortedKeys(x), ", ") + ")")
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i >= len(x) {
				return missing("(missing)")
			}
			cur = x[i]
		default:
			return missing("(missing)")
		}
	}
	return cur
}

// ruleAt describes the schema at a JSON pointer: type, format, pattern,
// enum, required.
func ruleAt(s *openapi3.Schema, pointer string) string {
	cur := s
	for _, seg := range strings.Split(strings.Trim(pointer, "/"), "/") {
		if seg == "" || cur == nil {
			continue
		}
		if _, err := strconv.Atoi(seg); err == nil && cur.Items != nil {
			cur = cur.Items.Value
			continue
		}
		props, _ := dict.Properties(cur)
		p := props[seg]
		if p == nil {
			_, req := dict.Properties(cur)
			if contains(req, seg) {
				return "required: " + strings.Join(req, ", ")
			}
			return ""
		}
		cur = p.Value
	}
	if cur == nil {
		return ""
	}
	var parts []string
	if t := value.Type(cur); t != "" {
		parts = append(parts, "type "+t)
	}
	if cur.Format != "" {
		parts = append(parts, "format "+cur.Format)
	}
	if cur.Pattern != "" {
		parts = append(parts, "pattern "+cur.Pattern)
	}
	if len(cur.Enum) > 0 {
		parts = append(parts, "enum "+text(cur.Enum))
	}
	if cur.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("maxLength %d", *cur.MaxLength))
	}
	if cur.Nullable {
		parts = append(parts, "nullable")
	}
	return strings.Join(parts, ", ")
}

// place is a media type that holds an example.
type place struct {
	node   *yaml.Node
	shared bool
	schema *openapi3.SchemaRef
	code   string
	named  bool
	where  string
}

func (p *place) example() any {
	if ex := yamldoc.Get(p.node, "example"); ex != nil {
		if v, err := yamldoc.Decode(ex); err == nil {
			return v
		}
	}
	return nil
}

func (em *emitter) operation(op *spec.Operation) *yaml.Node {
	return yamldoc.Get(yamldoc.Path(em.doc.Root, "paths", op.Path), strings.ToLower(op.Method))
}

func (em *emitter) mediaPlace(holder *yaml.Node, content openapi3.Content, where string) *place {
	if holder == nil {
		return nil
	}
	target, err := em.doc.Resolve(holder)
	if err != nil {
		return nil
	}
	for _, mt := range sortedKeys(content) {
		m := content[mt]
		if !spec.IsJSON(mt) || m == nil || m.Schema == nil || m.Schema.Value == nil {
			continue
		}
		node := yamldoc.Get(yamldoc.Get(target, "content"), mt)
		if node == nil {
			return nil
		}
		exs := yamldoc.Get(node, "examples")
		return &place{node: node, shared: yamldoc.Ref(holder) != "", schema: m.Schema, named: exs != nil && len(exs.Content) > 0,
			where: fmt.Sprintf("%s.content[%s]", where, mt)}
	}
	return nil
}

func (em *emitter) request(op *spec.Operation) *place {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return nil
	}
	return em.mediaPlace(yamldoc.Get(em.operation(op), "requestBody"), rb.Value.Content, op.Where+".requestBody")
}

func (em *emitter) response(op *spec.Operation, code string) *place {
	if op.Op.Responses == nil || code == "" {
		return nil
	}
	r := op.Op.Responses.Map()[code]
	if r == nil || r.Value == nil {
		return nil
	}
	p := em.mediaPlace(yamldoc.Get(yamldoc.Get(em.operation(op), "responses"), code), r.Value.Content, op.Where+".responses."+code)
	if p != nil {
		p.code = code
	}
	return p
}

// write sets the example of a media type; one that serves several
// operations must get the same example from all of them.
func (em *emitter) write(pl *place, v any, mode spec.Mode, by, src string) {
	if pl.named {
		return // named examples are curated
	}
	if errs := em.v.Validate(pl.schema.Value, spec.Normalize(v), mode); len(errs) > 0 {
		var msgs []string
		for i, e := range errs {
			if i == 3 {
				msgs = append(msgs, fmt.Sprintf("… %d more", len(errs)-3))
				break
			}
			msgs = append(msgs, violation(src, spec.Normalize(v), e, pl.schema.Value))
		}
		if !em.res.lint(em.ignore, CodeInvalid, pl.where, "the example violates the schema (%s)\n%s", by, strings.Join(msgs, "\n")) {
			em.bad[by] = true
			return
		}
	}
	if pl.shared {
		if prev, ok := em.media[pl.node]; ok && !compare.Equal(spec.Normalize(prev.value), spec.Normalize(v)) {
			em.res.problem(CodeShared, pl.where, "%s and %s share this response and need different examples; give one of them its own response\n  %s: %s\n  %s: %s",
				prev.by, by, prev.by, clip(text(prev.value)), by, clip(text(v)))
			em.bad[by] = true
			return
		}
		em.media[pl.node] = written{v, by}
	}
	em.set(pl.node, v, pl.example())
}

func (em *emitter) set(node *yaml.Node, v, cur any) {
	if cur != nil && compare.Equal(spec.Normalize(cur), spec.Normalize(v)) {
		return
	}
	if err := yamldoc.Set(node, "example", v); err == nil {
		em.changed = true
		if b, ok := em.base[node]; !ok || !compare.Equal(spec.Normalize(b), spec.Normalize(v)) {
			em.res.Stats.Examples++
		}
	}
}

// paramEntry is a parameter in a parameters list of the document.
type paramEntry struct {
	list   *yaml.Node
	index  int
	target *yaml.Node
	where  string
}

// paramEntry finds a path or query parameter of an operation: in its own
// parameters, else in those of the path.
func (em *emitter) paramEntry(op *spec.Operation, name, in string) *paramEntry {
	for _, holder := range []*yaml.Node{em.operation(op), yamldoc.Path(em.doc.Root, "paths", op.Path)} {
		list := yamldoc.Get(holder, "parameters")
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		for i, entry := range list.Content {
			target, err := em.doc.Resolve(entry)
			if err != nil {
				continue
			}
			n, at := yamldoc.Get(target, "name"), yamldoc.Get(target, "in")
			if n != nil && at != nil && n.Value == name && at.Value == in {
				return &paramEntry{list: list, index: i, target: target, where: fmt.Sprintf("%s.parameters[%s]", op.Where, name)}
			}
		}
	}
	return nil
}

func (em *emitter) param(op *spec.Operation, name string, v any, src string) {
	var p *openapi3.Parameter
	for _, x := range op.Params {
		if x.Name == name && (x.In == openapi3.ParameterInPath || x.In == openapi3.ParameterInQuery) {
			p = x
		}
	}
	if p == nil || p.Schema == nil || p.Schema.Value == nil {
		return
	}
	e := em.paramEntry(op, name, p.In)
	if e == nil {
		return
	}
	v = coerce(v, p.Schema.Value)
	if errs := em.v.Validate(p.Schema.Value, v, spec.ModePlain); len(errs) > 0 {
		if !em.res.lint(em.ignore, CodeInvalid, e.where, "the value %s violates the schema of {%s}: %s\n  data from: %s\n  schema:    %s", text(v), name, errs[0].Reason, src, ruleAt(p.Schema.Value, "")) {
			em.bad[op.ID] = true
			return
		}
	}
	if prev, ok := em.params[e.target]; ok && !compare.Equal(spec.Normalize(prev.value), spec.Normalize(v)) {
		if yamldoc.Ref(e.list.Content[e.index]) == "" {
			em.res.problem(CodeShared, e.where, "{%s} is declared once for several operations, %s needs %s and %s needs %s; declare it in each operation",
				name, prev.by, text(prev.value), op.ID, text(v))
			em.bad[op.ID] = true
			return
		}
		c := clone(e.target) // a $ref: this operation gets its own copy
		e.list.Content[e.index] = c
		e.target = c
	}
	em.params[e.target] = written{v, op.ID}
	var cur any
	if ex := yamldoc.Get(e.target, "example"); ex != nil {
		cur, _ = yamldoc.Decode(ex)
	}
	em.set(e.target, v, cur)
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

// coerce converts a value into the type of a parameter: a number into the
// text of a string parameter, a text into the number of a numeric one.
func coerce(v any, s *openapi3.Schema) any {
	switch value.Type(s) {
	case "string":
		if n, ok := v.(json.Number); ok {
			return n.String()
		}
	case "integer", "number":
		if str, ok := v.(string); ok {
			if _, err := strconv.ParseFloat(str, 64); err == nil {
				return json.Number(str)
			}
		}
	}
	return spec.Normalize(v)
}

// holders are the nodes of an operation that can hold examples: its path
// and query parameters ("param query limit"), the media types of its
// request body ("request application/json") and of its responses
// ("response 200 application/json"), $refs resolved.
func (em *emitter) holders(op *spec.Operation) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath && p.In != openapi3.ParameterInQuery {
			continue
		}
		if e := em.paramEntry(op, p.Name, p.In); e != nil {
			out["param "+p.In+" "+p.Name] = e.target
		}
	}
	media := func(holder *yaml.Node, key string) {
		target, err := em.doc.Resolve(holder)
		if err != nil {
			return
		}
		content := yamldoc.Get(target, "content")
		for _, mt := range yamldoc.Keys(content) {
			if n, err := em.doc.Resolve(yamldoc.Get(content, mt)); err == nil && n != nil {
				out[key+" "+mt] = n
			}
		}
	}
	opNode := em.operation(op)
	if rb := yamldoc.Get(opNode, "requestBody"); rb != nil {
		media(rb, "request")
	}
	responses := yamldoc.Get(opNode, "responses")
	for _, code := range yamldoc.Keys(responses) {
		media(yamldoc.Get(responses, code), "response "+code)
	}
	return out
}

// strip removes every example of an operation, single and named: the
// examples of a run come from the instance, never from the spec.
func (em *emitter) strip(op *spec.Operation) {
	for _, n := range em.holders(op) {
		if yamldoc.Ref(n) != "" {
			continue
		}
		a, b := yamldoc.Delete(n, "example"), yamldoc.Delete(n, "examples")
		em.changed = em.changed || a || b
	}
}

// has reports whether the output of the last run holds an example of an
// operation, or the operation has no place for one.
func (em *emitter) has(op *spec.Operation) bool {
	hs := em.holders(op)
	for _, n := range hs {
		if yamldoc.Get(n, "example") != nil {
			return true
		}
	}
	return len(hs) == 0
}

// remember keeps the examples the last run wrote for an operation, to
// tell which examples of this run are new.
func (em *emitter) remember(prev *emitter, op *spec.Operation) {
	from := prev.holders(op)
	for key, n := range em.holders(op) {
		if ex := yamldoc.Get(from[key], "example"); ex != nil {
			if v, err := yamldoc.Decode(ex); err == nil {
				em.base[n] = v
			}
		}
	}
}

// carry takes the examples of an unchanged operation from the output of the
// last run.
func (em *emitter) carry(op *spec.Operation) {
	for _, n := range em.holders(op) {
		if v, ok := em.base[n]; ok && yamldoc.Set(n, "example", v) == nil {
			em.changed = true
		}
	}
}

// remap moves the ids in the examples of unchanged operations when a new
// POST in the run shifted the ids the environment assigns.
func (em *emitter) remap(run []*cases.Case, needs map[string]bool, sm *sim, old *Recorded) {
	shift := map[string]map[string]any{}
	for id, n := range sm.created {
		o, ok := old.Created[id]
		x := sm.w.byOp[id]
		if !ok || o == n || x == nil {
			continue
		}
		if shift[x.table] == nil {
			shift[x.table] = map[string]any{}
		}
		shift[x.table][strconv.Itoa(o)] = json.Number(strconv.Itoa(n))
	}
	if len(shift) == 0 {
		return
	}
	idFn := func(t string, v any) any {
		if n, ok := shift[t][text(v)]; ok {
			return n
		}
		return v
	}
	done := map[string]bool{}
	for _, c := range run {
		op := c.Op
		if needs[op.ID] || done[op.ID] {
			continue
		}
		done[op.ID] = true
		before := em.res.Stats.Examples
		for _, p := range op.Params {
			if e := em.paramEntry(op, p.Name, p.In); e != nil && p.Schema != nil {
				if ex := yamldoc.Get(e.target, "example"); ex != nil {
					cur, _ := yamldoc.Decode(ex)
					t := tableName(p.Name)
					if strings.EqualFold(p.Name, "id") {
						t = tableName(segment(op.Path, p.Name))
					} else if rt := refTable(p.Name); rt != "" {
						t = rt
					}
					em.set(e.target, idFn(t, cur), cur)
				}
			}
		}
		places := []*place{em.request(op)}
		if op.Op.Responses != nil {
			for code := range op.Op.Responses.Map() {
				if strings.HasPrefix(code, "2") {
					places = append(places, em.response(op, code))
				}
			}
		}
		for _, pl := range places {
			if pl == nil || pl.named {
				continue
			}
			if cur := pl.example(); cur != nil {
				em.set(pl.node, walk(spec.Normalize(cur), pl.schema, "", idFn, nil, sm.tables, sm.rd.n), cur)
			}
		}
		if em.res.Stats.Examples > before {
			em.res.Stats.Remapped++
			em.res.note(CodeRemapped, op.ID, "a new POST moved the ids the environment assigns; the ids of its examples are moved, the rest is kept")
		}
	}
}

// segment is the literal segment in front of a parameter.
func segment(path, param string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if s == "{"+param+"}" {
			for j := i - 1; j >= 0; j-- {
				if !strings.HasPrefix(segs[j], "{") && !generic(strings.ToLower(segs[j])) {
					return segs[j]
				}
			}
		}
	}
	return ""
}
