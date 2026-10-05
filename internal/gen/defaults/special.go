package defaults

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Keys of the special entries, which configure the generator instead of
// setting a value.
const (
	SnapshotKey = "$snapshot" // where the records of a resource are fetched
	ModelKey    = "$model"    // corrections of the resource model
	ApitestKey  = "$apitest"  // the apitest Config that orders the cases
)

// Snapshot tells where the records of a resource come from: the operation
// whose response provides them and how many elements of a list are taken.
type Snapshot struct {
	// From is the request, "/BookPreset/Tier/A1?isbn=978", or the
	// operationId of a GET of the resource.
	From  string `json:"from"`
	Count int    `json:"count"` // records taken from the list; 0 means 1
	// Comment is free text; review writes the path template there.
	Comment string `json:"$comment,omitempty"`
	// Validation selects the elements that become records.
	Validation *Validation `json:"validation,omitempty"`
	// Mandatory is the place of "mandatoryFields" before "validation"; it
	// still works and adds to Validation.MandatoryFields.
	Mandatory []string `json:"mandatoryFields,omitempty"`
	// Seed are fields of the chosen elements ("code", "BookDetail.Tier")
	// whose values fill the placeholders of the "from" of later entries:
	// {code} takes the last seeded "code", {Book.code} the one of Book. One
	// set per record; an element without a value in a seed field is not
	// chosen.
	Seed []string `json:"seed,omitempty"`
}

// Validation is what an element of the list must fulfil to become a record.
// The list is searched until "count" elements fulfil all of it.
type Validation struct {
	// MandatoryFields must have a value, not null and not empty:
	// "Book.Author" or "ReadDTO.BookDetail.Author".
	MandatoryFields []string `json:"mandatoryFields"`
	// EqualFields must have exactly this value: {"Book.Author": "tom"}.
	EqualFields map[string]any `json:"equalFields"`
	// FollowingDetails are requests that must answer for the element, with
	// its values for the placeholders: "/book/{id}/details". Their answers
	// become the examples of those operations.
	FollowingDetails []string `json:"followingDetails"`
}

// Records is the number of records, at least 1.
func (s Snapshot) Records() int { return max(s.Count, 1) }

// Checks returns the validation of s, with "mandatoryFields" of the old
// place added.
func (s Snapshot) Checks() Validation {
	var v Validation
	if s.Validation != nil {
		v = *s.Validation
	}
	v.MandatoryFields = append(append([]string(nil), v.MandatoryFields...), s.Mandatory...)
	return v
}

// ModelFix corrects the detected model of one resource.
type ModelFix struct {
	// Schemas are the DTOs of the resource, the one its GETs return first.
	Schemas []string `json:"schemas,omitempty"`
	// Keys are fields that identify a record, in addition to the ones the
	// paths use; updates never change them.
	Keys []string `json:"keys,omitempty"`
	// Params map a path parameter to the field it holds, for a parameter
	// whose name is no field ({dockNumber} → Registry).
	Params map[string]string `json:"params,omitempty"`
}

// Run holds the fields of apitest's Config that decide which cases run and
// in which order. They must be the same as in the test, because the
// examples follow the state of the data at the position of each case.
type Run struct {
	MethodOrder  []string `json:"MethodOrder,omitempty"`
	DeleteLast   bool     `json:"DeleteLast,omitempty"`
	Tags         []string `json:"Tags,omitempty"`
	IncludeOps   []string `json:"IncludeOps,omitempty"`
	ExcludeOps   []string `json:"ExcludeOps,omitempty"`
	IgnoreFields []string `json:"IgnoreFields,omitempty"`
}

