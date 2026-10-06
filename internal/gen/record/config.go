// Package record writes the examples of a spec from a running instance:
// it reads the data with GET, selects one record per DTO, runs the writes
// against the instance (PUT, DELETE, POST of the same data) and writes what
// apitest will see in an empty environment that holds only the seed: the
// ids the sequences of that environment assign, and in the lists only the
// records that exist there at that point of apitest's run.
package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Config is the file "apitest-gen record" reads (defaults.json in the new
// format).
type Config struct {
	// Params are the values you set: "name" or "<operationId>.<name>".
	Params map[string]Param
	// Seed are the DTOs the empty environment holds before the test, one
	// record each, id 1.
	Seed []string
	// Select chooses the record of a DTO where a list returns several.
	Select map[string]Select
	// Bodies are bodies of writes and of POSTs that only read, by
	// operationId, laid over the body the run builds: for fields no record
	// holds, such as the filter of a table query.
	Bodies map[string]any
	// Tables are what the database knows about the table of a DTO: unique
	// indexes, soft delete, references. A table with an entry is written
	// through a copy: the run creates a copy with other unique values and
	// deletes the copy, never the record.
	Tables map[string]Table
	// Run is "$apitest", the Config apitest runs with.
	Run defaults.Run
	// Recorded is what the last run wrote ("$recorded"); nil before the
	// first one.
	Recorded *Recorded
}

// RecordedKey is the key the run keeps its state under in the file.
const RecordedKey = "$recorded"

// Recorded is the state a run keeps for the next one, in the defaults file.
type Recorded struct {
	Comment string `json:"$comment,omitempty"`
	// Operations are the fingerprints of the operations whose examples were
	// written: the next run only writes those of changed or new ones.
	Operations map[string]string `json:"operations"`
	// Created are the ids the empty environment assigns to the record each
	// POST creates.
	Created map[string]int `json:"created,omitempty"`
	// Seed are the records the empty environment must hold, by DTO, with
	// the ids it assigns.
	Seed map[string]any `json:"seed,omitempty"`
	// SeedOrder is the order to create the seed records in: a record after
	// the ones it refers to.
	SeedOrder []string `json:"seedOrder,omitempty"`
	// Params is a hash of "params": with other values every record is
	// selected again.
	Params string `json:"params,omitempty"`
	// Records are the records the run selected, in that order: the next
	// run takes them instead of reading and selecting them again.
	Records []StoredRecord `json:"records,omitempty"`
}

// StoredRecord is a selected record in "$recorded".
type StoredRecord struct {
	Table string `json:"table"`
	// From is where it was selected: "<operationId> <url>".
	From string `json:"from"`
	// Path is the POST path whose list it was selected from, "" for the
	// first record of the table.
	Path string `json:"path,omitempty"`
	// Keys are the fields the paths address it by.
	Keys []string `json:"keys,omitempty"`
	// More marks a further record of a seed DTO ("count").
	More bool `json:"more,omitempty"`
	// Ops are the fingerprints of the GETs whose answers Data holds: if
	// one changed, the record is read and selected again.
	Ops map[string]string `json:"ops"`
	// Select is a hash of the "select" entry of its DTO.
	Select string         `json:"select"`
	Data   map[string]any `json:"data"`
}

// Param is a value, the field of a fetched record that holds it
// ({"field": "Code"}), or a text put together from several fields
// ({"format": "{Planet.id}-{Dock.id}"}).
type Param struct {
	Value  any
	Field  string
	Format string
}

// errParam tells the forms a parameter can take.
var errParam = errors.New(`a parameter is a value, {"field": "<field of a record>"} or {"format": "{<DTO>.<field>}-{<field>}"}`)

// UnmarshalJSON reads a literal, {"field": "<name>"} or {"format": "<text>"}.
func (p *Param) UnmarshalJSON(b []byte) error {
	var ref struct {
		Field  string `json:"field"`
		Format string `json:"format"`
	}
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		if err := strict(b, &ref); err != nil || (ref.Field == "") == (ref.Format == "") {
			return errParam
		}
		if ref.Format != "" {
			if _, err := placeholders(ref.Format); err != nil {
				return err
			}
		}
		p.Field, p.Format = ref.Field, ref.Format
		return nil
	}
	return strict(b, &p.Value)
}

