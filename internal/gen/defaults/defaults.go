// Package defaults reads defaults.json: values that must be used wherever a
// field or parameter of that name appears, because they have to exist in
// the test environment. Keys are matched case-insensitively:
//
//	"PilotCode": "a"                          every field and parameter named PilotCode
//	"Garden.Name": "Berlin"                  field Name of DTO Garden
//	"#/components/schemas/Garden": {…}      the DTO Garden itself, merged
//	"listPilots.pageSize": 50                 parameter or body field of one operation
//	"/pilots/{id}": 7                         the generic path parameter of this path
//	"UpdatePilot.x-apitest-verify": false     an extension on the operation
//	"GetPilot.pilotCode": {"bind": "CreatePilot", "pointer": "/PilotCode"}
//	                                        where apitest takes the parameter from
//	"listDocks.zone": null                  no value yet: to be filled in
//	"$rejected": ["GetPilot.pilotCode"]          proposals "review" must not repeat
//	"$snapshot": {"Book": {"from": "GetBooks", "count": 3}}
//	                                        where the records of a resource
//	                                        are fetched with -base-url
//	"$model": {"Book": {"schemas": […], "keys": ["Id", "Code"]}}
//	                                        corrections of the resource model
//	"$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true}
//	                                        the apitest Config that decides
//	                                        the order of the cases
//	"$comment": "…"                         ignored
package defaults

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Entry is one key of the file.
type Entry struct {
	Key   string // as written in the file
	Value any    // the value; nil for bindings and sources
	// Bind is set for {"bind": "<operationId>", "pointer"|"header": …}.
	Bind *Bind
	// From is set for {"from": "GET /path", "pick": "/0/Code"}; such
	// entries are resolved by "apitest-gen discover".
	From *Source
	// Todo is set for null: the key is known, its value is still missing.
	Todo bool

	uses int
}

// Bind tells apitest where a parameter value comes from.
type Bind struct {
	From    string `json:"bind"`
	Pointer string `json:"pointer,omitempty"`
	Header  string `json:"header,omitempty"`
	// Request takes the value from the body the producer sent.
	Request bool `json:"request,omitempty"`
}

// Source is a read-only request that provides the value (discovery).
type Source struct {
	From string `json:"from"`
	Pick string `json:"pick"`
}

// Defaults is a loaded file.
type Defaults struct {
	Path string
	// Rejected are the keys of "$rejected": proposals of "apitest-gen
	// review" that were turned down and must not be proposed again.
	Rejected []string
	// Snapshot are the entries of "$snapshot" by resource name.
	Snapshot map[string]Snapshot
	// snapshotOrder are the names of Snapshot in file order.
	snapshotOrder []string
	// Model are the entries of "$model" by resource name.
	Model map[string]ModelFix
	// Run is "$apitest"; nil if the file has none.
	Run *Run

	entries map[string]*Entry // lower-case key → entry
	order   []string          // lower-case keys in file order
}

// Empty returns defaults without entries.
func Empty() *Defaults { return &Defaults{entries: map[string]*Entry{}} }

// Load reads a defaults file. A missing file gives empty defaults; a broken
// one is an error, because a silently missing value would produce examples
// that do not exist in the environment.
func Load(path string) (*Defaults, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		d := Empty()
		d.Path = path
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("defaults %s: %w", path, err)
	}
	d.Path = path
	return d, nil
}

// Parse reads the content of a defaults file.
func Parse(b []byte) (*Defaults, error) {
	d := Empty()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected a JSON object with key → value")
	}
	seen := map[string]string{}
	var problems []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("value of %q: %w", key, err)
		}
		if key == RejectedKey {
			var list []string
			if err := json.Unmarshal(raw, &list); err != nil {
				problems = append(problems, fmt.Sprintf("%q must be a list of keys", key))
			}
			d.Rejected = append(d.Rejected, list...)
			continue
		}
		if handled, err := d.special(key, raw); handled {
			if err != nil {
				problems = append(problems, err.Error())
			}
			continue
		}
		if strings.HasPrefix(key, "$") {
			continue
		}
		lower := strings.ToLower(key)
		if first, dup := seen[lower]; dup {
			problems = append(problems, fmt.Sprintf("%q and %q are the same key (keys ignore case)", first, key))
			continue
		}
		seen[lower] = key
		e, err := entry(key, raw)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		d.entries[lower] = e
		d.order = append(d.order, lower)
	}
	if err := closeObject(dec); err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return d, nil
}

