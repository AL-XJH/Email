package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserPreferenceShowRemoteImages(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}

	if code := client.do("POST", "/api/auth/login", map[string]string{
		"email": "admin@lanqin.local", "password": "ChangeMe123!",
	}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	type meResponse struct {
		Preferences UserPreferences `json:"preferences"`
	}
	var before meResponse
	if code := client.do("GET", "/api/me", nil, &before); code != http.StatusOK {
		t.Fatalf("me code=%d", code)
	}
	if before.Preferences.ShowRemoteImages {
		t.Fatal("showRemoteImages should default to false")
	}
	var updated struct {
		Preferences UserPreferences `json:"preferences"`
	}
	if code := client.do("POST", "/api/me/preferences", map[string]bool{"showRemoteImages": true}, &updated); code != http.StatusOK {
		t.Fatalf("update preferences code=%d", code)
	}
	if !updated.Preferences.ShowRemoteImages {
		t.Fatal("showRemoteImages was not enabled")
	}
	var after meResponse
	if code := client.do("GET", "/api/me", nil, &after); code != http.StatusOK {
		t.Fatalf("me after update code=%d", code)
	}
	if !after.Preferences.ShowRemoteImages {
		t.Fatal("showRemoteImages was not persisted")
	}
}
