package app

import (
	"context"
	"testing"
	"time"
)

func TestExternalIMAPSystemRole(t *testing.T) {
	cases := map[string]string{
		"Inbox":             "Inbox",
		"收件箱":               "Inbox",
		"Spam":              "Spam",
		"垃圾邮件":              "Spam",
		"Trash":             "Trash",
		"已删除邮件":             "Trash",
		"Archive":           "Archive",
		"存档":                "Archive",
		"外部 · Gmail · 垃圾邮件": "",
	}
	for input, want := range cases {
		if got := externalIMAPSystemRole(input); got != want {
			t.Fatalf("externalIMAPSystemRole(%q)=%q want %q", input, got, want)
		}
	}
}

// 外部视图的移动/删除要把本地文件夹换成远端文件夹名：镜像文件夹按
// local_folder_id 精确匹配，系统角色按快照里的 SPECIAL-USE role 找。
func TestExternalIMAPRemoteFolderForLocal(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	accountID := "ximap_remote_folder_test"
	now := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "remote folder test", "imap.example.test", 993, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	a.saveExternalIMAPFolderSnapshot(ctx, accountID, []externalIMAPRemoteFolder{
		{Name: "INBOX", Role: "Inbox"},
		{Name: "[Gmail]/垃圾邮件", Role: "Spam"},
		{Name: "[Gmail]/已删除邮件", Role: "Trash"},
	})
	if got, ok := a.externalIMAPRemoteFolderForLocal(ctx, accountID, "", "Spam"); !ok || got != "[Gmail]/垃圾邮件" {
		t.Fatalf("role spam resolved to %q ok=%v", got, ok)
	}
	if _, ok := a.externalIMAPRemoteFolderForLocal(ctx, accountID, "", "外部 · Gmail · 垃圾邮件"); ok {
		t.Fatal("mirror display name must not be treated as a system role")
	}

	// 镜像文件夹：有 folder_states 时按 local_folder_id 精确匹配。
	if _, err := a.db.ExecContext(ctx, `INSERT INTO folders(id,mailbox_id,name,role,created_at) VALUES(?,?,?,?,?)`,
		"fld_remote_test", mailbox.ID, "外部 · Gmail · 垃圾邮件", "外部 · gmail · 垃圾邮件", now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		accountID, "[Gmail]/垃圾邮件", "fld_remote_test", 1, 0, now, now); err != nil {
		t.Fatal(err)
	}
	if got, ok := a.externalIMAPRemoteFolderForLocal(ctx, accountID, "fld_remote_test", "不相关名字"); !ok || got != "[Gmail]/垃圾邮件" {
		t.Fatalf("local folder id resolved to %q ok=%v", got, ok)
	}
}

// Gmail 一封邮件同时挂在 INBOX 和「所有邮件」下，移动要挑当前文件夹对应的
// 那条映射，否则会从「所有邮件」里把整封信删掉。
func TestExternalIMAPPrimaryMappingPrefersCurrentFolder(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	now := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "ximap_primary_test", mailbox.UserID, mailbox.ID, "primary test", "imap.example.test", 993, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO folders(id,mailbox_id,name,role,created_at) VALUES(?,?,?,?,?)`,
		"fld_primary_test", mailbox.ID, "外部 · Gmail · 收件箱", "外部 · gmail · inbox", now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		"ximap_primary_test", "INBOX", "fld_primary_test", 1, 0, now, now); err != nil {
		t.Fatal(err)
	}
	mappings := []externalIMAPRemoteMapping{
		{AccountID: "ximap_primary_test", RemoteFolder: "[Gmail]/所有邮件", UID: 11},
		{AccountID: "ximap_primary_test", RemoteFolder: "INBOX", UID: 22},
	}
	msg := &MailMessage{ID: "mail_primary_test", FolderID: "fld_primary_test"}
	got := a.externalIMAPPrimaryMapping(ctx, msg, mappings)
	if got == nil || got.RemoteFolder != "INBOX" {
		t.Fatalf("primary mapping=%+v, want INBOX", got)
	}
}
