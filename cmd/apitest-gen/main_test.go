package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/record"
)

const shop = "../../testdata/gen/shop.yaml"

func cli(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestDictCreateUpdateDryRun(t *testing.T) {
	dictPath := filepath.Join(t.TempDir(), "global-dict.json")

	code, out, errOut := cli("dict", "-spec", shop, "-dict", dictPath)
	if code != 0 || !strings.Contains(out, "created") || errOut != "" {
		t.Fatalf("create: %d %q %q", code, out, errOut)
	}
	for _, want := range []string{"6 DTOs", "PATTERN_PENDING", "schemas.Garden.Address.Impossible", "TYPE_CONFLICT"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
		if strings.Contains(out, "\x1b[") {
			t.Fatal("colors in output that is no terminal")
		}
	}
	if strings.Contains(out, "VALUE_NEW") {
		t.Error("VALUE_NEW is only listed with -v")
	}
	first, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}

	// a second run keeps every value, even with another seed
	code, out, _ = cli("dict", "-spec", shop, "-dict", dictPath, "-seed", "99")
	if code != 0 || !strings.Contains(out, "updated") || !strings.Contains(out, "0 new") {
		t.Fatalf("update: %d %q", code, out)
	}
	second, _ := os.ReadFile(dictPath)
	if !bytes.Equal(first, second) {
		t.Error("an update without spec changes changed the dictionary")
	}

	// dry run writes nothing
	other := filepath.Join(t.TempDir(), "new.json")
	code, out, _ = cli("dict", "-spec", shop, "-dict", other, "-dry-run", "-v")
	if code != 0 || !strings.Contains(out, "dry run") || !strings.Contains(out, "VALUE_NEW") {
		t.Fatalf("dry run: %d %q", code, out)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("dry run wrote %s", other)
	}
}

func TestUsageAndErrors(t *testing.T) {
	if code, out, _ := cli("help"); code != 0 || !strings.Contains(out, "apitest-gen dict") || !strings.Contains(out, "-seed") {
		t.Errorf("help: %d %q", code, out)
	}
	if code, out, _ := cli(); code != 0 || !strings.Contains(out, "Usage") {
		t.Errorf("no arguments: %d %q", code, out)
	}
	if code, _, errOut := cli("frobnicate"); code != 2 || !strings.Contains(errOut, `unknown command "frobnicate"`) {
		t.Errorf("unknown command: %d %q", code, errOut)
	}
	if code, _, errOut := cli("dict"); code != 2 || !strings.Contains(errOut, "-spec is required") {
		t.Errorf("missing -spec: %d %q", code, errOut)
	}
	if code, _, errOut := cli("dict", "-spec", "does-not-exist.yaml"); code != 1 || errOut == "" {
		t.Errorf("missing spec file: %d %q", code, errOut)
	}
	broken := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := cli("dict", "-spec", shop, "-dict", broken); code != 1 || !strings.Contains(errOut, "dictionary") {
		t.Errorf("broken dictionary: %d %q", code, errOut)
	}
}

