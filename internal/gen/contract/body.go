package contract

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// uniqueNames are fields that usually must be unique per record.
var uniqueNames = []string{"isbn", "email", "code", "slug", "sku", "username", "login", "externalid", "iban", "serialnumber"}

// body builds the request body of a step for record rc. Every field the
// schema declares (readOnly ones left out) gets its value from, in this
// order: the overrides, a reference to a record the scenario created, the
// body base (the create of the record, for an update), a learned record,
// the example of the spec (first record), or a generated one. Optional
// fields are only sent when one of the first five has a value. unique
// lists the fields a run with Unique makes new.
func (g *gen) body(st *scenario.Step, op *spec.Operation, ref *openapi3.SchemaRef, rc *rec, base any) (any, []string) {
	if ref == nil || ref.Value == nil {
		return map[string]any{}, nil
	}
	s := ref.Value
	props := properties(s)
	if len(props) == 0 {
		// not an object: the example of the spec or a generated value
		if v, ok := requestExample(op); ok {
			return v, nil
		}
		v, _ := generated(s, "contract."+op.ID, "", "", idxOf(rc))
		return v, nil
	}
	required := requiredOf(s)
	dto := dict.DTORef(ref)
	resName := ""
	if rc != nil {
		resName = rc.res.Name
	}
	baseMap, _ := base.(map[string]any)
	example, _ := requestExample(op)
	exMap, _ := example.(map[string]any)
	var learned map[string]any
	if rc != nil {
		if l := g.in.Learned[rc.res.Name]; len(l) > 0 {
			learned = l[rc.idx%len(l)]
		}
	}
	out := map[string]any{}
	var unique []string
	generatedCount := 0
	v := spec.NewValidator()
	fits := func(p *openapi3.Schema, val any) bool {
		return len(scenario.Names(val)) > 0 || len(v.Validate(p, spec.Normalize(val), spec.ModeRequest)) == 0
	}
	for _, name := range sortedKeys(props) {
		p := props[name].Value
		if p == nil || p.ReadOnly {
			continue
		}
		ptr := "/body/" + name
		if val, ok := g.in.Overrides.value(idxOf(rc), resName+"."+name, dto+"."+name, op.ID+"."+name, name); ok {
			out[name] = val
			st.Notes[ptr] = "override"
		} else if target, how := g.reference(rc, name); target != "" {
			out[name] = "{{" + target + "}}"
			st.Notes[ptr] = how
		} else if val, ok := baseMap[name]; ok {
			out[name] = scenario.Clone(val)
		} else if val, ok := learned[name]; ok && fits(p, val) {
			out[name] = spec.Normalize(val)
			st.Notes[ptr] = "guess: learned from " + g.in.LearnedFrom
			g.out.Guesses = append(g.out.Guesses, Note{Step: st.ID, What: fmt.Sprintf("%s = %s", name, scenario.Text(val)),
				Why: "learned from " + g.in.LearnedFrom, Fix: fmt.Sprintf("values: {%s.%s: …}", resName, name)})
		} else if val, ok := exMap[name]; ok && (idxOf(rc) == 0 || !isText(p)) && fits(p, val) {
			out[name] = spec.Normalize(val)
		} else if contains(required, name) {
			val, ok := generated(p, "contract."+resName+"."+name, name, dto, idxOf(rc))
			if !ok {
				g.out.Guesses = append(g.out.Guesses, Note{Step: st.ID, What: "no value for " + name, Why: "the schema allows no generated value (pattern or constraints)",
					Fix: fmt.Sprintf("values: {%s.%s: …}", resName, name)})
				continue
			}
			out[name] = val
			generatedCount++
		} else {
			continue
		}
		if isUniqueField(p, name, rc) && len(scenario.Names(out[name])) == 0 {
			if _, isStr := out[name].(string); isStr {
				unique = append(unique, "/"+name)
			}
		}
	}
	if generatedCount > 0 {
		st.Why += fmt.Sprintf("; %d values generated from the schema", generatedCount)
	}
	return out, unique
}

