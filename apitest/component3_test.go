package apitest

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// Component tests of phase 3: authentication cases and token expiry.

// originalWD is the package directory, captured before tests change it.
var originalWD, _ = os.Getwd()

var deleteOps = []string{"Author/deleteAuthor", "Book/deleteBook", "Review/deleteReview", "Shelf/deleteShelf"}

// withSuffix returns all baseline cases ending in suffix with status s.
func withSuffix(suffix string, s Status) map[string]Status {
	m := map[string]Status{}
	for n := range baseline {
		if strings.HasSuffix(n, suffix) {
			m[n] = s
		}
	}
	return m
}

func TestTPL2_17_IgnoreAuth(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{IgnoreAuth: true}))
	changed := withSuffix("/unauthorized", StatusFailed)
	assertOnlyChanged(t, out.res, changed)
	if len(withSuffix("/unauthorized", StatusFailed)) != 17 {
		t.Error("every operation documenting 401 needs an unauthorized case")
	}
}

func TestTPL2_26_IgnoreSignature(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{IgnoreSignature: true}))
	assertOnlyChanged(t, out.res, withSuffix("/invalid-token", StatusFailed))
}

func TestTPL2_27_IgnoreRolesAndMissingForbiddenToken(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{IgnoreRoles: true}))
	assertOnlyChanged(t, out.res, withSuffix("/forbidden", StatusFailed))

	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.ForbiddenToken = nil
	out = runFake(t, cfg)
	assertOnlyChanged(t, out.res, withSuffix("/forbidden", StatusSkipped))
	if msg := message(out.res, "Shelf/createShelf/forbidden"); !strings.Contains(msg, "Config.ForbiddenToken is not set") {
		t.Errorf("message: %q", msg)
	}
	if out.res.Failed {
		t.Error("missing ForbiddenToken only skips cases")
	}
}

// The authentication cases of a DELETE run after the regular DELETE, so a
// DELETE the API accepts without token cannot remove the record the regular
// case needs: the regular DELETE passes, the unauthorized one fails.
func TestTPL2_28_UnauthorizedDeleteSucceeds(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{IgnoreAuth: true}))
	for _, op := range deleteOps {
		if s := statuses(out.res)[op+"/unauthorized"]; s != StatusFailed {
			t.Errorf("%s/unauthorized: %s", op, s)
		}
		if s := statuses(out.res)[op+"/default"]; s != StatusPassed {
			t.Errorf("%s/default: %s %q", op, s, message(out.res, op+"/default"))
		}
	}
}

// jwtWithExp builds an unsigned-looking JWT with the given expiry.
func jwtWithExp(exp time.Time) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." +
		enc.EncodeToString([]byte(fmt.Sprintf(`{"sub":"it","exp":%d}`, exp.Unix()))) + "." +
		enc.EncodeToString([]byte("signature-bytes"))
}

