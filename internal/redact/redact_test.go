package redact

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestTPL1_21_HeadersFieldsNested(t *testing.T) {
	r := New([]string{"iban"})
	r.AddHeader("X-API-Key")

	h := r.Header(http.Header{
		"Authorization": {"Bearer abc"},
		"Cookie":        {"session=1"},
		"Set-Cookie":    {"session=2"},
		"X-Api-Key":     {"k1"},
		"Content-Type":  {"application/json"},
	})
	for _, k := range []string{"Authorization", "Cookie", "Set-Cookie", "X-Api-Key"} {
		if got := h.Get(k); got != Mask {
			t.Errorf("header %s: got %q, want %q", k, got, Mask)
		}
	}
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type must stay: got %q", got)
	}

	in := map[string]any{
		"name":     "Anna",
		"password": "p",
		"profile": map[string]any{
			"accessToken": "t",
			"IBAN":        "DE00",
			"pin":         "1234",
			"items":       []any{map[string]any{"clientSecret": "s", "id": 1}},
		},
	}
	got := r.Value(in, "pin") // "pin" as a writeOnly property
	want := map[string]any{
		"name":     "Anna",
		"password": Mask,
		"profile": map[string]any{
			"accessToken": Mask,
			"IBAN":        Mask,
			"pin":         Mask,
			"items":       []any{map[string]any{"clientSecret": Mask, "id": 1}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Value:\n got  %v\n want %v", got, want)
	}
	if in["password"] != "p" {
		t.Error("Value must not modify its input")
	}
}

func TestTPL1_26_QueryParamInErrorText(t *testing.T) {
	r := New(nil)
	r.AddParam("api_key")
	r.AddSecret("s3cr3t-key-value")

	err := &url.Error{Op: "Get", URL: "http://127.0.0.1:1/stats?page=2&api_key=s3cr3t-key-value", Err: errors.New("connection refused")}
	got := r.String(err.Error())
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "page=2") || !strings.Contains(got, "api_key=***") {
		t.Errorf("unexpected redaction: %q", got)
	}

	// Parameter redaction works even if the value was never registered as a secret.
	if got := r.String(`GET /x?api_key=other&y=1`); got != `GET /x?api_key=***&y=1` {
		t.Errorf("got %q", got)
	}
	if got := r.String(`Cookie: a=1; api_key=zzz`); got != `Cookie: a=1; api_key=***` {
		t.Errorf("cookie: got %q", got)
	}
}

func TestJWTHeaderPayloadRedacted(t *testing.T) {
	r := New(nil)
	token := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJpdCJ9.c2lnbmF0dXJl"
	r.AddSecret(token)
	tampered := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJpdCJ9.d2lnbmF0dXJl"
	got := r.String("sent " + token + " and " + tampered)
	if strings.Contains(got, "eyJzdWIiOiJpdCJ9") {
		t.Errorf("payload of the (manipulated) token leaked: %q", got)
	}
}

func TestEmptySecretIgnored(t *testing.T) {
	r := New(nil)
	r.AddSecret("")
	if got := r.String("abc"); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestSecretHeaderName(t *testing.T) {
	r := New([]string{"tenantSecret"})
	for name, want := range map[string]bool{
		"Authorization":    true,
		"Set-Cookie":       true,
		"X-Api-Key":        true,
		"X-Auth-Token":     true,
		"X-Signature":      true,
		"X-Session-Id":     true,
		"X-Tenant-Secret":  true,
		"WWW-Authenticate": false,
		"Accept":           false,
		"X-Request-Id":     false,
	} {
		if got := r.SecretHeaderName(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
