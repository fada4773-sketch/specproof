// Package record keeps the examples of a spec in one file, the record
// file (examples.record.yaml): per tag the requests in the order they run,
// each with the answer the instance gave. The file is the one place the
// examples live; "apitest-gen record" writes them into the spec, and sends
// only the requests whose answer is missing or no longer fits the schema,
// to an empty instance.
//
//	Dock:
//	  - POST /docks:
//	      body: { name: Nord, capacity: 4 }
//	      save: { dockId: /id }
//	      response:
//	        status: 201
//	        body: { id: 1, name: Nord, capacity: 4 }
//	  - GET /docks/{dockId}:
//	      path: { dockId: "{{dockId}}" }
//	      response: …
package record

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// DefaultFile is the name of the record file.
const DefaultFile = "examples.record.yaml"

// header heads a new record file.
const header = `Examples for apitest, written by "apitest-gen record".
Per tag the requests in the order they run, top to bottom. Each entry is
"METHOD /path" (or the operationId) with:
  name:     example name, needed when an endpoint appears more than once
  path:     path parameters        query: query parameters
  body:     request body           save:  values for later entries, { name: /pointer }
  ignore:   fields apitest must not compare (time stamps, generated ids)
  response: the answer; empty means "apitest-gen record" sends the request
Use a saved value as "{{name}}". Docs: "apitest-gen help".`

// Keys of an entry.
const (
	keyName     = "name"
	keyPath     = "path"
	keyQuery    = "query"
	keyBody     = "body"
	keySave     = "save"
	keyIgnore   = "ignore"
	keyResponse = "response"
	keyStatus   = "status"
	keyHeaders  = "headers"
)

var entryKeys = []string{keyName, keyPath, keyQuery, keyBody, keySave, keyIgnore, keyResponse}

// File is a loaded record file.
type File struct {
	doc   *yamldoc.Doc
	Steps []*Step
}

// Step is one entry: a request and its answer.
type Step struct {
	// Tag is the section of the file the entry is in: a tag of the spec,
	// or any other heading such as "cleanup".
	Tag string
	Key string // as written: "POST /docks" or an operationId
	Op  *spec.Operation
	// Name is the example name; "" is the default example.
	Name  string
	Path  *yaml.Node // mapping of path parameters, nil without
	Query *yaml.Node // mapping of query parameters, nil without
	Body  *yaml.Node // nil without body
	Save  []Save
	// Ignore are fields apitest does not compare (x-apitest-ignore).
	Ignore []string
	// Response is the recorded answer; nil until it is recorded.
	Response *Response
	Line     int
	node     *yaml.Node // the mapping of the entry
}

// Save keeps a value of the answer under a name for later entries.
type Save struct {
	Name string
	// From is a JSON pointer into the answer ("/id") or "header <Name>".
	From string
}

// Response is a recorded answer.
type Response struct {
	Status int
	// Headers are the headers values are saved from ("header Location").
	Headers map[string]string
	Body    *yaml.Node // nil without body
}

// String is the step as the report shows it: "POST /docks", with its name.
func (st *Step) String() string {
	s := st.Key
	if st.Op != nil {
		s = st.Op.Method + " " + st.Op.Path
	}
	if st.Name != "" {
		s += " (" + st.Name + ")"
	}
	return s
}

// Example is the example name apitest gives the case of the step.
func (st *Step) Example() string {
	if st.Name == "" {
		return "default"
	}
	return st.Name
}

// Load reads a record file; a missing file gives an empty one.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	f, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// New returns an empty record file.
func New() *File {
	return &File{doc: yamldoc.New(header)}
}

// commentOnly matches content without any YAML value.
var commentOnly = regexp.MustCompile(`^(\s*(#[^\n]*)?\n?)*$`)