// placeholders returns the names in the braces of a format; it needs at
// least one and every brace closed.
func placeholders(format string) ([]string, error) {
	var names []string
	rest := format
	for {
		i := strings.IndexAny(rest, "{}")
		if i < 0 {
			break
		}
		if rest[i] == '}' {
			return nil, fmt.Errorf("format %q: \"}\" without \"{\"", format)
		}
		j := strings.IndexAny(rest[i+1:], "{}")
		if j < 0 || rest[i+1+j] == '{' {
			return nil, fmt.Errorf("format %q: \"{\" without \"}\"", format)
		}
		name := strings.TrimSpace(rest[i+1 : i+1+j])
		if name == "" {
			return nil, fmt.Errorf("format %q: empty \"{}\"; write a field: {<DTO>.<field>} or {<field>}", format)
		}
		names = append(names, name)
		rest = rest[i+2+j:]
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("format %q has no {<field>}; for a fixed value write the value itself", format)
	}
	return names, nil
}

// Select is what the record of a DTO must fulfil.
type Select struct {
	// From is the list the record is taken from (operationId of a GET);
	// without it the first list of the DTO the run reads.
	From string `json:"from,omitempty"`
	// Delete are the DELETEs that may remove the record so a POST can
	// create it again, where several could (by code or by name).
	Delete []string `json:"delete,omitempty"`
	// Equal fields must have exactly this value.
	Equal map[string]any `json:"equal,omitempty"`
	// Mandatory fields must be filled: not missing, null, "", [] or {}.
	Mandatory []string `json:"mandatory,omitempty"`
	// Details are GETs of the spec ("/Dock/{dockCode}/Config") that must
	// answer for the record with 2xx and pass their checks.
	Details map[string]Check `json:"details,omitempty"`
	// Count is how many records of a DTO of "seed" the empty environment
	// holds: the first ones of the list that pass, "*" every one.
	Count   Count  `json:"count,omitempty"`
	Comment string `json:"$comment,omitempty"`
}

// Count is the number of records of a seed DTO; 0 is not set (one), All
// is "*".
type Count int

// All is "count": "*": every element of the list that passes.
const All Count = -1

// UnmarshalJSON reads a number of at least 1 or "*".
func (c *Count) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == `"*"` {
		*c = All
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil || n < 1 {
		return fmt.Errorf(`"count" is a number of at least 1 or "*", not %s`, b)
	}
	*c = Count(n)
	return nil
}

// MarshalJSON writes "*" for All.
func (c Count) MarshalJSON() ([]byte, error) {
	if c == All {
		return []byte(`"*"`), nil
	}
	return json.Marshal(int(c))
}

// many reports a count other than one: the seed holds a list of records.
func (c Count) many() bool { return c != 0 && c != 1 }

// String is the count as the config writes it.
func (c Count) String() string {
	if c == All {
		return `"*"`
	}
	return fmt.Sprint(int(c))
}

// Check is what the answer of a detail must fulfil.
type Check struct {
	Equal     map[string]any `json:"equal,omitempty"`
	Mandatory []string       `json:"mandatory,omitempty"`
	Comment   string         `json:"$comment,omitempty"`
}

// Table is what the database knows about the table of a DTO.
type Table struct {
	// Name is the table in the database ("docks"), for the hints.
	Name string `json:"name,omitempty"`
	// SoftDelete: a DELETE only marks the row (deleted_at); it stays in
	// every index.
	SoftDelete bool `json:"softDelete,omitempty"`
	// Unique are the unique indexes: the fields of the DTO they cover.
	Unique []Unique `json:"unique,omitempty"`
	// Refs are the fields that refer to another table: field → reference.
	Refs    map[string]Ref `json:"refs,omitempty"`
	Comment string         `json:"$comment,omitempty"`
}