// reference returns the placeholder name a body field takes when it
// refers to a record the scenario created, and why.
func (g *gen) reference(rc *rec, field string) (string, string) {
	if rc == nil {
		return "", ""
	}
	r := rc.res
	switch link := g.in.Overrides.link(r.Name, field); {
	case strings.EqualFold(link, "none"):
		return "", ""
	case link != "":
		if t := g.m.Resource(link); t != nil {
			if target := g.recordOf(rc, t); target != nil && target.keyVar != "" {
				return target.keyVar, "override: refers to " + target.name
			}
		}
	}
	if t, _ := r.Ref(field); t != nil {
		if target := g.recordOf(rc, t); target != nil && target.keyVar != "" {
			return target.keyVar, fmt.Sprintf("sure: %s.%s refers to %s", r.Name, field, t.Name)
		}
	}
	// a field named like a value another record saved: "authorId", "originAuthorId"
	for _, other := range g.order {
		if other == r {
			continue
		}
		list := g.recs[other]
		if len(list) == 0 {
			continue
		}
		target := g.recordOf(rc, other)
		for _, name := range sortedKeys(target.vars) {
			v := target.vars[name]
			suffix := v[strings.LastIndex(v, ".")+1:]
			if strings.EqualFold(suffix, "id") || !matches(field, suffix) {
				continue
			}
			g.out.Guesses = append(g.out.Guesses, Note{What: fmt.Sprintf("%s.%s refers to %s", r.Name, field, target.name),
				Why: "the field is named like the value " + suffix, Fix: fmt.Sprintf("links: {%s.%s: none}", r.Name, field)})
			return v, "guess: named like " + suffix
		}
	}
	return "", ""
}

// recordOf is the record of resource t that record rc refers to.
func (g *gen) recordOf(rc *rec, t *model.Resource) *rec {
	if target := rc.refs[t]; target != nil {
		return target
	}
	if list := g.recs[t]; len(list) > 0 {
		return list[min(rc.idx, len(list)-1)]
	}
	return nil
}

// matches reports whether a field is named like a value: "authorId", or
// ends in it as a word: "originAuthorId".
func matches(field, name string) bool {
	if strings.EqualFold(field, name) {
		return true
	}
	if len(field) <= len(name) || !strings.EqualFold(field[len(field)-len(name):], name) {
		return false
	}
	c := field[len(field)-len(name)]
	return (c >= 'A' && c <= 'Z') || field[len(field)-len(name)-1] == '_'
}

func isUniqueField(p *openapi3.Schema, name string, rc *rec) bool {
	if v, _ := p.Extensions["x-apitest-unique"].(bool); v {
		return true
	}
	if contains(uniqueNames, strings.ToLower(name)) {
		return true
	}
	if rc != nil {
		for _, k := range rc.res.Keys {
			if strings.EqualFold(k, name) {
				return true
			}
		}
	}
	return false
}

func isText(p *openapi3.Schema) bool {
	return value.Type(p) == "string" && len(p.Enum) == 0
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// generated is a value for a schema, seeded by the path and the record.
func generated(s *openapi3.Schema, path, name, parent string, idx int) (any, bool) {
	r := value.Generate(s, value.Context{Seed: 42, Path: path + "#" + strconv.Itoa(idx), Name: name, Parent: parent})
	if !r.OK {
		return nil, false
	}
	return spec.Normalize(r.Value), true
}

// requestExample is the example of an operation's request body: example,
// else the first named example that no 4xx or 5xx response names.
func requestExample(op *spec.Operation) (any, bool) {
	_, media := cases.RequestMedia(op)
	if media == nil {
		return nil, false
	}
	if media.Example != nil {
		return spec.Normalize(media.Example), true
	}
	for _, k := range sortedKeys(media.Examples) {
		if errorExample(op, k) {
			continue
		}
		if ex := media.Examples[k]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
			return spec.Normalize(ex.Value.Value), true
		}
	}
	return nil, false
}

