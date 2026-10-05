package exec

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

func setup(t *testing.T) (*spec.Spec, map[string]*cases.Case) {
	t.Helper()
	s, err := spec.Load(t.Context(), "../../testdata/specs/exec.yaml")
	if err != nil {
		t.Fatal(err)
	}
	list, err := cases.Build(s, cases.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*cases.Case{}
	for _, c := range list {
		m[c.Op.ID] = c
	}
	return s, m
}

func prepare(t *testing.T, s *spec.Spec, c *cases.Case) (*Prepared, error) {
	t.Helper()
	return Prepare(c, Input{
		Base:    "http://127.0.0.1:1/api/v3",
		Auth:    ResolveAuth(c.Op.Security, s.Doc.Components.SecuritySchemes),
		Token:   "tok",
		Headers: map[string]string{"X-Extra": "1"},
	})
}

func TestPrepareAllParameterLocations(t *testing.T) {
	s, m := setup(t)
	p, err := prepare(t, s, m["updateBook"])
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != http.MethodPut || p.URL != "http://127.0.0.1:1/api/v3/books/42?tags=a&tags=b" {
		t.Errorf("request line: %s %s", p.Method, p.URL)
	}
	checks := map[string]string{
		"Authorization": "Bearer tok",
		"X-Trace":       "t-1",
		"Cookie":        "lang=de",
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"X-Extra":       "1",
	}
	for k, want := range checks {
		if got := p.Header.Get(k); got != want {
			t.Errorf("header %s: got %q, want %q", k, got, want)
		}
	}
	if string(p.Body) != `{"title":"Go"}` {
		t.Errorf("body: got %s", p.Body)
	}
}

func TestTPL1_09_RequiredParameterWithoutValue(t *testing.T) {
	s, m := setup(t)
	_, err := prepare(t, s, m["getMissing"])
	var nb *NotBuildableError
	if !errors.As(err, &nb) || !strings.Contains(nb.Reason, `"id"`) {
		t.Fatalf("got %v, want NOT_BUILDABLE naming parameter id", err)
	}
}

func TestTPL1_31_SecuritySchemes(t *testing.T) {
	s, m := setup(t)
	tests := []struct {
		op     string
		check  func(p *Prepared) bool
		reason string
	}{
		{"getStats", func(p *Prepared) bool {
			return strings.HasSuffix(p.URL, "/stats?api_key=tok") && p.Header.Get("Authorization") == ""
		}, ""},
		{"getSession", func(p *Prepared) bool { return p.Header.Get("Cookie") == "sid=tok" }, ""},
		{"getOAuth", func(p *Prepared) bool { return p.Header.Get("Authorization") == "Bearer tok" }, ""},
		{"postForm", func(p *Prepared) bool {
			return p.Header.Get("Authorization") == "" && string(p.Body) == "n=1&name=a+b" &&
				p.Header.Get("Content-Type") == "application/x-www-form-urlencoded"
		}, ""},
		{"getBasic", nil, "http/basic"},
	}
	for _, tc := range tests {
		t.Run(tc.op, func(t *testing.T) {
			p, err := prepare(t, s, m[tc.op])
			if tc.reason != "" {
				var nb *NotBuildableError
				if !errors.As(err, &nb) || !strings.Contains(nb.Reason, tc.reason) {
					t.Fatalf("got %v, want NOT_BUILDABLE with %q", err, tc.reason)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(p) {
				t.Errorf("unexpected request: %s headers %v body %s", p.URL, p.Header, p.Body)
			}
		})
	}
	if got := len(AllPlacements(s.Doc.Components.SecuritySchemes)); got != 5 {
		t.Errorf("AllPlacements: got %d, want 5 (basic excluded)", got)
	}
}

func TestTPL1_30_BaseURL(t *testing.T) {
	base, warn, err := Base("http://127.0.0.1:8080/api/v3/", "", "/api/v3")
	if err != nil || base != "http://127.0.0.1:8080/api/v3" || warn != "" {
		t.Errorf("got %q %q %v", base, warn, err)
	}
	_, warn, _ = Base("http://127.0.0.1:8080", "", "/api/v3")
	if !strings.Contains(warn, "/api/v3") {
		t.Errorf("missing base path must produce a warning, got %q", warn)
	}
	base, _, _ = Base("", "http://127.0.0.1:5555", "/api/v3")
	if base != "http://127.0.0.1:5555/api/v3" {
		t.Errorf("handler base: got %q", base)
	}
	if _, _, err := Base("localhost:8080", "", ""); err == nil {
		t.Error("relative BaseURL must be rejected")
	}
}

func TestTPL1_20_WriteProtection(t *testing.T) {
	tests := []struct {
		base    string
		allowed []string
		want    bool
	}{
		{"http://localhost:8080", nil, true},
		{"http://127.0.0.1:8080", nil, true},
		{"http://[::1]:8080", nil, true},
		{"https://api.example.com", nil, false},
		{"https://api.example.com", []string{"API.example.com"}, true},
		{"http://10.0.0.5", nil, false},
	}
	for _, tc := range tests {
		if got := WritesAllowed(tc.base, tc.allowed); got != tc.want {
			t.Errorf("WritesAllowed(%s, %v): got %v, want %v", tc.base, tc.allowed, got, tc.want)
		}
	}
	for m, want := range map[string]bool{"GET": false, "HEAD": false, "POST": true, "PUT": true, "PATCH": true, "DELETE": true} {
		if IsWrite(m) != want {
			t.Errorf("IsWrite(%s) != %v", m, want)
		}
	}
}

func TestSendTimeoutAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()

	resp, err := Send(t.Context(), srv.Client(), &Prepared{Method: "GET", URL: srv.URL + "/x", Header: http.Header{}}, time.Second)
	if err != nil || resp.Status != http.StatusTeapot || string(resp.Body) != `{"a":1}` {
		t.Fatalf("got %+v %v", resp, err)
	}
	start := time.Now()
	_, err = Send(t.Context(), srv.Client(), &Prepared{Method: "GET", URL: srv.URL + "/slow", Header: http.Header{}}, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") || time.Since(start) > 2*time.Second {
		t.Fatalf("got %v after %s, want timeout", err, time.Since(start))
	}
	if calls.Load() != 2 {
		t.Errorf("requests must not be retried: %d calls", calls.Load())
	}
}

func TestWaitReady(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if n.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if err := WaitReady(t.Context(), srv.Client(), srv.URL, "", time.Second); err != nil {
		t.Errorf("any response on / counts as reachable: %v", err)
	}
	if err := WaitReady(t.Context(), srv.Client(), srv.URL, "/health", 5*time.Second); err != nil || n.Load() != 3 {
		t.Errorf("health path must be retried until 2xx: %v after %d calls", err, n.Load())
	}
	err := WaitReady(context.Background(), srv.Client(), "http://127.0.0.1:1", "", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("unreachable: got %v", err)
	}
}
