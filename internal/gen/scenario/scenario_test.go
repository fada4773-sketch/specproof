package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/apply"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// pipeline runs apply and Run on a spec file the way apitest-gen does and
// writes the result back.
type pipeline struct {
	t    *testing.T
	path string
	dict *dict.Dict
	defs string
	// fetch is used instead of generated records
	fetch Fetcher
	// ignoreLinting is Input.IgnoreLinting
	ignoreLinting bool
}

type outcome struct {
	res      *Result
	text     string
	written  *spec.Spec
	problems []string // of Verify
}

func newPipeline(t *testing.T, defaultsJSON string) *pipeline {
	t.Helper()
	return newPipelineFile(t, "records.yaml", defaultsJSON)
}

func newPipelineFile(t *testing.T, file, defaultsJSON string) *pipeline {
	t.Helper()
	src, err := os.ReadFile("../../../testdata/gen/" + file)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), file)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	return &pipeline{t: t, path: path, defs: defaultsJSON}
}

func (p *pipeline) load(path string) *spec.Spec {
	p.t.Helper()
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		p.t.Fatal(err)
	}
	return s
}

func (p *pipeline) save(doc *yamldoc.Doc, path string) {
	p.t.Helper()
	b, err := doc.Bytes()
	if err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pipeline) run() outcome {
	t := p.t
	t.Helper()
	s := p.load(p.path)
	d, _, _ := dict.Build(s, p.dict, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(p.defs))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load(p.path)
	if err != nil {
		t.Fatal(err)
	}
	if ar := apply.Apply(doc, s, d, defs, apply.Options{Seed: 1}); len(ar.Fatal) > 0 {
		t.Fatalf("apply: %v", ar.Fatal)
	}
	mid := filepath.Join(filepath.Dir(p.path), "mid.yaml")
	p.save(doc, mid)
	ms := p.load(mid)
	res := Run(context.Background(), Input{Doc: doc, Spec: ms, Dict: d, Defaults: defs, Model: model.Detect(ms, defs.Model), Seed: 1, Fetch: p.fetch, IgnoreLinting: p.ignoreLinting})
	o := outcome{res: res}
	if len(res.Problems) > 0 {
		return o
	}
	p.save(doc, p.path)
	b, _ := os.ReadFile(p.path)
	o.text = string(b)
	o.written = p.load(p.path)
	o.problems = Verify(o.written, defs, res.Records)
	p.dict = d
	return o
}

// example returns the example of a response or the request body (code "").
func example(t *testing.T, s *spec.Spec, opID, code string) any {
	t.Helper()
	op := s.Op(opID)
	if op == nil {
		t.Fatalf("no operation %s", opID)
	}
	if code == "" {
		return op.Op.RequestBody.Value.Content["application/json"].Example
	}
	return op.Op.Responses.Value(code).Value.Content["application/json"].Example
}

func param(t *testing.T, s *spec.Spec, opID, name string) any {
	t.Helper()
	for _, p := range s.Op(opID).Params {
		if p.Name == name {
			return spec.Normalize(p.Example)
		}
	}
	t.Fatalf("%s has no parameter %s", opID, name)
	return nil
}

func field(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return spec.Normalize(v)
}

func notes(res *Result) string {
	var out []string
	for _, n := range append(append([]Note(nil), res.Notes...), res.Problems...) {
		out = append(out, n.String())
	}
	return strings.Join(out, "\n")
}

const order = `{"$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true}}`

