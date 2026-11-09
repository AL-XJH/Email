package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 只提交一个字段不能把没提交的安全开关当成 false：以前 {"siteTitle":"X"}
// 会静默关掉两步验证、Turnstile 和开放注册。
func TestPartialSettingsUpdatePreservesOmittedFields(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) {
		cfg.TwoFactorEnabled = true
		cfg.OpenRegistration = true
		cfg.SiteTitle = "Original"
	})
	ts := httptest.NewServer(a.Router())
	defer ts.Close()

	admin := &testClient{t: t, server: ts}
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login=%d", code)
	}
	if code := admin.do("POST", "/api/admin/settings", map[string]string{"siteTitle": "Renamed"}, nil); code != http.StatusOK {
		t.Fatalf("partial settings update=%d", code)
	}
	cfg := a.config()
	if cfg.SiteTitle != "Renamed" {
		t.Fatalf("siteTitle=%q", cfg.SiteTitle)
	}
	if !cfg.TwoFactorEnabled {
		t.Fatal("partial update disabled twoFactorEnabled")
	}
	if !cfg.OpenRegistration {
		t.Fatal("partial update disabled openRegistration")
	}
}
