package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 「全部已读」必须覆盖整个文件夹，并且改掉 maildir 文件名的 S 标志：
// 只改库的话，下一轮 maildir 同步会按缺 S 把它们判回未读。
func TestReadAllFolderMarksWholeFolderAndKeepsMaildirFlags(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = t.TempDir() })
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	var login map[string]any
	if code := client.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)
	mb, err := a.mailboxByID(ctx, mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	inboxID, err := a.ensureFolder(ctx, mailboxID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		now := a.now().UTC()
		msg := storedMessage{
			MailboxID:  mailboxID,
			FolderID:   inboxID,
			MessageUID: newID("uid"),
			MessageID:  "<read-all-" + newID("x") + "@example.test>",
			Subject:    "unread",
			From:       "sender@example.test",
			To:         []string{mb.Address},
			SentAt:     now,
			ReceivedAt: now,
			Snippet:    "body",
			BodyText:   "body",
			IsRead:     false,
		}
		id, err := a.insertMessage(ctx, msg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.writeStoredMessageToMaildir(ctx, id, msg, nil); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	var unreadBefore int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND folder_id=? AND is_read=0`, mailboxID, inboxID).Scan(&unreadBefore); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Updated   int `json:"updated"`
		Remaining int `json:"remaining"`
	}
	if code := client.do("POST", "/api/mail/read-all", map[string]any{"mailboxId": mailboxID, "folder": "Inbox"}, &response); code != http.StatusOK {
		t.Fatalf("read-all code=%d", code)
	}
	if response.Updated != unreadBefore || response.Remaining != 0 {
		t.Fatalf("read-all response = %+v, want updated=%d remaining=0", response, unreadBefore)
	}
	for _, id := range ids {
		var read int
		var rawPath string
		if err := a.db.QueryRow(`SELECT is_read,COALESCE(raw_path,'') FROM messages WHERE id=?`, id).Scan(&read, &rawPath); err != nil {
			t.Fatal(err)
		}
		if read != 1 {
			t.Fatalf("message %s is_read=%d, want 1", id, read)
		}
		if !strings.Contains(rawPath, maildirFlagSeparator()+"S") {
			t.Fatalf("message %s maildir path keeps no S flag: %s", id, rawPath)
		}
	}

	// 关键回归：同步一轮之后不能被判回未读。
	if _, err := a.syncMaildirOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var unread int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE folder_id=? AND is_read=0`, inboxID).Scan(&unread); err != nil {
		t.Fatal(err)
	}
	if unread != 0 {
		t.Fatalf("maildir sync flipped %d messages back to unread", unread)
	}
}