// With PUT before GET every GET expects what the last update sent; keys
// and the shared parameters stay consistent, and a second run changes
// nothing.
func TestRunGeneratedFollowsUpdates(t *testing.T) {
	p := newPipeline(t, order)
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v\n%s", o.res.Problems, o.problems, notes(o.res))
	}
	s := o.written
	byID, byCode := example(t, s, "UpdateDockById", ""), example(t, s, "UpdateDock", "")
	if field(byID, "Name") == field(byCode, "Name") {
		t.Errorf("both updates send the same name %v", field(byID, "Name"))
	}
	last := field(byCode, "Name") // UpdateDock runs after UpdateDockById
	for _, id := range []string{"GetDockById", "GetDock"} {
		if got := field(example(t, s, id, "200"), "Name"); got != last {
			t.Errorf("%s expects Name %v, the last update sent %v", id, got, last)
		}
	}
	if got := field(example(t, s, "GetDocks", "200"), 0, "Name"); got != last {
		t.Errorf("GetDocks expects Name %v, want %v", got, last)
	}
	dockID := field(example(t, s, "GetDockById", "200"), "Id")
	code := field(example(t, s, "GetDock", "200"), "Code")
	if param(t, s, "GetDockById", "id") != dockID || param(t, s, "GetDock", "Code") != code || param(t, s, "GetShips", "Code") != code {
		t.Errorf("parameters: id %v Code %v, record Id %v Code %v", param(t, s, "GetDockById", "id"), param(t, s, "GetDock", "Code"), dockID, code)
	}
	if !strings.Contains(fmt.Sprint(code), "") || strings.ToLower(fmt.Sprint(code)) != fmt.Sprint(code) {
		t.Errorf("Code %v does not fit the pattern of {Code}", code)
	}
	if field(byID, "Code") != code || field(byCode, "Code") != code {
		t.Error("an update changed the key Code")
	}
	if got := field(example(t, s, "UpdateDock", "200"), "Id"); got != dockID {
		t.Errorf("Response.Id %v, want the Dock Id %v", got, dockID)
	}
	if got := field(example(t, s, "UpdateDock", "200"), "Message"); got != "Successfully updated Dock" {
		t.Errorf("message %v", got)
	}
	if got := field(example(t, s, "GetDock", "404"), "Message"); got != "Error while processing the request" {
		t.Errorf("shared error message %v", got)
	}
	// the Ship paths share {id} with the Dock paths: one of them is copied
	shipID := field(example(t, s, "GetShipById", "200"), "Id")
	if param(t, s, "GetShipById", "id") != shipID || param(t, s, "GetDockById", "id") != dockID {
		t.Errorf("{id}: ship %v (record %v), dock %v (record %v)", param(t, s, "GetShipById", "id"), shipID, param(t, s, "GetDockById", "id"), dockID)
	}
	if o.res.Stats.Inlined == 0 || !strings.Contains(notes(o.res), CodeInlined) {
		t.Errorf("no parameter was copied:\n%s", notes(o.res))
	}
	if got := field(example(t, s, "GetShips", "200"), 0, "DockId"); got != dockID {
		t.Errorf("the ship refers to Dock %v, want %v", got, dockID)
	}
	if len(p.dict.Records["Dock"]) != 1 || p.dict.Records["Dock"][0]["Id"] != dockID {
		t.Errorf("dictionary records: %v", p.dict.Records)
	}

	again := p.run()
	if again.text != o.text {
		t.Error("the second run changed the spec")
	}
}

// In apitest's default order the reads run before the updates and show
// the start record.
func TestRunDefaultOrder(t *testing.T) {
	o := newPipeline(t, `{}`).run()
	if len(o.problems) > 0 {
		t.Fatal(o.problems)
	}
	start := field(o.written.Op("GetDocks").Op.Responses.Value("200").Value.Content["application/json"].Example, 0, "Name")
	if got := field(example(t, o.written, "GetDock", "200"), "Name"); got != start {
		t.Errorf("GetDock expects %v, the start record has %v", got, start)
	}
}

