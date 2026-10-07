package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// jsonNode converts JSON into a YAML node, keeping the order of the keys
// the instance sent.
func jsonNode(raw []byte) (*yaml.Node, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	n, err := tokenNode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return n, nil
}

func tokenNode(dec *json.Decoder) (*yaml.Node, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := tokenNode(dec)
				if err != nil {
					return nil, err
				}
				m.Content = append(m.Content, scalarNode(fmt.Sprint(k)), v)
			}
			_, err := dec.Token()
			return m, err
		case '[':
			s := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for dec.More() {
				v, err := tokenNode(dec)
				if err != nil {
					return nil, err
				}
				s.Content = append(s.Content, v)
			}
			_, err := dec.Token()
			return s, err
		}
		return nil, fmt.Errorf("unexpected %v", x)
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(x.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: x.String()}, nil
	case string:
		return scalarNode(x), nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(x)}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected token %v", t)
}

// valueNode converts a value (from JSON decoding) into a YAML node; the keys
// of objects are sorted.
func valueNode(v any) *yaml.Node {
	n, err := yamldoc.Node(v)
	if err != nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	return n
}

// decode converts a node into a value in apitest's canonical form (numbers
// as json.Number).
func decode(n *yaml.Node) any {
	if n == nil {
		return nil
	}
	v, err := yamldoc.Decode(n)
	if err != nil {
		return nil
	}
	return v
}

// compact writes short objects and lists of plain values on one line, so
// a record file stays short: { id: 1, name: Nord }.
func compact(n *yaml.Node) {
	if n == nil {
		return
	}
	for _, c := range n.Content {
		compact(c)
	}
	if n.Kind != yaml.MappingNode && n.Kind != yaml.SequenceNode {
		return
	}
	width := 0
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || strings.Contains(c.Value, "\n") {
			return
		}
		width += len(c.Value) + 3
	}
	if width <= 72 {
		n.Style = yaml.FlowStyle
	}
}

// placeholder matches "{{name}}" in a value.
var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)

// whole matches a value that is one placeholder and nothing else.
var whole = regexp.MustCompile(`^\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}$`)

// names are the placeholders a node uses.
func names(n *yaml.Node) []string {
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind == yaml.ScalarNode {
			for _, m := range placeholder.FindAllStringSubmatch(n.Value, -1) {
				out = append(out, m[1])
			}
		}
		for i, c := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 {
				continue // keys are names, never placeholders
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

// fill returns a copy of a node with its placeholders replaced by the
// values of vars: "{{shipId}}" alone takes the value with its type, a
// placeholder inside a text is written as text. missing lists the
// placeholders vars has no value for; they stay as they are.
func fill(n *yaml.Node, vars map[string]any) (out *yaml.Node, missing []string) {
	if n == nil {
		return nil, nil
	}
	if n.Kind == yaml.ScalarNode {
		if m := whole.FindStringSubmatch(n.Value); m != nil {
			v, ok := vars[m[1]]
			if !ok {
				return n, []string{m[1]}
			}
			return valueNode(v), nil
		}
		if !placeholder.MatchString(n.Value) {
			return n, nil
		}
		c := *n
		c.Value = placeholder.ReplaceAllStringFunc(n.Value, func(s string) string {
			name := placeholder.FindStringSubmatch(s)[1]
			v, ok := vars[name]
			if !ok {
				missing = append(missing, name)
				return s
			}
			return params.Scalar(v)
		})
		c.Tag, c.Style = "!!str", 0
		return &c, missing
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			c.Content[i] = child
			continue
		}
		v, miss := fill(child, vars)
		c.Content[i] = v
		missing = append(missing, miss...)
	}
	return &c, missing
}

// mapping returns the keys of a mapping node and their nodes, in order.
func mapping(n *yaml.Node) ([]string, map[string]*yaml.Node) {
	out := map[string]*yaml.Node{}
	var keys []string
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		keys = append(keys, n.Content[i].Value)
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return keys, out
}

// text is a value as compact JSON.
func text(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(b.String())
}

// clip shortens a text for the console.
func clip(s string) string {
	if len(s) > 600 {
		return s[:600] + fmt.Sprintf("… (%d bytes)", len(s))
	}
	return s
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

// listOf is the list in an answer: the answer itself if it is a list, else
// its one field that is a list; nil if there is none.
func listOf(body *yaml.Node) *yaml.Node {
	if body == nil {
		return nil
	}
	if body.Kind == yaml.SequenceNode {
		return body
	}
	var found *yaml.Node
	if body.Kind == yaml.MappingNode {
		for i := 1; i < len(body.Content); i += 2 {
			if body.Content[i].Kind == yaml.SequenceNode {
				if found != nil {
					return nil // two lists: which one is meant is unclear
				}
				found = body.Content[i]
			}
		}
	}
	return found
}

// filterList returns a copy of an answer whose list keeps only the elements
// whose fields have the values of want: a field name (any case) or a JSON
// pointer into the element, compared as text, so 7 matches "7". ok is false
// if the answer holds no list.
func filterList(body *yaml.Node, want map[string]any) (*yaml.Node, bool) {
	out := clone(body)
	list := listOf(out)
	if list == nil {
		return body, false
	}
	var kept []*yaml.Node
	for _, el := range list.Content {
		if elementMatches(decode(el), want) {
			kept = append(kept, el)
		}
	}
	list.Content = kept
	if len(kept) == 0 {
		list.Style = yaml.FlowStyle // "[]"
	}
	return out, true
}

func elementMatches(el any, want map[string]any) bool {
	for k, w := range want {
		var v any
		var ok bool
		if strings.HasPrefix(k, "/") {
			v, ok = bind.Pointer(el, k)
		} else if m, isMap := el.(map[string]any); isMap {
			if v, ok = m[k]; !ok {
				for f, fv := range m {
					if strings.EqualFold(f, k) {
						v, ok = fv, true
					}
				}
			}
		}
		if !ok || params.Scalar(v) != params.Scalar(w) {
			return false
		}
	}
	return true
}