// Unique is one unique index.
type Unique struct {
	Name string `json:"name,omitempty"`
	// Fields are the fields of the DTO, as the API names them (planetId)
	// or as columns (planet_id).
	Fields []string `json:"fields"`
	// Where is the condition of a partial index ("deleted_at IS NULL").
	Where   string `json:"where,omitempty"`
	Comment string `json:"$comment,omitempty"`
}

// Ref is a reference to another table.
type Ref struct {
	// To is the DTO it refers to.
	To string `json:"to"`
	// OnDelete is the action of the foreign key: CASCADE, SET NULL, …
	OnDelete string `json:"onDelete,omitempty"`
	Comment  string `json:"$comment,omitempty"`
}

// cascade reports a reference whose rows a DELETE of the target removes.
func (r Ref) cascade() bool { return strings.EqualFold(strings.TrimSpace(r.OnDelete), "CASCADE") }

// ignoresDeleted reports a partial index that leaves out deleted rows.
func (u Unique) ignoresDeleted() bool {
	return strings.Contains(strings.ToLower(u.Where), "deleted")
}

// table returns the entry of "tables" for a table, nil without one.
func (c *Config) table(table string, n namer) *Table {
	for _, name := range sortedKeys(c.Tables) {
		if n.table(name) == table {
			t := c.Tables[name]
			return &t
		}
	}
	return nil
}

// old are the keys of the format of "apitest-gen apply".
var old = []string{defaults.SnapshotKey, defaults.ModelKey}

// Load reads the config; the file must exist.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist; record needs its \"params\", \"seed\" and \"$apitest\" (pass the file with -defaults)", path)
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads the config from JSON.
func Parse(b []byte) (*Config, error) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if rest := bytes.TrimSpace(b[dec.InputOffset():]); len(rest) > 0 {
		line := 1 + bytes.Count(b[:dec.InputOffset()], []byte("\n"))
		return nil, fmt.Errorf("text after the JSON object (line %d): %.40q; remove it", line, rest)
	}
	c := &Config{}
	for key, v := range raw {
		var err error
		switch {
		case key == "params":
			c.Params, err = parseParams(v)
		case key == "seed":
			err = strict(v, &c.Seed)
		case key == "select":
			c.Select, err = parseSelect(v)
		case key == "bodies":
			err = strict(v, &c.Bodies)
			for k := range c.Bodies {
				if strings.HasPrefix(k, "$") {
					delete(c.Bodies, k)
				}
			}
		case key == "tables":
			c.Tables, err = parseTables(v)
		case key == defaults.ApitestKey:
			if err = strict(v, &c.Run); err == nil {
				err = defaults.CheckRun(c.Run)
			}
		case key == SuggestionsKey: // written by the run, read by you
		case key == RecordedKey:
			c.Recorded = &Recorded{}
			err = strict(v, c.Recorded)
		case strings.HasPrefix(key, "$comment"):
		case contains(old, key):
			return nil, fmt.Errorf("%q belongs to the format of \"apitest-gen apply\"; record reads \"params\", \"seed\", \"select\", \"bodies\", \"tables\" and %q", key, defaults.ApitestKey)
		default:
			return nil, fmt.Errorf("unknown key %q; record reads \"params\", \"seed\", \"select\", \"bodies\", \"tables\" and %q", key, defaults.ApitestKey)
		}
		if err != nil {
			return nil, fmt.Errorf("%q: %w", key, err)
		}
	}
	for name, s := range c.Select {
		for path := range s.Details {
			if !strings.HasPrefix(path, "/") {
				return nil, fmt.Errorf("\"select\".%s.details: %q is no path; write the path of a GET of the spec", name, path)
			}
		}
	}
	return c, nil
}