func TestJWTExpiry(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	// Expired before the run: abort before the first request.
	expired := jwtWithExp(now.Add(-time.Minute))
	cfg := bookstoreConfig(t, testserver.Faults{})
	srv := bookstore(testserver.Faults{})
	srv.Tokens = []string{expired}
	cfg.Handler, cfg.Token = srv, StaticToken(expired)
	cfg.now = func() time.Time { return now }
	out := runFake(t, cfg)
	if !out.res.Failed || len(out.res.Cases) != 0 || !strings.Contains(out.ft.output(), "expired at 2026-09-30T11:59:00Z") {
		t.Errorf("expired token must abort: %d cases, %s", len(out.res.Cases), out.ft.output())
	}
	if strings.Contains(out.ft.output(), expired) || strings.Contains(out.report, expired) {
		t.Error("the token must not appear in the output")
	}

	// Expires before the go test deadline: warning.
	valid := jwtWithExp(now.Add(time.Hour))
	cfg = bookstoreConfig(t, testserver.Faults{})
	srv = bookstore(testserver.Faults{})
	srv.Tokens, srv.APIKey = []string{valid}, valid
	cfg.Handler, cfg.Token = srv, StaticToken(valid)
	cfg.now = func() time.Time { return now }
	ft := newFakeT(t, "TestJWT")
	ft.deadline = now.Add(2 * time.Hour)
	res := run(ft, cfg)
	ft.finish()
	if res.Failed || !strings.Contains(ft.output(), "expires at 2026-09-30T13:00:00Z, before the go test deadline") {
		t.Errorf("warning expected: %s", ft.output())
	}

	// Expires during the run: the first 401 afterwards aborts the run.
	var requests atomic.Int32
	inner := bookstore(testserver.Faults{})
	inner.Tokens, inner.APIKey = []string{valid}, valid
	cfg = bookstoreConfig(t, testserver.Faults{})
	cfg.Token = StaticToken(valid)
	cfg.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) > 5 && r.Header.Get("Authorization") != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"token expired"}`))
			return
		}
		inner.ServeHTTP(w, r)
	})
	var calls atomic.Int32
	cfg.now = func() time.Time {
		if calls.Add(1) > 1 { // the first call is the check at the start
			return now.Add(2 * time.Hour)
		}
		return now
	}
	out = runFake(t, cfg)
	if !strings.Contains(out.report, "Config.Token expired at 2026-09-30T13:00:00Z during the run") {
		t.Errorf("the run must stop with a clear message:\n%s", out.ft.output())
	}
	if n := out.res.Summary.Counts[StatusError]; n != 1 {
		t.Errorf("exactly one ERROR instead of a 401 avalanche, got %v", out.res.Summary.Counts)
	}
}

func TestVerifyPolling(t *testing.T) {
	// The update becomes visible after 3 GETs: polling waits for it.
	out := runFake(t, bookstoreConfig(t, testserver.Faults{AsyncUpdate: 3}))
	assertOnlyChanged(t, out.res, nil)

	// Never visible within the timeout of 1 s: DATA_MISMATCH.
	start := time.Now()
	out = runFake(t, bookstoreConfig(t, testserver.Faults{AsyncUpdate: 1000}))
	assertOnlyChanged(t, out.res, map[string]Status{"Author/updateAuthor/default": StatusDataMismatch})
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("polling must stop after the timeout, took %s", d)
	}
}

func TestAuthCasesAreNotNamedExamples(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{}))
	// The Bookstore has 3 named request examples: valid-author, missing-name, rename.
	if !strings.Contains(out.report, "| Named examples executed | 3 | 3 |") {
		t.Errorf("authentication cases must not count as named examples:\n%s", out.report)
	}
}

func TestDisableReports(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	spec, _ := filepath.Abs(filepath.Join(originalWD, bookstoreSpec))
	cfg := Config{
		SpecPath:       spec,
		Handler:        bookstore(testserver.Faults{WrongStatus: true}),
		Token:          StaticToken(testToken),
		ForbiddenToken: StaticToken(readerToken),
		DisableReports: true,
	}
	ft := newFakeT(t, "TestNoReport")
	res := run(ft, cfg)
	ft.finish()
	if res.Report != "" {
		t.Errorf("Result.Report must be empty, got %q", res.Report)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("no files may be written, found %v", entries)
	}
	if !res.Failed || statuses(res)["Author/createAuthor/valid-author"] != StatusFailed {
		t.Errorf("failures are still reported through go test: %v", res.Summary.Counts)
	}
	if strings.Contains(ft.output(), "details in report") {
		t.Errorf("messages must not point to a report: %s", ft.output())
	}

	cfg.ReportJSON = true
	ft = newFakeT(t, "TestContradiction")
	if res := run(ft, cfg); !res.Failed || !strings.Contains(ft.output(), "DisableReports is set together with") {
		t.Errorf("contradicting report settings must be rejected: %s", ft.output())
	}
}
