package contract

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Overrides is the overrides file (contract-overrides.yaml): only what the
// generator cannot know. It survives every run of the generator.
type Overrides struct {
	// Values fix values by "Resource.field", "DTO.field", "operationId.param"
	// or a plain field or parameter name. A list gives one value per record
	// (the first record the first value).
	Values map[string]any
	// Links correct references: "Resource.field": "none" (the field refers
	// to nothing) or the name of the resource it refers to.
	Links map[string]string
	// Steps change generated steps by id; the keys given replace those of
	// the step.
	Steps map[string]map[string]any
	// Add are steps the generator cannot derive, each with "after": the id
	// of the step it follows.
	Add []map[string]any
	// Skip are ids of generated steps that must not run.
	Skip []string
	// Operations hold, by "METHOD /path", what an operation without a
	// generated step needs; init proposes an entry for each such operation.
	Operations map[string]*OpOverride
}

// OpOverride is an entry under "operations": values for the parameters of
// one operation and the status it answers with. A value that is null is
// still open and counts as not given.
type OpOverride struct {
	Path, Query, Header map[string]any
	// Status is the expected status; 0 while open. A DELETE, or a create the
	// generator does not use for records, only gets a step with it.
	Status int
	// Skip leaves the operation without a step; init proposes nothing for it.
	Skip bool
}

var overrideKeys = []string{"values", "links", "steps", "add", "skip", "operations"}

var operationKeys = []string{"path", "query", "header", "status", "skip"}

// LoadOverrides reads the overrides file; a missing file gives none.
func LoadOverrides(path string) (*Overrides, error) {
	o := &Overrides{Values: map[string]any{}, Links: map[string]string{}, Steps: map[string]map[string]any{}, Operations: map[string]*OpOverride{}}
	if path == "" {
		return o, nil
	}
	v, err := scenario.ReadYAML(path)
	if errors.Is(err, os.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return nil, err
	}
	if v == nil {
		return o, nil
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: the overrides are a mapping (%s)", path, strings.Join(overrideKeys, ", "))
	}
	where := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
	}
	for k := range top {
		if !slices.Contains(overrideKeys, k) {
			return nil, where("unknown key %q (known: %s)", k, strings.Join(overrideKeys, ", "))
		}
	}
	if m, ok := top["values"].(map[string]any); ok {
		o.Values = m
	} else if top["values"] != nil {
		return nil, where("values must map names to values")
	}
	if m, ok := top["links"].(map[string]any); ok {
		for k, x := range m {
			o.Links[k] = scenario.Text(x)
		}
	} else if top["links"] != nil {
		return nil, where("links must map Resource.field to a resource or none")
	}
	if m, ok := top["steps"].(map[string]any); ok {
		for id, x := range m {
			sm, ok := x.(map[string]any)
			if !ok {
				return nil, where("steps.%s must be a mapping of the keys to change", id)
			}
			if _, ok := sm["id"]; ok {
				return nil, where("steps.%s: the id cannot be changed; skip the step and add a new one", id)
			}
			o.Steps[id] = sm
		}
	} else if top["steps"] != nil {
		return nil, where("steps must map step ids to the keys to change")
	}
	if l, ok := top["add"].([]any); ok {
		for i, x := range l {
			sm, ok := x.(map[string]any)
			if !ok {
				return nil, where("add[%d] must be a step", i)
			}
			if scenario.Text(sm["after"]) == "" {
				return nil, where("add[%d] needs \"after\": the id of the step it follows", i)
			}
			o.Add = append(o.Add, sm)
		}
	} else if top["add"] != nil {
		return nil, where("add must be a list of steps")
	}
	if l, ok := top["skip"].([]any); ok {
		for _, x := range l {
			o.Skip = append(o.Skip, scenario.Text(x))
		}
	} else if top["skip"] != nil {
		return nil, where("skip must be a list of step ids")
	}
	if m, ok := top["operations"].(map[string]any); ok {
		for call, x := range m {
			key, ok := callKey(call)
			if !ok {
				return nil, where("operations: %q is no operation (\"GET /ships/{shipId}\")", call)
			}
			oo, err := parseOpOverride(x)
			if err != nil {
				return nil, where("operations.%s: %v", call, err)
			}
			o.Operations[key] = oo
		}
	} else if top["operations"] != nil {
		return nil, where("operations must map \"METHOD /path\" to its values")
	}
	return o, nil
}

// callKey normalizes "get  /x" to "GET /x"; false if it is no call.
func callKey(call string) (string, bool) {
	method, path, ok := strings.Cut(strings.TrimSpace(call), " ")
	path = strings.TrimSpace(path)
	if !ok || method == "" || !strings.HasPrefix(path, "/") {
		return "", false
	}
	return strings.ToUpper(method) + " " + path, true
}