// parseSelect reads "select"; keys starting with "$" are comments.
func parseSelect(b []byte) (map[string]Select, error) {
	var raw map[string]json.RawMessage
	if err := strict(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]Select{}
	for name, v := range raw {
		if strings.HasPrefix(name, "$") {
			continue
		}
		var fields map[string]json.RawMessage
		if err := strict(v, &fields); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for k := range fields {
			if strings.HasPrefix(k, "$") {
				delete(fields, k)
			}
		}
		clean, _ := json.Marshal(fields)
		var sel Select
		if err := strict(clean, &sel); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = sel
	}
	return out, nil
}

// parseTables reads "tables"; keys starting with "$" are comments.
func parseTables(b []byte) (map[string]Table, error) {
	var raw map[string]json.RawMessage
	if err := strict(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]Table{}
	for _, name := range sortedKeys(raw) {
		if strings.HasPrefix(name, "$") {
			continue
		}
		var t Table
		if err := strict(raw[name], &t); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for i, u := range t.Unique {
			if len(u.Fields) == 0 {
				return nil, fmt.Errorf("%s.unique[%d]: \"fields\" lists no field", name, i)
			}
		}
		for f, r := range t.Refs {
			if r.To == "" {
				return nil, fmt.Errorf("%s.refs.%s: \"to\" names no DTO", name, f)
			}
		}
		out[name] = t
	}
	return out, nil
}

// parseParams reads "params": "name": value, "<operationId>.<name>": value,
// or the parameters of one operation grouped, "<operationId>": {"<name>":
// value}. A value is a literal, {"field": "<field>"} or {"format":
// "<text with {<field>}>"}; keys starting with "$" are comments.
func parseParams(b []byte) (map[string]Param, error) {
	var raw map[string]json.RawMessage
	if err := strict(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]Param{}
	for _, key := range sortedKeys(raw) {
		if strings.HasPrefix(key, "$") {
			continue
		}
		v := raw[key]
		var group map[string]json.RawMessage
		if bytes.HasPrefix(bytes.TrimSpace(v), []byte("{")) && strict(v, &group) == nil {
			_, field := group["field"]
			_, format := group["format"]
			n := len(group)
			if param := (n == 1 && (field || format)) || (n == 2 && field && format); !param {
				if strings.Contains(key, ".") {
					opID, _, _ := strings.Cut(key, ".")
					return nil, fmt.Errorf("%q: %w; group parameters under the operationId alone: \"%s\": {\"<name>\": <value>}", key, errParam, opID)
				}
				for _, name := range sortedKeys(group) {
					if strings.HasPrefix(name, "$") {
						continue
					}
					var p Param
					if err := json.Unmarshal(group[name], &p); err != nil {
						return nil, fmt.Errorf("%q.%q: %w", key, name, err)
					}
					out[key+"."+name] = p
				}
				continue
			}
		}
		var p Param
		if err := json.Unmarshal(v, &p); err != nil {
			return nil, fmt.Errorf("%q: %w", key, err)
		}
		out[key] = p
	}
	return out, nil
}

// param returns the entry for a parameter of an operation: the one of the
// operation first, names ignore case.
func (c *Config) param(opID, name string) (Param, bool) {
	p, _, ok := c.paramEntry(opID, name)
	return p, ok
}

// paramEntry is param with the key of the entry ("GetShip.id", "planetCode").
func (c *Config) paramEntry(opID, name string) (Param, string, bool) {
	plain := ""
	for _, k := range sortedKeys(c.Params) {
		switch {
		case strings.EqualFold(k, opID+"."+name):
			return c.Params[k], k, true
		case strings.EqualFold(k, name):
			plain = k
		}
	}
	if plain != "" {
		return c.Params[plain], plain, true
	}
	return Param{}, "", false
}

// selection returns the entry of "select" for a table.
func (c *Config) selection(table string, n namer) Select {
	for name, s := range c.Select {
		if n.table(name) == table {
			return s
		}
	}
	return Select{}
}

