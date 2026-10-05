package defaults

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndLookup(t *testing.T) {
	d, err := Parse([]byte(`{
		"$comment": "ignored",
		"PilotCode": "a",
		"Garden.Name": "Mitte",
		"listPilots.Name": "Op",
		"/pilots/{id}": 7,
		"UpdatePilot.x-apitest-verify": false,
		"UpdatePilot.x-apitest-order": 2,
		"GetPilot.id": {"bind": "CreatePilot", "pointer": "/Id"},
		"planetCode": {"from": "GET /Planet", "pick": "/0/Code"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Len() != 8 {
		t.Errorf("Len = %d", d.Len())
	}
	// case-insensitive, DTO before operation before plain name
	if e := d.Field("garden", "listPilots", "name"); e == nil || e.Value != "Mitte" {
		t.Errorf("DTO key: %+v", e)
	}
	if e := d.Field("Other", "LISTPILOTS", "Name"); e == nil || e.Value != "Op" {
		t.Errorf("operation key: %+v", e)
	}
	if e := d.Field("", "", "pilotcode"); e == nil || e.Value != "a" {
		t.Errorf("plain key: %+v", e)
	}
	if d.Field("", "", "ParentPilotCode") != nil {
		t.Error("no substring matches")
	}
	if e := d.Plain("/pilots/{id}"); e == nil {
		t.Error("path key")
	}
	ext := d.Extensions("updatepilot")
	if len(ext) != 2 || ext[0].Name != "x-apitest-verify" || ext[1].Name != "x-apitest-order" {
		t.Errorf("extensions: %+v", ext)
	}
	if b := d.Binding("getpilot", "ID"); b == nil || b.Bind.From != "CreatePilot" || b.Bind.Pointer != "/Id" {
		t.Errorf("binding: %+v", b)
	}
	if d.Field("", "GetPilot", "id") != nil {
		t.Error("a binding is not a value")
	}
	if src := d.Sources(); len(src) != 1 || src[0].From.Pick != "/0/Code" {
		t.Errorf("sources: %+v", src)
	}

	d.Use(d.Plain("PilotCode"))
	unused := d.Unused()
	if len(unused) != 6 || unused[0] != "Garden.Name" {
		t.Errorf("unused: %v", unused)
	}
	if u := d.Usage(); len(u) != 7 || u[0].Key != "PilotCode" || u[0].Uses != 1 {
		t.Errorf("usage: %+v", u)
	}
}

func TestParseErrors(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"duplicate":        {`{"PilotCode": 1, "pilotcode": 2}`, "same key"},
		"not an object":    {`[1]`, "JSON object"},
		"bind without key": {`{"id": {"bind": "X", "pointer": "/Id"}}`, "<operationId>.<parameter>"},
		"bind both":        {`{"a.id": {"bind": "X", "pointer": "/Id", "header": "Location"}}`, "either"},
		"bind pointer":     {`{"a.id": {"bind": "X", "pointer": "Id"}}`, "must start with /"},
		"source method":    {`{"code": {"from": "POST /x", "pick": "/0"}}`, "GET /path"},
		"broken":           {`{"a": }`, "invalid"},
	} {
		if _, err := Parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestLoad(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.json")
	d, err := Load(missing)
	if err != nil || d.Len() != 0 {
		t.Errorf("missing file: %v %v", d, err)
	}
	broken := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(broken); err == nil || !strings.Contains(err.Error(), broken) {
		t.Errorf("broken file: %v", err)
	}
	for _, bad := range []string{`{"a": 1`, `{"a": 1} {}`, `{"a": 1} x`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
		if _, err := rawPairs([]byte(bad)); err == nil {
			t.Errorf("rawPairs %s: no error", bad)
		}
	}
}

func TestNullAndRejected(t *testing.T) {
	d, err := Parse([]byte(`{"zone": null, "code": "a", "$rejected": ["getPilot.id"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsTodo("Zone") || d.Field("", "", "zone") != nil {
		t.Error("null must be a todo without value")
	}
	if u := d.Unused(); len(u) != 1 || u[0] != "code" {
		t.Errorf("unused: %v", u)
	}
	if len(d.Rejected) != 1 || d.Rejected[0] != "getPilot.id" {
		t.Errorf("rejected: %v", d.Rejected)
	}
}

// Update keeps the order and values of a file, adds new keys once and
// replaces the "$review" block.
func TestUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.json")
	if err := os.WriteFile(path, []byte(`{"$review": "old", "b": 1, "a": {"x": [1, 2]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := Update(path, []Pair{{"B", 9}, {"c", nil}, {"d", "<x>"}}, map[string]string{"about": "new"})
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	got, _ := os.ReadFile(path)
	want := `{
  "$review": {
    "about": "new"
  },
  "b": 1,
  "a": {
    "x": [
      1,
      2
    ]
  },
  "c": null,
  "d": "<x>"
}
`
	if string(got) != want {
		t.Errorf("got\n%s", got)
	}
	if changed, _ := Update(path, []Pair{{"c", 1}}, map[string]string{"about": "new"}); changed {
		t.Error("nothing new, but the file changed")
	}
	if _, err := Update(path, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); strings.Contains(string(got), ReviewKey) {
		t.Errorf("review block not removed:\n%s", got)
	}
	missing := filepath.Join(t.TempDir(), "new.json")
	if _, err := Update(missing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(missing); string(got) != "{}\n" {
		t.Errorf("new file: %q", got)
	}
}

func TestSpecialKeys(t *testing.T) {
	d, err := Parse([]byte(`{
  "$snapshot": {"Book": {"from": "GetBooks", "count": 3}, "Ship": {"from": "GetShips"}},
  "$model": {"Book": {"keys": ["Code"]}},
  "$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true, "IgnoreFields": ["Message"]},
  "Name": "x"
}`))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := d.SnapshotFor("book"); !ok || s.From != "GetBooks" || s.Records() != 3 {
		t.Errorf("snapshot: %+v %v", s, ok)
	}
	if s, _ := d.SnapshotFor("Ship"); s.Records() != 1 {
		t.Errorf("default count: %d", s.Records())
	}
	if d.Model["Book"].Keys[0] != "Code" || !d.RunConfig().DeleteLast || d.RunConfig().IgnoreFields[0] != "Message" {
		t.Errorf("model %v, run %+v", d.Model, d.RunConfig())
	}
	if d.Len() != 1 {
		t.Errorf("the special keys are entries: %v", d.Keys())
	}
	if (&Defaults{}).RunConfig().DeleteLast {
		t.Error("RunConfig without $apitest")
	}

	for in, want := range map[string]string{
		`{"$apitest": {"MethodOrder": ["DELETE", "GET"]}}`:           "DELETE must be the last",
		`{"$apitest": {"MethodOrder": ["GET", "get"]}}`:              "twice",
		`{"$apitest": {"MethodOrder": ["FETCH"]}}`:                   "unknown method",
		`{"$apitest": {"DeleteFirst": true}}`:                        "MethodOrder, DeleteLast",
		`{"$snapshot": {"Book": {"from": "GetBooks", "count": -1}}}`: "1 or more",
		`{"$snapshot": {"Book": "GetBooks"}}`:                        `"from"`,
		`{"$model": {"Book": {"key": ["Id"]}}}`:                      `"keys"`,
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestSpecialKeysMerge(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, []byte(`{"$snapshot": {"Book": {"from": "GetBooks"}, "Ship": {"from": "GetShips"}}, "$apitest": {"DeleteLast": true}}`), 0o600)
	_ = os.WriteFile(b, []byte(`{"$snapshot": {"Book": {"from": "ListBooks", "count": 2}}, "$model": {"Book": {"keys": ["Code"]}}, "$apitest": {"MethodOrder": ["PUT"]}}`), 0o600)
	d, err := LoadAll(a + "," + b)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := d.SnapshotFor("Book"); s.From != "ListBooks" || s.Count != 2 {
		t.Errorf("Book: %+v", s)
	}
	if _, ok := d.SnapshotFor("Ship"); !ok || d.Model["Book"].Keys[0] != "Code" {
		t.Errorf("merge lost entries: %v %v", d.Snapshot, d.Model)
	}
	if r := d.RunConfig(); r.DeleteLast || r.MethodOrder[0] != "PUT" {
		t.Errorf("$apitest of the later file replaces the earlier one: %+v", r)
	}
}

// "$snapshot" keeps the order of the file; a later file replaces an entry
// in its place and adds new ones at the end. "seed" is read.
func TestSnapshotOrderAndSeed(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	_ = os.WriteFile(a, []byte(`{"$snapshot": {"Ship": {"from": "/Ship"}, "Book": {"from": "/Book", "count": 2, "seed": ["code", "tier"]}, "Dock": {"from": "/Dock"}}}`), 0o600)
	_ = os.WriteFile(b, []byte(`{"$snapshot": {"Pilot": {"from": "/Pilot"}, "Book": {"from": "/Book/A1"}}}`), 0o600)
	d, err := Load(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.SnapshotOrder(), ","); got != "Ship,Book,Dock" {
		t.Errorf("order %s", got)
	}
	if s, _ := d.SnapshotFor("book"); strings.Join(s.Seed, ",") != "code,tier" {
		t.Errorf("seed %v", s.Seed)
	}
	if d, err = LoadAll(a + "," + b); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.SnapshotOrder(), ","); got != "Ship,Book,Dock,Pilot" {
		t.Errorf("merged order %s", got)
	}
}

func TestSnapshotValidation(t *testing.T) {
	d, err := Parse([]byte(`{"$snapshot": {"Book": {"from": "/Book", "mandatoryFields": ["Isbn"], "validation": {
		"mandatoryFields": ["Book.Author"], "equalFields": {"Book.Author": "tom", "Book.Pages": 120},
		"followingDetails": ["/book/{id}/details"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := d.SnapshotFor("Book")
	v := s.Checks()
	if strings.Join(v.MandatoryFields, ",") != "Book.Author,Isbn" || v.EqualFields["Book.Author"] != "tom" ||
		fmt.Sprint(v.EqualFields["Book.Pages"]) != "120" || v.FollowingDetails[0] != "/book/{id}/details" {
		t.Errorf("checks: %+v", v)
	}
	if _, err := Parse([]byte(`{"$snapshot": {"Book": {"from": "/Book", "validation": {"mandatory": []}}}}`)); err == nil {
		t.Error("an unknown field in validation is accepted")
	}
}
