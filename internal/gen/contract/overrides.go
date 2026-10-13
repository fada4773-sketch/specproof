package contract

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/scenario"
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
}

var overrideKeys = []string{"values", "links", "steps", "add", "skip"}

// LoadOverrides reads the overrides file; a missing file gives none.
func LoadOverrides(path string) (*Overrides, error) {
	o := &Overrides{Values: map[string]any{}, Links: map[string]string{}, Steps: map[string]map[string]any{}}
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
	return o, nil
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
