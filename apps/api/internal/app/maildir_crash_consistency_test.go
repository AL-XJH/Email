package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 崩溃窗口：writeRawMessageToMaildirFolder 的顺序是
//   tmp 写盘 → rename 到 cur/new → UPDATE messages.raw_path
// 进程若死在 rename 之后、UPDATE 之前，磁盘上会多出一份「内容正确、文件名里带着我们
// 自己 message id」的副本，而库里仍指向旧文件。下一轮 sync 若把它当新邮件导入，
// 用户就会看到同一封邮件两遍。这组用例把这个窗口固定下来。

func maildirCrashFixture(t *testing.T) (a *App, mailboxID, folderID, base string, mb maildirMailbox) {
	t.Helper()
	a = newTestApp(t)
	root := t.TempDir()
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = root })
	ctx := context.Background()
	mailboxID = adminMailboxIDForTest(t, a)
	folderID, err := a.ensureFolder(ctx, mailboxID, "Inbox")
	if err != nil {
		t.Fatalf("ensure inbox: %v", err)
	}
	mb, err = a.maildirMailboxByID(ctx, mailboxID)
	if err != nil {
		t.Fatalf("load maildir mailbox: %v", err)
	}
	base = filepath.Join(root, mb.Domain, mb.LocalPart, "Maildir")
	return a, mailboxID, folderID, base, mb
}

func insertCrashFixtureMessage(t *testing.T, a *App, mailboxID, folderID, messageID string) (string, storedMessage) {
	t.Helper()
	now := a.now().UTC()
	msg := storedMessage{
		MailboxID:  mailboxID,
		FolderID:   folderID,
		MessageUID: newID("uid"),
		MessageID:  messageID,
		Subject:    "crash window",
		From:       "sender@example.test",
		To:         []string{"admin@lanqin.local"},
		SentAt:     now,
		ReceivedAt: now,
		BodyText:   "hello",
	}
	id, err := a.insertMessage(context.Background(), msg, nil)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if err := a.writeStoredMessageToMaildir(context.Background(), id, msg, nil); err != nil {
		t.Fatalf("write maildir: %v", err)
	}
	return id, msg
}

func messageCountByID(t *testing.T, a *App, mailboxID, messageID string) int {
	t.Helper()
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE mailbox_id=? AND message_id=?`, mailboxID, messageID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return count
}

func trackedRawPath(t *testing.T, a *App, id string) string {
	t.Helper()
	var raw string
	if err := a.db.QueryRow(`SELECT COALESCE(raw_path,'') FROM messages WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatalf("load raw_path: %v", err)
	}
	return raw
}

// 同一封邮件在磁盘上留下两份（旧文件仍被库引用），不能导入成第二封。
func TestMaildirSyncDoesNotDuplicateOwnOrphanAfterCrash(t *testing.T) {
	a, mailboxID, folderID, base, _ := maildirCrashFixture(t)
	ctx := context.Background()
	id, _ := insertCrashFixtureMessage(t, a, mailboxID, folderID, "<crash-window@example.test>")

	tracked := trackedRawPath(t, a, id)
	if tracked == "" {
		t.Fatal("fixture wrote no maildir file")
	}
	raw, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatalf("read tracked file: %v", err)
	}
	// 模拟 rename 之后、UPDATE 之前被杀：新文件名带我们自己的内部 id，
	// 内容与旧文件一致，但库里还指向旧文件。
	orphan := filepath.Join(filepath.Dir(tracked), fmt.Sprintf("%d.%s.msg_crash@example.test:2,", time.Now().UnixNano(), id))
	if err := os.WriteFile(orphan, raw, 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	if _, err := a.syncMaildirOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if got := messageCountByID(t, a, mailboxID, "<crash-window@example.test>"); got != 1 {
		t.Fatalf("导入成 %d 封，期望 1 封：崩溃窗口留下的同名副本被当成了新邮件", got)
	}
	if got := trackedRawPath(t, a, id); got != tracked {
		t.Fatalf("raw_path 被改成了 %q，期望仍指向 %q", got, tracked)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("内容与已跟踪文件完全一致的副本应当被清掉，实际仍在（err=%v）", err)
	}
	_ = base
}

// 反过来：库里指向的旧文件已经不存在了，磁盘上这份「自己的副本」必须被认领，
// 否则邮件会从 webmail 里消失（row 还在但文件没了 = missing_raw）。
func TestMaildirSyncAdoptsOwnOrphanWhenTrackedFileDisappeared(t *testing.T) {
	a, mailboxID, folderID, _, _ := maildirCrashFixture(t)
	ctx := context.Background()
	id, _ := insertCrashFixtureMessage(t, a, mailboxID, folderID, "<crash-adopt@example.test>")

	tracked := trackedRawPath(t, a, id)
	raw, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatalf("read tracked file: %v", err)
	}
	orphan := filepath.Join(filepath.Dir(tracked), fmt.Sprintf("%d.%s.msg_crash@example.test:2,", time.Now().UnixNano(), id))
	if err := os.WriteFile(orphan, raw, 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	if err := os.Remove(tracked); err != nil {
		t.Fatalf("remove tracked file: %v", err)
	}

	if _, err := a.syncMaildirOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := messageCountByID(t, a, mailboxID, "<crash-adopt@example.test>"); got != 1 {
		t.Fatalf("losing one file must not create a second message row, got %d", got)
	}
	if got := trackedRawPath(t, a, id); got != orphan {
		t.Fatalf("raw_path=%q, want the surviving copy %q", got, orphan)
	}
}

// tmp/ 下没 rename 完的半成品不能被导入（它们没有 :2, 后缀，也不是最终文件）。
func TestMaildirSyncIgnoresHalfWrittenTmpFiles(t *testing.T) {
	a, mailboxID, folderID, base, _ := maildirCrashFixture(t)
	ctx := context.Background()
	id, _ := insertCrashFixtureMessage(t, a, mailboxID, folderID, "<crash-tmp@example.test>")
	tracked := trackedRawPath(t, a, id)
	if !strings.Contains(tracked, string(filepath.Separator)+"cur"+string(filepath.Separator)) &&
		!strings.Contains(tracked, string(filepath.Separator)+"new"+string(filepath.Separator)) {
		t.Fatalf("unexpected tracked path %q", tracked)
	}
	tmpDir := filepath.Join(base, "cur", "..", "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	half := filepath.Join(tmpDir, fmt.Sprintf("%d.%s.half", time.Now().UnixNano(), newID("mail")))
	body := "From: sender@example.test\r\nTo: admin@lanqin.local\r\nSubject: half\r\n\r\nhalf written"
	if err := os.WriteFile(half, []byte(body), 0o600); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	if _, err := a.syncMaildirOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE subject='half'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("tmp/ 里的半成品被导入成 %d 封邮件", count)
	}
}
