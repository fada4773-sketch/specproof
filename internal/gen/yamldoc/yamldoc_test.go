package yamldoc

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const sample = `# head comment
openapi: 3.0.3
paths:
  /a:
    get:
      # keep me
      summary: old # line comment
      parameters:
        - $ref: "#/components/parameters/P"
components:
  parameters:
    P: { name: p, in: query, schema: { type: integer } }
    Q:
      $ref: "#/components/parameters/P"
    Ext:
      $ref: "other.yaml#/P"
`

func TestSetKeepsCommentsAndOrder(t *testing.T) {
	d, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	get := Path(d.Root, "paths", "/a", "get")
	if err := Set(get, "summary", "new"); err != nil {
		t.Fatal(err)
	}
	if err := Set(get, "x-added", map[string]any{"num": json.Number("42"), "s": "2026-01-01"}); err != nil {
		t.Fatal(err)
	}
	b, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"# head comment", "# keep me", "summary: new # line comment", "num: 42", `s: "2026-01-01"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "summary:") > strings.Index(out, "parameters:") || strings.Index(out, "parameters:") > strings.Index(out, "x-added:") {
		t.Errorf("key order changed:\n%s", out)
	}
}

func TestResolveAndRefSafety(t *testing.T) {
	d, _ := Parse([]byte(sample))
	param := Path(d.Root, "paths", "/a", "get", "parameters", "0")
	target, err := d.Resolve(param)
	if err != nil || Get(target, "name").Value != "p" {
		t.Fatalf("resolve: %v %v", target, err)
	}
	// chains of references are followed
	if target2, err := d.Resolve(Path(d.Root, "components", "parameters", "Q")); err != nil || target2 != target {
		t.Errorf("chain: %v", err)
	}
	if _, err := d.Resolve(Path(d.Root, "components", "parameters", "Ext")); err == nil || !strings.Contains(err.Error(), "external") {
		t.Errorf("external: %v", err)
	}
	if err := Set(param, "example", 1); err == nil {
		t.Error("wrote next to a $ref")
	}
	if Path(d.Root, "paths", "/a", "get", "parameters", "9") != nil || Path(d.Root, "paths", "/a", "get", "parameters", "x") != nil {
		t.Error("bad sequence index must give nil")
	}
}

func TestDecodeNumbers(t *testing.T) {
	d, _ := Parse([]byte("a: 12\nb: 1.5\nc: \"12\"\nd: 2026-01-01\ne: [true, null]\n"))
	v, err := Decode(d.Root)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["a"] != json.Number("12") || m["b"] != json.Number("1.5") || m["c"] != "12" || m["d"] != "2026-01-01" {
		t.Errorf("decode: %#v", m)
	}
	if e := m["e"].([]any); e[0] != true || e[1] != nil {
		t.Errorf("list: %#v", e)
	}
}

func TestJSONKeepsOrder(t *testing.T) {
	d, err := Parse([]byte(`{"openapi": "3.0.3", "zeta": 1, "alpha": {"b": [1, 2], "a": "x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Set(d.Root, "added", true); err != nil {
		t.Fatal(err)
	}
	b, _ := d.Bytes()
	out := string(b)
	if !strings.HasPrefix(strings.TrimSpace(out), "{") || strings.Index(out, "zeta") > strings.Index(out, "alpha") || !strings.Contains(out, `"added": true`) {
		t.Errorf("JSON output:\n%s", out)
	}
	var check map[string]any
	if err := json.Unmarshal(b, &check); err != nil {
		t.Errorf("invalid JSON: %v\n%s", err, out)
	}
}

func TestLongKeysStaySimple(t *testing.T) {
	long := "/" + strings.Repeat("segment/", 20) + "{id}"
	d, _ := Parse([]byte("paths:\n  " + long + ":\n    get:\n      summary: x\n  /short: {}\n"))
	b, _ := d.Bytes()
	out := string(b)
	if strings.Contains(out, "? ") || !strings.Contains(out, "  "+long+":\n    get:\n      summary: x") {
		t.Errorf("long key:\n%s", out)
	}
	if again, err := Parse(b); err != nil || Path(again.Root, "paths", long, "get", "summary").Value != "x" {
		t.Errorf("reparse: %v", err)
	}
}

// Long keys keep their usual form in every place yaml.v3 writes them as
// "? key": as the key of a list item, with a mapping, a list, a flow
// mapping, a quoted text or nothing as the value.
func TestLongKeysInLists(t *testing.T) {
	long := "GET /" + strings.Repeat("segment/", 16) + "{dockId}"
	src := "Dock:\n" +
		"  - " + long + ":\n      path: {dockId: 1}\n      response:\n        status: 200\n" +
		"  - " + long + "/a: 5\n" +
		"  - " + long + "/b:\n      - x\n      - y\n" +
		"  - " + long + "/c: {status: 1}\n" +
		"  - " + long + "/d: 'a: b'\n" +
		"  - " + long + "/e:\n" +
		"  - - " + long + "/f: {}\n" +
		"other:\n  " + long + "/g:\n    '" + long + "': x\n"
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Decode(d.Root)
	b, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "? ") || strings.Contains(string(b), "\n  : ") {
		t.Errorf("explicit keys left:\n%s", b)
	}
	for _, line := range []string{"  - " + long + ":\n      path: {dockId: 1}\n      response:", "  - " + long + "/a: 5\n",
		"  - " + long + "/b:\n      - x\n", "  - " + long + "/d: 'a: b'\n", "  - " + long + "/e:\n", "  - - " + long + "/f: {}\n",
		"other:\n  " + long + "/g:\n    '" + long + "': x\n"} {
		if !strings.Contains(string(b), line) {
			t.Errorf("output misses %q:\n%s", line, b)
		}
	}
	again, err := Parse(b)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, b)
	}
	got, _ := Decode(again.Root)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("content changed:\n%v\nwant\n%v", got, want)
	}
	for _, tc := range []struct {
		rest string
		want bool
	}{{"- x", true}, {"-", true}, {"? k", true}, {"a: b", true}, {"a:", true}, {"'a b': x", true}, {`"a": x`, true}, {"'a: b'", false}, {`"x`, false},
		{"{a: b}", false}, {"[a: b]", false}, {"|", false}, {"x # a: b", false}, {"5", false}, {"http://x", false}} {
		if startsBlock(tc.rest) != tc.want {
			t.Errorf("startsBlock(%q) != %v", tc.rest, tc.want)
		}
	}
}

