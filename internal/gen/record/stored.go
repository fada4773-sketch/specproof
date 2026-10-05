package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// load takes the records the last run stored: each one whose GETs and
// "select" entry are unchanged. The others are read and selected again;
// where the instance still holds the stored one, it is taken again. It
// returns the number of records taken.
func (rd *reader) load(old *Recorded) int {
	rd.stored = map[string][]StoredRecord{}
	same := old.Params == paramsHash(rd.cfg)
	n := 0
	for _, st := range old.Records {
		rd.stored[st.Table] = append(rd.stored[st.Table], st)
		if !same || !rd.valid(st) {
			continue
		}
		r := st.rec()
		if r.path != "" {
			if rd.forPath[r.path] != nil {
				continue
			}
			rd.forPath[r.path] = r
		}
		rd.addRec(r)
		n++
	}
	return n
}

// valid reports whether a stored record still fits the spec and the
// config.
func (rd *reader) valid(st StoredRecord) bool {
	if len(st.Ops) == 0 || len(st.Data) == 0 || st.Select != selectHash(rd.cfg.selection(st.Table, rd.n)) {
		return false
	}
	for id, fp := range st.Ops {
		op := rd.s.Op(id)
		if op == nil || fingerprint(op) != fp {
			return false
		}
	}
	return true
}

// rec turns a stored record into one of the run.
func (st StoredRecord) rec() *rec {
	r := &rec{table: st.Table, data: map[string]any{}, from: st.From, keys: map[string]bool{}, path: st.Path}
	for k, v := range st.Data {
		r.data[k] = v
	}
	for _, k := range st.Keys {
		r.keys[strings.ToLower(k)] = true
	}
	r.ops = sortedKeys(st.Ops)
	if _, id := idOf(r.data); id != nil {
		r.id = id
	}
	return r
}

// storedRecords are the records of the run as the next one takes them,
// with the ids the instance holds them with now.
func (rd *reader) storedRecords() []StoredRecord {
	var out []StoredRecord
	for _, r := range rd.order {
		data := map[string]any{}
		for k, v := range r.data {
			data[k] = v
		}
		if k, _ := idOf(data); k != "" && r.newID != nil {
			data[k] = r.newID
		}
		ops := map[string]string{}
		for _, id := range r.ops {
			if op := rd.s.Op(id); op != nil {
				ops[id] = fingerprint(op)
			}
		}
		out = append(out, StoredRecord{Table: r.table, From: r.from, Path: r.path, Keys: sortedKeys(r.keys), Ops: ops,
			Select: selectHash(rd.cfg.selection(r.table, rd.n)), Data: data})
	}
	return out
}

// selectHash is a hash of a "select" entry without its comments.
func selectHash(s Select) string {
	s.Comment = ""
	details := map[string]Check{}
	for k, c := range s.Details {
		c.Comment = ""
		details[k] = c
	}
	s.Details = details
	return hash(s)
}

// paramsHash is a hash of "params".
func paramsHash(c *Config) string { return hash(c.Params) }

func hash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
