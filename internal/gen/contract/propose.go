package contract

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Proposal is the entry init proposes under "operations" of the overrides
// for an operation without a step: the values it still needs, null while
// open, and why.
type Proposal struct {
	Call string // "METHOD /path"
	Why  []string
	// Path, Query and Header are the parameters that need a value; nil is
	// open, else the value was learned from the stage.
	Path, Query, Header map[string]any
	// Learned notes, by "path/name", where a value comes from.
	Learned map[string]string
	// Status is open: the operation runs only once it is set.
	Status bool
	// Documented is the lowest documented 2xx, 0 if none.
	Documented int
}

// propose builds the proposal of an operation that got no step.
func (g *gen) propose(op *spec.Operation) *Proposal {
	p := &Proposal{Call: op.Method + " " + op.Path, Documented: lowestSuccess(op), Learned: map[string]string{}}
	oo := g.in.Overrides.operation(op)
	mo := g.m.Op(op)
	switch {
	case g.success(op) == 0:
		p.Status = true
		p.Why = append(p.Why, "no 2xx response documented: set the status it answers with")
	case g.optIn(op):
	case op.Method == http.MethodDelete:
		p.Status = true
		p.Why = append(p.Why, "a DELETE outside the life cycle of a record runs only with a status")
	case mo != nil && mo.Role == model.RoleCreate && !g.recordCreate(op):
		p.Status = true
		p.Why = append(p.Why, fmt.Sprintf("another create of %s; the records use the first one, this one runs only with a status", mo.Resource.Name))
	}
	rc := g.recordFor(op)
	for _, prm := range op.Params {
		if prm.In == openapi3.ParameterInCookie {
			continue
		}
		if _, ok := oo.param(prm); ok {
			continue
		}
		why := g.missing(op, prm, rc)
		if why == "" {
			continue
		}
		p.Why = append(p.Why, why)
		var v any
		if lv, note, ok := g.learnedParam(op, prm); ok {
			v = lv
			p.Learned[prm.In+"/"+prm.Name] = note
		}
		p.set(prm, v)
	}
	if len(p.Why) == 0 {
		p.Status = true
		p.Why = append(p.Why, "the generator found no place for it; with a status it runs once")
	}
	return p
}

// missing says why a parameter has no value, "" if it has one.
func (g *gen) missing(op *spec.Operation, p *openapi3.Parameter, rc *rec) string {
	if _, ok := g.boundValue(op, p, rc); ok {
		return ""
	}
	if _, ok := g.in.Overrides.value(0, op.ID+"."+p.Name, p.Name); ok {
		return ""
	}
	if p.In == openapi3.ParameterInPath {
		if b := g.binds.For(op, p); b != nil && !b.Source.HasConst {
			if src := g.resourceOfCreate(b.Producer); src != nil {
				return fmt.Sprintf("{%s} comes from %s %s, which created no record", p.Name, b.Producer.Method, b.Producer.Path)
			}
			return fmt.Sprintf("{%s} comes from %s %s, which is no create of the scenario", p.Name, b.Producer.Method, b.Producer.Path)
		}
		if p.Example != nil || len(p.Examples) > 0 {
			return ""
		}
		return fmt.Sprintf("no value for the path parameter {%s}: no create gives it and the spec has no example", p.Name)
	}
	if !p.Required {
		return ""
	}
	if strings.EqualFold(p.Name, "If-Match") && rc != nil && rc.etag != "" {
		return ""
	}
	if _, ok := paramExample(p); ok {
		return ""
	}
	return fmt.Sprintf("no value for the required %s parameter %q", p.In, p.Name)
}

func (p *Proposal) set(prm *openapi3.Parameter, v any) {
	var m *map[string]any
	switch prm.In {
	case openapi3.ParameterInPath:
		m = &p.Path
	case openapi3.ParameterInQuery:
		m = &p.Query
	default:
		m = &p.Header
	}
	if *m == nil {
		*m = map[string]any{}
	}
	(*m)[prm.Name] = v
}

// learnedParam is a value for a parameter from the records learned from
// the stage: the field the parameter names a key of, or the field of the
// same name.
func (g *gen) learnedParam(op *spec.Operation, p *openapi3.Parameter) (any, string, bool) {
	if len(g.in.Learned) == 0 {
		return nil, "", false
	}
	type field struct {
		r    *model.Resource
		name string
	}
	var cands []field
	if mo := g.m.Op(op); mo != nil {
		if mp := mo.Param(p.Name); mp != nil && mp.Resource != nil && mp.Field != "" {
			cands = append(cands, field{mp.Resource, mp.Field})
		}
	}
	if b := g.binds.For(op, p); b != nil && !b.Source.FromRequest && b.Source.Header == "" && strings.Count(b.Source.Pointer, "/") == 1 {
		if mo := g.m.Op(b.Producer); mo != nil {
			cands = append(cands, field{mo.Resource, b.Source.Pointer[1:]})
		}
	}
	for _, r := range g.m.Resources {
		for _, k := range r.Keys {
			if strings.EqualFold(k, p.Name) || strings.EqualFold(r.Name+k, p.Name) {
				cands = append(cands, field{r, k})
			}
		}
	}
	for _, r := range g.m.Resources {
		if f := r.Field(p.Name); f != "" {
			cands = append(cands, field{r, f})
		}
		for _, f := range r.FieldNames() { // dockCode: field code of Dock
			if strings.EqualFold(r.Name+f, p.Name) {
				cands = append(cands, field{r, f})
			}
		}
	}
	v := spec.NewValidator()
	for _, c := range cands {
		for _, rec := range g.in.Learned[c.r.Name] {
			val, ok := rec[c.name]
			if !ok || val == nil {
				continue
			}
			switch val.(type) {
			case map[string]any, []any:
				continue
			}
			if p.Schema != nil && p.Schema.Value != nil && len(v.Validate(p.Schema.Value, spec.Normalize(val), spec.ModeRequest)) > 0 {
				continue
			}
			note := fmt.Sprintf("learned from %s (%s.%s)", g.in.LearnedFrom, c.r.Name, c.name)
			if p.In == openapi3.ParameterInPath {
				note += "; it may not exist on the empty instance"
			}
			return spec.Normalize(val), note, true
		}
	}
	return nil, "", false
}