// copySpec copies a test spec into a temporary directory.
func copySpec(t *testing.T, name string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	b, err := os.ReadFile("../../testdata/gen/" + name)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestApplyInPlace(t *testing.T) {
	dir, path := copySpec(t, "apply.yaml")
	before, _ := os.ReadFile(path)
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(`{"PilotCode": "a1", "pilotId": 7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dictPath := filepath.Join(dir, "dict.json")

	// without a subcommand, apitest-gen runs apply
	code, out, errOut := cli("-spec", path, "-dict", dictPath, "-defaults", defs)
	if code != 0 || errOut != "" {
		t.Fatalf("apply: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(out, "dictionary "+dictPath+" created") || !strings.Contains(out, "examples added") {
		t.Errorf("output:\n%s", out)
	}
	after, _ := os.ReadFile(path)
	if string(after) == string(before) || !strings.Contains(string(after), "PilotCode: a1") {
		t.Errorf("spec not written:\n%s", after)
	}
	if _, err := os.Stat(dictPath); err != nil {
		t.Errorf("dictionary not written: %v", err)
	}

	// a second run changes nothing
	if code, _, _ := cli("apply", "-spec", path, "-dict", dictPath, "-defaults", defs); code != 0 {
		t.Fatalf("second run: %d", code)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(after) {
		t.Error("the second run changed the spec")
	}
}

func TestApplyOutDryRunAndFatal(t *testing.T) {
	dir, path := copySpec(t, "apply.yaml")
	before, _ := os.ReadFile(path)
	out := filepath.Join(dir, "out.yaml")
	if code, _, errOut := cli("-spec", path, "-dict", filepath.Join(dir, "d.json"), "-defaults", filepath.Join(dir, "none.json"), "-out", out); code != 0 {
		t.Fatalf("-out: %d %q", code, errOut)
	}
	if now, _ := os.ReadFile(path); string(now) != string(before) {
		t.Error("-out changed the source spec")
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("-out not written: %v", err)
	}

	dry := filepath.Join(dir, "dry.json")
	code, stdout, _ := cli("-spec", path, "-dict", dry, "-dry-run", "-v")
	if code != 0 || !strings.Contains(stdout, "dry run: nothing written") || !strings.Contains(stdout, "EXAMPLE_ADDED") {
		t.Errorf("dry run: %d\n%s", code, stdout)
	}
	if _, err := os.Stat(dry); !os.IsNotExist(err) {
		t.Error("dry run wrote the dictionary")
	}
	if now, _ := os.ReadFile(path); string(now) != string(before) {
		t.Error("dry run changed the spec")
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"PilotCode": "longer than ten characters"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, errOut := cli("-spec", path, "-dict", filepath.Join(dir, "d2.json"), "-defaults", bad)
	if code != 1 || !strings.Contains(stdout, "FATAL DEFAULT_INVALID") || !strings.Contains(errOut, "nothing was written") {
		t.Errorf("fatal: %d\n%s\n%s", code, stdout, errOut)
	}
	if now, _ := os.ReadFile(path); string(now) != string(before) {
		t.Error("a fatal run changed the spec")
	}
	if _, err := os.Stat(filepath.Join(dir, "d2.json")); !os.IsNotExist(err) {
		t.Error("a fatal run wrote the dictionary")
	}
}

func TestDiscoverAndApply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer qa-token" || r.Header.Get("X-Tenant") != "t1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"Code": "old", "Active": false}, {"Code": "qa1", "Active": true}]`))
	}))
	defer srv.Close()
	t.Setenv("QA_TOKEN", "qa-token")

	dir, path := copySpec(t, "apply.yaml")
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(`{"PilotCode": {"from": "GET /pilots", "pick": "/[Active=true]/Code"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(dir, "qa.json")

	code, out, errOut := cli("discover", "-defaults", defs, "-base-url", srv.URL, "-token-env", "QA_TOKEN", "-header", "X-Tenant: t1", "-out", resolved)
	if code != 0 || !strings.Contains(out, `PilotCode = "qa1" (GET /pilots)`) {
		t.Fatalf("discover: %d %q %q", code, out, errOut)
	}
	b, _ := os.ReadFile(resolved)
	if strings.Contains(string(b), "qa-token") || !strings.Contains(string(b), `"PilotCode": "qa1"`) {
		t.Errorf("resolved file:\n%s", b)
	}

	// apply with the resolved file after the defaults
	if code, out, errOut := cli("-spec", path, "-dict", filepath.Join(dir, "d.json"), "-defaults", defs+","+resolved); code != 0 || strings.Contains(out, "SOURCE_UNRESOLVED") {
		t.Fatalf("apply with file: %d\n%s\n%s", code, out, errOut)
	}
	if spec, _ := os.ReadFile(path); !strings.Contains(string(spec), "PilotCode: qa1") {
		t.Errorf("value not in the spec:\n%s", spec)
	}

	// apply with -base-url fetches in memory; without, it warns
	_, path2 := copySpec(t, "apply.yaml")
	if code, out, _ := cli("-spec", path2, "-dict", filepath.Join(dir, "d2.json"), "-defaults", defs, "-base-url", srv.URL, "-token-env", "QA_TOKEN", "-header", "X-Tenant: t1"); code != 0 || !strings.Contains(out, "SOURCE_RESOLVED") {
		t.Fatalf("apply -base-url: %d\n%s", code, out)
	}
	_, path3 := copySpec(t, "apply.yaml")
	if code, out, _ := cli("-spec", path3, "-dict", filepath.Join(dir, "d3.json"), "-defaults", defs); code != 0 || !strings.Contains(out, "SOURCE_UNRESOLVED") {
		t.Errorf("unresolved: %d\n%s", code, out)
	}

	// errors
	if code, _, errOut := cli("discover", "-defaults", defs); code != 2 || !strings.Contains(errOut, "-base-url is required") {
		t.Errorf("missing base url: %d %q", code, errOut)
	}
	if code, _, errOut := cli("discover", "-defaults", defs, "-base-url", srv.URL, "-token-env", "NOT_SET_ANYWHERE"); code != 1 || !strings.Contains(errOut, "NOT_SET_ANYWHERE is empty") {
		t.Errorf("empty token env: %d %q", code, errOut)
	}
	if code, _, errOut := cli("discover", "-defaults", defs, "-base-url", srv.URL, "-header", "broken"); code != 2 || !strings.Contains(errOut, "Name: value") {
		t.Errorf("bad header: %d %q", code, errOut)
	}
}

func TestCheck(t *testing.T) {
	code, out, errOut := cli("check", "-spec", "../../testdata/gen/apply.yaml", "-defaults", "none.json")
	if code != 1 || !strings.Contains(out, "NOT_BUILDABLE") || !strings.Contains(out, "EXAMPLE_SCHEMA") || !strings.Contains(errOut, "problems") {
		t.Errorf("check before apply: %d\n%s\n%s", code, out, errOut)
	}
	dir, path := copySpec(t, "apply.yaml")
	code, out, _ = cli("-spec", path, "-dict", filepath.Join(dir, "d.json"), "-defaults", filepath.Join(dir, "none.json"), "-check")
	if code != 0 || !strings.Contains(out, "check: 5 of 5 cases can be sent, 0 problems") {
		t.Errorf("apply -check: %d\n%s", code, out)
	}
	if code, out, _ := cli("check", "-spec", path); code != 0 || !strings.Contains(out, "0 problems") {
		t.Errorf("check after apply: %d\n%s", code, out)
	}
	if code, _, errOut := cli("check", "-spec", "missing.yaml"); code != 1 || errOut == "" {
		t.Errorf("missing spec: %d %q", code, errOut)
	}
}

// A default that does not fit the written spec stops the run before
// anything is saved: spec, dictionary and defaults stay as they are.
func TestApplyVerifyFailsWritesNothing(t *testing.T) {
	dir, path := copySpec(t, "lists.yaml")
	before, _ := os.ReadFile(path)
	defs := filepath.Join(dir, "defaults.json")
	bad := `{"getPlanetById.id": {"bind": "listPlanets", "pointer": "/0/Nope"}, "noSuchField": 1}`
	if err := os.WriteFile(defs, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	dictPath := filepath.Join(dir, "dict.json")
	code, out, errOut := cli("-spec", path, "-dict", dictPath, "-defaults", defs)
	if code != 1 || !strings.Contains(errOut, "nothing was written") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"BIND_UNVERIFIED", `nothing at /0/Nope`, "DEFAULT_UNUSED", `"noSuchField"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the spec was changed")
	}
	if _, err := os.Stat(dictPath); err == nil {
		t.Error("the dictionary was written")
	}
	if after, _ := os.ReadFile(defs); string(after) != bad {
		t.Error("the defaults were changed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".apitest-gen-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}

	// with the right pointer everything is written and verified
	if err := os.WriteFile(defs, []byte(`{"getPlanetById.id": {"bind": "listPlanets", "pointer": "/0/Id"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = cli("-spec", path, "-dict", dictPath, "-defaults", defs)
	if code != 0 || !strings.Contains(out, "verify: 1 defaults entries and the examples of") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if after, _ := os.ReadFile(path); string(after) == string(before) {
		t.Error("the spec was not written")
	}
}

// With -base-url the records come from the running instance; a failing
// request stops the run before anything is written.
func TestApplySnapshot(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		dock := `{"Id": 7, "Code": "abc", "Name": "Moon Dock"}`
		switch r.URL.Path {
		case "/Dock":
			_, _ = w.Write([]byte("[" + dock + "]"))
		case "/Dock/id/7", "/Dock/abc":
			_, _ = w.Write([]byte(dock))
		case "/Dock/abc/Ship":
			_, _ = w.Write([]byte(`[{"Id": 31, "Name": "Pilot Ship", "DockId": 7}]`))
		case "/Ship/id/31":
			_, _ = w.Write([]byte(`{"Id": 31, "Name": "Pilot Ship", "DockId": 7}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir, path := copySpec(t, "records.yaml")
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(`{"$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dictPath := filepath.Join(dir, "dict.json")

	up = false
	before, _ := os.ReadFile(path)
	code, out, errOut := cli("-spec", path, "-dict", dictPath, "-defaults", defs, "-base-url", srv.URL)
	if code != 1 || !strings.Contains(out, "SNAPSHOT_FAILED") || !strings.Contains(errOut, "nothing was written") {
		t.Fatalf("instance down: %d\n%s\n%s", code, out, errOut)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the spec was changed")
	}

	up = true
	code, out, errOut = cli("-spec", path, "-dict", dictPath, "-defaults", defs, "-base-url", srv.URL)
	if code != 0 || !strings.Contains(out, "records: 2 resources, 2 records (snapshot of "+srv.URL) {
		t.Fatalf("snapshot: %d\n%s\n%s", code, out, errOut)
	}
	written, _ := os.ReadFile(path)
	for _, want := range []string{"Code: abc", "Id: 31", "Message: Successfully updated Dock"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("spec lacks %q", want)
		}
	}
	if d, _ := os.ReadFile(dictPath); !strings.Contains(string(d), `"records"`) {
		t.Errorf("dictionary without records:\n%s", d)
	}

	// review shows the model and proposes nothing for the record keys
	code, out, _ = cli("review", "-spec", path, "-dict", dictPath, "-defaults", defs, "-dry-run")
	if code != 0 || !strings.Contains(out, "Dock: schemas DockRead, DockUpdate; keys Id, Code") {
		t.Errorf("review: %d\n%s", code, out)
	}
}

// With -base-url nothing comes from the examples of the spec: neither the
// query of the snapshot nor the request bodies.
func TestApplySnapshotIgnoresSpecExamples(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		dock := `{"Id": 7, "Code": "abc", "Name": "Moon Dock"}`
		switch r.URL.Path {
		case "/Dock":
			_, _ = w.Write([]byte("[" + dock + "]"))
		case "/Dock/id/7", "/Dock/abc":
			_, _ = w.Write([]byte(dock))
		case "/Dock/abc/Ship":
			_, _ = w.Write([]byte(`[{"Id": 31, "Name": "Pilot Ship", "DockId": 7}]`))
		case "/Ship/id/31":
			_, _ = w.Write([]byte(`{"Id": 31, "Name": "Pilot Ship", "DockId": 7}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir, path := copySpec(t, "records.yaml")
	src, _ := os.ReadFile(path)
	spec := strings.Replace(string(src), "      operationId: GetDocks\n",
		"      operationId: GetDocks\n      parameters:\n        - { name: zone, in: query, schema: { type: string }, example: SpecZone }\n", 1)
	spec = strings.Replace(spec, `            schema: { $ref: "#/components/schemas/DockUpdate" }`+"\n",
		`            schema: { $ref: "#/components/schemas/DockUpdate" }`+"\n            example: { Name: Spec Garden, Code: spec }\n", 1)
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := cli("-spec", path, "-dict", filepath.Join(dir, "dict.json"), "-defaults", filepath.Join(dir, "none.json"), "-base-url", srv.URL)
	if code != 0 {
		t.Fatalf("%d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(strings.Join(queries, "&"), "SpecZone") {
		t.Errorf("the snapshot used the example of the spec: %q", queries)
	}
	if written, _ := os.ReadFile(path); strings.Contains(string(written), "Spec Garden") {
		t.Errorf("the example of the spec was kept:\n%s", written)
	}
}

// -ignorelinting lets data that violates its schema through as notes;
// -debug saves the dictionary of a failed run.
func TestApplyIgnoreLintingAndDebug(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		dock := `{"Id": 7, "Code": "abc", "Name": "Moon Dock with a name longer than forty characters"}`
		switch r.URL.Path {
		case "/Dock":
			_, _ = w.Write([]byte("[" + dock + "]"))
		case "/Dock/id/7", "/Dock/abc":
			_, _ = w.Write([]byte(dock))
		case "/Dock/abc/Ship":
			_, _ = w.Write([]byte(`[{"Id": 31, "Name": "Pilot Ship", "DockId": 7}]`))
		case "/Ship/id/31":
			_, _ = w.Write([]byte(`{"Id": 31, "Name": "Pilot Ship", "DockId": 7}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir, path := copySpec(t, "records.yaml")
	none := filepath.Join(dir, "none.json")
	dictPath := filepath.Join(dir, "dict.json")

	code, out, _ := cli("-spec", path, "-dict", dictPath, "-defaults", none, "-base-url", srv.URL)
	if code != 1 || !strings.Contains(out, "violates the schema") {
		t.Fatalf("without -ignorelinting: %d\n%s", code, out)
	}
	code, out, errOut := cli("-spec", path, "-dict", dictPath, "-defaults", none, "-base-url", srv.URL, "-ignorelinting")
	if code != 0 || !strings.Contains(out, "LINT_IGNORED") {
		t.Fatalf("-ignorelinting: %d\n%s\n%s", code, out, errOut)
	}
	if written, _ := os.ReadFile(path); !strings.Contains(string(written), "longer than forty") {
		t.Error("the fetched record was not written")
	}

	up = false
	dir, path = copySpec(t, "records.yaml")
	before, _ := os.ReadFile(path)
	dictPath = filepath.Join(dir, "dict.json")
	code, out, _ = cli("-spec", path, "-dict", dictPath, "-defaults", none, "-base-url", srv.URL, "-debug")
	if code != 1 || !strings.Contains(out, "debug: "+dictPath+" saved despite the error") {
		t.Fatalf("-debug: %d\n%s", code, out)
	}
	if _, err := os.Stat(dictPath); err != nil {
		t.Errorf("-debug did not save the dictionary: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("-debug changed the spec")
	}
}

// record reads a GET-only instance, writes the examples, the lock file and
// the seed, and a second run leaves the spec alone.
func TestRecordCommand(t *testing.T) {
	data := map[string]string{
		"/Planet":                   `[{"id": 7, "planetCode": "P1", "name": "Mars"}]`,
		"/Planet/P1":                `{"id": 7, "planetCode": "P1", "name": "Mars"}`,
		"/Planet/P1/Dock":           `[{"id": 31, "dockCode": "D2", "planetId": 7, "name": "South"}]`,
		"/Planet/P1/Ship":           `[{"id": 101, "shipCode": "S1", "dockId": 31, "name": "Falcon"}]`,
		"/Ship/id/101":              `{"id": 101, "shipCode": "S1", "dockId": 31, "name": "Falcon"}`,
		"/Planet/P1/Dock/D2/Config": `{"settings": {"mode": "auto"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := data[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	dir, spec := copySpec(t, "record.yaml")
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(`{"params": {"planetCode": "P1"}, "seed": ["Planet", "Dock"], "$apitest": {"DeleteLast": true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"record", "-spec", spec, "-base-url", srv.URL, "-defaults", defs, "-read-only"}
	code, out, errOut := cli(args...)
	if code != 0 || errOut != "" {
		t.Fatalf("record: %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"REQUESTS to " + srv.URL, "── Dock", "#001 GET", "record: 13 operations, 13 with new or changed schemas",
		"FINDINGS", "WHAT THE CODES MEAN", "examples written", `"$recorded" updated`, "2 seed records"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	b, err := os.ReadFile(spec)
	if err != nil || !strings.Contains(string(b), "shipCode: S1") {
		t.Errorf("spec without the examples: %v", err)
	}
	if b, err := os.ReadFile(defs); err != nil || !strings.Contains(string(b), `"$recorded": {`) || !strings.HasPrefix(string(b), `{"params": {"planetCode": "P1"}`) {
		t.Errorf("defaults file: %s %v", b, err)
	}
	code, out, _ = cli(args...)
	if code != 0 || !strings.Contains(out, "10 unchanged") || !strings.Contains(out, "0 examples written") {
		t.Errorf("second run: %d\n%s", code, out)
	}
	if code, _, errOut := cli("record", "-spec", spec); code != 2 || !strings.Contains(errOut, "-base-url is required") {
		t.Errorf("without -base-url: %d %q", code, errOut)
	}
	if err := os.WriteFile(defs, []byte(`{"$snapshot": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := cli(args...); code != 1 || !strings.Contains(errOut, "belongs to the format of") {
		t.Errorf("old format: %d %q", code, errOut)
	}
}

// record -show-bodies writes every request with its body and answer, the
// findings and the summary into record-log.html in the current directory;
// the console shows only a short report.
func TestRecordShowBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{
			"/Planet":         `[{"id": 7, "planetCode": "P1", "name": "Mars <b>"}]`,
			"/Planet/P1":      `{"id": 7, "planetCode": "P1", "name": "Mars <b>"}`,
			"/Planet/P1/Dock": `[{"id": 31, "dockCode": "D2", "planetId": 7, "name": "South"}]`,
		}[r.URL.Path]
		if body == "" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	dir, spec := copySpec(t, "record.yaml")
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(`{"params": {"planetCode": "P1"}, "seed": ["Planet"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	code, out, errOut := cli("record", "-spec", spec, "-base-url", srv.URL, "-defaults", defs, "-read-only", "-show-bodies")
	if code != 0 || errOut != "" {
		t.Fatalf("record: %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"apitest-gen record: ", "13 operations", "findings: ", "examples written", "log: " + filepath.Join(dir, "record-log.html")} {
		if !strings.Contains(out, want) {
			t.Errorf("console misses %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"REQUESTS", "#001", "FINDINGS", "FETCH_FAILED", "Mars"} {
		if strings.Contains(out, not) {
			t.Errorf("console shows %q:\n%s", not, out)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "record-log.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, want := range []string{"<details", "#001", "/Planet/P1/Dock", "Findings", "FETCH_FAILED", "What the codes mean",
		`<span class="j-key">&#34;name&#34;</span>: <span class="j-str">&#34;Mars \u003cb\u003e&#34;</span>`, `class="req failed" open`, "examples written"} {
		if !strings.Contains(page, want) {
			t.Errorf("log misses %q", want)
		}
	}
	if strings.Contains(page, "Mars <b>") {
		t.Error("the log does not escape the answers")
	}
}

// record -ignorelinting writes data that violates its schema and prints
// none of the violations; without it they stop the run.
func TestRecordIgnoreLinting(t *testing.T) {
	data := map[string]string{
		"/Planet":         `[{"id": 7, "planetCode": "P1", "name": 5}]`,
		"/Planet/P1":      `{"id": 7, "planetCode": "P1", "name": 5}`,
		"/Planet/P1/Dock": `[{"id": 31, "dockCode": "D2", "planetId": 7, "name": "South"}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := data[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	dir, spec := copySpec(t, "record.yaml")
	defs := filepath.Join(dir, "defaults.json")
	if err := os.WriteFile(defs, []byte(`{"params": {"planetCode": "P1"}, "seed": ["Planet", "Dock"], "$apitest": {"DeleteLast": true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"record", "-spec", spec, "-base-url", srv.URL, "-defaults", defs, "-read-only"}
	if code, out, _ := cli(args...); code != 1 || !strings.Contains(out, "EXAMPLE_INVALID") {
		t.Fatalf("without -ignorelinting: %d\n%s", code, out)
	}
	code, out, _ := cli(append(args, "-ignorelinting")...)
	if strings.Contains(out, "LINT_IGNORED") || strings.Contains(out, "violates the schema") {
		t.Errorf("-ignorelinting prints the violations: %d\n%s", code, out)
	}
	if !strings.Contains(out, "examples written") {
		t.Errorf("-ignorelinting: %d\n%s", code, out)
	}
}

func TestTable(t *testing.T) {
	t.Setenv("COLUMNS", "60")
	var b strings.Builder
	table(&b, style{}, []string{"CODE", "WHERE", "MESSAGE"}, [][]cell{
		{{"NO_DATA", red}, {"GetShip", ""}, {"not read (status 404); it gets no example, and this line is long enough to wrap", ""}},
		{{"BUILT", ""}, {"CreateDockShip", ""}, {"short", ""}},
	})
	want := `  CODE     WHERE           MESSAGE
  NO_DATA  GetShip         not read (status 404); it gets no
                           example, and this line is long
                           enough to wrap
  BUILT    CreateDockShip  short
`
	if b.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", b.String(), want)
	}
	if got := (style{on: true}).paint(red, "x"); got != "\x1b[31mx\x1b[0m" {
		t.Errorf("paint %q", got)
	}
}

func TestLogEntryBodies(t *testing.T) {
	e := record.Entry{N: 3, Tag: "Dock", Method: "PUT", URL: "/Dock/D1", Why: "update", Status: 200, Body: map[string]any{"name": "North"}}
	var b strings.Builder
	tag := ""
	logEntry(&b, style{}, e, &tag)
	if strings.Contains(b.String(), "sent:") || !strings.Contains(b.String(), "── Dock") || !strings.Contains(b.String(), "#003 PUT    200 /Dock/D1") {
		t.Errorf("a write that succeeded:\n%s", b.String())
	}
	b.Reset()
	e.Status, e.Resp = 409, map[string]any{"message": "exists"}
	logEntry(&b, style{}, e, &tag)
	if !strings.Contains(b.String(), `sent:   {"name":"North"}`) || !strings.Contains(b.String(), `answer: {"message":"exists"}`) || strings.Contains(b.String(), "── Dock") {
		t.Errorf("a write that failed:\n%s", b.String())
	}
	b.Reset()
	e.Origin = []string{`{dockCode} = D1 ← "params".dockCode`, "body ← the dock selected from #2 GET /Planet/P1/Dock (GetDocks)"}
	logEntry(&b, style{}, e, &tag)
	if !strings.Contains(b.String(), `from:   {dockCode} = D1 ← "params".dockCode`) || !strings.Contains(b.String(), "from:   body ← the dock selected from #2") {
		t.Errorf("a write that failed, with its origin:\n%s", b.String())
	}
	b.Reset()
	probe := record.Entry{N: 4, Tag: "Dock", Method: "GET", URL: "/Dock/D1/Config", Why: "details", Status: 404, Resp: map[string]any{"message": "none"}, Probe: true, Origin: e.Origin}
	logEntry(&b, style{}, probe, &tag)
	if strings.Contains(b.String(), "answer:") || strings.Contains(b.String(), "from:") || !strings.Contains(b.String(), "#004 GET    404 /Dock/D1/Config") {
		t.Errorf("a select check that rejects a candidate:\n%s", b.String())
	}
}

// In the HTML log a select check that rejects a candidate is no failure:
// it stays closed, shows its verdict and counts in no "failed".
func TestRunLogProbe(t *testing.T) {
	l := &runLog{Entries: []record.Entry{
		{N: 1, Tag: "Dock", Method: "GET", URL: "/Dock/D1/Config", Status: 404, Probe: true, Origin: []string{"{dockCode} = D1 ← x"}},
		{N: 2, Tag: "Dock", Method: "PUT", URL: "/Dock/D2", Status: 500, Origin: []string{"body ← the dock selected from #3"}},
	}, Probes: map[int]string{1: "the dock is rejected, the next one is checked: #1 GET /Dock/D1/Config answers 404"}}
	v := l.view()
	if len(v.Tags) != 1 || v.Tags[0].Failed != 1 {
		t.Fatalf("tags: %+v", v.Tags)
	}
	p, w := v.Tags[0].Requests[0], v.Tags[0].Requests[1]
	if p.Failed || p.StatusClass != "probe" || !strings.Contains(p.Verdict, "rejected") || !w.Failed || w.StatusClass != "fail" {
		t.Errorf("probe %+v, write %+v", p, w)
	}
	path, err := l.write(filepath.Join(t.TempDir(), "log.html"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{"select check", "where the values come from", "body ← the dock selected from #3", "the next one is checked"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log misses %q", want)
		}
	}
}
