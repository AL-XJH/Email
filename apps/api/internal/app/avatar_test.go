package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testAvatarPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

func uploadTestAvatar(t *testing.T, client *testClient, data []byte, contentType string) (*http.Response, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", client.server.URL+"/api/me/avatar", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if contentType != "" {
		req.Header.Set("X-Test-Content-Type", contentType)
	}
	req.AddCookie(client.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp, payload
}

func TestAvatarUploadServeAndDelete(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	if code := client.do("POST", "/api/auth/login", map[string]string{
		"email": "admin@lanqin.local", "password": "ChangeMe123!",
	}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	var me struct {
		User User `json:"user"`
	}
	if code := client.do("GET", "/api/me", nil, &me); code != http.StatusOK {
		t.Fatalf("me code=%d", code)
	}
	if me.User.AvatarURL != "" {
		t.Fatalf("default avatarUrl=%q, want empty", me.User.AvatarURL)
	}

	resp, payload := uploadTestAvatar(t, client, testAvatarPNG, "image/png")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload code=%d body=%s", resp.StatusCode, payload)
	}
	var uploaded struct {
		AvatarURL string `json:"avatarUrl"`
	}
	if err := json.Unmarshal(payload, &uploaded); err != nil || uploaded.AvatarURL == "" {
		t.Fatalf("upload body=%s err=%v", payload, err)
	}
	req, _ := http.NewRequest("GET", ts.URL+uploaded.AvatarURL, nil)
	req.AddCookie(client.cookie)
	avatarResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(avatarResp.Body)
	avatarResp.Body.Close()
	if avatarResp.StatusCode != http.StatusOK || !bytes.Equal(served, testAvatarPNG) {
		t.Fatalf("avatar response code=%d bytes=%d", avatarResp.StatusCode, len(served))
	}
	if got := avatarResp.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("avatar content-type=%q", got)
	}
	if code := client.do("GET", "/api/me", nil, &me); code != http.StatusOK || me.User.AvatarURL == "" {
		t.Fatalf("me after upload code=%d url=%q", code, me.User.AvatarURL)
	}

	badResp, _ := uploadTestAvatar(t, client, []byte("<html>not an image</html>"), "text/html")
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid avatar code=%d, want 400", badResp.StatusCode)
	}
	if code := client.do("DELETE", "/api/me/avatar", nil, nil); code != http.StatusOK {
		t.Fatalf("delete avatar code=%d", code)
	}
	req, _ = http.NewRequest("GET", ts.URL+uploaded.AvatarURL, nil)
	req.AddCookie(client.cookie)
	after, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	after.Body.Close()
	if after.StatusCode != http.StatusNotFound {
		t.Fatalf("avatar after delete code=%d, want 404", after.StatusCode)
	}
	var afterMe struct {
		User User `json:"user"`
	}
	if code := client.do("GET", "/api/me", nil, &afterMe); code != http.StatusOK || afterMe.User.AvatarURL != "" {
		t.Fatalf("me after delete code=%d url=%q", code, afterMe.User.AvatarURL)
	}
}

func TestAvatarRejectsOversizedUpload(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	if code := client.do("POST", "/api/auth/login", map[string]string{
		"email": "admin@lanqin.local", "password": "ChangeMe123!",
	}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	resp, _ := uploadTestAvatar(t, client, bytes.Repeat([]byte{0x89}, maxAvatarBytes+1), "image/png")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized avatar code=%d, want 400", resp.StatusCode)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		// response body is consumed by the helper; status is the contract.
	}
}