// errorExample reports whether an error response names an example.
func errorExample(op *spec.Operation, name string) bool {
	if op.Op.Responses == nil {
		return false
	}
	for code, r := range op.Op.Responses.Map() {
		if code[0] < '4' || r == nil || r.Value == nil {
			continue
		}
		for _, m := range r.Value.Content {
			if m != nil && m.Examples[name] != nil {
				return true
			}
		}
	}
	return false
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

func requiredOf(s *openapi3.Schema) []string {
	var out []string
	var add func(x *openapi3.Schema, depth int)
	add = func(x *openapi3.Schema, depth int) {
		if x == nil || depth > 20 {
			return
		}
		out = append(out, x.Required...)
		for _, part := range x.AllOf {
			add(part.Value, depth+1)
		}
	}
	add(s, 0)
	return out
}

func hasProperty(s *openapi3.Schema, name string) bool {
	_, ok := properties(s)[name]
	return ok
}

func requestSchema(op *spec.Operation) *openapi3.Schema {
	_, media := cases.RequestMedia(op)
	if media == nil || media.Schema == nil {
		return nil
	}
	return media.Schema.Value
}

func hasRequestProperty(op *spec.Operation, name string) bool {
	s := requestSchema(op)
	return s != nil && hasProperty(s, name)
}

func responseSchema(op *spec.Operation, status int) *openapi3.Schema {
	_, r := cases.ResponseFor(op, status)
	if r == nil {
		return nil
	}
	_, media := cases.PickMedia(r.Content)
	if media == nil || media.Schema == nil {
		return nil
	}
	return media.Schema.Value
}

// change changes one field of an update body, so the read after it shows
// whether the update was stored: a text gets " (revised)", a number one
// more. Keys, references and unique fields stay. A PATCH sends only the
// changed field. It returns the body and the field.
func change(body any, s *openapi3.Schema, r *model.Resource, patch bool) (any, string) {
	m, ok := body.(map[string]any)
	if !ok {
		return body, ""
	}
	props := properties(s)
	for _, name := range sortedKeys(m) {
		ref := props[name]
		if ref == nil || ref.Value == nil || r.Fixed(name) || contains(uniqueNames, strings.ToLower(name)) || len(scenario.Names(m[name])) > 0 {
			continue
		}
		p := ref.Value
		if len(p.Enum) > 0 || p.Pattern != "" || p.Format != "" {
			continue
		}
		var nv any
		switch value.Type(p) {
		case "string":
			str, _ := m[name].(string)
			nv = str + " (revised)"
			if p.MaxLength != nil && uint64(len(nv.(string))) > *p.MaxLength {
				if *p.MaxLength < 2 {
					continue
				}
				nv = strings.Repeat("r", int(*p.MaxLength))
				if str == nv {
					continue
				}
			}
		case "integer", "number":
			n, err := strconv.ParseFloat(scenario.Text(m[name]), 64)
			if err != nil {
				continue
			}
			n++
			if p.Max != nil && n > *p.Max {
				n -= 2
			}
			if p.Min != nil && n < *p.Min {
				continue
			}
			nv = json.Number(strconv.FormatFloat(n, 'f', -1, 64))
		default:
			continue
		}
		if patch {
			return map[string]any{name: nv}, name
		}
		m[name] = nv
		return m, name
	}
	return m, ""
}

// withoutRequired is a copy of a body without its first required field
// that holds a plain value.
func withoutRequired(body any, s *openapi3.Schema) (any, string) {
	m, ok := body.(map[string]any)
	if !ok || s == nil {
		return body, ""
	}
	req := requiredOf(s)
	for _, name := range sortedKeys(m) {
		if !contains(req, name) || len(scenario.Names(m[name])) > 0 {
			continue
		}
		c := scenario.Clone(m).(map[string]any)
		delete(c, name)
		return c, name
	}
	return body, ""
}
