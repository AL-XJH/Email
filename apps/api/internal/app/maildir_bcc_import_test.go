package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// IMAP clients store Bcc in a private draft's RFC822 headers. Preserve it when
// synchronizing that draft back into the webmail model.
func TestMaildirPrivateDraftRetainsBCCOnImport(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	root := t.TempDir()
	a.updateConfig(func(c *Config) { c.MaildirRoot = root })
	_, mb := defaultAdminUserAndMailbox(t, a)
	dir := filepath.Join(root, "lanqin.local", "admin", "Maildir", ".Drafts", "cur")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: admin@lanqin.local\r\nTo: visible@example.test\r\nCc: copy@example.test\r\nBcc: Hidden Person <hidden@example.test>, second@example.test\r\nMessage-ID: <private-bcc-import@example.test>\r\nSubject: private draft\r\n\r\nSynthetic private draft body.\r\n")
	if err := os.WriteFile(filepath.Join(dir, "private-bcc:2,DS"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncMaildirOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := a.db.QueryRow(`SELECT id FROM messages WHERE mailbox_id=? AND message_id='<private-bcc-import@example.test>'`, mb.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	if code := client.do(http.MethodPost, "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	var detail MailMessage
	if code := client.do(http.MethodGet, "/api/mail/messages/"+id+"?markRead=0", nil, &detail); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(detail.BCC) != 2 || detail.BCC[0] != "hidden@example.test" || detail.BCC[1] != "second@example.test" {
		t.Fatalf("private RFC822 Bcc lost during import/detail API: got %v", detail.BCC)
	}
	if len(detail.To) != 1 || detail.To[0] != "visible@example.test" || len(detail.CC) != 1 || detail.CC[0] != "copy@example.test" {
		t.Fatalf("visible recipients changed: To=%v Cc=%v", detail.To, detail.CC)
	}
}
