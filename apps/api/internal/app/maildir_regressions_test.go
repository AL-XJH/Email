package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 附件写入失败时，回滚路径不能再查数据库：SQLite 只有一条连接，
// 事务正握着它，再发查询会一直等到 ctx 超时，期间全站数据库操作全部停摆。
func TestInsertMessageAttachmentFailureRollbackDoesNotDeadlock(t *testing.T) {
	a := newTestApp(t)
	dataFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(dataFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.updateConfig(func(cfg *Config) { cfg.DataDir = dataFile })
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)
	folderID, err := a.ensureFolder(ctx, mailboxID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	now := a.now().UTC()
	msg := storedMessage{
		MailboxID:  mailboxID,
		FolderID:   folderID,
		MessageUID: newID("uid"),
		MessageID:  "<attach-rollback@example.test>",
		Subject:    "with attachment",
		From:       "a@example.test",
		To:         []string{"admin@lanqin.local"},
		SentAt:     now,
		ReceivedAt: now,
		BodyText:   "body",
	}
	attachments := []AttachmentInput{{Filename: "a.bin", ContentType: "application/octet-stream", ContentBase64: "AAAA"}}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.insertMessageWithDB(ctx, tx, msg, attachments)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			_ = tx.Rollback()
			t.Fatal("expected attachment failure")
		}
		_ = tx.Rollback()
	case <-time.After(3 * time.Second):
		t.Fatal("insertMessageWithDB blocked: rollback path queried the database while the transaction held the only connection")
	}
	// 事务回滚后数据库仍然可用。
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
		t.Fatalf("database unusable after rollback: %v", err)
	}
}

// 同一文件夹里两个不同文件共用 Message-ID（MDA 重复投递、两个别名投给同一个人、
// 用户手工复制）是两封邮件，不能把第二个文件从磁盘删掉。
func TestMaildirSyncKeepsDuplicateMessageIDFiles(t *testing.T) {
	a := newTestApp(t)
	root := t.TempDir()
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = root })
	mb, err := a.mailboxByAddress(context.Background(), "admin@lanqin.local")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(mb.Address, "@", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected mailbox address %q", mb.Address)
	}
	base := filepath.Join(root, parts[1], parts[0], "Maildir")
	dir := filepath.Join(base, "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "1000000001.first")
	second := filepath.Join(dir, "1000000002.second")
	body := func(text string) string {
		return "From: sender@example.test\r\nTo: " + mb.Address + "\r\nSubject: dup\r\nMessage-ID: <dup@example.test>\r\n\r\n" + text
	}
	if err := os.WriteFile(first, []byte(body("FIRST body")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(body("SECOND body")), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.syncMaildirOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("first file disappeared: %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("second file was deleted by maildir sync: %v", err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND message_id=?`, mb.ID, "<dup@example.test>").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("imported %d messages, want 2 (both files must be indexed)", count)
	}
}

// 在 Sent 文件夹里把邮件标为未读，不能被下一轮同步翻回已读。
func TestMarkUnreadInSentSurvivesMaildirSync(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = t.TempDir() })
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)
	mb, err := a.mailboxByID(ctx, mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	sentID, err := a.ensureFolder(ctx, mailboxID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	now := a.now().UTC()
	msg := storedMessage{
		MailboxID:  mailboxID,
		FolderID:   sentID,
		MessageUID: newID("uid"),
		MessageID:  "<sent-unread@example.test>",
		Subject:    "sent",
		From:       mb.Address,
		To:         []string{"person@example.test"},
		SentAt:     now,
		ReceivedAt: now,
		BodyText:   "body",
		IsRead:     true,
	}
	id, err := a.insertMessage(ctx, msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.writeStoredMessageToMaildir(ctx, id, msg, nil); err != nil {
		t.Fatal(err)
	}
	read := false
	if err := a.updateMessageMaildirFlags(ctx, id, &read, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncMaildirOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var isRead int
	var rawPath string
	if err := a.db.QueryRow(`SELECT is_read,COALESCE(raw_path,'') FROM messages WHERE id=?`, id).Scan(&isRead, &rawPath); err != nil {
		t.Fatal(err)
	}
	if isRead != 0 {
		t.Fatalf("message flipped back to read after sync, path=%s", rawPath)
	}
}
