package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 列表页用的轻量统计必须与完整统计给出同一组计数：
// /me/stats/summary 只省掉按文件夹明细和附件聚合，不能把数字算错。
func TestMailStatsSummaryMatchesFullStats(t *testing.T) {
	a, ctx, mailboxID := invariantFixture(t)
	insertInboxMessage(t, a, ctx, mailboxID, "<sum-1@example.test>", false)
	insertInboxMessage(t, a, ctx, mailboxID, "<sum-2@example.test>", true)
	id, _, _ := insertInboxMessage(t, a, ctx, mailboxID, "<sum-3@example.test>", false)
	if _, err := a.db.ExecContext(ctx, `UPDATE messages SET is_starred=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	var ownerID string
	if err := a.db.QueryRowContext(ctx, `SELECT user_id FROM mailboxes WHERE id=?`, mailboxID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	user := &User{ID: ownerID}

	call := func(target string, out any) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
		rec := httptest.NewRecorder()
		if target == "/api/me/stats/summary?mailboxId="+mailboxID {
			a.handleMailStatsSummary(rec, req)
		} else {
			a.handleMailStats(rec, req)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", target, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}

	var summary MailStatsSummary
	call("/api/me/stats/summary?mailboxId="+mailboxID, &summary)
	var full MailStats
	call("/api/me/stats?mailboxId="+mailboxID, &full)

	if summary.TotalMessages != full.TotalMessages || summary.UnreadMessages != full.UnreadMessages {
		t.Fatalf("summary=%d/%d full=%d/%d", summary.TotalMessages, summary.UnreadMessages, full.TotalMessages, full.UnreadMessages)
	}
	if summary.StarredMessages != full.StarredMessages || summary.StorageBytes != full.StorageBytes {
		t.Fatalf("summary starred/bytes=%d/%d full=%d/%d", summary.StarredMessages, summary.StorageBytes, full.StarredMessages, full.StorageBytes)
	}
	if summary.StarredMessages < 1 {
		t.Fatalf("starred=%d, want >=1", summary.StarredMessages)
	}
	var sumTotal, sumUnread int64
	for _, item := range full.ByFolder {
		sumTotal += item.Count
		sumUnread += item.Unread
	}
	if sumTotal != summary.TotalMessages || sumUnread != summary.UnreadMessages {
		t.Fatalf("byFolder 合计=%d/%d，总数=%d/%d", sumTotal, sumUnread, summary.TotalMessages, summary.UnreadMessages)
	}
}
