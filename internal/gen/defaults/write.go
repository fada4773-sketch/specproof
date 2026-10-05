package defaults

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Special keys.
const (
	RejectedKey = "$rejected" // proposals that were turned down
	ReviewKey   = "$review"   // written by "apitest-gen review", replaced on every run
)

// Pair is a key with its value, for Update.
type Pair struct {
	Key   string
	Value any // nil is written as null: a value still to be filled in
}

// Ordered is a JSON object that keeps the order of its keys when written.
type Ordered []Pair

// MarshalJSON writes the pairs in their order.
func (o Ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	for i, p := range o {
		if i > 0 {
			b.WriteString(",")
		}
		b.Write(encode(p.Key))
		b.WriteString(":")
		b.Write(encode(p.Value))
	}
	b.WriteString("}")
	return b.Bytes(), nil
}

// Get returns the value of key.
func (o Ordered) Get(key string) (any, bool) {
	for _, p := range o {
		if p.Key == key {
			return p.Value, true
		}
	}
	return nil, false
}

// Update adds entries to the defaults file at path and replaces its
// "$review" block; review nil removes the block. Existing keys keep their
// place and value, keys that already exist (ignoring case) are not added
// again. A missing file is created. It returns whether the file changed.
func Update(path string, add []Pair, review any) (bool, error) {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	var pairs []rawPair
	if len(bytes.TrimSpace(old)) > 0 {
		if pairs, err = rawPairs(old); err != nil {
			return false, fmt.Errorf("defaults %s: %w", path, err)
		}
	}
	seen := map[string]bool{}
	kept := pairs[:0]
	for _, p := range pairs {
		if p.key == ReviewKey {
			continue
		}
		seen[strings.ToLower(p.key)] = true
		kept = append(kept, p)
	}
	pairs = kept
	if review != nil {
		pairs = append([]rawPair{{ReviewKey, encode(review)}}, pairs...)
	}
	for _, a := range add {
		if seen[strings.ToLower(a.Key)] {
			continue
		}
		seen[strings.ToLower(a.Key)] = true
		pairs = append(pairs, rawPair{a.Key, encode(a.Value)})
	}
	var b bytes.Buffer
	b.WriteString("{")
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(",")
		}
		var v bytes.Buffer
		if err := json.Indent(&v, p.raw, "  ", "  "); err != nil {
			return false, err
		}
		fmt.Fprintf(&b, "\n  %s: %s", encode(p.key), v.Bytes())
	}
	if len(pairs) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if bytes.Equal(b.Bytes(), old) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".defaults-*.json")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

type rawPair struct {
	key string
	raw json.RawMessage
}

// rawPairs reads the top-level keys of a JSON object in file order.
func rawPairs(b []byte) ([]rawPair, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected a JSON object with key → value")
	}
	var out []rawPair
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
		out = append(out, rawPair{key, raw})
	}
	if err := closeObject(dec); err != nil {
		return nil, err
	}
	return out, nil
}

// encode renders v as JSON without HTML escaping.
func encode(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}