// Parse reads the content of a record file.
func Parse(b []byte) (*File, error) {
	if commentOnly.Match(b) {
		f := New()
		if c := strings.TrimSpace(string(b)); c != "" {
			f.doc = yamldoc.New(strings.TrimSpace(strings.ReplaceAll("\n"+c, "\n#", "\n")))
		}
		return f, nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		return nil, errors.New("the record file is YAML: per tag a list of entries")
	}
	doc, err := yamldoc.Parse(b)
	if err != nil {
		return nil, err
	}
	f := &File{doc: doc}
	root := doc.Root
	for i := 0; i+1 < len(root.Content); i += 2 {
		tag, list := root.Content[i].Value, root.Content[i+1]
		if list.Kind == yaml.ScalarNode && list.Tag == "!!null" {
			continue
		}
		if list.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("line %d: tag %q must hold a list of entries (\"- POST /path: …\")", list.Line, tag)
		}
		for _, item := range list.Content {
			st, err := parseStep(tag, item)
			if err != nil {
				return nil, err
			}
			f.Steps = append(f.Steps, st)
		}
	}
	return f, nil
}

func parseStep(tag string, item *yaml.Node) (*Step, error) {
	if item.Kind == yaml.ScalarNode && item.Value != "" {
		// "- GET /docks" without colon: an entry without settings
		item.Kind, item.Tag, item.Content = yaml.MappingNode, "!!map", []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: item.Value}, {Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}}
		item.Value, item.Style = "", 0
	}
	if item.Kind != yaml.MappingNode || len(item.Content) != 2 {
		return nil, fmt.Errorf("line %d: an entry is \"- METHOD /path:\" followed by its settings", item.Line)
	}
	st := &Step{Tag: tag, Key: strings.TrimSpace(item.Content[0].Value), Line: item.Line}
	n := item.Content[1]
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || n.Value == "") {
		n = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: n.Line}
		item.Content[1] = n
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: the settings of %q must be a mapping (name, path, query, body, save, ignore, response)", n.Line, st.Key)
	}
	st.node = n
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		null := v.Kind == yaml.ScalarNode && v.Tag == "!!null"
		switch k {
		case keyName:
			st.Name = strings.TrimSpace(v.Value)
		case keyPath, keyQuery:
			if null {
				continue
			}
			if v.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("line %d: %s of %q must map parameter names to values", v.Line, k, st.Key)
			}
			if k == keyPath {
				st.Path = v
			} else {
				st.Query = v
			}
		case keyBody:
			st.Body = v
		case keySave:
			if null {
				continue
			}
			if v.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("line %d: save of %q must map names to a pointer, e.g. { dockId: /id }", v.Line, st.Key)
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				name, from := v.Content[j].Value, strings.TrimSpace(v.Content[j+1].Value)
				if !validName.MatchString(name) {
					return nil, fmt.Errorf("line %d: %q is no name for a saved value: letters, digits, _ and -", v.Content[j].Line, name)
				}
				if !strings.HasPrefix(from, "/") && !strings.HasPrefix(from, "header ") && from != "" {
					return nil, fmt.Errorf("line %d: save %s: %q is neither a JSON pointer (/id) nor \"header <Name>\"", v.Content[j].Line, name, from)
				}
				st.Save = append(st.Save, Save{Name: name, From: from})
			}
		case keyIgnore:
			if null {
				continue
			}
			if v.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("line %d: ignore of %q must be a list of field names", v.Line, st.Key)
			}
			for _, c := range v.Content {
				st.Ignore = append(st.Ignore, c.Value)
			}
		case keyResponse:
			if null {
				continue
			}
			r, err := parseResponse(st.Key, v)
			if err != nil {
				return nil, err
			}
			st.Response = r
		default:
			return nil, fmt.Errorf("line %d: unknown setting %q of %q (allowed: %s)", n.Content[i].Line, k, st.Key, strings.Join(entryKeys, ", "))
		}
	}
	return st, nil
}

