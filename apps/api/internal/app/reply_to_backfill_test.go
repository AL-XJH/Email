package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplyToBackfillStartupRetriesAndPreservesRows(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	root := t.TempDir()
	a.updateConfig(func(c *Config) { c.MaildirRoot = root })
	_, mb := defaultAdminUserAndMailbox(t, a)
	folder, err := a.ensureFolder(t.Context(), mb.ID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	insert := func(name, raw, existing string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if raw != "" {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
		}
		id, err := a.insertMessage(t.Context(), storedMessage{MailboxID: mb.ID, FolderID: folder, MessageUID: newID("uid"), MessageID: "<" + name + "@example.test>", From: "sender@example.test", To: []string{mb.Address}, Subject: name, BodyText: "body unchanged", RawPath: path, ReplyTo: existing, SentAt: time.Now(), ReceivedAt: time.Now()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	header := "From: sender@example.test\r\nReply-To: Support\r\n <support@example.test>\r\nSubject: old mail\r\n\r\n"
	var repaired []string
	// Cross the 100-row page boundary used by the startup migration.
	for i := 0; i < 105; i++ {
		repaired = append(repaired, insert(fmt.Sprintf("old-%03d", i), header+strings.Repeat("body", 32), ""))
	}
	missing := insert("missing", "", "")
	broken := insert("broken", "not a valid header\r\n\r\nbody", "")
	oversize := insert("oversize", "X-Large: "+strings.Repeat("x", replyToHeaderLimit)+"\r\nReply-To: late@example.test\r\n\r\n", "")
	preserved := insert("preserved", header, "chosen@example.test")
	none := insert("none", "From: sender@example.test\r\n\r\nbody", "")
	symlink := insert("symlink", "", "")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	outsideID := insert("outside-row", "", "")
	if _, err := a.db.Exec(`UPDATE messages SET raw_path=? WHERE id=?`, outside, outsideID); err != nil {
		t.Fatal(err)
	}
	if err := a.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range repaired {
		var reply, body string
		if err := a.db.QueryRow(`SELECT reply_to,body_text FROM messages WHERE id=?`, id).Scan(&reply, &body); err != nil {
			t.Fatal(err)
		}
		if reply != "Support <support@example.test>" || body != "body unchanged" {
			t.Fatalf("backfill reply=%q body=%q", reply, body)
		}
	}
	for _, id := range []string{missing, broken, oversize, symlink, outsideID} {
		var reply string
		var marker int
		if err := a.db.QueryRow(`SELECT reply_to FROM messages WHERE id=?`, id).Scan(&reply); err != nil {
			t.Fatal(err)
		}
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM system_settings WHERE key=?`, "replyToHeaderChecked:"+id).Scan(&marker); err != nil {
			t.Fatal(err)
		}
		if reply != "" || marker != 0 {
			t.Fatalf("failed/unsafe raw marked complete: reply=%q marker=%d", reply, marker)
		}
	}
	var value string
	if err := a.db.QueryRow(`SELECT reply_to FROM messages WHERE id=?`, preserved).Scan(&value); err != nil || value != "chosen@example.test" {
		t.Fatalf("existing Reply-To changed: %q err=%v", value, err)
	}
	if err := a.db.QueryRow(`SELECT value FROM system_settings WHERE key=?`, "replyToHeaderChecked:"+none).Scan(&value); err != nil || value != filepath.Join(root, "none") {
		t.Fatalf("successful no-header read not remembered: value=%q err=%v", value, err)
	}
	// Retry the previously unavailable file; no global completed flag may hide it.
	if err := os.WriteFile(filepath.Join(root, "missing"), []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	// A changed file for an already-checked path must not be parsed again.
	if err := os.WriteFile(filepath.Join(root, "none"), []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := *a.config()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := newTestAppWithConfig(t, cfg)
	stopTestWorkers(b)
	for _, tc := range []struct{ id, want string }{{missing, "Support <support@example.test>"}, {none, ""}, {preserved, "chosen@example.test"}} {
		if err := b.db.QueryRow(`SELECT reply_to FROM messages WHERE id=?`, tc.id).Scan(&value); err != nil || value != tc.want {
			t.Fatalf("after restart reply=%q want=%q err=%v", value, tc.want, err)
		}
	}
	t.Log("105 historical Reply-To rows repaired; unreadable/unsafe/malformed/oversized deferred; missing file repaired on actual restart; existing values and checked empty headers preserved")
}

type replyToCountReader struct {
	source io.Reader
	n      int
}

func (c *replyToCountReader) Read(p []byte) (int, error) {
	n, err := c.source.Read(p)
	c.n += n
	return n, err
}
func TestReplyToHeaderReadIsBounded(t *testing.T) {
	body := &replyToCountReader{source: strings.NewReader("Reply-To: support@example.test\r\n\r\n" + strings.Repeat("b", 1<<20))}
	header, err := readReplyToHeader(body)
	if err != nil || header.Get("Reply-To") != "support@example.test" || body.n > 4096 {
		t.Fatalf("body read beyond header buffer: bytes=%d header=%v err=%v", body.n, header, err)
	}
	large := &replyToCountReader{source: strings.NewReader("X-Large: " + strings.Repeat("x", replyToHeaderLimit*2))}
	if _, err := readReplyToHeader(large); err == nil {
		t.Fatal("oversized header accepted")
	}
	if large.n > replyToHeaderLimit+1 {
		t.Fatalf("unbounded header read: %d", large.n)
	}
	if _, err := readReplyToHeader(strings.NewReader("Reply-To: support@example.test\r\n")); err == nil {
		t.Fatal("unterminated header treated as complete")
	}
	if _, err := readReplyToHeader(strings.NewReader("From: sender@example.test\nReply-To: support@example.test\n\nbody")); err != nil {
		t.Fatal(err)
	}
}
