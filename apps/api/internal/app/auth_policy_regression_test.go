package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const policyTestLogin = "admin@lanqin.local"

func policyRequest(t *testing.T, a *App, command, body string) int {
	t.Helper()
	return policyRequestURL(t, a, "/auth-policy?key=test-auth-policy-secret?command="+command, body)
}

func policyRequestURL(t *testing.T, a *App, target, body string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleAuthPolicy(w, r)
	var out struct {
		Status *int `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || out.Status == nil {
		t.Fatalf("invalid Dovecot response: HTTP %d %s, error=%v", w.Code, w.Body.String(), err)
	}
	return *out.Status
}

func policyBody(extra string) string {
	return `{"login":"admin@lanqin.local","protocol":"imap","remote":"198.51.100.25"` + extra + `}`
}

func policyFailures(t *testing.T, a *App, scope, value string) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(`SELECT COALESCE(SUM(failure_count),0) FROM login_rate_limits WHERE scope=? AND subject_hash=?`, scope, loginRateLimitHash(scope, value)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthPolicyRealDovecotPayloads(t *testing.T) {
	for _, extra := range []string{`,"tls":true`, `,"device_id":"","fail_type":"","pwhash":"test-hash","session_id":"test-session","tls":true`} {
		t.Run(extra, func(t *testing.T) {
			a := newTestApp(t)
			body := policyBody(extra)
			if status := policyRequest(t, a, "allow", body); status != 0 {
				t.Fatalf("enabled user rejected: %d", status)
			}
			if _, err := a.db.Exec(`UPDATE users SET disabled=1 WHERE email=?`, policyTestLogin); err != nil {
				t.Fatal(err)
			}
			if status := policyRequest(t, a, "allow", body); status != -1 {
				t.Fatalf("disabled user accepted: %d", status)
			}
		})
	}
}

func TestAuthPolicyRejectsInvalidPayloads(t *testing.T) {
	a := newTestApp(t)
	for name, body := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "truncated": `{"login":`,
		"no identity": `{}`, "wrong tls type": policyBody(`,"tls":"yes"`),
		"unknown field":  policyBody(`,"unexpected":true`),
		"trailing value": policyBody("") + ` {"success":true}`, "trailing garbage": policyBody("") + ` junk`,
		"oversized value":               policyBody(`,"device_id":"` + strings.Repeat("x", 70<<10) + `"`),
		"oversized trailing whitespace": policyBody("") + strings.Repeat(" ", 70<<10),
	} {
		t.Run(name, func(t *testing.T) {
			if s := policyRequest(t, a, "allow", body); s != -1 {
				t.Fatalf("invalid payload accepted: %d", s)
			}
		})
	}
	for _, command := range []string{"", "unknown", "allow?command=report"} {
		if s := policyRequest(t, a, command, policyBody("")); s != -1 {
			t.Fatalf("invalid command %q accepted: %d", command, s)
		}
	}
}

func TestAuthPolicyCommandAndSecretParsing(t *testing.T) {
	a := newTestApp(t)
	for _, target := range []string{
		"/auth-policy?command=allow&key=test-auth-policy-secret",
		"/auth-policy?key=test-auth-policy-secret?command=allow",
	} {
		if s := policyRequestURL(t, a, target, policyBody("")); s != 0 {
			t.Fatalf("valid URL %s rejected: %d", target, s)
		}
	}
	for _, target := range []string{
		"/auth-policy?command=allow", "/auth-policy?key=wrong?command=allow",
		"/auth-policy?key=test-auth-policy-secret?command=report&command=allow",
	} {
		if s := policyRequestURL(t, a, target, policyBody("")); s != -1 {
			t.Fatalf("invalid URL accepted: %s status=%d", target, s)
		}
	}
}

func TestAuthPolicyFailureReportsAndLocks(t *testing.T) {
	for _, extra := range []string{
		`,"success":false,"policy_reject":false,"tls":true`,
		`,"success":false,"policy_reject":false,"fail_type":"credentials","device_id":"","session_id":"test-session","pwhash":"test-hash","tls":true`,
		`,"fail_type":"credentials","policy_reject":false`,
	} {
		t.Run(extra, func(t *testing.T) {
			a := newTestApp(t)
			for i := 0; i < loginRateLimitAccountFailures; i++ {
				if s := policyRequest(t, a, "report", policyBody(extra)); s != -1 {
					t.Fatalf("failure report accepted: %d", s)
				}
			}
			if n := policyFailures(t, a, "account", policyTestLogin); n != loginRateLimitAccountFailures {
				t.Fatalf("failure count=%d", n)
			}
			if retry, err := a.checkLoginRateLimit(context.Background(), policyTestLogin, "198.51.100.25"); err != nil || retry <= 0 {
				t.Fatalf("missing lock: %v %v", retry, err)
			}
			if s := policyRequest(t, a, "allow", policyBody(`,"tls":true`)); s != -1 {
				t.Fatalf("locked user accepted: %d", s)
			}
		})
	}
}

func TestAuthPolicyActiveLocksSurviveReports(t *testing.T) {
	for _, scope := range []string{"account", "ip"} {
		t.Run(scope, func(t *testing.T) {
			a := newTestApp(t)
			value, count := policyTestLogin, loginRateLimitAccountFailures
			account := policyTestLogin
			if scope == "ip" {
				value, count, account = "198.51.100.25", loginRateLimitIPFailures, "other@example.test"
			}
			for i := 0; i < count; i++ {
				if _, err := a.recordLoginFailure(context.Background(), account, "198.51.100.25"); err != nil {
					t.Fatal(err)
				}
			}
			for _, req := range []struct{ command, extra string }{
				{"allow", ""}, {"report", `,"success":true,"policy_reject":false`},
				{"report", `,"success":false,"policy_reject":false,"fail_type":"credentials"`},
				{"report", `,"success":true,"policy_reject":true`},
			} {
				if s := policyRequest(t, a, req.command, policyBody(req.extra)); s != -1 {
					t.Fatalf("%s bypassed active %s lock: %d", req.command, scope, s)
				}
				if n := policyFailures(t, a, scope, value); n != count {
					t.Fatalf("report changed locked failures: %d want %d", n, count)
				}
			}
		})
	}
}

func TestAuthPolicyOnlySuccessfulReportsClearFailures(t *testing.T) {
	for name, req := range map[string]struct {
		command, extra   string
		status, failures int
	}{
		"allow cannot clear":    {"allow", `,"success":true`, -1, 1},
		"allow cannot record":   {"allow", `,"success":false`, -1, 1},
		"policy reject":         {"report", `,"success":false,"policy_reject":true`, -1, 1},
		"contradictory success": {"report", `,"success":true,"policy_reject":true`, -1, 1},
		"failed success":        {"report", `,"success":true,"fail_type":"credentials"`, -1, 2},
		"missing outcome":       {"report", `,"fail_type":""`, -1, 1},
		"success":               {"report", `,"success":true,"policy_reject":false,"tls":true`, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			if _, err := a.recordLoginFailure(context.Background(), policyTestLogin, "198.51.100.25"); err != nil {
				t.Fatal(err)
			}
			if s := policyRequest(t, a, req.command, policyBody(req.extra)); s != req.status {
				t.Fatalf("status=%d want %d", s, req.status)
			}
			if n := policyFailures(t, a, "account", policyTestLogin); n != req.failures {
				t.Fatalf("failures=%d want %d", n, req.failures)
			}
			if n := policyFailures(t, a, "ip", "198.51.100.25"); n < 1 {
				t.Fatal("report erased IP failure history")
			}
		})
	}
}

func TestAuthPolicyDisabledSuccessCannotClear(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.recordLoginFailure(context.Background(), policyTestLogin, "198.51.100.25"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE users SET disabled=1 WHERE email=?`, policyTestLogin); err != nil {
		t.Fatal(err)
	}
	if s := policyRequest(t, a, "report", policyBody(`,"success":true,"policy_reject":false`)); s != -1 {
		t.Fatalf("disabled success status=%d", s)
	}
	if n := policyFailures(t, a, "account", policyTestLogin); n != 1 {
		t.Fatalf("disabled success cleared failures: %d", n)
	}
}

func TestAuthPolicySuccessfulReportDoesNotChargeProtocolRate(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.db.Exec(`UPDATE users SET role='user' WHERE email=?`, policyTestLogin); err != nil {
		t.Fatal(err)
	}
	if s := policyRequest(t, a, "allow", policyBody("")); s != 0 {
		t.Fatalf("allow status=%d", s)
	}
	var before int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM imap_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("allow recorded %d attempts, want 1", before)
	}
	if s := policyRequest(t, a, "report", policyBody(`,"success":true,"policy_reject":false,"tls":true`)); s != 0 {
		t.Fatalf("report status=%d", s)
	}
	var after int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM imap_events`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("report charged another attempt: %d -> %d", before, after)
	}
}

func TestAuthPolicyUnavailableStateRejects(t *testing.T) {
	t.Run("database", func(t *testing.T) {
		a := newTestApp(t)
		if err := a.db.Close(); err != nil {
			t.Fatal(err)
		}
		if s := policyRequest(t, a, "allow", policyBody("")); s != -1 {
			t.Fatalf("unavailable database accepted: %d", s)
		}
	})
	t.Run("secret", func(t *testing.T) {
		a := newTestApp(t)
		cfg := *a.config()
		cfg.AuthPolicySecret = ""
		a.setConfig(cfg)
		if s := policyRequest(t, a, "allow", policyBody("")); s != -1 {
			t.Fatalf("empty secret accepted: %d", s)
		}
	})
}

func TestAuthPolicyDoesNotRelaxGlobalJSONDecoder(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"login":"x","tls":true}`))
	var req struct {
		Login string `json:"login"`
	}
	if err := decodeJSON(r, &req); err == nil {
		t.Fatal("global JSON decoder now allows unknown fields")
	}
}
