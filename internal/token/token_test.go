package token

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func jwt(t *testing.T, claims string, sigLen int) string {
	t.Helper()
	sig := make([]byte, sigLen)
	if _, err := rand.Read(sig); err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString(sig)
}

func TestTPL1_24_TamperChangesSignature(t *testing.T) {
	for i := range 100 {
		// Signature lengths 1..100 cover every padding situation of base64url.
		tok := jwt(t, fmt.Sprintf(`{"sub":"u%d"}`, i), i+1)
		got := Tamper(tok)
		if got == tok {
			t.Fatalf("token %d unchanged", i)
		}
		a, b := strings.Split(tok, "."), strings.Split(got, ".")
		if a[0] != b[0] {
			t.Fatalf("token %d: header changed", i)
		}
		payload, err := base64.RawURLEncoding.DecodeString(b[1])
		var claims map[string]any
		if err != nil || json.Unmarshal(payload, &claims) != nil || claims["sub"] != fmt.Sprintf("u%d", i) || claims["apitest"] != "invalid-token" {
			t.Fatalf("token %d: payload %s must keep the claims and add the marker", i, payload)
		}
		sa, _ := base64.RawURLEncoding.DecodeString(a[2])
		sb, err := base64.RawURLEncoding.DecodeString(b[2])
		if err != nil || len(sa) != len(sb) {
			t.Fatalf("token %d: signature must keep its length (err %v)", i, err)
		}
		for j := range sa {
			if sa[j] == sb[j] {
				t.Fatalf("token %d: signature byte %d unchanged", i, j)
			}
		}
	}
	// A payload that is not a JSON object is changed as well.
	tok := "eyJhbGciOiJIUzI1NiJ9.bm8." + base64.RawURLEncoding.EncodeToString([]byte("sig"))
	if got := strings.Split(Tamper(tok), "."); got[1] == "bm8" {
		t.Errorf("payload unchanged: %v", got)
	}
	for i := range 100 {
		raw := make([]byte, i%40+1)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		for _, tok := range []string{hex.EncodeToString(raw), base64.StdEncoding.EncodeToString(raw), "9", "z", "Z", "-"} {
			got := Tamper(tok)
			if len(got) != len(tok) {
				t.Fatalf("opaque token %q: got %q", tok, got)
			}
			for j := 0; j < len(tok); j += 2 {
				if got[j] == tok[j] {
					t.Fatalf("opaque token %q: character %d unchanged in %q", tok, j, got)
				}
			}
		}
	}
	if Tamper("") == "" {
		t.Error("empty token must become a non-empty invalid token")
	}
}

func TestTPL1_25_Expiry(t *testing.T) {
	exp := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		tok  string
		ok   bool
	}{
		{"with exp", jwt(t, fmt.Sprintf(`{"sub":"u","exp":%d}`, exp.Unix()), 32), true},
		{"float exp", jwt(t, fmt.Sprintf(`{"exp":%d.0}`, exp.Unix()), 32), true},
		{"without exp", jwt(t, `{"sub":"u"}`, 32), false},
		{"not a JWT", "opaque-token-value", false},
		{"broken payload", "eyJhbGciOiJIUzI1NiJ9.!!!.c2ln", false},
		{"payload not JSON", "eyJhbGciOiJIUzI1NiJ9.bm8.c2ln", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Expiry(tc.tok)
			if ok != tc.ok || (ok && !got.Equal(exp)) {
				t.Errorf("got %v %v, want %v %v", got, ok, exp, tc.ok)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	long := jwt(t, `{"sub":"u","exp":4102444800}`, 256)
	if got := Truncate(long); got != long[:TruncateLen] {
		t.Errorf("long token: %d characters", len(got))
	}
	for _, tok := range []string{"ab", "abcdef", strings.Repeat("x", TruncateLen)} {
		if got := Truncate(tok); got == tok || !strings.HasPrefix(tok, got) || got == "" {
			t.Errorf("%q: got %q", tok, got)
		}
	}
	for _, tok := range []string{"", "a"} {
		if got := Truncate(tok); got == tok || got == "" {
			t.Errorf("%q: got %q", tok, got)
		}
	}
}
