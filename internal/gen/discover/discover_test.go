package discover

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
)

func TestResolveInDependencyOrder(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Planet":
			_, _ = w.Write([]byte(`[{"Code": "x", "Active": false}, {"Code": "r", "Active": true}]`))
		case "/Planet/r/Moon":
			_, _ = w.Write([]byte(`[{"OrbitCode": "de", "Id": 7}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// the dependent source comes first in the file
	defs, err := defaults.Parse([]byte(`{
		"orbitCode": {"from": "GET /Planet/{planetCode}/Moon", "pick": "/0/OrbitCode"},
		"moonId": {"from": "GET /Planet/{planetCode}/Moon", "pick": "/[OrbitCode=de]/Id"},
		"planetCode": {"from": "GET /Planet", "pick": "/[Active=true]/Code"},
		"PilotCode": "a"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(t.Context(), defs, Options{BaseURL: srv.URL + "/", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Key != "planetCode" || got[0].Value != "r" {
		t.Fatalf("resolved: %+v", got)
	}
	if e := defs.Plain("orbitCode"); e == nil || e.Value != "de" {
		t.Errorf("orbitCode: %+v", e)
	}
	if e := defs.Plain("moonId"); e == nil || e.Value != json.Number("7") {
		t.Errorf("moonId: %+v", e)
	}
	for _, s := range seen {
		if !strings.HasPrefix(s, "GET ") || !strings.HasSuffix(s, "Bearer secret") {
			t.Errorf("request %q", s)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/empty" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	for name, tc := range map[string]struct{ defs, want string }{
		"cycle":       {`{"a": {"from": "GET /x/{b}", "pick": "/0"}, "b": {"from": "GET /y/{a}", "pick": "/0"}}`, "cycle"},
		"placeholder": {`{"a": {"from": "GET /x/{unknown}", "pick": "/0"}}`, "no value for unknown"},
		"status":      {`{"a": {"from": "GET /fails", "pick": "/0"}}`, "status 500"},
		"empty":       {`{"a": {"from": "GET /empty", "pick": "/0/Code"}}`, "not in a list of 0"},
	} {
		defs, err := defaults.Parse([]byte(tc.defs))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(t.Context(), defs, Options{BaseURL: srv.URL}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	defs, _ := defaults.Parse([]byte(`{"a": {"from": "GET /x", "pick": "/0"}}`))
	if _, err := Resolve(t.Context(), defs, Options{}); err == nil || !strings.Contains(err.Error(), "-base-url") {
		t.Errorf("no base URL: %v", err)
	}
}

func TestPick(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"items": [{"id": 1, "tags": {"a/b": "slash"}}, {"id": 2, "on": true}], "n": null}`), &doc)
	for pick, want := range map[string]any{
		"/items/1/id":             float64(2),
		"/items/[on=true]/id":     float64(2),
		"/items/[id=1]/tags/a~1b": "slash",
	} {
		if got, err := Pick(doc, pick); err != nil || got != want {
			t.Errorf("%s: %v %v", pick, got, err)
		}
	}
	for pick, want := range map[string]string{
		"items":         "must start with /",
		"/items/9":      "not in a list",
		"/missing":      "no field",
		"/items/[id]":   "[Field=value]",
		"/items/[id=5]": "no element",
		"/n":            "null",
		"/items/0/id/x": "cannot select",
	} {
		if _, err := Pick(doc, pick); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", pick, err, want)
		}
	}
}