func TestNewAndSetNode(t *testing.T) {
	d := New("head")
	if err := SetNode(d.Root, "a", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	old := Get(d.Root, "a")
	old.LineComment = "kept"
	if err := SetNode(d.Root, "a", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "2"}); err != nil {
		t.Fatal(err)
	}
	b, _ := d.Bytes()
	if string(b) != "# head\n\na: 2 # kept\n" {
		t.Errorf("output %q", b)
	}
	if SetNode(Get(d.Root, "a"), "x", &yaml.Node{}) == nil {
		t.Error("SetNode on a scalar")
	}
	ref, _ := Parse([]byte("r: {$ref: '#/x'}\n"))
	if SetNode(Get(ref.Root, "r"), "x", &yaml.Node{}) == nil {
		t.Error("SetNode next to a $ref")
	}
}

func TestLoadSave(t *testing.T) {
	path := t.TempDir() + "/spec.yaml"
	if err := os.WriteFile(path, []byte(sample), 0o640); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// compared with the mode before, not 0o640: Windows has no Unix modes
	before, _ := os.Stat(path)
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("mode changed from %v to %v", before.Mode().Perm(), info.Mode().Perm())
	}
	if _, err := Load(path + ".missing"); err == nil {
		t.Error("missing file must fail")
	}
	if _, err := Parse([]byte("- a list\n")); err == nil {
		t.Error("a list is no OpenAPI document")
	}
}

func TestDelete(t *testing.T) {
	d, err := Parse([]byte("a: 1\nexample: 2\nb: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := d.Root
	if !Delete(m, "example") || Delete(m, "example") || Delete(nil, "a") {
		t.Fatal("Delete reports wrong")
	}
	if got := strings.Join(Keys(m), ","); got != "a,b" {
		t.Errorf("keys %s", got)
	}
}
