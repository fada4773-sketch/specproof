// Package params resolves parameter values (FR-PARAM-01/02) and serializes
// them according to the OpenAPI style rules (FR-PARAM-07).
package params

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Rank is the source of a parameter value, in order of precedence.
type Rank int

// Ranks in order of precedence (see "How it works" in the README).
const (
	RankNone          Rank = iota
	RankBinding            // 1: value from an earlier response
	RankFixed              // 2: Config.Params
	RankNamedExample       // 3: named example with the case's name
	RankExample            // 4: example of the parameter
	RankSchemaExample      // 5: example/examples[0] of the schema
	RankDefault            // 6: default of the schema
	RankEnum               // 7: first enum value
)

func (r Rank) String() string {
	switch r {
	case RankBinding:
		return "Binding"
	case RankFixed:
		return "Config.Params"
	case RankNamedExample:
		return "named example"
	case RankExample:
		return "parameter example"
	case RankSchemaExample:
		return "schema example"
	case RankDefault:
		return "schema default"
	case RankEnum:
		return "enum"
	default:
		return "no source"
	}
}

// Inputs are the sources that depend on the run rather than on the spec.
type Inputs struct {
	CaseName string            // example name of the case, e.g. "valid-planet"
	Fixed    map[string]string // Config.Params
	OpID     string            // operation of the case, for "<operationId>.<name>" keys
	// Binding returns a bound value for a parameter, if any (rank 1).
	Binding func(p *openapi3.Parameter) (any, bool)
	// Override sets path parameters by name before any other source: the
	// unknown key of a not-found case.
	Override map[string]any
}

// Value is a resolved parameter value.
type Value struct {
	V    any
	Rank Rank
}

// Resolve determines the value of p. ok is false if no source provides a
// value, or if p is optional and only a schema-level source would (FR-PARAM-02).
func Resolve(p *openapi3.Parameter, in Inputs) (Value, bool) {
	v, ok := resolve(p, in)
	if !ok {
		return Value{}, false
	}
	if !p.Required && v.Rank > RankExample {
		return Value{}, false
	}
	return v, true
}

func resolve(p *openapi3.Parameter, in Inputs) (Value, bool) {
	if v, ok := in.Override[p.Name]; ok && p.In == openapi3.ParameterInPath {
		return Value{spec.Normalize(v), RankBinding}, true
	}
	if in.Binding != nil {
		if v, ok := in.Binding(p); ok {
			return Value{spec.Normalize(v), RankBinding}, true
		}
	}
	if v, ok := in.Fixed[in.OpID+"."+p.Name]; ok && in.OpID != "" {
		return Value{v, RankFixed}, true
	}
	if v, ok := in.Fixed[p.Name]; ok {
		return Value{v, RankFixed}, true
	}
	if in.CaseName != "" {
		if ex := p.Examples[in.CaseName]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
			return Value{spec.Normalize(ex.Value.Value), RankNamedExample}, true
		}
	}
	if p.Example != nil {
		return Value{spec.Normalize(p.Example), RankExample}, true
	}
	if p.Schema == nil || p.Schema.Value == nil {
		return Value{}, false
	}
	s := p.Schema.Value
	switch {
	case s.Example != nil:
		return Value{spec.Normalize(s.Example), RankSchemaExample}, true
	case len(s.Examples) > 0:
		return Value{spec.Normalize(s.Examples[0]), RankSchemaExample}, true
	case s.Default != nil:
		return Value{spec.Normalize(s.Default), RankDefault}, true
	case len(s.Enum) > 0:
		return Value{spec.Normalize(s.Enum[0]), RankEnum}, true
	}
	return Value{}, false
}

// Pair is one serialized query parameter or cookie.
type Pair struct {
	Key, Value string
}

// Path serializes a path parameter value (styles simple, label, matrix).
// The result is already escaped for use in a URL path.
func Path(p *openapi3.Parameter, v any) (string, error) {
	if s, ok, err := contentValue(p, v); ok || err != nil {
		return url.PathEscape(s), err
	}
	style := p.Style
	if style == "" {
		style = openapi3.SerializationSimple
	}
	explode := p.Explode != nil && *p.Explode
	esc := url.PathEscape
	switch style {
	case openapi3.SerializationSimple:
		return join(v, ",", "=", ",", explode, esc)
	case openapi3.SerializationLabel:
		sep := ","
		if explode {
			sep = "."
		}
		s, err := join(v, sep, "=", ",", explode, esc)
		return "." + s, err
	case openapi3.SerializationMatrix:
		return matrix(p.Name, v, explode, esc)
	default:
		return "", fmt.Errorf("parameter %q: style %q is not allowed for path parameters", p.Name, style)
	}
}

// Header serializes a header parameter value (style simple).
func Header(p *openapi3.Parameter, v any) (string, error) {
	if s, ok, err := contentValue(p, v); ok || err != nil {
		return s, err
	}
	explode := p.Explode != nil && *p.Explode
	return join(v, ",", "=", ",", explode, identity)
}

