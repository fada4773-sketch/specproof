// Package dict is the global dictionary of apitest-gen: one node per DTO
// field and per parameter, with the constraints from the spec and the
// example value. The spec is the source of the constraints; the values are
// write-protected, so a value once generated or set by hand stays until it
// no longer fits its schema.
package dict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Version is the format version written to new files.
const Version = 1

// Dict is the content of global-dict.json.
type Dict struct {
	Version int `json:"version"`
	// Schemas holds one node per DTO in components.schemas.
	Schemas map[string]*Node `json:"schemas"`
	// Parameters holds one node per parameter, keyed "<in>.<name>",
	// e.g. "path.planetCode" or "header.X-Tenant-Id".
	Parameters map[string]*Node `json:"parameters"`
	// Paths holds the values of generic path parameters such as {id},
	// keyed by path ("/pilots/{id}"), because one value per parameter name
	// would give every resource the same id.
	Paths map[string]any `json:"paths,omitempty"`
	// Records are the records the examples of each resource show, as the
	// test database starts with them (apitest-gen writes them on every run).
	Records map[string][]map[string]any `json:"records,omitempty"`
}

// Node describes a DTO, a field or a parameter. Constraints come from the
// spec; Value is the example. A field that refers to another DTO has Ref
// and no own properties: its value is built from that DTO.
type Node struct {
	Type      string   `json:"type,omitempty"`
	Format    string   `json:"format,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Enum      []any    `json:"enum,omitempty"`
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	MinLength uint64   `json:"minLength,omitempty"`
	MaxLength *uint64  `json:"maxLength,omitempty"`
	MinItems  uint64   `json:"minItems,omitempty"`
	MaxItems  *uint64  `json:"maxItems,omitempty"`
	Nullable  bool     `json:"nullable,omitempty"`
	ReadOnly  bool     `json:"readOnly,omitempty"`
	WriteOnly bool     `json:"writeOnly,omitempty"`
	Required  bool     `json:"required,omitempty"`

	Ref        string           `json:"ref,omitempty"`
	Properties map[string]*Node `json:"properties,omitempty"`
	Items      *Node            `json:"items,omitempty"`

	// Value is the example of a leaf: a field or parameter of a primitive
	// type, an array of primitives or a free object. nil means none yet.
	Value any `json:"value,omitempty"`
}

// Leaf reports whether the node carries a value itself instead of being
// built from properties, items or another DTO.
func (n *Node) Leaf() bool {
	return n.Ref == "" && len(n.Properties) == 0 && (n.Items == nil || n.Items.Leaf())
}

// New returns an empty dictionary.
func New() *Dict {
	return &Dict{Version: Version, Schemas: map[string]*Node{}, Parameters: map[string]*Node{}, Paths: map[string]any{}}
}

// Load reads a dictionary. A missing file is not an error: exists is false
// and an empty dictionary is returned.
func Load(path string) (d *Dict, exists bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	d = New()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(d); err != nil {
		return nil, true, fmt.Errorf("dictionary %s: %w", path, err)
	}
	if d.Version > Version {
		return nil, true, fmt.Errorf("dictionary %s has version %d; this apitest-gen supports up to %d", path, d.Version, Version)
	}
	if d.Schemas == nil {
		d.Schemas = map[string]*Node{}
	}
	if d.Parameters == nil {
		d.Parameters = map[string]*Node{}
	}
	if d.Paths == nil {
		d.Paths = map[string]any{}
	}
	return d, true, nil
}

// Save writes the dictionary atomically, with sorted keys and two-space
// indentation, so unchanged content gives an unchanged file.
func (d *Dict) Save(path string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dict-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