// Verify finds an example that does not show the record of its position.
func TestVerifyFindsStaleExample(t *testing.T) {
	p := newPipeline(t, order)
	o := p.run()
	name := fmt.Sprint(field(example(t, o.written, "GetDock", "200"), "Name"))
	stale := strings.Replace(o.text, "Name: "+name+"\n", "Name: Old Dock\n", 1)
	if stale == o.text {
		t.Fatal("nothing replaced")
	}
	if err := os.WriteFile(p.path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	defs, _ := defaults.Parse([]byte(order))
	problems := Verify(p.load(p.path), defs, o.res.Records)
	if len(problems) == 0 || !strings.Contains(problems[0], CodeStale) || !strings.Contains(problems[0], `"Old Dock"`) || !strings.Contains(problems[0], "changed by Dock/UpdateDock/default") {
		t.Fatalf("problems: %v", problems)
	}
}

// fakeInstance serves docks and ships like a seeded test database.
func fakeInstance(t *testing.T, docks []map[string]any, detailName string) Fetcher {
	t.Helper()
	ships := []map[string]any{{"Id": 31, "Name": "Pilot Ship", "DockId": 7}}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	find := func(field string, v any) map[string]any {
		for _, d := range docks {
			if fmt.Sprint(d[field]) == fmt.Sprint(v) {
				out := map[string]any{}
				for k, x := range d {
					out[k] = x
				}
				if detailName != "" {
					out["Name"] = detailName
				}
				return out
			}
		}
		return nil
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		var v any
		switch {
		case len(segs) == 1 && segs[0] == "Dock":
			v = docks
		case len(segs) == 3 && segs[0] == "Dock" && segs[1] == "id":
			v = find("Id", segs[2])
		case len(segs) == 2 && segs[0] == "Dock":
			v = find("Code", segs[1])
		case len(segs) == 3 && segs[0] == "Dock" && segs[2] == "Ship":
			v = ships
		case len(segs) == 3 && segs[0] == "Ship":
			v = ships[0]
		}
		if m, ok := v.(map[string]any); v == nil || (ok && m == nil) {
			http.NotFound(w, r)
			return
		}
		write(w, v)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	opt := discover.Options{BaseURL: srv.URL, Client: srv.Client()}
	return func(ctx context.Context, path string) (any, error) { return discover.Get(ctx, opt, path) }
}

var seeded = []map[string]any{
	{"Id": 7, "Code": "abc", "Name": "Moon Dock"},
	{"Id": 8, "Code": "def", "Name": "Planet Dock"},
	{"Id": 9, "Code": "ghi", "Name": "Garden Dock"},
}

// The snapshot takes "count" elements of the list; the examples show the
// fetched records, the list example the fetched elements.
func TestRunSnapshot(t *testing.T) {
	p := newPipeline(t, `{"$snapshot": {"Dock": {"from": "GetDocks", "count": 2}}, "DockRead.Name": "Ignored"}`)
	p.fetch = fakeInstance(t, seeded, "")
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v", o.res.Problems, o.problems)
	}
	s := o.written
	list := example(t, s, "GetDocks", "200").([]any)
	if len(list) != 2 || field(list, 1, "Name") != "Planet Dock" || field(list, 0, "Code") != "abc" {
		t.Errorf("list: %v", list)
	}
	if param(t, s, "GetDock", "Code") != "abc" || field(example(t, s, "GetDock", "200"), "Name") != "Moon Dock" {
		t.Errorf("GetDock: %v %v", param(t, s, "GetDock", "Code"), example(t, s, "GetDock", "200"))
	}
	if got := field(example(t, s, "GetShipById", "200"), "Name"); got != "Pilot Ship" {
		t.Errorf("ship: %v", got)
	}
	all := notes(o.res)
	for _, want := range []string{"SNAPSHOT Dock: 2 of 3 elements from GET /Dock (GetDocks)", CodeSnapshotWins, `"DockRead.Name" = "Ignored"`} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
	if o.res.Stats.Fetched < 4 {
		t.Errorf("fetched %d", o.res.Stats.Fetched)
	}
}

// A key default selects the fetched record with that key.
func TestRunSnapshotKeyDefault(t *testing.T) {
	p := newPipeline(t, `{"GetDock.Code": "def"}`)
	p.fetch = fakeInstance(t, seeded, "")
	o := p.run()
	if len(o.res.Problems) > 0 {
		t.Fatal(o.res.Problems)
	}
	if got := field(example(t, o.written, "GetDockById", "200"), "Id"); fmt.Sprint(got) != "8" {
		t.Errorf("selected Dock %v, want 8", got)
	}

	p = newPipeline(t, `{"GetDock.Code": "zzz"}`)
	p.fetch = fakeInstance(t, seeded, "")
	if o := p.run(); len(o.res.Problems) == 0 || o.res.Problems[0].Code != CodeSnapshotKey {
		t.Errorf("problems: %v", o.res.Problems)
	}
}

func TestRunSnapshotProblems(t *testing.T) {
	for name, c := range map[string]struct {
		defs   string
		docks  []map[string]any
		detail string
		code   string
	}{
		"short":    {`{"$snapshot": {"Dock": {"from": "GetDocks", "count": 5}}}`, seeded, "", CodeSnapshotShort},
		"mismatch": {`{}`, seeded, "Other Name", CodeSnapshotDiff},
		"source":   {`{"$snapshot": {"Dock": {"from": "GetShips"}}}`, seeded, "", CodeSnapshotFail},
		"schema":   {`{}`, []map[string]any{{"Id": "seven", "Code": "abc", "Name": "x"}}, "", CodeSnapshotFail},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPipeline(t, c.defs)
			p.fetch = fakeInstance(t, c.docks, c.detail)
			o := p.run()
			if len(o.res.Problems) == 0 || o.res.Problems[0].Code != c.code {
				t.Fatalf("problems: %v", o.res.Problems)
			}
		})
	}
}

// A mismatch names the record, both requests as sent and every field that
// differs with both values, and how to fix it.
func TestRunSnapshotMismatchMessage(t *testing.T) {
	p := newPipeline(t, `{}`)
	p.fetch = fakeInstance(t, seeded, "Other Name")
	o := p.run()
	if len(o.res.Problems) == 0 {
		t.Fatal("no problem")
	}
	pr := o.res.Problems[0]
	for _, want := range []string{
		`for the same Dock (Id=7 Code="abc") disagree about 1 field`,
		"A  GET /Dock (GetDocks)",
		"B  GET /Dock/id/7 (GetDockById)",
		`Name  A "Moon Dock"  B "Other Name"`,
		`"IgnoreFields": ["Name"]`,
	} {
		if pr.Code != CodeSnapshotDiff || pr.Where != "Dock #1" || !strings.Contains(pr.Message, want) {
			t.Errorf("message lacks %q:\n%s", want, pr)
		}
	}
}

