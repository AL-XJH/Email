package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateDraftClientTokenIsIdempotent(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	var login map[string]any
	if code := client.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	mailboxID := adminMailboxIDForTest(t, a)
	payload := map[string]any{
		"mailboxId":   mailboxID,
		"subject":     "idempotent draft",
		"text":        "same body",
		"html":        "<p>same body</p>",
		"clientToken": "compose-token-1",
	}
	var first, second MailMessage
	if code := client.do("POST", "/api/mail/drafts", payload, &first); code != http.StatusCreated {
		t.Fatalf("first create code=%d", code)
	}
	if code := client.do("POST", "/api/mail/drafts", payload, &second); code != http.StatusOK {
		t.Fatalf("retry create code=%d", code)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("duplicate drafts: first=%q second=%q", first.ID, second.ID)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND draft_token=?`, mailboxID, "compose-token-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("draft rows=%d, want 1", count)
	}
}
