package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		"[Gmail]/已删除邮件":           "已删除邮件",
		"[Gmail]/All Mail":        "所有邮件",
		"[Google Mail]/Sent Mail": "已发送",
		"[Gmail]/Spam":            "垃圾邮件",
		"INBOX":                   "收件箱",
		"Notes":                   "备忘录",
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

// 同步顺序不确定：INBOX 后跑时也必须认出「所有邮件」已经存过的那封信。
func TestExternalIMAPCrossFolderDedupeBothDirections(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	now := a.now().UTC().Format(time.RFC3339Nano)
	accountID := "ximap_cross_folder_dedupe"
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "Gmail", "imap.gmail.com", 993, externalIMAPTLS, "remote@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	inboxID, err := a.ensureFolder(ctx, mailbox.ID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	allID, err := a.ensureFolder(ctx, mailbox.ID, "外部 · Gmail · [Gmail]/所有邮件")
	if err != nil {
		t.Fatal(err)
	}
	account, err := a.externalIMAPAccountByID(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	base := storedMessage{MailboxID: mailbox.ID, MessageID: "<both@example.test>", Subject: "both", From: "sender@example.test"}
	first := base
	first.FolderID = allID
	firstID, err := a.insertExternalIMAPMessageOnce(ctx, account, "[Gmail]/所有邮件",
		externalIMAPRemoteMessage{UID: 1, UIDValidity: 9}, first, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == "" {
		t.Fatal("first import must insert a message")
	}
	second := base
	second.FolderID = inboxID
	secondID, err := a.insertExternalIMAPMessageOnce(ctx, account, "INBOX",
		externalIMAPRemoteMessage{UID: 1, UIDValidity: 4}, second, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if secondID != "" {
		t.Fatalf("INBOX import must reuse the copy stored by All Mail, got %q", secondID)
	}
	var copies int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND message_id=?`, mailbox.ID, "<both@example.test>").Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Fatalf("expected exactly one stored copy, got %d", copies)
	}
}

// 建号前必须先连一次：连不上就 400，不要把坏账号写进库再让用户自己发现。
func TestCreateExternalIMAPAccountValidatesConnection(t *testing.T) {
	dir := newTestDir(t)
	a := newTestAppWithConfig(t, Config{
		Addr:                          ":0",
		DBPath:                        filepath.Join(dir, "lanqin.db"),
		DataDir:                       filepath.Join(dir, "data"),
		CookieName:                    "lanqin_test",
		SessionTTLHours:               24,
		AdminEmail:                    "admin@lanqin.local",
		AdminPassword:                 "ChangeMe123!",
		PublicHostname:                "mail.example.test",
		PublicBaseURL:                 "http://localhost:5173",
		AllowInsecureHTTP:             true,
		ExternalIMAPEnabled:           true,
		ExternalIMAPSecretKey:         "test-secret",
		ExternalIMAPAllowPrivateHosts: true,
		ExternalIMAPVerifyOnCreate:    true,
	})
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	_, mb := defaultAdminUserAndMailbox(t, a)
	payload := map[string]any{"mailboxId": mb.ID, "name": "dead", "host": "127.0.0.1", "port": 1, "tlsMode": "tls", "username": "user@example.test", "password": "pw", "storageMode": "remote", "syncReadState": true, "enabled": true}
	if code := admin.do("POST", "/api/me/external-imap-accounts", payload, nil); code != http.StatusBadRequest {
		t.Fatalf("unreachable account must be rejected with 400, got %d", code)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM external_imap_accounts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed account must not be saved, rows=%d", count)
	}
}

type pickUsernameFakeFactory struct {
	ok map[string]bool
}

func (f pickUsernameFakeFactory) openExternalIMAPClient(ctx context.Context, account externalIMAPAccountRecord) (externalIMAPClient, error) {
	return pickUsernameFakeClient{username: account.Username, ok: f.ok[strings.ToLower(account.Username)]}, nil
}

type pickUsernameFakeClient struct {
	username string
	ok       bool
}

func (c pickUsernameFakeClient) Close() error { return nil }
func (c pickUsernameFakeClient) ListFolders(ctx context.Context) ([]externalIMAPRemoteFolder, error) {
	if !c.ok {
		return nil, errors.New("imap: NO User is authenticated but not connected.")
	}
	return []externalIMAPRemoteFolder{{Name: "INBOX", Role: "inbox"}}, nil
}
func (c pickUsernameFakeClient) FetchSummaries(ctx context.Context, folder string, query string, cursor string, limit int) ([]externalIMAPRemoteMessage, string, int, error) {
	return nil, "", 0, nil
}
func (c pickUsernameFakeClient) FetchNew(ctx context.Context, folder string, afterUID uint32, limit int) ([]externalIMAPRemoteMessage, error) {
	return nil, nil
}
func (c pickUsernameFakeClient) FetchRaw(ctx context.Context, folder string, uid uint32) ([]byte, externalIMAPRemoteMessage, error) {
	return nil, externalIMAPRemoteMessage{}, nil
}
func (c pickUsernameFakeClient) FetchAttachments(ctx context.Context, folder string, uid uint32) ([]Attachment, error) {
	return nil, nil
}
func (c pickUsernameFakeClient) FetchPart(ctx context.Context, folder string, uid uint32, partID string) ([]byte, Attachment, error) {
	return nil, Attachment{}, nil
}
func (c pickUsernameFakeClient) SetRead(ctx context.Context, folder string, uid uint32, read bool) error {
	return nil
}

// 微软授权返回的登录别名连不上时，要退到用户填的邮箱主地址。
func TestPickExternalIMAPOAuthUsernameFallsBackToPrimary(t *testing.T) {
	a := &App{}
	a.externalIMAP = pickUsernameFakeFactory{ok: map[string]bool{"al-xjh@outlook.com": true}}
	username, err := a.pickExternalIMAPOAuthUsername(context.Background(), externalIMAPOAuthOutlook,
		externalIMAPOAuthProvider{Name: "Outlook", Host: "outlook.office365.com", Port: 993},
		[]string{"al-xjh@outlook.com", "xjh@alxjh.cn"}, "access", "refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if username != "al-xjh@outlook.com" {
		t.Fatalf("username=%q, want primary mailbox", username)
	}
	if _, err := a.pickExternalIMAPOAuthUsername(context.Background(), externalIMAPOAuthOutlook,
		externalIMAPOAuthProvider{Host: "outlook.office365.com", Port: 993}, []string{"xjh@alxjh.cn"}, "access", "refresh"); err == nil {
		t.Fatal("expected error when no candidate can log in")
	}
}

// 文件夹顺序：保存后列表按 position 返回；本地模式还要把镜像文件夹的 sort_order
// 在原有取值之间互换，侧栏顺序才跟着变。
func TestExternalIMAPFolderOrderAppliesToLocalFolders(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	accountID := "ximap_folder_order"
	now := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "order", "imap.example.test", 993, externalIMAPTLS, "order@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	insertFolder := func(id, name string, order int) {
		if _, err := a.db.ExecContext(ctx, `INSERT INTO folders(id,mailbox_id,name,role,sort_order,uid_validity,uid_next,highest_modseq,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, mailbox.ID, name, "", order, 1, 1, 1, now); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			accountID, name, id, 1, 0, now, now); err != nil {
			t.Fatal(err)
		}
	}
	insertFolder("fld_order_a", "INBOX", 30)
	insertFolder("fld_order_b", "Junk", 40)
	account, err := a.externalIMAPAccountByID(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.replaceExternalIMAPFolderOrder(ctx, account, []string{"Junk", "INBOX"}); err != nil {
		t.Fatalf("save order: %v", err)
	}
	if got := a.externalIMAPFolderOrder(ctx, accountID); len(got) != 2 || got[0] != "Junk" || got[1] != "INBOX" {
		t.Fatalf("order = %v, want [Junk INBOX]", got)
	}
	var inboxOrder, junkOrder int
	if err := a.db.QueryRowContext(ctx, `SELECT sort_order FROM folders WHERE id='fld_order_a'`).Scan(&inboxOrder); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT sort_order FROM folders WHERE id='fld_order_b'`).Scan(&junkOrder); err != nil {
		t.Fatal(err)
	}
	if junkOrder != 30 || inboxOrder != 40 {
		t.Fatalf("local sort_order inbox=%d junk=%d, want swapped 40/30", inboxOrder, junkOrder)
	}
}

// Outlook 的系统文件夹是英文短名，设置页要显示中文。
func TestExternalIMAPFolderDisplayNameTranslatesOutlookNames(t *testing.T) {
	cases := map[string]string{
		"INBOX":                            "收件箱",
		"Junk":                             "垃圾邮件",
		"Sent":                             "已发送",
		"Deleted":                          "已删除邮件",
		"Outbox":                           "发件箱",
		"Notes":                            "备忘录",
		"Synchronization Issues/Conflicts": "同步问题/冲突",
		"我的文件夹":                            "我的文件夹",
	}
	for remote, want := range cases {
		if got := externalIMAPFolderDisplayName(remote); got != want {
			t.Errorf("displayName(%q)=%q, want %q", remote, got, want)
		}
	}
}

// 本地删掉外部导入件后必须留墓碑并摘掉映射：映射留着会被 prune 当孤儿，
// 连带把游标归零，删掉的信又整批回来。
func TestRecordExternalIMAPDeletionLeavesTombstone(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	now := a.now().UTC().Format(time.RFC3339Nano)
	accountID := "ximap_tombstone"
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "tombstone", "imap.example.test", 993, externalIMAPTLS, "tomb@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	folderID, err := a.ensureFolder(ctx, mailbox.ID, "外部 · Gmail · INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO messages(id,mailbox_id,folder_id,recipient_addr,message_uid,message_id,subject,from_addr,from_name,to_addrs,cc_addrs,bcc_addrs,sent_at,received_at,snippet,body_text,body_html,is_read,is_starred,has_attachments,size_bytes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "msg_tombstone", mailbox.ID, folderID, "", "uid-1", "<tomb@example.test>", "tomb", "a@example.test", "", "[]", "[]", "[]", now, now, "", "", "", 0, 0, 0, 0, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, accountID, "INBOX", 7, 42, "<tomb@example.test>", "msg_tombstone", 0, now, now); err != nil {
		t.Fatal(err)
	}
	a.recordExternalIMAPDeletion(ctx, "msg_tombstone")
	var mappings int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_imap_messages WHERE local_message_id='msg_tombstone'`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 {
		t.Fatalf("mapping rows=%d, want 0", mappings)
	}
	deleted := a.externalIMAPDeletedUIDs(ctx, accountID, "INBOX", 7)
	if !deleted[42] {
		t.Fatalf("uid 42 must be tombstoned, got %v", deleted)
	}
	if other := a.externalIMAPDeletedUIDs(ctx, accountID, "INBOX", 8); len(other) != 0 {
		t.Fatalf("tombstone must not leak into another UIDVALIDITY, got %v", other)
	}
}

// 一个文件夹出现孤儿时，只重置这个文件夹的游标，别把其它文件夹也倒回去。
func TestPruneOrphanExternalIMAPResetsOnlyAffectedFolder(t *testing.T) {
	a := newTestApp(t)
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	now := a.now().UTC().Format(time.RFC3339Nano)
	accountID := "ximap_prune_scope"
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "prune", "imap.example.test", 993, externalIMAPTLS, "prune@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"INBOX", "Archive"} {
		if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,uid_validity,last_uid,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
			accountID, folder, 1, 500, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, accountID, "INBOX", 1, 10, "<gone@example.test>", "msg_gone", 0, now, now); err != nil {
		t.Fatal(err)
	}
	a.pruneOrphanExternalIMAPMessages(ctx, accountID)
	var inboxUID, archiveUID int
	if err := a.db.QueryRowContext(ctx, `SELECT last_uid FROM external_imap_folder_states WHERE account_id=? AND remote_folder='INBOX'`, accountID).Scan(&inboxUID); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT last_uid FROM external_imap_folder_states WHERE account_id=? AND remote_folder='Archive'`, accountID).Scan(&archiveUID); err != nil {
		t.Fatal(err)
	}
	if inboxUID != 0 {
		t.Fatalf("INBOX cursor=%d, want reset to 0", inboxUID)
	}
	if archiveUID != 500 {
		t.Fatalf("Archive cursor=%d, must stay 500", archiveUID)
	}
}

// 删除只是把本地邮件移进 Trash，映射还指向 INBOX；列表必须按当前文件夹过滤，
// 否则删完还在原地，用户得点好几次（第二次才是彻底删除）。
func TestExternalIMAPLocalFolderListHidesMovedMessages(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPEnabled = true })
	_, mailbox := defaultAdminUserAndMailbox(t, a)
	ctx := context.Background()
	now := a.now().UTC().Format(time.RFC3339Nano)
	accountID := "ximap_list_scope"
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, mailbox.UserID, mailbox.ID, "scope", "imap.example.test", 993, externalIMAPTLS, "scope@example.test", "ciphertext", externalIMAPStorageLocal, 1, 1, "idle", now, now); err != nil {
		t.Fatal(err)
	}
	inboxID, err := a.ensureFolder(ctx, mailbox.ID, "外部 · Gmail · INBOX")
	if err != nil {
		t.Fatal(err)
	}
	trashID, err := a.ensureFolder(ctx, mailbox.ID, "Trash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		accountID, "INBOX", inboxID, 1, 100, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO messages(id,mailbox_id,folder_id,recipient_addr,message_uid,message_id,subject,from_addr,from_name,to_addrs,cc_addrs,bcc_addrs,sent_at,received_at,snippet,body_text,body_html,is_read,is_starred,has_attachments,size_bytes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "msg_scope", mailbox.ID, inboxID, "", "uid-scope", "<scope@example.test>", "scope", "a@example.test", "", "[]", "[]", "[]", now, now, "", "", "", 0, 0, 0, 0, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, accountID, "INBOX", 1, 100, "<scope@example.test>", "msg_scope", 0, now, now); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	admin := &testClient{t: t, server: ts}
	if code := admin.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	var listed struct {
		Items      []MailMessage `json:"items"`
		TotalCount int           `json:"totalCount"`
	}
	if code := admin.do("GET", "/api/mail/external-accounts/"+accountID+"/messages?folder=INBOX", nil, &listed); code != http.StatusOK {
		t.Fatalf("list code=%d", code)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("items=%d, want 1 before delete", len(listed.Items))
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE messages SET folder_id=? WHERE id='msg_scope'`, trashID); err != nil {
		t.Fatal(err)
	}
	if code := admin.do("GET", "/api/mail/external-accounts/"+accountID+"/messages?folder=INBOX", nil, &listed); code != http.StatusOK {
		t.Fatalf("list after move code=%d", code)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("items=%d after moving to Trash, want 0", len(listed.Items))
	}
}