// mandatoryFields: an empty object has no value either.
func TestRunSnapshotMandatoryEmptyObject(t *testing.T) {
	pilots := []any{
		map[string]any{"Code": "a", "Detail": map[string]any{}},
		map[string]any{"Code": "b", "Detail": map[string]any{"License": map[string]any{"Expires": "2030-01-01"}}},
	}
	p := newPipelineFile(t, "mandatory.yaml", `{"$snapshot": {"Pilot": {"from": "/Pilot", "validation": {"mandatoryFields": ["Detail"]}}}}`)
	p.fetch = func(_ context.Context, path string) (any, error) {
		if path == "/Pilot" {
			return pilots, nil
		}
		return pilots[1], nil
	}
	o := p.run()
	if recs := o.res.Records.Records("Pilot"); len(o.res.Problems) > 0 || len(recs) != 1 || recs[0]["Code"] != "b" {
		t.Errorf("records %v, problems %v", recs, o.res.Problems)
	}
}

// An empty list means the test starts without such records.
func TestRunSnapshotEmpty(t *testing.T) {
	p := newPipeline(t, `{}`)
	p.fetch = fakeInstance(t, []map[string]any{}, "")
	o := p.run()
	if !strings.Contains(notes(o.res), CodeSnapshotEmpty) {
		t.Errorf("notes:\n%s", notes(o.res))
	}
	if len(o.res.Records.Records("Dock")) != 0 {
		t.Errorf("records: %v", o.res.Records.Records("Dock"))
	}
}

func TestRunFetchFails(t *testing.T) {
	p := newPipeline(t, `{}`)
	p.fetch = func(context.Context, string) (any, error) { return nil, errors.New("connection refused") }
	o := p.run()
	if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, "connection refused") {
		t.Fatalf("problems: %v", o.res.Problems)
	}
}

func TestOrderRejectsUnknownTag(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/records.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Order(s, defaults.Run{Tags: []string{"Nope"}}); err == nil || !strings.Contains(err.Error(), "$apitest") {
		t.Errorf("err = %v", err)
	}
}

// Key defaults set the keys of the first record: "/Planet/id/{id}" its Id,
// "Code" its Code; every example of the planet and its moons follows.
func TestRunKeyDefaults(t *testing.T) {
	o := newPipelineFile(t, "lists.yaml", `{"/Planet/id/{id}": 100, "Code": "terra"}`).run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v", o.res.Problems, o.problems)
	}
	s := o.written
	if got := example(t, s, "getPlanetById", "200"); field(got, "Id") != json.Number("100") || field(got, "Code") != "terra" {
		t.Errorf("planet by id: %v", got)
	}
	if got := param(t, s, "getPlanetById", "id"); got != json.Number("100") {
		t.Errorf("{id}: %v", got)
	}
	if got := field(example(t, s, "listMoonsOfPlanet", "200"), 0, "PlanetId"); got != json.Number("100") {
		t.Errorf("moon refers to planet %v", got)
	}

	conflict := newPipelineFile(t, "lists.yaml", `{"/Planet/id/{id}": 100, "getPlanetById.id": 5}`).run()
	if len(conflict.res.Problems) == 0 || conflict.res.Problems[0].Code != CodeDefault {
		t.Errorf("problems: %v", conflict.res.Problems)
	}
}

// "from" in "$snapshot" can be the request itself. Placeholders left in it
// are filled where a value is known; an unknown optional query parameter
// is dropped, an unknown path parameter stops the run.
func TestRunSnapshotURL(t *testing.T) {
	var asked []string
	fetch := func(_ context.Context, path string) (any, error) {
		asked = append(asked, path)
		dock := map[string]any{"Code": "abc", "Name": "Moon Dock"}
		switch strings.Split(path, "?")[0] {
		case "/DockPreset/Tier/A1":
			return []any{dock}, nil
		case "/Dock/abc":
			return dock, nil
		}
		return nil, errors.New("status 404")
	}
	p := newPipelineFile(t, "tiers.yaml", `{"$snapshot": {"Dock": {"from": "/DockPreset/Tier/A1?dockCode={dockCode}&pilotNumber=7", "$comment": "set by hand"}}}`)
	p.fetch = fetch
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v", o.res.Problems, o.problems)
	}
	if len(asked) == 0 || asked[0] != "/DockPreset/Tier/A1?pilotNumber=7" {
		t.Errorf("requests: %v", asked)
	}
	if got := field(example(t, o.written, "GetDock", "200"), "Name"); got != "Moon Dock" {
		t.Errorf("GetDock: %v", got)
	}

	for from, want := range map[string]string{
		"/DockPreset/Tier/{tier}": `{tier} in "/DockPreset/Tier/{tier}" has no value`,
		"GET /Nope/A1":            "is no GET of Dock",
	} {
		p := newPipelineFile(t, "tiers.yaml", `{"$snapshot": {"Dock": {"from": "`+from+`"}}}`)
		p.fetch = fetch
		o := p.run()
		if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, want) {
			t.Errorf("%s: problems %v", from, o.res.Problems)
		}
	}
}

