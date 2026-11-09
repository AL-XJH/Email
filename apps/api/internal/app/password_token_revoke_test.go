package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 用户自助改密的语义是「账号可能被盗，结束攻击者的一切访问」。
// 以前只撤销 session，长期 API token 仍然有效，等于留了一把备用钥匙。
func TestChangePasswordRevokesAPITokens(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()

	admin := &testClient{t: t, server: ts}
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	var created struct {
		Token string `json:"token"`
	}
	if code := admin.do("POST", "/api/me/api-tokens", map[string]any{"name": "revoke-test", "scopes": []string{"*"}}, &created); code != http.StatusCreated || created.Token == "" {
		t.Fatalf("create token code=%d token=%q", code, created.Token)
	}

	tokenRequest := func() int {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/open/domains", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+created.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := tokenRequest(); code != http.StatusOK {
		t.Fatalf("token before password change code=%d", code)
	}

	if code := admin.do("POST", "/api/me/password", map[string]string{
		"currentPassword": "ChangeMe123!",
		"newPassword":     "NewChangeMe123!",
	}, nil); code != http.StatusOK {
		t.Fatalf("change password code=%d", code)
	}
	if code := tokenRequest(); code != http.StatusUnauthorized {
		t.Fatalf("token after password change code=%d, want 401", code)
	}
}