// seeded reports whether a table is part of the seed.
func (c *Config) seeded(table string, n namer) bool {
	for _, name := range c.Seed {
		if n.table(name) == table {
			return true
		}
	}
	return false
}

func strict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	return dec.Decode(v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// check compares the config with the spec: every operation and parameter
// of "params" and every "from" of "select" must exist.
func (c *Config) check(s *spec.Spec, n namer) error {
	var errs []string
	has := func(op *spec.Operation, name string) bool {
		for _, p := range op.Params {
			if strings.EqualFold(p.Name, name) && (p.In == openapi3.ParameterInPath || p.In == openapi3.ParameterInQuery) {
				return true
			}
		}
		return false
	}
	for _, key := range sortedKeys(c.Params) {
		opID, name, scoped := strings.Cut(key, ".")
		if !scoped {
			found := false
			for _, op := range s.Ops {
				found = found || has(op, key)
			}
			if !found {
				errs = append(errs, fmt.Sprintf("\"params\".%s: no operation has a parameter %q", key, key))
			}
			continue
		}
		op := opByID(s, opID)
		switch {
		case op == nil:
			errs = append(errs, fmt.Sprintf("\"params\".%s: no operation %q in the spec", key, opID))
		case !has(op, name):
			var names []string
			for _, p := range op.Params {
				if p.In == openapi3.ParameterInPath || p.In == openapi3.ParameterInQuery {
					names = append(names, p.Name)
				}
			}
			errs = append(errs, fmt.Sprintf("\"params\".%s: %s has no parameter %q (it has: %s)", key, op.ID, name, strings.Join(names, ", ")))
		}
	}
	for _, name := range sortedKeys(c.Select) {
		if c.Select[name].Count != 0 && !c.seeded(n.table(name), n) {
			errs = append(errs, fmt.Sprintf("\"select\".%s.count: %s is not in \"seed\"; \"count\" says how many records of a seed DTO the empty environment holds", name, name))
		}
		for _, id := range c.Select[name].Delete {
			if op := opByID(s, id); op == nil || op.Method != "DELETE" {
				errs = append(errs, fmt.Sprintf("\"select\".%s.delete: no DELETE %q in the spec", name, id))
			}
		}
		from := c.Select[name].From
		if from == "" {
			continue
		}
		op := opByID(s, from)
		if op == nil || op.Method != "GET" {
			errs = append(errs, fmt.Sprintf("\"select\".%s.from: no GET %q in the spec", name, from))
			continue
		}
		items, _, list := listShape(responseSchema(op, 0))
		if t := n.table(name); !list || n.of(items) != t {
			errs = append(errs, fmt.Sprintf("\"select\".%s.from: %s does not return a list of %s; name the GET that lists them (e.g. %s)", name, op.ID, name, listsOf(s, n, t)))
		}
	}
	for _, id := range sortedKeys(c.Bodies) {
		if op := opByID(s, id); op == nil || requestSchema(op) == nil {
			errs = append(errs, fmt.Sprintf("\"bodies\".%s: no operation %q with a JSON body in the spec", id, id))
		}
	}
	known := map[string]bool{}
	if s.Doc != nil && s.Doc.Components != nil {
		for name := range s.Doc.Components.Schemas {
			known[n.table(name)] = true
		}
	}
	for _, name := range sortedKeys(c.Tables) {
		if !known[n.table(name)] {
			errs = append(errs, fmt.Sprintf("\"tables\".%s: no DTO of the spec belongs to it", name))
		}
		for _, f := range sortedKeys(c.Tables[name].Refs) {
			if to := c.Tables[name].Refs[f].To; !known[n.table(to)] {
				errs = append(errs, fmt.Sprintf("\"tables\".%s.refs.%s: no DTO %q in the spec", name, f, to))
			}
		}
	}
	errs = append(errs, c.fillEqual()...)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// fillEqual replaces a value "{name}" in the "equal" of "select" and of its
// "details" by the value "params" sets for that parameter, so a value is
// set once: "equal": {"Planet.planetCode": "{planetCode}"}. It returns
// the placeholders without such a value.
func (c *Config) fillEqual() []string {
	var errs []string
	fill := func(where string, eq map[string]any) {
		for _, f := range sortedKeys(eq) {
			str, ok := eq[f].(string)
			if !ok || len(str) < 3 || str[0] != '{' || str[len(str)-1] != '}' || strings.ContainsAny(str[1:len(str)-1], "{}") {
				continue
			}
			name := strings.TrimSpace(str[1 : len(str)-1])
			p, ok := c.param("", name)
			if !ok || p.Value == nil {
				errs = append(errs, fmt.Sprintf("%s.equal.%s: %q needs a value of \"params\".%s (a plain value, not a field or format)", where, f, str, name))
				continue
			}
			eq[f] = p.Value
		}
	}
	for _, name := range sortedKeys(c.Select) {
		sel := c.Select[name]
		fill(fmt.Sprintf("\"select\".%s", name), sel.Equal)
		for _, path := range sortedKeys(sel.Details) {
			fill(fmt.Sprintf("\"select\".%s.details.%s", name, path), sel.Details[path].Equal)
		}
	}
	return errs
}

// opByID finds an operation, ignoring case.
func opByID(s *spec.Spec, id string) *spec.Operation {
	for _, op := range s.Ops {
		if strings.EqualFold(op.ID, id) {
			return op
		}
	}
	return nil
}

// listsOf names the GETs that list a table.
func listsOf(s *spec.Spec, n namer, t string) string {
	var out []string
	for _, op := range s.Ops {
		if op.Method != "GET" {
			continue
		}
		if items, _, ok := listShape(responseSchema(op, 0)); ok && n.of(items) == t {
			out = append(out, op.ID)
		}
	}
	if len(out) == 0 {
		return "none lists them"
	}
	return strings.Join(out, ", ")
}

// SaveRecorded writes "$recorded" into the defaults file and leaves the
// rest of the file as it is.
func SaveRecorded(path string, r *Recorded) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	r.Comment = "written by apitest-gen record: what the next run needs; do not edit"
	val, err := json.MarshalIndent(r, "  ", "  ")
	if err != nil {
		return err
	}
	out, err := setKey(b, RecordedKey, val)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

// deleteKey removes a key of the top-level object and its comma; a file
// without the key stays as it is.
func deleteKey(b []byte, key string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("the file is no JSON object")
	}
	for dec.More() {
		start := int(dec.InputOffset())
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if t != key {
			continue
		}
		end := int(dec.InputOffset())
		if !bytes.HasPrefix(bytes.TrimLeft(b[start:end], " \t\r\n"), []byte(",")) {
			// the first member: its comma follows it
			rest := bytes.TrimLeft(b[end:], " \t\r\n")
			if bytes.HasPrefix(rest, []byte(",")) {
				end = len(b) - len(rest) + 1
			}
		}
		return append(append([]byte{}, b[:start]...), b[end:]...), nil
	}
	return b, nil
}

// setKey replaces the value of a key of the top-level object in place, or
// adds the key at its end.
func setKey(b []byte, key string, val []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("the file is no JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if t == key {
			end := int(dec.InputOffset())
			start := end - len(raw)
			return append(append(append([]byte{}, b[:start]...), val...), b[end:]...), nil
		}
	}
	end := bytes.LastIndexByte(b, '}')
	head := bytes.TrimRight(b[:end], " \t\r\n")
	sep := ",\n"
	if bytes.HasSuffix(head, []byte("{")) {
		sep = "\n"
	}
	var out []byte
	out = append(out, head...)
	out = append(out, sep+"  "+`"`+key+`": `...)
	out = append(out, val...)
	out = append(out, "\n"...)
	out = append(out, b[end:]...)
	return out, nil
}
