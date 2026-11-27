package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// Gmail 的 LIST 会返回 \Noselect 的层级节点（[Gmail]）。属性值是单个反斜杠加
// 名字，比较时必须用库常量；写成原始字符串里的两个反斜杠会永远匹配不上，
// 同步就会去 SELECT [Gmail] 并报 NONEXISTENT。
func TestMailboxHasNoSelectMatchesLibraryAttrs(t *testing.T) {
	if !mailboxHasNoSelect([]imap.MailboxAttr{imap.MailboxAttrNoSelect}) {
		t.Fatal(`\Noselect must be treated as non selectable`)
	}
	if !mailboxHasNoSelect([]imap.MailboxAttr{imap.MailboxAttrHasChildren, imap.MailboxAttrNonExistent}) {
		t.Fatal(`\NonExistent must be treated as non selectable`)
	}
	if mailboxHasNoSelect([]imap.MailboxAttr{imap.MailboxAttrHasChildren}) {
		t.Fatal("a selectable parent folder must not be filtered out")
	}
}

// 清理链路手里可能拿着已经消失的 message 引用，删除必须是幂等的，
// 否则一个孤儿引用会让整个外部账号删不掉。
func TestDeleteMessageSucceedsForMissingRow(t *testing.T) {
	a := newTestApp(t)
	if err := a.deleteMessage(context.Background(), "mail_missing_for_delete"); err != nil {
		t.Fatalf("deleting an already gone message must not fail: %v", err)
	}
}

func TestExternalIMAPCleanupRemovesOrphanMappings(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	accountID := "ximap_orphan_cleanup"
	now := a.now().UTC().Format(time.RFC3339Nano)

	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "orphan cleanup", "imap.example.test", 993, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}

	// 一条指向已消失邮件的孤儿映射，模拟旧版本切换存储模式后的残留。
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, accountID, "INBOX", 7, 1, "<orphan@example.test>", "mail_orphan_gone", 0, now, now); err != nil {
		t.Fatal(err)
	}
	// 一条正常消息 + 映射，必须保留。
	rawPath := filepath.Join(t.TempDir(), "alive.eml")
	if err := os.WriteFile(rawPath, []byte("Subject: alive\r\n\r\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO messages(id,mailbox_id,recipient_addr,message_uid,message_id,subject,from_addr,to_addrs,sent_at,received_at,snippet,body_text,body_html,raw_path,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "mail_alive", mailbox.ID, "local@example.test", newID("uid"), "<alive@example.test>", "alive", "sender@example.test", "[]", now, now, "", "body", "", rawPath, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, accountID, "INBOX", 7, 2, "<alive@example.test>", "mail_alive", 0, now, now); err != nil {
		t.Fatal(err)
	}
	// 游标停在 99：清理掉孤儿映射后必须归零，否则这些邮件永远不会再被拉取。
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,last_uid,uid_validity,local_folder_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)`, accountID, "INBOX", 99, 7, "", now, now); err != nil {
		t.Fatal(err)
	}

	a.pruneOrphanExternalIMAPMessages(ctx, accountID)

	var lastUID int
	if err := a.db.QueryRowContext(ctx, `SELECT last_uid FROM external_imap_folder_states WHERE account_id=? AND remote_folder=?`, accountID, "INBOX").Scan(&lastUID); err != nil {
		t.Fatal(err)
	}
	if lastUID != 0 {
		t.Fatalf("prune must reset the folder cursor, got last_uid=%d", lastUID)
	}

	var mapped int
	var survivor string
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(local_message_id),'') FROM external_imap_messages WHERE account_id=?`, accountID).Scan(&mapped, &survivor); err != nil {
		t.Fatal(err)
	}
	if mapped != 1 || survivor != "mail_alive" {
		t.Fatalf("prune must drop only the orphan mapping: mapped=%d survivor=%q", mapped, survivor)
	}

	// 清空账号数据时必须把映射一起删掉，否则下一轮同步又会把它们当成已导入。
	if err := a.deleteExternalIMAPLocalData(ctx, accountID, mailbox.UserID); err != nil {
		t.Fatalf("cleanup must not fail: %v", err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_imap_messages WHERE account_id=?`, accountID).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if mapped != 0 {
		t.Fatalf("cleanup left %d mapping rows behind", mapped)
	}
	// 文件在 maildir 根外，删除会失败；行仍必须被清掉，不能因此卡住账号删除。
	var alive int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=?`, "mail_alive").Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 0 {
		t.Fatal("cleanup must force-remove the message row even when the maildir file cannot be deleted")
	}
}

