package record

import (
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// generatedMark is the comment at the x-apitest-compare: schema record
// writes for a generated answer; only an extension with this comment is
// removed again once the entry has a real answer.
const generatedMark = "apitest-gen record: generated answer"

// ignored writes an entry with status ignore. It is not sent, but apitest
// still runs its case, so the spec gets examples that fit the schema: the
// values of the entry and its stored answer where they fit, else values
// generated from the schema. A generated answer cannot match the
// instance, so apitest compares that response with the schema only
// (x-apitest-compare: schema). The record file stays as it is.
func (w *writer) ignored(st *Step, r *request, named bool, v *spec.Validator) {
	op := st.Op
	var gen, failed []string
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath && p.In != openapi3.ParameterInQuery {
			continue
		}
		values, nodes := r.path, r.pathNodes
		if p.In == openapi3.ParameterInQuery {
			values, nodes = r.query, r.queryNodes
		}
		cur, has := values[p.Name]
		if has && len(names(nodes[p.Name])) == 0 && paramFits(v, p, cur) {
			continue
		}
		if !has && p.In == openapi3.ParameterInQuery && !p.Required {
			continue
		}
		delete(values, p.Name)
		delete(nodes, p.Name)
		if p.Schema == nil || p.Schema.Value == nil {
			failed = append(failed, p.In+" "+p.Name+" (no schema)")
			continue
		}
		val, msg := w.generate(p.Schema.Value, "param."+op.ID+"."+p.Name, p.Name, spec.ModePlain, v)
		if msg != "" {
			failed = append(failed, p.In+" "+p.Name+" ("+msg+")")
			continue
		}
		values[p.Name], nodes[p.Name] = val, valueNode(val)
		gen = append(gen, p.In+" "+p.Name)
	}
	if m := requestMedia(op); m != nil && m.Schema != nil && m.Schema.Value != nil {
		required := op.Op.RequestBody.Value.Required
		fits := r.hasBody && len(names(r.bodyNode)) == 0 && firstViolation(v, m.Schema.Value, r.body, spec.ModeRequest) == ""
		if !fits && (r.hasBody || required) {
			r.body, r.hasBody, r.bodyNode = nil, false, nil
			if val, msg := w.generate(m.Schema.Value, "body."+op.ID, "", spec.ModeRequest, v); msg != "" {
				failed = append(failed, "body ("+msg+")")
			} else {
				n := valueNode(val)
				compact(n)
				r.body, r.hasBody, r.bodyNode = val, true, n
				gen = append(gen, "body")
			}
		}
	}
	resp := st.Response
	if resp != nil && responseFits(v, st) != "" {
		resp = nil
	}
	code := ""
	if resp == nil {
		resp, code = w.answer(st, v, &gen, &failed)
	}
	w.step(st, r, resp, named, v)
	if code != "" {
		w.compareSchema(st, code)
	}
	switch {
	case len(gen) > 0 && len(failed) > 0:
		w.res.note(CodeGenerated, st.where(), "status ignore: not sent; generated from the schema: %s; not generated: %s", strings.Join(gen, ", "), strings.Join(failed, ", "))
	case len(gen) > 0:
		w.res.note(CodeGenerated, st.where(), "status ignore: not sent; generated from the schema: %s", strings.Join(gen, ", "))
	case len(failed) > 0:
		w.res.note(CodeGenerated, st.where(), "status ignore: not sent; not generated: %s; set the values in the entry", strings.Join(failed, ", "))
	}
}

