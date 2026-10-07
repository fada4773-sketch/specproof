// Package yamldoc edits an OpenAPI file as a YAML node tree, so comments
// and the order of keys survive. JSON files are read the same way and
// written back as JSON with their key order.
package yamldoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is a loaded file.
type Doc struct {
	Root *yaml.Node // the top-level mapping
	doc  yaml.Node
	json bool
}

// Load reads a YAML or JSON file.
func Load(path string) (*Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// New returns an empty YAML document; comment, if set, heads the file.
func New(comment string) *Doc {
	d := &Doc{}
	d.Root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	d.doc = yaml.Node{Kind: yaml.DocumentNode, HeadComment: comment, Content: []*yaml.Node{d.Root}}
	return d
}

// Parse reads YAML or JSON content.
func Parse(b []byte) (*Doc, error) {
	d := &Doc{json: bytes.HasPrefix(bytes.TrimSpace(b), []byte("{"))}
	if err := yaml.Unmarshal(b, &d.doc); err != nil {
		return nil, err
	}
	if len(d.doc.Content) == 0 || d.doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the file is not a mapping")
	}
	d.Root = d.doc.Content[0]
	return d, nil
}

// Bytes renders the document: YAML with two-space indentation, or JSON
// with the original key order.
func (d *Doc) Bytes() ([]byte, error) {
	if d.json {
		var buf bytes.Buffer
		if err := writeJSON(&buf, d.Root, ""); err != nil {
			return nil, err
		}
		buf.WriteByte('\n')
		return buf.Bytes(), nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&d.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return simpleLongKeys(buf.Bytes()), nil
}

// longKey matches the explicit key form yaml.v3 uses for keys longer than
// 128 characters: "? key" followed by ": value" at the same indentation.
var longKey = regexp.MustCompile(`(?m)^( *)\? (\S[^\n]*)\n( *): (.*)$`)

// simpleLongKeys turns "? key\n: value" back into "key:" with the value in
// the usual place, so long OpenAPI paths keep their normal form.
func simpleLongKeys(b []byte) []byte {
	return longKey.ReplaceAllFunc(b, func(m []byte) []byte {
		g := longKey.FindSubmatch(m)
		indent, key, indent2, rest := string(g[1]), string(g[2]), string(g[3]), string(g[4])
		if indent != indent2 {
			return m
		}
		// a nested mapping or sequence starts on the next line, a scalar
		// stays on the line of the key
		if strings.HasPrefix(rest, "- ") || (strings.Contains(rest, ":") && !strings.HasPrefix(rest, "'") && !strings.HasPrefix(rest, `"`)) {
			return []byte(indent + key + ":\n" + indent + "  " + rest)
		}
		return []byte(indent + key + ": " + rest)
	})
}

// Save writes the document atomically.
func (d *Doc) Save(path string) error {
	b, err := d.Bytes()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spec-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp.Name(), info.Mode().Perm())
	}
	return os.Rename(tmp.Name(), path)
}

// Get returns the value of key in mapping n, or nil.
func Get(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// Keys returns the keys of mapping n in document order.
func Keys(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		keys = append(keys, n.Content[i].Value)
	}
	return keys
}

// Path follows keys from n; numeric keys index sequences.
func Path(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		if n == nil {
			return nil
		}
		if n.Kind == yaml.SequenceNode {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
			continue
		}
		n = Get(n, k)
	}
	return n
}

// Ref returns the $ref of a mapping, or "".
func Ref(n *yaml.Node) string {
	if r := Get(n, "$ref"); r != nil {
		return r.Value
	}
	return ""
}