// Without "$snapshot" a list below an unknown path parameter is no source:
// its value only the user knows.
func TestRunSnapshotUnknownParam(t *testing.T) {
	p := newPipelineFile(t, "tiers.yaml", `{}`)
	p.fetch = func(context.Context, string) (any, error) { return nil, errors.New("not asked") }
	o := p.run()
	if len(o.res.Problems) > 0 || !strings.Contains(notes(o.res), `"from": "/path?query"`) {
		t.Errorf("problems %v\n%s", o.res.Problems, notes(o.res))
	}
}

func TestMatchPath(t *testing.T) {
	for _, c := range []struct {
		template, path string
		want           bool
	}{
		{"/BookPreset/Tier/{tier}", "/BookPreset/Tier/A1", true},
		{"/BookPreset/Tier/{tier}", "/bookpreset/tier/{tier}", true},
		{"/Book/{Code}", "/Book/abc/Article", false},
		{"/Book/id/{id}", "/Book/x/7", false},
	} {
		if got := MatchPath(c.template, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v", c.template, c.path, got)
		}
	}
}

// "mandatoryFields": only elements with a value in every listed field
// become records. A segment is a field or a DTO name, which is looked up
// in the element and below it; in a list one element with a value counts.
func TestRunSnapshotMandatory(t *testing.T) {
	pilots := []any{
		map[string]any{"Code": "a", "Rank": nil, "Detail": map[string]any{"License": map[string]any{"Expires": "2030-01-01"}}},
		map[string]any{"Code": "b", "Rank": json.Number("2"), "Detail": map[string]any{"License": map[string]any{"Expires": nil}}},
		map[string]any{"Code": "c", "Rank": json.Number("3"), "Detail": map[string]any{"License": map[string]any{"Expires": "2031-01-01"}},
			"Ships": []any{map[string]any{"Callsign": nil}, map[string]any{"Callsign": "X1"}}},
		map[string]any{"Code": "d", "Rank": json.Number("4"), "Detail": map[string]any{"License": map[string]any{"Expires": "2032-01-01"}},
			"Ships": []any{map[string]any{"Callsign": ""}}},
	}
	fetch := func(_ context.Context, path string) (any, error) {
		if path == "/Pilot" {
			return pilots, nil
		}
		for _, p := range pilots {
			if "/Pilot/"+p.(map[string]any)["Code"].(string) == path {
				return p, nil
			}
		}
		return nil, errors.New("status 404")
	}
	run := func(mandatory string, count int) outcome {
		p := newPipelineFile(t, "mandatory.yaml", fmt.Sprintf(`{"$snapshot": {"Pilot": {"from": "/Pilot", "count": %d, "mandatoryFields": %s}}}`, count, mandatory))
		p.fetch = fetch
		return p.run()
	}
	codes := func(o outcome) string {
		var out []string
		for _, r := range o.res.Records.Records("Pilot") {
			out = append(out, fmt.Sprint(r["Code"]))
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		mandatory string
		count     int
		want      string
	}{
		{`[]`, 1, "a"},
		{`["rank", ""]`, 2, "b,c"},
		{`["PilotRead.Detail.License.Expires", "Rank"]`, 2, "c,d"},
		{`["License.Expires"]`, 3, "a,c,d"},
		{`["PilotDetail.License.Expires"]`, 1, "a"},
		{`["ShipInfo.Callsign"]`, 1, "c"},
		{`["Ships.Callsign"]`, 1, "c"},
		{`["pilot.rank"]`, 1, "b"},
		{`["Pilot.Detail.License.Expires", "Pilot.Rank"]`, 1, "c"},
	} {
		o := run(c.mandatory, c.count)
		if len(o.res.Problems) > 0 || codes(o) != c.want {
			t.Errorf("%s: records %q, want %q; problems %v", c.mandatory, codes(o), c.want, o.res.Problems)
		}
	}

	o := run(`["Ships.Callsign"]`, 2)
	if len(o.res.Problems) == 0 || o.res.Problems[0].Code != CodeSnapshotShort || !strings.Contains(o.res.Problems[0].Message, "1 pass all of it") {
		t.Errorf("short: %v", o.res.Problems)
	}
	o = run(`["Detail.Licence"]`, 1)
	if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, `"Detail.Licence" matches no field`) {
		t.Errorf("typo: %v", o.res.Problems)
	}
}

