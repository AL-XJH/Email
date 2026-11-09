package app

import (
	"net/http"
	"testing"
)

func bulkTestMailbox(t *testing.T) (*App, *testClient, string, string, string) {
	t.Helper()
	a, admin, _, owner, domain := setupQuotaTest(t)
	mailbox := createTestMailbox(t, admin, domain.ID, "bulkbox", "Bulk box", "", map[string]any{
		"userId": owner.ID,
	})
	var folderID string
	if err := a.db.QueryRow(`SELECT id FROM folders WHERE mailbox_id=? AND role='inbox'`, mailbox.ID).Scan(&folderID); err != nil {
		t.Fatalf("load inbox folder: %v", err)
	}
	client := &testClient{t: t, server: admin.server}
	if code := client.do("POST", "/api/auth/login", map[string]string{
		"email": owner.Email, "password": "Password123!",
	}, nil); code != http.StatusOK {
		t.Fatalf("owner login code=%d", code)
	}
	return a, client, mailbox.ID, folderID, owner.ID
}

func insertBulkTestMessage(t *testing.T, a *App, mailboxID, folderID, id string, uid int, starred, read bool) {
	t.Helper()
	ts := "2026-11-01T10:00:00Z"
	if _, err := a.db.Exec(`INSERT INTO messages(id,mailbox_id,folder_id,message_uid,message_id,thread_id,subject,from_addr,from_name,to_addrs,
		sent_at,received_at,snippet,body_text,body_html,is_read,is_starred,size_bytes,imap_uid,created_at,updated_at)
		VALUES(? ,?, ?, ?, ?, ?, 'subject', 'a@example.com', 'A', '["bulk@example.test"]', ?, ?, 'snip', 'body', '<p>body</p>', ?, ?, 1024, ?, ?, ?)`,
		id, mailboxID, folderID, "uid_"+id, id+"@example.test", id+"@example.test", ts, ts, boolInt(read), boolInt(starred), uid, ts, ts); err != nil {
		t.Fatalf("insert message %s: %v", id, err)
	}
}

func TestBulkMessageActionMarksReadInOneRequest(t *testing.T) {
	a, client, mailboxID, folderID, _ := bulkTestMailbox(t)
	for i, id := range []string{"msg_bulk1", "msg_bulk2", "msg_bulk3"} {
		insertBulkTestMessage(t, a, mailboxID, folderID, id, i+1, false, false)
	}
	var resp struct {
		OK      bool     `json:"ok"`
		Updated int      `json:"updated"`
		Missing []string `json:"missing"`
	}
	if code := client.do("POST", "/api/mail/messages/bulk", map[string]any{
		"ids":    []string{"msg_bulk1", "msg_bulk2", "msg_bulk3"},
		"action": "read",
	}, &resp); code != http.StatusOK {
		t.Fatalf("bulk read code=%d", code)
	}
	if !resp.OK || resp.Updated != 3 {
		t.Fatalf("resp=%+v, want ok with 3 updated", resp)
	}
	var unread int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id IN ('msg_bulk1','msg_bulk2','msg_bulk3') AND is_read=0`).Scan(&unread); err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 0 {
		t.Fatalf("%d messages still unread", unread)
	}
}

func TestBulkMessageActionSkipsForeignAndUnknownIDs(t *testing.T) {
	a, client, mailboxID, folderID, _ := bulkTestMailbox(t)
	insertBulkTestMessage(t, a, mailboxID, folderID, "msg_owned", 1, false, false)

	// 另一封属于管理员邮箱的邮件：批量操作不能碰它。
	var adminMailboxID, adminFolderID string
	if err := a.db.QueryRow(`SELECT id FROM mailboxes WHERE user_id=(SELECT id FROM users WHERE email='admin@lanqin.local') LIMIT 1`).Scan(&adminMailboxID); err != nil {
		t.Fatalf("load admin mailbox: %v", err)
	}
	if err := a.db.QueryRow(`SELECT id FROM folders WHERE mailbox_id=? AND role='inbox'`, adminMailboxID).Scan(&adminFolderID); err != nil {
		t.Fatalf("load admin inbox: %v", err)
	}
	insertBulkTestMessage(t, a, adminMailboxID, adminFolderID, "msg_foreign", 9001, false, false)

	var resp struct {
		OK      bool     `json:"ok"`
		Updated int      `json:"updated"`
		Missing []string `json:"missing"`
	}
	if code := client.do("POST", "/api/mail/messages/bulk", map[string]any{
		"ids":    []string{"msg_owned", "msg_foreign", "msg_ghost"},
		"action": "star",
	}, &resp); code != http.StatusOK {
		t.Fatalf("bulk star code=%d", code)
	}
	if resp.Updated != 1 {
		t.Fatalf("updated=%d, want 1", resp.Updated)
	}
	if len(resp.Missing) != 2 {
		t.Fatalf("missing=%v, want the foreign and unknown ids", resp.Missing)
	}
	var foreignStarred, ownedStarred int
	if err := a.db.QueryRow(`SELECT is_starred FROM messages WHERE id='msg_foreign'`).Scan(&foreignStarred); err != nil {
		t.Fatalf("load foreign: %v", err)
	}
	if err := a.db.QueryRow(`SELECT is_starred FROM messages WHERE id='msg_owned'`).Scan(&ownedStarred); err != nil {
		t.Fatalf("load owned: %v", err)
	}
	if foreignStarred != 0 {
		t.Fatal("bulk action touched a message from another user")
	}
	if ownedStarred != 1 {
		t.Fatal("owned message was not starred")
	}
}

func TestBulkMessageActionRejectsUnknownAction(t *testing.T) {
	_, client, mailboxID, folderID, _ := bulkTestMailbox(t)
	_ = mailboxID
	_ = folderID
	if code := client.do("POST", "/api/mail/messages/bulk", map[string]any{
		"ids": []string{"msg_any"}, "action": "explode",
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown action code=%d, want 400", code)
	}
	if code := client.do("POST", "/api/mail/messages/bulk", map[string]any{
		"ids": []string{}, "action": "read",
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty ids code=%d, want 400", code)
	}
}