// Resolve follows local $refs ("#/components/...") from n until a node
// without $ref. External references return an error, because their target
// lives in another file.
func (d *Doc) Resolve(n *yaml.Node) (*yaml.Node, error) {
	for range 32 {
		ref := Ref(n)
		if ref == "" {
			return n, nil
		}
		if !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("external reference %q", ref)
		}
		var keys []string
		for _, k := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			keys = append(keys, strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~"))
		}
		next := Path(d.Root, keys...)
		if next == nil {
			return nil, fmt.Errorf("reference %q points to nothing", ref)
		}
		n = next
	}
	return nil, errors.New("reference chain too long")
}

// Set sets key in mapping n to value, replacing an existing value in place
// (its comments stay) or appending the key. It never writes next to a $ref.
func Set(n *yaml.Node, key string, value any) error {
	if n == nil || n.Kind != yaml.MappingNode {
		return errors.New("not a mapping")
	}
	if Ref(n) != "" {
		return errors.New("refusing to write next to a $ref")
	}
	v, err := Node(value)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			old := n.Content[i+1]
			v.HeadComment, v.LineComment, v.FootComment = old.HeadComment, old.LineComment, old.FootComment
			n.Content[i+1] = v
			return nil
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return nil
}

// SetNode sets key in mapping n to the node v, like Set: an existing value
// is replaced in place and keeps its comments. It never writes next to a
// $ref.
func SetNode(n *yaml.Node, key string, v *yaml.Node) error {
	if n == nil || n.Kind != yaml.MappingNode {
		return errors.New("not a mapping")
	}
	if Ref(n) != "" {
		return errors.New("refusing to write next to a $ref")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			old := n.Content[i+1]
			v.HeadComment, v.LineComment, v.FootComment = old.HeadComment, old.LineComment, old.FootComment
			n.Content[i+1] = v
			return nil
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return nil
}

// Delete removes key from mapping n; it reports whether the key was there.
func Delete(n *yaml.Node, key string) bool {
	if n == nil || n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Node converts a Go value (from JSON decoding) into a YAML node. Objects
// get sorted keys, so the output is stable.
func Node(value any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(plain(value)); err != nil {
		return nil, err
	}
	return &n, nil
}

// plain replaces json.Number, which YAML would write as a quoted string,
// by int64 or float64.
func plain(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, c := range x {
			out[k] = plain(c)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = plain(c)
		}
		return out
	}
	return v
}

// Decode converts a YAML node into a JSON-like Go value with json.Number
// for numbers, the canonical form of apitest's validator.
func Decode(n *yaml.Node) (any, error) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, n, ""); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(&buf)
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// writeJSON renders a node as JSON, keeping the order of mapping keys.
func writeJSON(buf *bytes.Buffer, n *yaml.Node, indent string) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			buf.WriteString("null")
			return nil
		}
		return writeJSON(buf, n.Content[0], indent)
	case yaml.AliasNode:
		return writeJSON(buf, n.Alias, indent)
	case yaml.MappingNode:
		if len(n.Content) == 0 {
			buf.WriteString("{}")
			return nil
		}
		buf.WriteString("{\n")
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, _ := json.Marshal(n.Content[i].Value)
			buf.WriteString(indent + "  ")
			buf.Write(k)
			buf.WriteString(": ")
			if err := writeJSON(buf, n.Content[i+1], indent+"  "); err != nil {
				return err
			}
			if i+2 < len(n.Content) {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "}")
	case yaml.SequenceNode:
		if len(n.Content) == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteString("[\n")
		for i, c := range n.Content {
			buf.WriteString(indent + "  ")
			if err := writeJSON(buf, c, indent+"  "); err != nil {
				return err
			}
			if i+1 < len(n.Content) {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "]")
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return err
		}
		if n.Tag == "!!str" || n.Tag == "!!timestamp" || n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
			v = n.Value
		}
		b, err := json.Marshal(v)
		if err != nil {
			// e.g. a YAML timestamp: keep the text
			b, _ = json.Marshal(n.Value)
		}
		buf.Write(b)
	default:
		return fmt.Errorf("unsupported YAML node kind %d", n.Kind)
	}
	return nil
}
