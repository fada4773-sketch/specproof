package tree

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// Encode writes the data file: the records with the request that answered
// them, so the scenario can be built again without fetching.
func (d *Data) Encode(head string) ([]byte, error) {
	var recs []any
	for _, r := range d.Records {
		m := map[string]any{"id": r.ID, "node": r.Node.PathName(), "request": r.Request, "value": r.Value}
		if r.Parent != nil {
			m["parent"] = r.Parent.ID
		}
		if len(r.Saved) > 0 {
			m["saved"] = r.Saved
		}
		recs = append(recs, m)
	}
	top := map[string]any{"source": d.Source, "requests": d.Requests, "records": recs}
	if len(d.Notes) > 0 {
		var notes []any
		for _, n := range d.Notes {
			notes = append(notes, n)
		}
		top["notes"] = notes
	}
	if recs == nil {
		top["records"] = []any{}
	}
	return scenario.EncodeValue(head, top)
}

// LoadData reads a data file and ties its records to the nodes of the tree.
// Records of nodes the tree no longer has are left out; stale lists them.
func LoadData(path string, t *Tree) (d *Data, stale []string, err error) {
	v, err := scenario.ReadYAML(path)
	if err != nil {
		return nil, nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%s: not a data file of apitest-gen tree", path)
	}
	d = &Data{Source: scenario.Text(top["source"])}
	byPath := map[string]*Node{}
	t.Walk(func(n *Node) {
		if !n.IsRequire {
			byPath[n.PathName()] = n
		}
	})
	// an older fetch named the records of a node whose name ends in a
	// digit without a separator: dock2's third record was dock23, like the
	// 23rd of dock. A parent is the record of that id at the node above.
	byID := map[string][]*Record{}
	parentOf := func(id string, n *Node) *Record {
		for _, r := range byID[id] {
			if r.Node == n {
				return r
			}
		}
		if rs := byID[id]; len(rs) > 0 {
			return rs[0]
		}
		return nil
	}
	list, _ := top["records"].([]any)
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		node := byPath[scenario.Text(m["node"])]
		if node == nil {
			stale = append(stale, scenario.Text(m["id"]))
			continue
		}
		r := &Record{ID: scenario.Text(m["id"]), Node: node, Value: m["value"], Request: scenario.Text(m["request"])}
		if p := scenario.Text(m["parent"]); p != "" {
			r.Parent = parentOf(p, node.Parent)
		}
		if node.Parent != nil && (r.Parent == nil || r.Parent.Node != node.Parent) {
			stale = append(stale, r.ID)
			continue
		}
		if s, ok := m["saved"].(map[string]any); ok {
			r.Saved = s
		}
		byID[r.ID] = append(byID[r.ID], r)
		d.Records = append(d.Records, r)
	}
	for _, r := range d.Records {
		r.ID = renamedID(r.ID, r.Node.Name)
	}
	if notes, ok := top["notes"].([]any); ok {
		for _, n := range notes {
			d.Notes = append(d.Notes, scenario.Text(n))
		}
	}
	return d, stale, nil
}

// renamedID is the id a record of the older form has now (dock23 of the
// node dock2: dock2_3); other ids stay.
func renamedID(id, node string) string {
	n, ok := strings.CutPrefix(id, node)
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" {
		return id
	}
	i, err := strconv.Atoi(n)
	if err != nil {
		return id
	}
	return recordID(node, i)
}