// Elements the validation rejects need not fit the schema: an empty object
// in the list is skipped, not a broken instance. A kept element that
// violates the schema still stops the run, unless IgnoreLinting.
func TestRunSnapshotValidationSkipsInvalid(t *testing.T) {
	list := []any{map[string]any{}, map[string]any{"Code": "a", "Rank": json.Number("1")}, map[string]any{}}
	fetch := func(_ context.Context, path string) (any, error) {
		if path == "/Pilot" {
			return list, nil
		}
		return list[1], nil
	}
	run := func(validation string, ignore bool) outcome {
		p := newPipelineFile(t, "mandatory.yaml", `{"$snapshot": {"Pilot": {"from": "/Pilot", "validation": `+validation+`}}}`)
		p.fetch = fetch
		p.ignoreLinting = ignore
		return p.run()
	}
	for _, v := range []string{`{"mandatoryFields": ["Code"]}`, `{"equalFields": {"Rank": 1}}`} {
		o := run(v, false)
		recs := o.res.Records.Records("Pilot")
		if len(o.res.Problems) > 0 || len(recs) != 1 || recs[0]["Code"] != "a" {
			t.Errorf("%s: records %v, problems %v", v, recs, o.res.Problems)
		}
	}
	o := run(`{"mandatoryFields": []}`, false)
	if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, "element 0 violates the schema") {
		t.Errorf("without validation: %v", o.res.Problems)
	}
	o = run(`{"mandatoryFields": []}`, true)
	if len(o.res.Problems) > 0 || !hasNote(o.res, CodeLint, "element 0 violates the schema") {
		t.Errorf("IgnoreLinting: problems %v, notes %v", o.res.Problems, o.res.Notes)
	}
}

func hasNote(res *Result, code, text string) bool {
	for _, n := range res.Notes {
		if n.Code == code && strings.Contains(n.Message, text) {
			return true
		}
	}
	return false
}

// Records that come from the followingDetails of another resource have no
// source of their own: completing them must not crash, and the request
// they came from is not sent again (with the key of the first parent it
// would mix up the records).
func TestRunSnapshotDetailRecordsComplete(t *testing.T) {
	asked := map[string]int{}
	fetch := func(_ context.Context, path string) (any, error) {
		asked[path]++
		switch path {
		case "/Ship":
			return []any{map[string]any{"Id": json.Number("4"), "Code": "dd", "Pilot": "tom"}, map[string]any{"Id": json.Number("5"), "Code": "ee", "Pilot": "tom"}}, nil
		case "/Ship/4", "/Ship/5":
			return map[string]any{"Id": json.Number(path[6:]), "Code": map[string]string{"4": "dd", "5": "ee"}[path[6:]], "Pilot": "tom"}, nil
		case "/Ship/4/details", "/Ship/5/details":
			return map[string]any{"Engine": "ion", "Decks": json.Number("3")}, nil
		case "/Ship/4/crew", "/Ship/5/crew":
			return map[string]any{"Captain": "Pilot " + path[6:7]}, nil
		}
		return nil, errors.New("status 404")
	}
	p := newPipelineFile(t, "details.yaml", `{"$snapshot": {"Ship": {"from": "/Ship", "count": 2, "validation": {"followingDetails": ["/Ship/{id}/crew"]}}}}`)
	p.fetch = fetch
	o := p.run()
	if len(o.res.Problems) > 0 {
		t.Fatalf("problems: %v", o.res.Problems)
	}
	if recs := o.res.Records.Records("CrewInfo"); len(recs) != 2 || recs[1]["Captain"] != "Pilot 5" {
		t.Errorf("crew records: %v", recs)
	}
	if asked["/Ship/4/crew"] != 1 || asked["/Ship/5/crew"] != 1 {
		t.Errorf("crew asked %d and %d times, want once each", asked["/Ship/4/crew"], asked["/Ship/5/crew"])
	}
}