func entry(key string, raw json.RawMessage) (*Entry, error) {
	e := &Entry{Key: key}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&e.Value); err != nil {
		return nil, fmt.Errorf("%q: %w", key, err)
	}
	if e.Value == nil {
		e.Todo = true
		return e, nil
	}
	obj, ok := e.Value.(map[string]any)
	if !ok {
		return e, nil
	}
	switch {
	case obj["bind"] != nil:
		b := &Bind{}
		if err := json.Unmarshal(raw, b); err != nil || b.From == "" {
			return nil, fmt.Errorf(`%q: a binding needs {"bind": "<operationId>", "pointer": "/field"} or "header"`, key)
		}
		if (b.Pointer == "") == (b.Header == "") {
			return nil, fmt.Errorf(`%q: set either "pointer" or "header"`, key)
		}
		if b.Pointer != "" && !strings.HasPrefix(b.Pointer, "/") {
			return nil, fmt.Errorf("%q: pointer %q must start with /", key, b.Pointer)
		}
		if !strings.Contains(key, ".") {
			return nil, fmt.Errorf(`%q: a binding needs a key "<operationId>.<parameter>"`, key)
		}
		e.Value, e.Bind = nil, b
	case obj["from"] != nil:
		src := &Source{}
		if err := json.Unmarshal(raw, src); err != nil || !strings.HasPrefix(src.From, "GET ") || src.Pick == "" {
			return nil, fmt.Errorf(`%q: a source needs {"from": "GET /path", "pick": "/0/field"}`, key)
		}
		e.Value, e.From = nil, src
	}
	return e, nil
}

// closeObject reads the closing brace of the top-level object and checks
// that nothing follows. Without the check, Go 1.26 accepts "{" as empty.
func closeObject(dec *json.Decoder) error {
	tok, err := dec.Token()
	if errors.Is(err, io.EOF) {
		return errors.New("unexpected end of the file: the object is not closed with }")
	}
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return fmt.Errorf("expected } at the end of the object, got %v", tok)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected content after the closing }")
	}
	return nil
}

// Len is the number of entries.
func (d *Defaults) Len() int { return len(d.order) }

func (d *Defaults) find(key string) *Entry {
	e := d.entries[strings.ToLower(key)]
	if e != nil && e.Value != nil {
		return e
	}
	return nil
}

// Use marks an entry as applied, for the usage report.
func (d *Defaults) Use(e *Entry) { e.uses++ }

// Field returns the value entry for a field or parameter: DTO-qualified
// first, then operation-qualified, then the plain name.
func (d *Defaults) Field(dto, operationID, name string) *Entry {
	for _, k := range []string{dto + "." + name, operationID + "." + name, name} {
		if strings.HasPrefix(k, ".") {
			continue
		}
		if e := d.find(k); e != nil {
			return e
		}
	}
	return nil
}

// Scoped returns only the operation-qualified entry for name.
func (d *Defaults) Scoped(operationID, name string) *Entry {
	if operationID == "" {
		return nil
	}
	return d.find(operationID + "." + name)
}

// Plain returns only the entry with exactly this key.
func (d *Defaults) Plain(key string) *Entry { return d.find(key) }

// Extension is "<operationId>.x-<name>": value.
type Extension struct {
	Entry *Entry
	Name  string // e.g. "x-apitest-verify"
}

// Extensions returns the extensions for an operation, in file order.
func (d *Defaults) Extensions(operationID string) []Extension {
	prefix := strings.ToLower(operationID) + ".x-"
	var out []Extension
	for _, k := range d.order {
		if strings.HasPrefix(k, prefix) {
			e := d.entries[k]
			out = append(out, Extension{Entry: e, Name: e.Key[len(operationID)+1:]})
		}
	}
	return out
}

