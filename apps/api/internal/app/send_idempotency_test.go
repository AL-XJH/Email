package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebmailSendClientTokenIsIdempotent(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	var login map[string]any
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("admin login=%d", code)
	}
	domainID := mustDefaultDomainID(t, a)
	alice := createTestMailbox(t, admin, domainID, "idem-sender", "Idem Sender", "Password123!", nil)
	bob := createTestMailbox(t, admin, domainID, "idem-recipient", "Idem Recipient", "Password123!", nil)
	client := &testClient{t: t, server: ts}
	if code := client.do("POST", "/api/auth/login", map[string]string{"email": alice.Address, "password": "Password123!"}, &login); code != http.StatusOK {
		t.Fatalf("alice login=%d", code)
	}
	payload := map[string]any{
		"to":          []string{bob.Address},
		"subject":     "idempotent send",
		"text":        "once",
		"html":        "<p>once</p>",
		"clientToken": "compose-send-token-1",
	}
	var first, second MailMessage
	if code := client.do("POST", "/api/mail/send", payload, &first); code != http.StatusCreated {
		t.Fatalf("first send=%d", code)
	}
	var replayBody map[string]any
	if code := client.do("POST", "/api/mail/send", payload, &replayBody); code != http.StatusOK {
		t.Fatalf("replay send=%d body=%v", code, replayBody)
	}
	second.ID, _ = replayBody["id"].(string)
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("duplicate sends: first=%q second=%q", first.ID, second.ID)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND subject='idempotent send'`, alice.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("sent copies=%d, want 1", count)
	}
}
