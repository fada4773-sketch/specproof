// Package scenario holds the files of the scenario mode: the settings of a
// run, the scenario (the ordered steps a run sends), and the snapshot (the
// answers of a run: the golden snapshot of an integration test,
// the pre and post snapshots of a regression check), with the diff of two
// snapshots.
//
// The files are YAML. The package reads and writes them with the YAML
// library kin-openapi already brings into the library (oasdiff/yaml3), so
// apitest needs no further dependency. Values are kept in apitest's
// canonical form: numbers as json.Number, strings stay strings even where
// YAML would read a time stamp.
package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	yaml "github.com/oasdiff/yaml3"
)

// decodeYAML parses a YAML document into plain values: maps, lists,
// json.Number, string, bool and nil.
func decodeYAML(b []byte) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return nil, nil
	}
	return plain(&doc)
}

func plain(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return plain(n.Content[0])
	case yaml.AliasNode:
		return plain(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: a key must be a plain value", k.Line)
			}
			v, err := plain(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[k.Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		l := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := plain(c)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		return l, nil
	case yaml.ScalarNode:
		if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return n.Value, nil
		}
		switch n.ShortTag() {
		case "!!int", "!!float":
			if json.Valid([]byte(n.Value)) {
				return json.Number(n.Value), nil
			}
			return n.Value, nil
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return nil, err
			}
			return b, nil
		case "!!null":
			return nil, nil
		}
		return n.Value, nil
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

// into converts plain values into a struct through JSON, keeping numbers
// as json.Number.
func into(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(out)
}

// node converts a plain value into a YAML node with sorted keys; short
// objects and lists of plain values are written on one line.
func node(v any) *yaml.Node {
	switch x := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case map[string]any:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			m.Content = append(m.Content, str(k), node(x[k]))
		}
		flow(m)
		return m
	case []any:
		s := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, c := range x {
			s.Content = append(s.Content, node(c))
		}
		flow(s)
		return s
	case []string:
		s := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, c := range x {
			s.Content = append(s.Content, str(c))
		}
		flow(s)
		return s
	case map[string]string:
		m := map[string]any{}
		for k, s := range x {
			m[k] = s
		}
		return node(m)
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(x.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: x.String()}
	case string:
		return str(x)
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(x)}
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(x)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(x)}
	case float64:
		return node(json.Number(fmt.Sprint(x)))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return str(fmt.Sprint(v))
	}
	var p any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&p) != nil {
		return str(string(b))
	}
	return node(p)
}

// str is a string node; the encoder quotes it where YAML would read
// another type (a number, a time stamp, "true").
func str(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

// flow writes short collections of plain values on one line.
func flow(n *yaml.Node) {
	width := 0
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || strings.Contains(c.Value, "\n") {
			return
		}
		width += len(c.Value) + 3
	}
	if width <= 80 {
		n.Style = yaml.FlowStyle
	}
}

// mapping builds a mapping node from key/value pairs in the given order;
// nil values are left out.
func mapping(pairs ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(pairs); i += 2 {
		k := pairs[i].(string)
		var v *yaml.Node
		switch x := pairs[i+1].(type) {
		case nil:
			continue
		case *yaml.Node:
			if x == nil {
				continue
			}
			v = x
		default:
			v = node(x)
		}
		m.Content = append(m.Content, str(k), v)
	}
	return m
}

// encode writes a document with a head comment.
func encode(comment string, root *yaml.Node) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode, HeadComment: comment, Content: []*yaml.Node{root}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadYAML reads a YAML file into plain values (maps, lists, json.Number,
// string, bool, nil).
func ReadYAML(path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := decodeYAML(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// EncodeValue writes plain values as YAML; head heads the file as a
// comment.
func EncodeValue(head string, v any) ([]byte, error) {
	return encode(head, node(v))
}
