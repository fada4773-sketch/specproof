package scenario

import (
	"bytes"
	"fmt"
	"os"
	"slices"

	yaml "github.com/oasdiff/yaml3"
)

// The answers of the last run with Update can stand in the scenario file
// itself, next to the steps: one file holds the test.
//
//	golden: {spec: ../openapi.yaml, specVersion: "1.2", created: 2026-10-16T10:12:00Z, reread: true}
//	steps:
//	  - id: pilots1.create
//	    call: POST /pilots
//	    expect: 201
//	    answer:
//	      request: POST /pilots
//	      status: 201
//	      body: {id: 100, name: Lin}
//	      volatile: [/id]

// goldenOf reads the golden section and the answers of the steps; nil when
// the file has neither.
func goldenOf(top map[string]any, list []any) *Snapshot {
	meta, hasMeta := top["golden"].(map[string]any)
	var shots []*Shot
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		if a, ok := m["answer"].(map[string]any); ok {
			shots = append(shots, shotOf(Text(m["id"]), a))
		}
	}
	if !hasMeta && len(shots) == 0 {
		return nil
	}
	s := &Snapshot{Kind: KindGolden, Steps: shots}
	if hasMeta {
		s.Spec = Text(meta["spec"])
		s.SpecVersion = Text(meta["specVersion"])
		s.BaseURL = Text(meta["baseURL"])
		s.Created = Text(meta["created"])
		s.Reread, _ = meta["reread"].(bool)
	}
	return s
}

// goldenMeta writes the golden section.
func goldenMeta(s *Snapshot) *yaml.Node {
	opt := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	var reread any
	if s.Reread {
		reread = true
	}
	m := mapping("spec", opt(s.Spec), "specVersion", opt(s.SpecVersion), "baseURL", opt(s.BaseURL), "created", opt(s.Created), "reread", reread)
	m.Style = yaml.FlowStyle
	return m
}

// WriteAnswers writes a golden snapshot into a scenario file: the golden
// section and the answer of each step, in place, so the comments of the
// file stay. A step without answer in the snapshot loses its old one.
func WriteAnswers(path string, s *Snapshot) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: a scenario is a mapping with the key \"steps\"", path)
	}
	root := doc.Content[0]
	setKey(root, "golden", goldenMeta(s), "steps")
	steps := valueNode(root, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: \"steps\" must be a list", path)
	}
	for _, st := range steps.Content {
		if st.Kind != yaml.MappingNode {
			continue
		}
		id := valueNode(st, "id")
		if id == nil {
			continue
		}
		if sh := s.Shot(id.Value); sh != nil {
			setKey(st, "answer", shotNode(sh, false), "")
		} else {
			deleteKey(st, "answer")
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return WriteFile(path, buf.Bytes())
}

// valueNode is the value of a key of a mapping node, nil without.
func valueNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setKey sets a key of a mapping node: in place, else before the key
// before (at the end when empty or not there).
func setKey(m *yaml.Node, key string, v *yaml.Node, before string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	at := len(m.Content)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if before != "" && m.Content[i].Value == before {
			at = i
			break
		}
	}
	m.Content = slices.Insert(m.Content, at, str(key), v)
}

func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}