// "validation": equalFields pick elements with a value, followingDetails
// must answer for an element with its values; the list is searched until
// "count" elements pass. The answers become the examples of their
// operations and the records of their resources.
func TestRunSnapshotValidation(t *testing.T) {
	ships := []any{
		map[string]any{"Id": json.Number("1"), "Code": "aa", "Pilot": "tom"}, // no details
		map[string]any{"Id": json.Number("2"), "Code": "bb", "Pilot": "ada"}, // wrong pilot
		map[string]any{"Id": json.Number("3"), "Code": "cc", "Pilot": "tom"}, // no price
		map[string]any{"Id": json.Number("4"), "Code": "dd", "Pilot": "tom"}, // passes
		map[string]any{"Id": json.Number("5"), "Code": "ee", "Pilot": "tom"}, // passes
		map[string]any{"Id": json.Number("6"), "Code": "ff", "Pilot": nil},   // no pilot
	}
	var asked []string
	fetch := func(_ context.Context, path string) (any, error) {
		asked = append(asked, path)
		switch path {
		case "/Ship":
			return ships, nil
		case "/Ship/3/details", "/Ship/4/details", "/Ship/5/details":
			return map[string]any{"Engine": "ion-" + path[6:7], "Decks": json.Number("3")}, nil
		case "/Ship/3/crew", "/Ship/4/crew", "/Ship/5/crew":
			return map[string]any{"Captain": "Pilot " + path[6:7]}, nil
		case "/Ship/dd/price", "/Ship/ee/price":
			return map[string]any{"Amount": json.Number("120.5"), "Currency": "EUR"}, nil
		case "/Ship/1/details":
			return map[string]any{}, nil
		}
		for _, s := range ships {
			if path == "/Ship/"+s.(map[string]any)["Id"].(json.Number).String() {
				return s, nil
			}
		}
		return nil, errors.New("status 404")
	}
	defs := `{"$snapshot": {"Ship": {"from": "/Ship", "count": 2, "validation": {
		"mandatoryFields": ["Ship.Pilot"],
		"equalFields": {"Ship.Pilot": "tom"},
		"followingDetails": ["/Ship/{id}/details", "/Ship/{id}/crew", "/Ship/{code}/price"]}}}}`
	p := newPipelineFile(t, "details.yaml", defs)
	p.fetch = fetch
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v\n%s", o.res.Problems, o.problems, notes(o.res))
	}
	var ids []string
	for _, r := range o.res.Records.Records("Ship") {
		ids = append(ids, fmt.Sprint(r["Id"]))
	}
	if strings.Join(ids, ",") != "4,5" {
		t.Errorf("records %v", ids)
	}
	for _, unasked := range []string{"/Ship/2/details", "/Ship/6/details", "/Ship/ee/price"} {
		for _, a := range asked {
			if a == unasked && unasked != "/Ship/ee/price" {
				t.Errorf("%s asked, the element failed a field check", a)
			}
		}
	}
	s := o.written
	if got := field(example(t, s, "GetShipDetails", "200"), "Engine"); got != "ion-4" {
		t.Errorf("details example: %v", example(t, s, "GetShipDetails", "200"))
	}
	if got := field(example(t, s, "GetShipPrice", "200"), "Currency"); got != "EUR" {
		t.Errorf("price example: %v", example(t, s, "GetShipPrice", "200"))
	}
	if param(t, s, "GetShipDetails", "id") != json.Number("4") || param(t, s, "GetShipPrice", "code") != "dd" {
		t.Errorf("parameters: %v %v", param(t, s, "GetShipDetails", "id"), param(t, s, "GetShipPrice", "code"))
	}
	// ShipDetail belongs to Ship (the stem): the details are part of the records
	if recs := p.dict.Records["Ship"]; len(recs) != 2 || recs[0]["Engine"] != "ion-4" || recs[1]["Engine"] != "ion-5" {
		t.Errorf("dictionary records: %v", p.dict.Records)
	}

	// CrewInfo is a resource of its own below Ship: one record per chosen ship
	if got := field(example(t, s, "GetShipCrew", "200"), "Captain"); got != "Pilot 4" || param(t, s, "GetShipCrew", "id") != json.Number("4") {
		t.Errorf("crew example: %v", example(t, s, "GetShipCrew", "200"))
	}
	if recs := p.dict.Records["CrewInfo"]; len(recs) != 2 || recs[1]["Captain"] != "Pilot 5" {
		t.Errorf("crew records: %v", p.dict.Records["CrewInfo"])
	}

	// too few: the reasons are listed
	p = newPipelineFile(t, "details.yaml", strings.Replace(defs, `"count": 2`, `"count": 3`, 1))
	p.fetch = fetch
	o = p.run()
	if len(o.res.Problems) == 0 || o.res.Problems[0].Code != CodeSnapshotShort ||
		!strings.Contains(o.res.Problems[0].Message, "6 returned, 4 pass the fields, 2 pass all of it") ||
		!strings.Contains(o.res.Problems[0].Message, "GET /Ship/{code}/price failed") {
		t.Errorf("short: %v", o.res.Problems)
	}

	// mistakes in the validation stop the run
	for bad, want := range map[string]string{
		`"equalFields": {"Ship.Captain": "tom"}`:       `equalFields: "Ship.Captain" matches no field`,
		`"followingDetails": ["/Ship/{id}/engine"]`:    `"/Ship/{id}/engine" is no GET of the spec`,
		`"followingDetails": ["/Ship/{hull}/details"]`: `{hull} in "/Ship/{hull}/details" is no field of Ship`,
	} {
		p := newPipelineFile(t, "details.yaml", `{"$snapshot": {"Ship": {"from": "/Ship", "validation": {`+bad+`}}}}`)
		p.fetch = fetch
		o := p.run()
		if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, want) {
			t.Errorf("%s: %v", bad, o.res.Problems)
		}
	}
}

