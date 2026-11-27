package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// 附件下载必须支持 Range：大附件断点续传、音视频拖进度都靠它。
// 以前是 writeAttachmentHeaders + io.Copy，只有 200 全量下发。
func TestAttachmentDownloadSupportsRange(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	id, _, _ := insertInboxMessage(t, a, ctx, mailboxID, "<range@example.test>", true)

	dir := filepath.Join(a.config().DataDir, "attachments", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "hello.txt"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	attID := newID("att")
	if _, err := a.db.ExecContext(ctx, `INSERT INTO attachments (id,message_id,filename,content_type,size_bytes,storage_path,created_at) VALUES (?,?,?,?,?,?,?)`,
		attID, id, name, "text/plain", 11, path, now); err != nil {
		t.Fatal(err)
	}

	var ownerID string
	if err := a.db.QueryRowContext(ctx, `SELECT user_id FROM mailboxes WHERE id=?`, mailboxID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	user := &User{ID: ownerID}
	req := httptest.NewRequest(http.MethodGet, "/api/mail/attachments/"+attID, nil)
	req.Header.Set("Range", "bytes=0-4")
	req = withChiParam(req, "id", attID)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	rec := httptest.NewRecorder()
	a.handleAttachment(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status=%d, want 206 (Range 请求被当成全量下发)", rec.Code)
	}
	if got := rec.Body.String(); got != "hello" {
		t.Fatalf("body=%q, want %q", got, "hello")
	}
	if cr := rec.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 0-4/11") {
		t.Fatalf("Content-Range=%q", cr)
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("Accept-Ranges=%q", ar)
	}
}

// 文件夹计数与统计必须排除外部账号导入件；两套聚合（侧栏 / 统计面板）
// 要给出同一组数字，否则侧栏角标和统计面板对不上。
func TestFolderCountsExcludeExternalImports(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	localID, _, inboxID := insertInboxMessage(t, a, ctx, mailboxID, "<local@example.test>", false)
	_, _, _ = insertInboxMessage(t, a, ctx, mailboxID, "<external@example.test>", false)

	// 造一条外部账号映射，把第二封标成「外部导入」。
	var userID string
	if err := a.db.QueryRowContext(ctx, `SELECT user_id FROM mailboxes WHERE id=?`, mailboxID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_accounts (id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,created_at,updated_at)
		VALUES ('acct1',?,?,'ext','imap.example.test',993,'tls','u','cipher',?,?)`, userID, mailboxID, now, now); err != nil {
		t.Fatal(err)
	}
	var externalLocalID string
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE mailbox_id=? AND message_id LIKE '%<external@example.test>'`, mailboxID).Scan(&externalLocalID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_messages (account_id,remote_folder,uid_validity,uid,message_id,local_message_id,is_read,created_at,updated_at)
		VALUES ('acct1','INBOX',1,42,'<external@example.test>',?,0,?,?)`, externalLocalID, now, now); err != nil {
		t.Fatal(err)
	}

	folders, err := a.loadMailFolderCounts(ctx, mailboxID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var inboxTotal int
	for _, f := range folders {
		if f.ID == inboxID {
			inboxTotal = f.TotalCount
		}
	}
	var extRows, allInboxRows int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_imap_messages WHERE local_message_id=?`, externalLocalID).Scan(&extRows); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE folder_id=? AND mailbox_id=?`, inboxID, mailboxID).Scan(&allInboxRows); err != nil {
		t.Fatal(err)
	}
	if extRows != 1 {
		t.Fatalf("fixture: external mapping rows=%d", extRows)
	}
	if inboxTotal != allInboxRows-1 {
		t.Fatalf("Inbox total=%d, want %d（Inbox 共 %d 封，其中 1 封是外部导入）", inboxTotal, allInboxRows-1, allInboxRows)
	}

	totals, err := a.loadMailboxTotals(ctx, mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	breakdown, err := a.loadFolderBreakdown(ctx, mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	var sumTotal, sumUnread int64
	for _, item := range breakdown {
		sumTotal += item.Count
		sumUnread += item.Unread
	}
	if sumTotal != totals.TotalMessages || sumUnread != totals.UnreadMessages {
		t.Fatalf("统计面板按文件夹合计=%d/%d，总数=%d/%d：两套聚合不一致", sumTotal, sumUnread, totals.TotalMessages, totals.UnreadMessages)
	}
	var allRows int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE mailbox_id=?`, mailboxID).Scan(&allRows); err != nil {
		t.Fatal(err)
	}
	if totals.TotalMessages != int64(allRows-1) {
		t.Fatalf("总邮件=%d, want %d（共 %d 封，其中 1 封外部导入）", totals.TotalMessages, allRows-1, allRows)
	}
	_ = localID
}

// 小工具：给 handler 塞 chi URL 参数。
func withChiParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
