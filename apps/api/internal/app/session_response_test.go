package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionResponseDistinguishesReauthenticationFailure(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	if code := client.do(http.MethodPost, "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login HTTP %d", code)
	}
	call := func(path, body string, withCookie bool) (int, http.Header) {
		t.Helper()
		method := http.MethodGet
		if body != "" {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if withCookie {
			req.AddCookie(client.cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header
	}
	code, headers := call("/api/me/password", `{"currentPassword":"mistyped","newPassword":"Replacement123!"}`, true)
	if code != http.StatusUnauthorized || headers.Get("X-Lanqin-Session") != "valid" {
		t.Fatalf("reauth failure must retain valid session marker: HTTP %d header=%q", code, headers.Get("X-Lanqin-Session"))
	}
	code, headers = call("/api/me", "", true)
	if code != http.StatusOK || headers.Get("X-Lanqin-Session") != "valid" {
		t.Fatalf("existing session no longer usable: HTTP %d marker=%q", code, headers.Get("X-Lanqin-Session"))
	}
	code, headers = call("/api/me", "", false)
	if code != http.StatusUnauthorized || headers.Get("X-Lanqin-Session") != "expired" {
		t.Fatalf("missing session must be distinguishable: HTTP %d marker=%q", code, headers.Get("X-Lanqin-Session"))
	}
	// Cross-origin development clients must be allowed to inspect the marker;
	// unknown origins must not acquire access by sending the same request header.
	for _, origin := range []string{"http://localhost:5173", "https://untrusted.example.test"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(client.cookie)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Lanqin-Session", "expired")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Lanqin-Session") != "valid" {
			t.Fatalf("request header affected server session state: HTTP %d", resp.StatusCode)
		}
		if origin == "http://localhost:5173" && resp.Header.Get("Access-Control-Expose-Headers") != "X-Lanqin-Session" {
			t.Fatal("trusted origin cannot read the session result")
		}
		if origin == "https://untrusted.example.test" && resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("untrusted origin was allowed")
		}
	}
}