// answer is the answer written for an ignored entry without a stored one
// that fits: the lowest documented 2xx with a generated body (an example
// the spec holds there may belong to another request). code is the
// response whose body was generated, "" if none was.
func (w *writer) answer(st *Step, v *spec.Validator, gen, failed *[]string) (*Response, string) {
	op := st.Op
	code := lowestSuccess(op)
	if code == "" {
		return nil, ""
	}
	status, err := strconv.Atoi(code)
	if err != nil {
		status = 200 // "2XX"
	}
	resp := &Response{Status: status}
	m := responseMedia(op, code)
	if m == nil || m.Schema == nil || m.Schema.Value == nil {
		return resp, ""
	}
	val, msg := w.generate(m.Schema.Value, "response."+op.ID+"."+code, "", spec.ModeResponse, v)
	if msg != "" {
		*failed = append(*failed, "response "+code+" ("+msg+")")
		return resp, ""
	}
	n := valueNode(val)
	compact(n)
	resp.Body = n
	*gen = append(*gen, "response "+code+" (apitest checks it against the schema only)")
	return resp, code
}

// generate builds a value for a schema that fits it: a request leaves out
// readOnly fields, a response writeOnly ones. msg says why there is none.
func (w *writer) generate(s *openapi3.Schema, path, name string, mode spec.Mode, v *spec.Validator) (any, string) {
	r := value.Generate(s, value.Context{Seed: w.seed, Path: "record." + path, Name: name})
	if !r.OK {
		return nil, "cannot generate a value: " + r.Reason
	}
	val := fitMode(spec.Normalize(r.Value), s, mode)
	if mode == spec.ModePlain {
		val = coerce(val, s)
	}
	if msg := firstViolation(v, s, val, mode); msg != "" {
		return nil, "the generated value violates the schema: " + msg
	}
	return val, ""
}

// fitMode removes the fields a request (readOnly) or a response
// (writeOnly) does not have.
func fitMode(v any, s *openapi3.Schema, mode spec.Mode) any {
	switch mode {
	case spec.ModeRequest:
		return strip(v, s, func(p *openapi3.Schema) bool { return p.ReadOnly }, 0)
	case spec.ModeResponse:
		return strip(v, s, func(p *openapi3.Schema) bool { return p.WriteOnly }, 0)
	}
	return v
}

// paramFits reports whether a value fits the schema of a parameter.
func paramFits(v *spec.Validator, p *openapi3.Parameter, val any) bool {
	if p.Schema == nil || p.Schema.Value == nil {
		return true
	}
	return firstViolation(v, p.Schema.Value, val, spec.ModePlain) == ""
}

// compareSchema sets x-apitest-compare: schema at a response whose example
// was generated; a response other operations share gets a copy first.
func (w *writer) compareSchema(st *Step, code string) {
	r := w.ownedResponse(st, code, "x-apitest-compare")
	if r == nil {
		return
	}
	if n := yamldoc.Get(r, "x-apitest-compare"); n != nil && n.Value == "schema" {
		return
	}
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "schema", LineComment: generatedMark}
	if yamldoc.SetNode(r, "x-apitest-compare", n) == nil {
		w.changed = true
	}
}

// uncompareSchema removes the x-apitest-compare: schema record wrote for a
// generated answer, now that the entry has a real one.
func (w *writer) uncompareSchema(op *spec.Operation, code string) {
	r := yamldoc.Get(yamldoc.Get(w.operation(op), "responses"), code)
	if yamldoc.Ref(r) != "" {
		return
	}
	if n := yamldoc.Get(r, "x-apitest-compare"); n != nil && strings.Contains(n.LineComment, generatedMark) && yamldoc.Delete(r, "x-apitest-compare") {
		w.changed = true
	}
}

// ownedResponse is the response node of a step's operation, made its own
// copy if other operations share it; nil if that is not possible.
func (w *writer) ownedResponse(st *Step, code, ext string) *yaml.Node {
	responses := yamldoc.Get(w.operation(st.Op), "responses")
	if yamldoc.Ref(yamldoc.Get(responses, code)) != "" && !w.inline(responses, code) {
		w.res.problem(CodeShared, st.where(), "the response %s cannot get its own copy for %s", code, ext)
		return nil
	}
	return yamldoc.Get(responses, code)
}
