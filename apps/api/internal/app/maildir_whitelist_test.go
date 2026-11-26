package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Sieve runs before the API imports Maildir. The whitelist must therefore also
// be honored for first imports from Spam, without reapplying to existing mail.
func TestWhitelistRescuesFirstImportFromSpam(t *testing.T) {
	for _, scope := range []string{"mailbox", "account"} {
		t.Run(scope, func(t *testing.T) {
			a := newTestApp(t)
			stopTestWorkers(a)
			root := t.TempDir()
			a.updateConfig(func(c *Config) { c.MaildirRoot = root })
			_, mb := defaultAdminUserAndMailbox(t, a)
			ts := httptest.NewServer(a.Router())
			defer ts.Close()
			client := &testClient{t: t, server: ts}
			if code := client.do(http.MethodPost, "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
				t.Fatal(code)
			}
			mailboxID := mb.ID
			if scope == "account" {
				mailboxID = ""
			}
			if code := client.do(http.MethodPost, "/api/me/allowed-senders", map[string]string{"mailboxId": mailboxID, "email": "trusted@example.test"}, nil); code != http.StatusCreated {
				t.Fatal(code)
			}
			spam := filepath.Join(root, "lanqin.local", "admin", "Maildir", ".Spam", "new")
			if err := os.MkdirAll(spam, 0755); err != nil {
				t.Fatal(err)
			}
			for i, from := range []string{"trusted@example.test", "unknown@example.test"} {
				raw := fmt.Sprintf("From: %s\r\nTo: admin@lanqin.local\r\nMessage-ID: <whitelist-spam-%d@example.test>\r\nSubject: safe synthetic fixture\r\nX-Spam-Status: Yes, score=8 required=6\r\n\r\nBody.\r\n", from, i)
				if err := os.WriteFile(filepath.Join(spam, fmt.Sprintf("fixture-%d", i)), []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.syncMaildirOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			var trustedID string
			for i, want := range []string{"Inbox", "Spam"} {
				var id, folder, path string
				err := a.db.QueryRow(`SELECT m.id,f.name,m.raw_path FROM messages m JOIN folders f ON f.id=m.folder_id WHERE m.message_id=?`, fmt.Sprintf("<whitelist-spam-%d@example.test>", i)).Scan(&id, &folder, &path)
				if err != nil {
					t.Fatal(err)
				}
				if folder != want {
					t.Errorf("message %d folder=%q want=%q", i, folder, want)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					trustedID = id
				}
			}
			// The user may deliberately move an already-imported trusted message
			// to Spam. Neither a quick scan nor a periodic full scan may undo it.
			if code := client.do(http.MethodPost, "/api/mail/messages/"+trustedID+"/move", map[string]string{"folder": "Spam"}, nil); code != http.StatusOK {
				t.Fatal(code)
			}
			for scan := 0; scan < 2; scan++ {
				if scan == 1 {
					a.maildirRuns = maildirFullScanEvery - 1
				}
				if _, err := a.syncMaildirOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
				var folder string
				if err := a.db.QueryRow(`SELECT f.name FROM messages m JOIN folders f ON f.id=m.folder_id WHERE m.id=?`, trustedID).Scan(&folder); err != nil {
					t.Fatal(err)
				}
				if folder != "Spam" {
					t.Fatalf("scan %d undid manual move: %q", scan, folder)
				}
				var count int
				if err := a.db.QueryRow(`SELECT count(*) FROM messages WHERE message_id='<whitelist-spam-0@example.test>'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("scan %d duplicate count=%d err=%v", scan, count, err)
				}
			}
		})
	}
}