// special parses the special keys; handled reports whether key is one.
func (d *Defaults) special(key string, raw json.RawMessage) (handled bool, err error) {
	switch key {
	case SnapshotKey:
		m := map[string]Snapshot{}
		if err := strict(raw, &m); err != nil {
			return true, fmt.Errorf(`%q must map a resource to {"from": "<operationId>", "count": 1}: %w`, key, err)
		}
		for name, s := range m {
			if s.Count < 0 {
				return true, fmt.Errorf("%q: count of %q must be 1 or more", key, name)
			}
		}
		order, err := objectKeys(raw)
		if err != nil {
			return true, fmt.Errorf("%q: %w", key, err)
		}
		d.Snapshot = m
		d.snapshotOrder = order
	case ModelKey:
		m := map[string]ModelFix{}
		if err := strict(raw, &m); err != nil {
			return true, fmt.Errorf(`%q must map a resource to {"schemas": [...], "keys": [...], "params": {...}}: %w`, key, err)
		}
		d.Model = m
	case ApitestKey:
		r := &Run{}
		if err := strict(raw, r); err != nil {
			return true, fmt.Errorf(`%q takes the apitest Config fields MethodOrder, DeleteLast, Tags, IncludeOps, ExcludeOps and IgnoreFields: %w`, key, err)
		}
		if err := checkMethodOrder(r.MethodOrder); err != nil {
			return true, fmt.Errorf("%q: %w", key, err)
		}
		d.Run = r
	default:
		return false, nil
	}
	return true, nil
}

// strict decodes raw into v and rejects unknown fields, so a typo does not
// silently change the order of the cases.
func strict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	return dec.Decode(v)
}

// CheckRun applies the rules of apitest's Config to "$apitest".
func CheckRun(r Run) error { return checkMethodOrder(r.MethodOrder) }

// checkMethodOrder applies the rules of apitest's Config.MethodOrder.
func checkMethodOrder(order []string) error {
	seen := map[string]bool{}
	for i, m := range order {
		up := strings.ToUpper(strings.TrimSpace(m))
		switch {
		case seen[up]:
			return fmt.Errorf("MethodOrder lists %s twice", up)
		case up == "DELETE" && i != len(order)-1:
			return fmt.Errorf("MethodOrder: DELETE must be the last entry")
		case !slices.Contains([]string{"POST", "GET", "PUT", "PATCH", "DELETE"}, up):
			return fmt.Errorf("MethodOrder: unknown method %q (valid: POST, GET, PUT, PATCH, DELETE last)", m)
		}
		seen[up] = true
	}
	return nil
}

// SnapshotFor returns the "$snapshot" entry of a resource, ignoring case.
func (d *Defaults) SnapshotFor(resource string) (Snapshot, bool) {
	for name, s := range d.Snapshot {
		if strings.EqualFold(name, resource) {
			return s, true
		}
	}
	return Snapshot{}, false
}

// SnapshotOrder returns the resources of "$snapshot" in the order of the
// file; the snapshot runs them in this order. Entries set without a file
// follow by name.
func (d *Defaults) SnapshotOrder() []string {
	var out []string
	for _, name := range d.snapshotOrder {
		if _, ok := d.Snapshot[name]; ok {
			out = append(out, name)
		}
	}
	var rest []string
	for name := range d.Snapshot {
		if !slices.Contains(out, name) {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// objectKeys returns the keys of a JSON object in file order.
func objectKeys(raw json.RawMessage) ([]string, error) {
	pairs, err := rawPairs(raw)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(pairs))
	for i, p := range pairs {
		keys[i] = p.key
	}
	return keys, nil
}

// RunConfig returns "$apitest", or the apitest defaults if there is none.
func (d *Defaults) RunConfig() Run {
	if d.Run == nil {
		return Run{}
	}
	return *d.Run
}

// mergeSpecial takes the special entries of a later file: a resource
// replaces the one with the same name, "$apitest" replaces the whole entry.
func (d *Defaults) mergeSpecial(other *Defaults) {
	// a resource keeps its place; new ones follow in the order of the later
	// file
	for _, name := range other.snapshotOrder {
		if d.Snapshot == nil {
			d.Snapshot = map[string]Snapshot{}
		}
		if _, ok := d.Snapshot[name]; !ok {
			d.snapshotOrder = append(d.snapshotOrder, name)
		}
		d.Snapshot[name] = other.Snapshot[name]
	}
	for name, f := range other.Model {
		if d.Model == nil {
			d.Model = map[string]ModelFix{}
		}
		d.Model[name] = f
	}
	if other.Run != nil {
		d.Run = other.Run
	}
}