// Gmail 的「所有邮件」会把 INBOX 的邮件再列一遍：导入时必须跨文件夹按
// Message-ID 认同一封，只补映射，不再插一份新的，否则每封本地存两份。
func TestExternalIMAPAllMailFolderDeduplicatesAcrossFolders(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	now := a.now().UTC().Format(time.RFC3339Nano)
	accountID := "ximap_all_mail_dedupe"
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "Gmail", "imap.gmail.com", 993, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	inboxID, err := a.ensureFolder(ctx, mailbox.ID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	existingID, err := a.insertMessage(ctx, storedMessage{
		MailboxID: mailbox.ID, FolderID: inboxID, MessageUID: newID("uid"), MessageID: "<dup@example.test>",
		Subject: "dup", From: "sender@example.test", To: []string{mailbox.Address},
		SentAt: a.now().UTC(), ReceivedAt: a.now().UTC(), BodyText: "body", Snippet: "body",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	allFolderID, err := a.ensureFolder(ctx, mailbox.ID, "外部 · Gmail · [Gmail]/所有邮件")
	if err != nil {
		t.Fatal(err)
	}
	account, err := a.externalIMAPAccountByID(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	msgID, err := a.insertExternalIMAPMessageOnce(ctx, account, "[Gmail]/所有邮件",
		externalIMAPRemoteMessage{UID: 1, UIDValidity: 7, IsRead: true},
		storedMessage{MailboxID: mailbox.ID, FolderID: allFolderID, MessageID: "<dup@example.test>", Subject: "dup", From: "sender@example.test"},
		nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "" {
		t.Fatalf("duplicate message must not be inserted again, got id=%q", msgID)
	}
	var copies int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND message_id=?`, mailbox.ID, "<dup@example.test>").Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Fatalf("expected exactly one stored copy, got %d", copies)
	}
	var mappedTo string
	if err := a.db.QueryRowContext(ctx, `SELECT local_message_id FROM external_imap_messages WHERE account_id=? AND remote_folder=? AND uid=?`, accountID, "[Gmail]/所有邮件", 1).Scan(&mappedTo); err != nil {
		t.Fatal(err)
	}
	if mappedTo != existingID {
		t.Fatalf("mapping must point at the existing copy: got %q want %q", mappedTo, existingID)
	}
}

// 侧栏不该出现「[Gmail]/已删除邮件」这种带层级前缀的名字。
func TestExternalIMAPFolderDisplayName(t *testing.T) {
	cases := map[string]string{
		"[Gmail]/已删除邮件":        "已删除邮件",
		"[Gmail]/All Mail":       "所有邮件",
		"[Google Mail]/Sent Mail": "已发邮件",
		"[Gmail]/Spam":           "垃圾邮件",
		"INBOX":                  "INBOX",
		"Notes":                  "Notes",
		"自定义标签":                   "自定义标签",
	}
	for in, want := range cases {
		if got := externalIMAPFolderDisplayName(in); got != want {
			t.Fatalf("display name for %q = %q, want %q", in, got, want)
		}
	}
}

// 外部 IMAP 首次同步会把几周前的旧邮件整批导进来，这些不该推 Telegram。
func TestTelegramNotificationSkipsBackfilledMail(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.TelegramNotifyMaxAgeMinutes = 60 })
	now := a.now()
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"fresh", now.Add(-5 * time.Minute), false},
		{"just under limit", now.Add(-59 * time.Minute), false},
		{"backfilled", now.Add(-3 * time.Hour), true},
		{"weeks old", now.Add(-30 * 24 * time.Hour), true},
	}
	for _, c := range cases {
		if got := a.telegramNotificationTooOld(c.at.UTC().Format(time.RFC3339Nano)); got != c.want {
			t.Fatalf("%s: tooOld=%v want %v", c.name, got, c.want)
		}
	}
	if a.telegramNotificationTooOld("") {
		t.Fatal("missing received_at must not be treated as old")
	}
	a.updateConfig(func(cfg *Config) { cfg.TelegramNotifyMaxAgeMinutes = 0 })
	if a.telegramNotificationTooOld(now.Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)) {
		t.Fatal("0 must disable the age gate")
	}
}

// Gmail 中文界面 LIST 不带 SPECIAL-USE，只靠 \All 会漏判「所有邮件」，
// 结果同一封邮件在 INBOX 和所有邮件各存一份。
func TestExternalIMAPFolderIsAllMail(t *testing.T) {
	cases := []struct {
		name string
		f    externalIMAPRemoteFolder
		want bool
	}{
		{"special use attr", externalIMAPRemoteFolder{Name: "[Gmail]/All Mail", Role: externalIMAPFolderRoleAll}, true},
		{"chinese name only", externalIMAPRemoteFolder{Name: "[Gmail]/所有邮件", Role: "Imported"}, true},
		{"english name only", externalIMAPRemoteFolder{Name: "[Google Mail]/All Mail", Role: "Imported"}, true},
		{"inbox", externalIMAPRemoteFolder{Name: "INBOX", Role: "Inbox"}, false},
		{"custom label", externalIMAPRemoteFolder{Name: "[Gmail]/账单", Role: "Imported"}, false},
		{"trash", externalIMAPRemoteFolder{Name: "[Gmail]/已删除邮件", Role: "Trash"}, false},
	}
	for _, c := range cases {
		if got := externalIMAPFolderIsAllMail(c.f); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// 隐藏文件夹要能存、能去重，并且能把对应的本地文件夹从列表里滤掉。
func TestExternalIMAPHiddenFolders(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	accountID := "ximap_hidden_folders"
	now := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "hidden folders", "imap.example.test", 993, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	// 重复、空白都要归一。
	if err := a.replaceExternalIMAPHiddenFolders(ctx, accountID, []string{"Notes", " Notes ", "", "Notes"}); err != nil {
		t.Fatalf("save hidden folders: %v", err)
	}
	hidden := a.externalIMAPHiddenFolderSet(ctx, accountID)
	if len(hidden) != 1 || !hidden["Notes"] {
		t.Fatalf("hidden set = %v, want only Notes", hidden)
	}

	var folderID string
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM folders WHERE mailbox_id=? ORDER BY sort_order,id LIMIT 1`, mailbox.ID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)`, accountID, "Notes", folderID, 1, 0, now, now); err != nil {
		t.Fatal(err)
	}
	if got := a.hiddenExternalLocalFolderIDs(ctx, mailbox.ID); !got[folderID] {
		t.Fatalf("local folder %s linked to hidden Notes must be filtered, got %v", folderID, got)
	}
	// 取消隐藏后本地文件夹要回到列表。
	if err := a.replaceExternalIMAPHiddenFolders(ctx, accountID, nil); err != nil {
		t.Fatal(err)
	}
	if got := a.hiddenExternalLocalFolderIDs(ctx, mailbox.ID); len(got) != 0 {
		t.Fatalf("after unhide hidden local folders = %v, want none", got)
	}
}