// overridesHead heads a new overrides file.
const overridesHead = "Only what the generator cannot know. Everything else is generated."

// operationsHead heads the "operations" section.
const operationsHead = "Operations without a generated step, proposed by \"apitest-gen contract init\".\n" +
	"Fill in the null values (or set skip: true) and run init again."

// WriteProposals adds the proposals to the overrides file under
// "operations": an entry for an operation that has none, and the open keys
// an entry misses. What is there stays as it is, comments too. It returns
// how many entries it added or extended.
func WriteProposals(path string, ps []*Proposal) (int, error) {
	if len(ps) == 0 {
		return 0, nil
	}
	doc, prefix, err := loadOverridesDoc(path)
	if err != nil {
		return 0, err
	}
	ops := yamldoc.Get(doc.Root, "operations")
	if ops == nil || ops.Kind != yaml.MappingNode {
		ops = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if err := yamldoc.SetNode(doc.Root, "operations", ops); err != nil {
			return 0, err
		}
		key := doc.Root.Content[len(doc.Root.Content)-2]
		if key.Value == "operations" && key.HeadComment == "" {
			key.HeadComment = operationsHead
		}
	}
	existing := map[string]*yaml.Node{}
	for i := 0; i+1 < len(ops.Content); i += 2 {
		if k, ok := callKey(ops.Content[i].Value); ok {
			existing[k] = ops.Content[i+1]
		}
	}
	n := 0
	for _, p := range ps {
		entry := existing[p.Call]
		if entry == nil {
			entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if p.fill(entry) {
				ops.Content = append(ops.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.Call, HeadComment: strings.Join(p.Why, "\n")}, entry)
				n++
			}
			continue
		}
		if entry.Kind != yaml.MappingNode {
			continue // null or something else: the user's, leave it
		}
		if p.fill(entry) {
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	b, err := doc.Bytes()
	if err != nil {
		return 0, err
	}
	return n, scenario.WriteFile(path, append(prefix, b...))
}

// fill adds the keys of the proposal an entry misses; true if it added one.
func (p *Proposal) fill(entry *yaml.Node) bool {
	added := false
	for _, in := range []struct {
		key string
		m   map[string]any
	}{{"path", p.Path}, {"query", p.Query}, {"header", p.Header}} {
		if len(in.m) == 0 {
			continue
		}
		sub := yamldoc.Get(entry, in.key)
		if sub == nil || sub.Kind != yaml.MappingNode {
			if sub != nil && !isNull(sub) {
				continue
			}
			sub = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			_ = yamldoc.SetNode(entry, in.key, sub)
		}
		for _, name := range sortedKeys(in.m) {
			if yamldoc.Get(sub, name) != nil {
				continue
			}
			v := nullNode()
			if in.m[name] != nil {
				if nv, err := yamldoc.Node(in.m[name]); err == nil {
					v = nv
				}
			}
			v.LineComment = p.Learned[in.key+"/"+name]
			sub.Content = append(sub.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, v)
			added = true
		}
	}
	if p.Status && yamldoc.Get(entry, "status") == nil {
		v := nullNode()
		v.LineComment = "the status it answers with"
		if p.Documented != 0 {
			v.LineComment = fmt.Sprintf("documented: %d", p.Documented)
		}
		entry.Content = append(entry.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "status"}, v)
		added = true
	}
	return added
}

func nullNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// loadOverridesDoc reads the overrides as a node tree; a missing file gives
// a new one. A file with only comments is kept as the prefix of the new
// content.
func loadOverridesDoc(path string) (*yamldoc.Doc, []byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return yamldoc.New(overridesHead), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if doc, err := yamldoc.Parse(b); err == nil {
		return doc, nil, nil
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if v != nil {
		return nil, nil, fmt.Errorf("%s: the overrides are a mapping", path)
	}
	prefix := bytes.TrimRight(b, "\n")
	if len(prefix) > 0 {
		prefix = append(prefix, '\n', '\n')
	}
	return yamldoc.New(""), prefix, nil
}