func parseOpOverride(x any) (*OpOverride, error) {
	oo := &OpOverride{}
	if x == nil {
		return oo, nil
	}
	m, ok := x.(map[string]any)
	if !ok {
		return nil, errors.New("must be a mapping (" + strings.Join(operationKeys, ", ") + ")")
	}
	for k, v := range m {
		switch k {
		case "path", "query", "header":
			if v == nil {
				continue
			}
			pm, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s must map parameter names to values", k)
			}
			switch k {
			case "path":
				oo.Path = pm
			case "query":
				oo.Query = pm
			default:
				oo.Header = pm
			}
		case "status":
			if v == nil {
				continue
			}
			n, err := strconv.Atoi(scenario.Text(v))
			if err != nil || n < 100 || n > 599 {
				return nil, fmt.Errorf("status must be an HTTP status, not %q", scenario.Text(v))
			}
			oo.Status = n
		case "skip":
			b, ok := v.(bool)
			if !ok && v != nil {
				return nil, errors.New("skip must be true or false")
			}
			oo.Skip = b
		default:
			return nil, fmt.Errorf("unknown key %q (known: %s)", k, strings.Join(operationKeys, ", "))
		}
	}
	return oo, nil
}

// operation returns the entry of an operation, or nil.
func (o *Overrides) operation(op *spec.Operation) *OpOverride {
	if o == nil {
		return nil
	}
	return o.Operations[op.Method+" "+op.Path]
}

// param returns the value an entry gives a parameter; null is no value.
func (oo *OpOverride) param(p *openapi3.Parameter) (any, bool) {
	if oo == nil {
		return nil, false
	}
	var m map[string]any
	switch p.In {
	case openapi3.ParameterInPath:
		m = oo.Path
	case openapi3.ParameterInQuery:
		m = oo.Query
	case openapi3.ParameterInHeader:
		m = oo.Header
	}
	for k, v := range m {
		if v != nil && (k == p.Name || (p.In == openapi3.ParameterInHeader && strings.EqualFold(k, p.Name))) {
			return v, true
		}
	}
	return nil, false
}

// apply changes, adds and skips steps of a generated scenario.
func (o *Overrides) apply(sc *scenario.Scenario) error {
	var errs []string
	for _, id := range sortedKeys(o.Steps) {
		st := sc.Step(id)
		if st == nil {
			errs = append(errs, fmt.Sprintf("steps.%s: no generated step has this id", id))
			continue
		}
		m := st.Map()
		for k, v := range o.Steps[id] {
			if v == nil {
				delete(m, k)
				continue
			}
			m[k] = v
		}
		ns, err := scenario.ParseStep(m)
		if err != nil {
			errs = append(errs, fmt.Sprintf("steps.%s: %v", id, err))
			continue
		}
		ns.Why, ns.Notes = "override", st.Notes
		*st = *ns
	}
	for i, add := range o.Add {
		after := scenario.Text(add["after"])
		m := map[string]any{}
		for k, v := range add {
			if k != "after" {
				m[k] = v
			}
		}
		ns, err := scenario.ParseStep(m)
		if err != nil {
			errs = append(errs, fmt.Sprintf("add[%d]: %v", i, err))
			continue
		}
		ns.Why = "override: add"
		idx := slices.IndexFunc(sc.Steps, func(s *scenario.Step) bool { return s.ID == after })
		if idx < 0 {
			errs = append(errs, fmt.Sprintf("add[%d]: after %q names no step", i, after))
			continue
		}
		sc.Steps = slices.Insert(sc.Steps, idx+1, ns)
	}
	for _, id := range o.Skip {
		idx := slices.IndexFunc(sc.Steps, func(s *scenario.Step) bool { return s.ID == id })
		if idx < 0 {
			errs = append(errs, fmt.Sprintf("skip: no step has the id %q", id))
			continue
		}
		sc.Steps = slices.Delete(sc.Steps, idx, idx+1)
	}
	if len(errs) > 0 {
		return fmt.Errorf("contract-overrides: %s", strings.Join(errs, "\n  "))
	}
	if err := sc.Check(); err != nil {
		return fmt.Errorf("the scenario with the overrides does not hold together (a skipped step may save a value a later step needs):\n  %s", strings.ReplaceAll(err.Error(), "\n", "\n  "))
	}
	return nil
}

// value returns the override for a field or parameter of record idx (from
// 0), trying the keys in order.
func (o *Overrides) value(idx int, keys ...string) (any, bool) {
	if o == nil {
		return nil, false
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		v, ok := o.Values[k]
		if !ok {
			continue
		}
		if l, isList := v.([]any); isList {
			if len(l) == 0 {
				continue
			}
			return l[idx%len(l)], true
		}
		return v, true
	}
	return nil, false
}

// link returns the override of a reference: "none", a resource name, or "".
func (o *Overrides) link(resource, field string) string {
	if o == nil {
		return ""
	}
	for k, v := range o.Links {
		if strings.EqualFold(k, resource+"."+field) {
			return v
		}
	}
	return ""
}
