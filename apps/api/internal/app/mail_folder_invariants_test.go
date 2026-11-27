package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 跨文件夹操作的「账」：移动 / 标已读 / 彻底删除之后，两边文件夹的计数、
// 磁盘位置、附件文件必须同时对上。这类不一致用户一眼就能看见（侧栏数字、
// 列表里的邮件、磁盘占用），但平时的正向测试只验证单点。

func invariantFixture(t *testing.T) (a *App, ctx context.Context, mailboxID string) {
	t.Helper()
	a = newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = t.TempDir() })
	return a, context.Background(), adminMailboxIDForTest(t, a)
}

func insertInboxMessage(t *testing.T, a *App, ctx context.Context, mailboxID, messageID string, read bool) (string, storedMessage, string) {
	t.Helper()
	folderID, err := a.ensureFolder(ctx, mailboxID, "Inbox")
	if err != nil {
		t.Fatalf("ensure inbox: %v", err)
	}
	now := a.now().UTC()
	msg := storedMessage{
		MailboxID:  mailboxID,
		FolderID:   folderID,
		MessageUID: newID("uid"),
		MessageID:  messageID,
		Subject:    "invariant",
		From:       "sender@example.test",
		To:         []string{"admin@lanqin.local"},
		SentAt:     now,
		ReceivedAt: now,
		BodyText:   "body",
		IsRead:     read,
	}
	id, err := a.insertMessage(ctx, msg, nil)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := a.writeStoredMessageToMaildir(ctx, id, msg, nil); err != nil {
		t.Fatalf("write maildir: %v", err)
	}
	return id, msg, folderID
}