// Query serializes a query parameter into key/value pairs (styles form,
// spaceDelimited, pipeDelimited, deepObject). Values are not yet escaped.
func Query(p *openapi3.Parameter, v any) ([]Pair, error) {
	if s, ok, err := contentValue(p, v); ok || err != nil {
		return []Pair{{p.Name, s}}, err
	}
	style := p.Style
	if style == "" {
		style = openapi3.SerializationForm
	}
	explode := p.Explode == nil || *p.Explode // form defaults to explode=true
	switch style {
	case openapi3.SerializationForm:
		return form(p.Name, v, explode, ",")
	case openapi3.SerializationSpaceDelimited:
		return form(p.Name, v, explode, " ")
	case openapi3.SerializationPipeDelimited:
		return form(p.Name, v, explode, "|")
	case openapi3.SerializationDeepObject:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parameter %q: deepObject expects an object", p.Name)
		}
		var out []Pair
		for _, k := range sortedKeys(obj) {
			out = append(out, Pair{p.Name + "[" + k + "]", scalar(obj[k])})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("parameter %q: style %q is not allowed for query parameters", p.Name, style)
	}
}

// Cookie serializes a cookie parameter (style form).
func Cookie(p *openapi3.Parameter, v any) (Pair, error) {
	pairs, err := Query(&openapi3.Parameter{Name: p.Name, Style: openapi3.SerializationForm, Explode: boolPtr(false), Content: p.Content}, v)
	if err != nil || len(pairs) == 0 {
		return Pair{}, err
	}
	return pairs[0], nil
}

// EncodeQuery builds a query string from pairs, keeping their order.
func EncodeQuery(pairs []Pair) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.Key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	return b.String()
}

func form(name string, v any, explode bool, sep string) ([]Pair, error) {
	switch x := v.(type) {
	case []any:
		if explode {
			out := make([]Pair, 0, len(x))
			for _, e := range x {
				out = append(out, Pair{name, scalar(e)})
			}
			return out, nil
		}
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, scalar(e))
		}
		return []Pair{{name, strings.Join(parts, sep)}}, nil
	case map[string]any:
		keys := sortedKeys(x)
		if explode {
			out := make([]Pair, 0, len(keys))
			for _, k := range keys {
				out = append(out, Pair{k, scalar(x[k])})
			}
			return out, nil
		}
		parts := make([]string, 0, 2*len(keys))
		for _, k := range keys {
			parts = append(parts, k, scalar(x[k]))
		}
		return []Pair{{name, strings.Join(parts, sep)}}, nil
	default:
		return []Pair{{name, scalar(v)}}, nil
	}
}

func join(v any, sep, kv, pairSep string, explode bool, esc func(string) string) (string, error) {
	switch x := v.(type) {
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, esc(scalar(e)))
		}
		return strings.Join(parts, sep), nil
	case map[string]any:
		keys := sortedKeys(x)
		parts := make([]string, 0, 2*len(keys))
		for _, k := range keys {
			if explode {
				parts = append(parts, esc(k)+kv+esc(scalar(x[k])))
			} else {
				parts = append(parts, esc(k), esc(scalar(x[k])))
			}
		}
		if explode {
			return strings.Join(parts, sep), nil
		}
		return strings.Join(parts, pairSep), nil
	default:
		return esc(scalar(v)), nil
	}
}

func matrix(name string, v any, explode bool, esc func(string) string) (string, error) {
	switch x := v.(type) {
	case []any:
		if explode {
			var b strings.Builder
			for _, e := range x {
				b.WriteString(";" + name + "=" + esc(scalar(e)))
			}
			return b.String(), nil
		}
		s, err := join(x, ",", "=", ",", false, esc)
		return ";" + name + "=" + s, err
	case map[string]any:
		if explode {
			var b strings.Builder
			for _, k := range sortedKeys(x) {
				b.WriteString(";" + esc(k) + "=" + esc(scalar(x[k])))
			}
			return b.String(), nil
		}
		s, err := join(x, ",", "=", ",", false, esc)
		return ";" + name + "=" + s, err
	default:
		return ";" + name + "=" + esc(scalar(v)), nil
	}
}

// contentValue handles parameters defined with "content" instead of
// "schema": the value is serialized as JSON.
func contentValue(p *openapi3.Parameter, v any) (string, bool, error) {
	if len(p.Content) == 0 {
		return "", false, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", true, fmt.Errorf("Parameter %q: %w", p.Name, err)
	}
	return string(b), true, nil
}

// scalar formats a primitive value. Nested values are encoded as JSON.
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	default:
		return fmt.Sprint(x)
	}
}

// Scalar formats a primitive value the same way parameters are serialized.
func Scalar(v any) string { return scalar(v) }

func identity(s string) string { return s }

func boolPtr(b bool) *bool { return &b }

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