func parseResponse(key string, v *yaml.Node) (*Response, error) {
	if v.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: response of %q must have status and body", v.Line, key)
	}
	r := &Response{}
	for i := 0; i+1 < len(v.Content); i += 2 {
		switch k, c := v.Content[i].Value, v.Content[i+1]; k {
		case keyStatus:
			n, err := strconv.Atoi(c.Value)
			if err != nil || n < 100 || n > 599 {
				return nil, fmt.Errorf("line %d: status of %q must be an HTTP status, not %q", c.Line, key, c.Value)
			}
			r.Status = n
		case keyBody:
			r.Body = c
		case keyHeaders:
			keys, m := mapping(c)
			for _, h := range keys {
				if r.Headers == nil {
					r.Headers = map[string]string{}
				}
				r.Headers[http.CanonicalHeaderKey(h)] = m[h].Value
			}
		default:
			return nil, fmt.Errorf("line %d: unknown setting %q in the response of %q (allowed: status, headers, body)", v.Content[i].Line, k, key)
		}
	}
	if r.Status == 0 {
		return nil, fmt.Errorf("line %d: the response of %q has no status", v.Line, key)
	}
	return r, nil
}

// validName is the form of a saved value's name.
var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Bytes renders the file.
func (f *File) Bytes() ([]byte, error) { return f.doc.Bytes() }

// Save writes the file atomically.
func (f *File) Save(path string) error { return f.doc.Save(path) }

// setResponse stores an answer in the entry of a step.
func (st *Step) setResponse(r *Response) {
	st.Response = r
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, scalarNode(keyStatus), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(r.Status)})
	if len(r.Headers) > 0 {
		h := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		for _, k := range sortedKeys(r.Headers) {
			h.Content = append(h.Content, scalarNode(k), scalarNode(r.Headers[k]))
		}
		m.Content = append(m.Content, scalarNode(keyHeaders), h)
	}
	if r.Body != nil {
		m.Content = append(m.Content, scalarNode(keyBody), r.Body)
	}
	_ = yamldoc.SetNode(st.node, keyResponse, m)
}

// addSave adds a saved value to the entry of a step.
func (st *Step) addSave(s Save) {
	st.Save = append(st.Save, s)
	m := yamldoc.Get(st.node, keySave)
	if m == nil || m.Kind != yaml.MappingNode {
		m = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		insertBefore(st.node, keySave, m, keyIgnore, keyResponse)
	}
	m.Content = append(m.Content, scalarNode(s.Name), scalarNode(s.From))
}

// insertBefore adds key to mapping n in front of the first of the keys
// that follow it, else at the end.
func insertBefore(n *yaml.Node, key string, v *yaml.Node, after ...string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		for _, a := range after {
			if n.Content[i].Value == a {
				n.Content = append(n.Content[:i], append([]*yaml.Node{scalarNode(key), v}, n.Content[i:]...)...)
				return
			}
		}
	}
	n.Content = append(n.Content, scalarNode(key), v)
}

func scalarNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// list returns the list of entries of a tag, created at the end of the
// file if the tag has none.
func (f *File) list(tag string) *yaml.Node {
	root := f.doc.Root
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == tag {
			l := root.Content[i+1]
			if l.Kind != yaml.SequenceNode {
				l = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				root.Content[i+1] = l
			}
			return l
		}
	}
	l := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, scalarNode(tag), l)
	return l
}

// insert adds a step to the list of its tag at position pos of that list
// and to the steps of the file after the step before it (nil: first of
// the tag).
func (f *File) insert(st *Step, before *Step, pos int) {
	l := f.list(st.Tag)
	item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalarNode(st.Key), st.node}}
	pos = min(max(pos, 0), len(l.Content))
	l.Content = append(l.Content[:pos], append([]*yaml.Node{item}, l.Content[pos:]...)...)
	at := len(f.Steps)
	if before != nil {
		for i, s := range f.Steps {
			if s == before {
				at = i + 1
			}
		}
	} else {
		// first of its tag: in front of the first step of the tag, or
		// after the last step of the tags in front of it
		at = len(f.Steps)
		for i, s := range f.Steps {
			if s.Tag == st.Tag {
				at = i
				break
			}
		}
	}
	f.Steps = append(f.Steps[:at], append([]*Step{st}, f.Steps[at:]...)...)
}

// position is the index of a step in the list of its tag.
func (f *File) position(st *Step) int {
	n := 0
	for _, s := range f.Steps {
		if s == st {
			return n
		}
		if s.Tag == st.Tag {
			n++
		}
	}
	return n
}