// Binding returns the binding of a parameter of an operation.
func (d *Defaults) Binding(operationID, param string) *Entry {
	e := d.entries[strings.ToLower(operationID+"."+param)]
	if e != nil && e.Bind != nil {
		return e
	}
	return nil
}

// Todos returns the keys whose value is still null, in file order.
func (d *Defaults) Todos() []string {
	var out []string
	for _, k := range d.order {
		if e := d.entries[k]; e.Todo {
			out = append(out, e.Key)
		}
	}
	return out
}

// IsTodo reports whether key is in the file with the value null.
func (d *Defaults) IsTodo(key string) bool {
	e := d.entries[strings.ToLower(key)]
	return e != nil && e.Todo
}

// Bindings returns the binding entries, in file order.
func (d *Defaults) Bindings() []*Entry {
	var out []*Entry
	for _, k := range d.order {
		if e := d.entries[k]; e.Bind != nil {
			out = append(out, e)
		}
	}
	return out
}

// Sources returns the discovery entries, in file order.
func (d *Defaults) Sources() []*Entry {
	var out []*Entry
	for _, k := range d.order {
		if e := d.entries[k]; e.From != nil {
			out = append(out, e)
		}
	}
	return out
}

// Unused returns the value and binding entries that were never applied,
// usually a typo in a name or an operationId.
func (d *Defaults) Unused() []string {
	var out []string
	for _, k := range d.order {
		if e := d.entries[k]; e.uses == 0 && e.From == nil && !e.Todo {
			out = append(out, e.Key)
		}
	}
	return out
}

// Usage is how often an entry was applied.
type Usage struct {
	Key  string
	Uses int
}

// Usage returns how often each value and binding entry was applied, in
// file order.
func (d *Defaults) Usage() []Usage {
	var out []Usage
	for _, k := range d.order {
		if e := d.entries[k]; e.From == nil && !e.Todo {
			out = append(out, Usage{e.Key, e.uses})
		}
	}
	return out
}

// Keys returns all keys as written, in file order.
func (d *Defaults) Keys() []string {
	out := make([]string, 0, len(d.order))
	for _, k := range d.order {
		out = append(out, d.entries[k].Key)
	}
	return slices.Clip(out)
}

// Resolve sets the value of a source entry, e.g. after discovery.
func (d *Defaults) Resolve(e *Entry, v any) { e.Value = v }

// Merge adds the entries of other; an entry with the same key (ignoring
// case) replaces the existing one, so later files override earlier ones.
// A value replaces a source of the same key, which is how a resolved
// discovery file fills the sources of defaults.json.
func (d *Defaults) Merge(other *Defaults) {
	for _, k := range other.order {
		if _, ok := d.entries[k]; !ok {
			d.order = append(d.order, k)
		}
		d.entries[k] = other.entries[k]
	}
}

// LoadAll loads several files, comma-separated or as a list, and merges
// them in order.
func LoadAll(paths ...string) (*Defaults, error) {
	out := Empty()
	for _, p := range paths {
		for _, one := range strings.Split(p, ",") {
			if one = strings.TrimSpace(one); one == "" {
				continue
			}
			d, err := Load(one)
			if err != nil {
				return nil, err
			}
			out.Merge(d)
			out.Rejected = append(out.Rejected, d.Rejected...)
			out.mergeSpecial(d)
			if out.Path == "" {
				out.Path = one
			}
		}
	}
	return out, nil
}

// Params returns the scalar values by key, as apitest's Config.Params takes
// them: "name" or "<operationId>.<name>".
func (d *Defaults) Params() map[string]string {
	out := map[string]string{}
	for _, k := range d.order {
		e := d.entries[k]
		switch v := e.Value.(type) {
		case string:
			out[e.Key] = v
		case json.Number:
			out[e.Key] = v.String()
		case bool:
			out[e.Key] = strconv.FormatBool(v)
		}
	}
	return out
}
