package app

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func adminMailboxIDForTest(t *testing.T, a *App) string {
	t.Helper()
	var id string
	if err := a.db.QueryRow(`SELECT id FROM mailboxes WHERE address='admin@lanqin.local'`).Scan(&id); err != nil {
		t.Fatalf("lookup admin mailbox: %v", err)
	}
	return id
}

// 更新草稿换附件时，必须先写新附件、成功后再删旧附件。
// 旧实现先删文件和 attachments 行再写新的，中途失败就永久丢掉原附件。
func TestUpdateDraftReplacesAttachmentsWithoutLosingOldOnSuccess(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	var login map[string]any
	if code := client.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	mailboxID := adminMailboxIDForTest(t, a)
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }

	var draft MailMessage
	if code := client.do("POST", "/api/mail/drafts", map[string]any{
		"mailboxId": mailboxID,
		"to":        []string{"someone@example.test"},
		"subject":   "draft v1",
		"text":      "first body",
		"attachments": []map[string]any{{
			"filename": "old.txt", "contentType": "text/plain", "contentBase64": encode("old attachment"),
		}},
	}, &draft); code != http.StatusCreated {
		t.Fatalf("create draft code=%d", code)
	}

	var oldID, oldPath string
	if err := a.db.QueryRow(`SELECT id,storage_path FROM attachments WHERE message_id=?`, draft.ID).Scan(&oldID, &oldPath); err != nil {
		t.Fatalf("load old attachment: %v", err)
	}

	var updated MailMessage
	if code := client.do("POST", "/api/mail/drafts/"+draft.ID, map[string]any{
		"mailboxId": mailboxID,
		"to":        []string{"someone@example.test"},
		"subject":   "draft v2",
		"text":      "second body",
		"attachments": []map[string]any{{
			"filename": "new.txt", "contentType": "text/plain", "contentBase64": encode("new attachment"),
		}},
	}, &updated); code != http.StatusOK {
		t.Fatalf("update draft code=%d", code)
	}

	var names []string
	rows, err := a.db.Query(`SELECT filename FROM attachments WHERE message_id=?`, draft.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan attachment: %v", err)
		}
		names = append(names, name)
	}
	rows.Close()
	if len(names) != 1 || names[0] != "new.txt" {
		t.Fatalf("attachments after replace = %v, want [new.txt]", names)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old attachment file still exists at %s (err=%v)", oldPath, err)
	}
	var body string
	if err := a.db.QueryRow(`SELECT body_text FROM messages WHERE id=?`, draft.ID).Scan(&body); err != nil {
		t.Fatalf("load draft body: %v", err)
	}
	if body != "second body" {
		t.Fatalf("draft body = %q", body)
	}
}

// 删除自定义文件夹时要把邮件文件一起搬到 Inbox 目录：
// 只改 folder_id 会让 IMAP 仍看到旧文件夹，重建同名文件夹后旧邮件被同步吸回。
func TestDeleteMailFolderRelocatesMaildirFiles(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = t.TempDir() })
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	var login map[string]any
	if code := client.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	mailboxID := adminMailboxIDForTest(t, a)

	var folder MailFolder
	if code := client.do("POST", "/api/mail/folders", map[string]any{"name": "TempFolder"}, &folder); code != http.StatusCreated {
		t.Fatalf("create folder code=%d", code)
	}
	user, _, err := a.userByEmail(t.Context(), "admin@lanqin.local")
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	mb, err := a.mailboxByAddress(t.Context(), "admin@lanqin.local")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	msg, err := a.sendMailNow(t.Context(), user, mb, mailComposeInput{
		MailboxID: mailboxID,
		To:        []string{"recipient@example.test"},
		Subject:   "folder relocation",
		Text:      "body",
		HTML:      "<p>body</p>",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if code := client.do("POST", "/api/mail/messages/"+msg.ID+"/move", map[string]any{"folder": "TempFolder"}, nil); code != http.StatusOK {
		t.Fatalf("move code=%d", code)
	}
	var beforePath string
	if err := a.db.QueryRow(`SELECT raw_path FROM messages WHERE id=?`, msg.ID).Scan(&beforePath); err != nil {
		t.Fatalf("load raw path: %v", err)
	}
	if !strings.Contains(beforePath, ".TempFolder") {
		t.Fatalf("message not in custom folder: %s", beforePath)
	}
	if code := client.do("DELETE", "/api/mail/folders/"+folder.ID, nil, nil); code != http.StatusOK {
		t.Fatalf("delete folder code=%d", code)
	}
	var afterPath string
	if err := a.db.QueryRow(`SELECT raw_path FROM messages WHERE id=?`, msg.ID).Scan(&afterPath); err != nil {
		t.Fatalf("load raw path after delete: %v", err)
	}
	if strings.Contains(afterPath, ".TempFolder") {
		t.Fatalf("maildir file still under deleted folder: %s", afterPath)
	}
	if _, err := os.Stat(afterPath); err != nil {
		t.Fatalf("relocated file missing: %v", err)
	}
	folderBase := filepath.Dir(filepath.Dir(beforePath))
	if _, err := os.Stat(folderBase); !os.IsNotExist(err) {
		t.Fatalf("deleted folder directory still exists: %s (err=%v)", folderBase, err)
	}
}
