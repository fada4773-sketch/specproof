package apply

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Verify checks the written spec s, loaded the way apitest loads it,
// against the defaults before anything is saved:
//
//   - every key of the defaults matched something and was written
//   - every binding is in the spec, from the right producer, and its
//     pointer finds a value in the producer's example
//   - every extension is set on its operation with its value
//   - every example apply wrote fits its schema
//
// It returns the problems; with any, nothing must be saved.
func Verify(s *spec.Spec, defs *defaults.Defaults, res *Result) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	for _, n := range res.Notes {
		switch n.Code {
		case CodeDefaultUnused, CodeBindSkipped, CodeSharedParam:
			// a default that was not written
			add("%s %s: %s", n.Code, n.Where, n.Message)
		}
	}

	for _, f := range s.Findings {
		// curated named examples are never changed and reported on their own
		if f.Kind == spec.FindingExampleSchema && !strings.Contains(f.Where, ".examples.") {
			add("EXAMPLE_SCHEMA %s: %s", f.Where, f.Message)
		}
	}

	set, err := bind.Resolve(s)
	if err != nil {
		add("BIND_INVALID the written spec: %v", err)
	}
	for _, e := range defs.Bindings() {
		if set == nil {
			break
		}
		if msg := verifyBinding(s, set, e); msg != "" {
			add("BIND_UNVERIFIED %q: %s", e.Key, msg)
		}
	}

	for _, k := range defs.Keys() {
		opID, name, ok := strings.Cut(k, ".")
		if !ok || !strings.HasPrefix(name, "x-") {
			continue
		}
		e := defs.Plain(k)
		op := s.Op(opID)
		if e == nil || op == nil {
			continue // reported as unused
		}
		got, has := op.Op.Extensions[name]
		if !has {
			add("EXT_UNVERIFIED %q: %s has no %s in the written spec", k, opID, name)
			continue
		}
		if canonical(got) != canonical(e.Value) {
			add("EXT_UNVERIFIED %q: %s has %s = %s, want %s", k, opID, name, canonical(got), canonical(e.Value))
		}
	}
	return out
}

// verifyBinding checks one binding entry against the written spec.
func verifyBinding(s *spec.Spec, set *bind.Set, e *defaults.Entry) string {
	opID, param, _ := strings.Cut(e.Key, ".")
	op := s.Op(opID)
	if op == nil {
		return fmt.Sprintf("operation %q does not exist", opID)
	}
	i := slices.IndexFunc(op.Params, func(p *openapi3.Parameter) bool { return strings.EqualFold(p.Name, param) })
	if i < 0 {
		return fmt.Sprintf("%s has no parameter %q", opID, param)
	}
	p := op.Params[i]
	if p.In != openapi3.ParameterInPath {
		return "" // only path parameters are written as bindings
	}
	b := set.For(op, p)
	switch {
	case b == nil:
		return "the parameter is not bound in the written spec"
	case !b.Producer.Is(e.Bind.From):
		return fmt.Sprintf("the written spec binds it to %s, not to %s", b.Producer.ID, e.Bind.From)
	case e.Bind.Header != "":
		if !strings.EqualFold(b.Source.Header, e.Bind.Header) {
			return fmt.Sprintf("the written spec takes %s, not header %s", b.Source, e.Bind.Header)
		}
		return ""
	case b.Source.Pointer != e.Bind.Pointer || b.Source.FromRequest != e.Bind.Request:
		return fmt.Sprintf("the written spec takes %s, not %s", b.Source, e.Bind.Pointer)
	}
	ex, where := producerExampleIn(b.Producer, e.Bind.Request)
	if ex == nil {
		return "" // no example to check against; apitest checks it at run time
	}
	if _, ok := bind.Pointer(ex, e.Bind.Pointer); !ok {
		return fmt.Sprintf("%s has nothing at %s in its %s example; check the pointer", b.Producer.ID, e.Bind.Pointer, where)
	}
	return ""
}

// producerExampleIn returns the example of the producer's request body or
// lowest 2xx JSON response in a loaded spec: "example", or the first named
// example.
func producerExampleIn(op *spec.Operation, request bool) (any, string) {
	var content openapi3.Content
	where := "request"
	if request {
		if rb := op.Op.RequestBody; rb != nil && rb.Value != nil {
			content = rb.Value.Content
		}
	} else if op.Op.Responses != nil {
		var codes []string
		for code := range op.Op.Responses.Map() {
			if len(code) == 3 && code[0] == '2' {
				codes = append(codes, code)
			}
		}
		slices.Sort(codes)
		if len(codes) > 0 {
			where = codes[0] + " response"
			if r := op.Op.Responses.Value(codes[0]); r != nil && r.Value != nil {
				content = r.Value.Content
			}
		}
	}
	for mt, m := range content {
		if !spec.IsJSON(mt) {
			continue
		}
		if m.Example != nil {
			return spec.Normalize(m.Example), where
		}
		names := make([]string, 0, len(m.Examples))
		for n := range m.Examples {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			if ex := m.Examples[n]; ex != nil && ex.Value != nil {
				return spec.Normalize(ex.Value.Value), where
			}
		}
	}
	return nil, where
}

// canonical renders a value as JSON for comparison, numbers and maps of
// both YAML and JSON origin alike.
func canonical(v any) string {
	b, _ := json.Marshal(spec.Normalize(v))
	return string(b)
}
