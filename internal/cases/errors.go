package cases

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/regexgen"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Names of the generated error cases.
const (
	NotFoundExample = "not-found"
	ConflictExample = "conflict"
)

// notFoundExt sets the value a not-found case sends at a path parameter.
const notFoundExt = "x-apitest-not-found"

// generatedErrors derives the error cases apitest provokes itself from the
// first regular case of op, which provides body and the other parameters:
//
//   - not-found: op has a path parameter and documents 404; the last path
//     parameter, the key of the addressed record, gets a value no record
//     has (x-apitest-not-found of the parameter, else one derived from its
//     schema), every other value is the one of the regular case;
//   - conflict: a POST with a body that documents 409; the regular body is
//     sent a second time after the regular case.
//
// A named example of the same name in the spec wins. The answer is only
// checked against the schema of the documented response.
func generatedErrors(op *spec.Operation, regular []*Case) []*Case {
	if op.Op.Responses == nil {
		return nil
	}
	var tmpl *Case
	have := map[string]bool{}
	for _, c := range regular {
		have[c.Example] = true
		if tmpl == nil && c.Kind == Positive {
			tmpl = c
		}
	}
	if tmpl == nil {
		return nil
	}
	var out []*Case
	add := func(kind Kind, name, code string, ref *openapi3.ResponseRef) *Case {
		c := &Case{
			Name:         Name(group(op), op.ID, name),
			Group:        group(op),
			Example:      name,
			ParamExample: tmpl.Example,
			Op:           op,
			Kind:         kind,
			Rank:         5,
			Order:        tmpl.Order,
			MediaType:    tmpl.MediaType,
			Body:         tmpl.Body,
			HasBody:      tmpl.HasBody,
			Skip:         tmpl.Skip,
			NotBuildable: tmpl.NotBuildable,
			Compare:      "schema", // error bodies are only checked against the schema
		}
		c.Expect = expectation(code, ref.Value)
		c.Ignore = append(c.Ignore, tmpl.Ignore...)
		out = append(out, c)
		return c
	}
	if r404 := op.Op.Responses.Value("404"); r404 != nil && !have[NotFoundExample] {
		if p := keyParam(op); p != nil {
			c := add(NotFound, NotFoundExample, "404", r404)
			c.NotFoundParam = p.Name
			v, why := notFoundValue(p)
			if why != "" {
				c.setNotBuildable(why)
			}
			c.NotFoundValue = v
		}
	}
	if r409 := op.Op.Responses.Value("409"); r409 != nil && op.Method == http.MethodPost && tmpl.HasBody && !have[ConflictExample] {
		add(Conflict, ConflictExample, "409", r409)
	}
	return out
}

// keyParam is the last path parameter of op: the key of the record the path
// addresses ("/docks/{dockId}/berths/{berthId}" → berthId).
func keyParam(op *spec.Operation) *openapi3.Parameter {
	segs := strings.Split(strings.Trim(op.Path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		name, ok := strings.CutPrefix(segs[i], "{")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, "}")
		for _, p := range op.Params {
			if p.In == openapi3.ParameterInPath && p.Name == name {
				return p
			}
		}
	}
	return nil
}

// notFoundValue is a key no record has: x-apitest-not-found of the
// parameter, else a value derived from its schema that fits it. why says
// why there is none.
func notFoundValue(p *openapi3.Parameter) (v any, why string) {
	if x, ok := p.Extensions[notFoundExt]; ok {
		return spec.Normalize(x), ""
	}
	if p.Schema == nil || p.Schema.Value == nil {
		return "apitest-not-found", ""
	}
	s := p.Schema.Value
	if len(s.Enum) > 0 {
		return nil, fmt.Sprintf("not-found: {%s} is an enum, every value may exist; set %s at the parameter", p.Name, notFoundExt)
	}
	switch {
	case s.Type.Is("integer") || s.Type.Is("number"):
		n := 999999999.0
		if s.Max != nil && n > *s.Max {
			n = *s.Max
		}
		if s.Min != nil && n < *s.Min {
			n = *s.Min
		}
		v = json.Number(fmt.Sprint(int64(n)))
	case s.Format == "uuid":
		v = "00000000-0000-4000-8000-000000000404"
	case s.Pattern != "":
		lim := regexgen.Limits{Min: int(s.MinLength), Max: -1}
		if s.MaxLength != nil {
			lim.Max = int(*s.MaxLength)
		}
		str, ok := regexgen.Highest(s.Pattern, lim)
		if !ok {
			str, ok = regexgen.Generate(s.Pattern, lim, rand.New(rand.NewPCG(404, 404)), 50)
		}
		if !ok {
			return nil, fmt.Sprintf("not-found: no value for {%s} matches its pattern %q; set %s at the parameter", p.Name, s.Pattern, notFoundExt)
		}
		v = str
	default:
		str := "apitest-not-found"
		if s.MaxLength != nil && uint64(len(str)) > *s.MaxLength {
			str = str[:*s.MaxLength]
		}
		for uint64(len(str)) < s.MinLength {
			str += "x"
		}
		v = str
	}
	if errs := spec.NewValidator().Validate(s, v, spec.ModePlain); len(errs) > 0 {
		return nil, fmt.Sprintf("not-found: no value for {%s} that fits its schema (%s); set %s at the parameter", p.Name, errs[0].String(), notFoundExt)
	}
	return v, ""
}