func folderCounts(t *testing.T, a *App, ctx context.Context, mailboxID, name string) (unread, total int) {
	t.Helper()
	var unreadCount, totalCount int
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN m.is_read=0 THEN 1 ELSE 0 END),0), COUNT(m.id)
		FROM folders f LEFT JOIN messages m ON m.folder_id=f.id WHERE f.mailbox_id=? AND f.name=?`, mailboxID, name).Scan(&unreadCount, &totalCount); err != nil {
		t.Fatalf("count folder %s: %v", name, err)
	}
	return unreadCount, totalCount
}

// 移动之后源文件夹的未读/总数必须减少，目标文件夹必须增加。
func TestMoveKeepsFolderCountsConsistent(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	baseUnread, baseTotal := folderCounts(t, a, ctx, mailboxID, "Inbox")
	baseArchiveUnread, baseArchiveTotal := folderCounts(t, a, ctx, mailboxID, "Archive")

	id, msg, _ := insertInboxMessage(t, a, ctx, mailboxID, "<inv-move@example.test>", false)
	if unread, total := folderCounts(t, a, ctx, mailboxID, "Inbox"); unread != baseUnread+1 || total != baseTotal+1 {
		t.Fatalf("inbox after insert: unread=%d total=%d, want %d/%d", unread, total, baseUnread+1, baseTotal+1)
	}
	if err := a.applyMessageMove(ctx, msgPtr(msg, id), "Archive"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if unread, total := folderCounts(t, a, ctx, mailboxID, "Inbox"); unread != baseUnread || total != baseTotal {
		t.Fatalf("inbox after move: unread=%d total=%d, want %d/%d", unread, total, baseUnread, baseTotal)
	}
	if unread, total := folderCounts(t, a, ctx, mailboxID, "Archive"); unread != baseArchiveUnread+1 || total != baseArchiveTotal+1 {
		t.Fatalf("archive after move: unread=%d total=%d, want %d/%d", unread, total, baseArchiveUnread+1, baseArchiveTotal+1)
	}
}

// 同一封邮件移动到它已经在的文件夹，不能丢文件、不能变成两行。
func TestMoveToSameFolderIsHarmless(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	var baseCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=?`, mailboxID).Scan(&baseCount); err != nil {
		t.Fatal(err)
	}
	id, msg, _ := insertInboxMessage(t, a, ctx, mailboxID, "<inv-same@example.test>", false)
	before := trackedRawPath(t, a, id)

	if err := a.applyMessageMove(ctx, msgPtr(msg, id), "Inbox"); err != nil {
		t.Fatalf("move to same folder: %v", err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=?`, mailboxID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != baseCount+1 {
		t.Fatalf("同文件夹移动后 messages=%d，期望 %d（不能多出一行）", count, baseCount+1)
	}
	after := trackedRawPath(t, a, id)
	if _, err := os.Stat(after); err != nil {
		t.Fatalf("raw file missing after no-op move: %v", err)
	}
	if after != before {
		// 允许重写文件名，但必须还是 Inbox 目录
		if filepath.Dir(after) != filepath.Dir(before) {
			t.Fatalf("no-op move changed directory: %q -> %q", before, after)
		}
	}
}

// 标为已读必须让「文件夹未读数」跟着降，否则侧栏数字和列表永远对不上。
func TestMarkReadUpdatesFolderUnreadCount(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	baseUnread, baseTotal := folderCounts(t, a, ctx, mailboxID, "Inbox")
	id, msg, _ := insertInboxMessage(t, a, ctx, mailboxID, "<inv-read@example.test>", false)

	if err := a.applyMessageRead(ctx, msgPtr(msg, id), true); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if unread, total := folderCounts(t, a, ctx, mailboxID, "Inbox"); unread != baseUnread || total != baseTotal+1 {
		t.Fatalf("after mark read: unread=%d total=%d, want %d/%d", unread, total, baseUnread, baseTotal+1)
	}
	// 再标未读要回去
	if err := a.applyMessageRead(ctx, msgPtr(msg, id), false); err != nil {
		t.Fatalf("mark unread: %v", err)
	}
	if unread, _ := folderCounts(t, a, ctx, mailboxID, "Inbox"); unread != baseUnread+1 {
		t.Fatalf("after mark unread: unread=%d, want %d", unread, baseUnread+1)
	}
}

// 彻底删除（在 Trash 里再删）要同时清掉：库行、原文文件、附件文件与目录。
func TestTrashDeleteRemovesRowFilesAndAttachments(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	trashID, err := a.ensureFolder(ctx, mailboxID, "Trash")
	if err != nil {
		t.Fatal(err)
	}
	now := a.now().UTC()
	msg := storedMessage{
		MailboxID: mailboxID, FolderID: trashID, MessageUID: newID("uid"),
		MessageID: "<inv-trash@example.test>", Subject: "with attachment",
		From: "sender@example.test", To: []string{"admin@lanqin.local"},
		SentAt: now, ReceivedAt: now, BodyText: "body",
	}
	attachments := []AttachmentInput{{Filename: "a.txt", ContentType: "text/plain", ContentBase64: "aGVsbG8="}}
	id, err := a.insertMessage(ctx, msg, attachments)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.writeStoredMessageToMaildir(ctx, id, msg, attachments); err != nil {
		t.Fatal(err)
	}
	raw := trackedRawPath(t, a, id)
	var storagePath string
	if err := a.db.QueryRow(`SELECT storage_path FROM attachments WHERE message_id=?`, id).Scan(&storagePath); err != nil {
		t.Fatalf("load attachment: %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("attachment file missing before delete: %v", err)
	}

	full := MailMessage{ID: id, MailboxID: mailboxID, FolderID: trashID, Folder: "Trash"}
	if err := a.applyMessageDelete(ctx, &full); err != nil {
		t.Fatalf("delete in trash: %v", err)
	}
	var rows int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id=?`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("messages row still present after delete")
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE message_id=?`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("attachments rows still present after delete")
	}
	if _, err := os.Stat(raw); !os.IsNotExist(err) {
		t.Fatalf("raw file still on disk after delete: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(storagePath)); !os.IsNotExist(err) {
		t.Fatalf("attachment directory still on disk after delete: %v", err)
	}
}

// 删除非 Trash 里的邮件应当只是移进 Trash：文件还在、库行还在、计数搬到 Trash。
func TestDeleteOutsideTrashMovesToTrash(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	id, msg, _ := insertInboxMessage(t, a, ctx, mailboxID, "<inv-soft@example.test>", false)

	full := MailMessage{ID: id, MailboxID: mailboxID, FolderID: msg.FolderID, Folder: "Inbox"}
	if err := a.applyMessageDelete(ctx, &full); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	var folderName string
	if err := a.db.QueryRow(`SELECT f.name FROM messages m JOIN folders f ON f.id=m.folder_id WHERE m.id=?`, id).Scan(&folderName); err != nil {
		t.Fatal(err)
	}
	if folderName != "Trash" {
		t.Fatalf("folder=%q, want Trash", folderName)
	}
	if _, err := os.Stat(trackedRawPath(t, a, id)); err != nil {
		t.Fatalf("file must survive soft delete: %v", err)
	}
	if unread, total := folderCounts(t, a, ctx, mailboxID, "Trash"); unread < 1 || total < 1 {
		t.Fatalf("trash counts: unread=%d total=%d, want at least 1/1", unread, total)
	}
}

func msgPtr(m storedMessage, id string) *MailMessage {
	return &MailMessage{
		ID: id, MailboxID: m.MailboxID, FolderID: m.FolderID, Folder: "Inbox",
		Subject: m.Subject, From: m.From, IsRead: m.IsRead,
	}
}

var _ = fmt.Sprintf