// A "$snapshot" entry without "from" means: generate, fetch nothing. A list
// below such a resource is generated too, unless "from" names a request
// with real values.
func TestRunSnapshotWithoutFrom(t *testing.T) {
	var asked []string
	fetch := func(_ context.Context, path string) (any, error) {
		asked = append(asked, path)
		if strings.HasPrefix(path, "/Dock/abc/Ship") {
			return []any{map[string]any{"Id": json.Number("31"), "Name": "Pilot Ship", "DockId": json.Number("7")}}, nil
		}
		if path == "/Ship/id/31" {
			return map[string]any{"Id": json.Number("31"), "Name": "Pilot Ship", "DockId": json.Number("7")}, nil
		}
		return nil, errors.New("status 404")
	}
	p := newPipeline(t, `{"$snapshot": {"Dock": {"from": "", "count": 2}}}`)
	p.fetch = fetch
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 || len(asked) > 0 {
		t.Fatalf("problems %v %v, requests %v", o.res.Problems, o.problems, asked)
	}
	all := notes(o.res)
	for _, want := range []string{`GENERATED Dock: "$snapshot" has no "from"`, "GENERATED Ship: its list runs below Dock"} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
	if n := len(o.res.Records.Records("Dock")); n != 2 {
		t.Errorf("%d Dock records", n)
	}

	// a request with real values still fetches the ships
	p = newPipeline(t, `{"$snapshot": {"Dock": {"count": 1}, "Ship": {"from": "/Dock/abc/Ship"}}}`)
	p.fetch = fetch
	if o := p.run(); len(o.res.Problems) > 0 || o.res.Records.Records("Ship")[0]["Name"] != "Pilot Ship" {
		t.Errorf("problems %v, ships %v", o.res.Problems, o.res.Records.Records("Ship"))
	}
	// a placeholder for a generated key cannot be filled
	p = newPipeline(t, `{"$snapshot": {"Dock": {}, "Ship": {"from": "/Dock/{Code}/Ship"}}}`)
	p.fetch = fetch
	if o := p.run(); len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, `{Code} in "/Dock/{Code}/Ship" has no value`) {
		t.Errorf("problems %v", o.res.Problems)
	}
}

// The records of model.yaml: every path parameter gets the record of its
// resource, relations take the keys of the records they refer to, a write
// of no resource leaves the fields it changes out of the examples after
// it, and so does a key the server assigns.
func TestRunModelShapes(t *testing.T) {
	o := newPipelineFile(t, "model.yaml", order).run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems %v %v", o.res.Problems, o.problems)
	}
	s := o.written
	moon := example(t, s, "GetMoon", "200")
	planet := example(t, s, "GetPlanet", "200")
	if got, want := param(t, s, "GetGardensOfMoon", "moonCode"), field(moon, "MoonCode"); got != want {
		t.Errorf("GetGardensOfMoon {moonCode} = %v, the Moon has %v", got, want)
	}
	if got, want := param(t, s, "GetGardensOfMoon", "planetCode"), field(planet, "PlanetCode"); got != want {
		t.Errorf("GetGardensOfMoon {planetCode} = %v, the Planet has %v", got, want)
	}
	garden := field(example(t, s, "GetGardensOfMoon", "200"), 0)
	if field(garden, "MoonCode") != field(moon, "MoonCode") || field(garden, "MoonId") != field(moon, "Id") || field(garden, "PlanetCode") != field(planet, "PlanetCode") {
		t.Errorf("Garden %v does not refer to Moon %v and Planet %v", garden, moon, planet)
	}
	// CloseShip (PUT before GET) changes the ship in a way no model shows
	ship, ok := example(t, s, "GetShip", "200").(map[string]any)
	if _, has := ship["Mode"]; !ok || has || ship["ShipCode"] == nil {
		t.Errorf("GetShip after CloseShip: %v", ship)
	}
	// the server assigns the code of a created booking
	bookings := example(t, s, "GetBookings", "200")
	if field(bookings, 0, "BookingCode") == nil || field(bookings, 1, "Seats") == nil || field(bookings, 1, "BookingCode") != nil {
		t.Errorf("GetBookings after CreateBooking: %v", bookings)
	}
	all := notes(o.res)
	for _, want := range []string{
		"SIDE_EFFECT paths./Ship/{shipCode}/close.put: Ship/CloseShip/default changes records of Ship the model cannot follow; the examples after it leave out Ship.Mode",
		"SIDE_EFFECT paths./Dock/{dockCode}/Ship/{shipCode}/link.put: Dock/LinkShipToDock/default changes records of Dock, Ship the model cannot follow; the examples after it leave out Dock.ShipCode, Dock.Ships, Ship.DockCode",
		"CREATED_KEY paths./Booking.post: the server assigns Booking.BookingCode when CreateBooking runs; the examples of the cases that read the created Booking leave it out",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
}

// A key the default body of a create sends fits the path parameter that
// holds it later: Person.name has no pattern, {name} has one.
func TestRunCreatedKeyFitsParameter(t *testing.T) {
	o := newPipelineFile(t, "model.yaml", order).run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems %v %v", o.res.Problems, o.problems)
	}
	name, _ := field(example(t, o.written, "CreatePersonOfPlanet", ""), "name").(string)
	if len(name) != 5 || strings.ToLower(name) != name || strings.ContainsAny(name, "0123456789 ") {
		t.Errorf("CreatePersonOfPlanet sends name %q, {name} needs ^[a-z]{5}$", name)
	}
	if got := param(t, o.written, "UpdatePersonOfPlanet", "name"); got != name && got != field(o.res.Records.Records("PlanetPerson")[0], "name") {
		t.Errorf("UpdatePersonOfPlanet {name} = %v", got)
	}
}
