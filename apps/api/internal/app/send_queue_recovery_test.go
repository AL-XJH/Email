package app

import (
	"context"
	"encoding/base64"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func insertStuckSendQueueRow(t *testing.T, a *App, id string, attempts, maxAttempts int, payload string) {
	t.Helper()
	ctx := context.Background()
	var mailboxID, userID string
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM mailboxes LIMIT 1`).Scan(&mailboxID); err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Fatalf("load user: %v", err)
	}
	now := a.now().UTC()
	old := now.Add(-2 * sendQueueStaleAfter).Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `INSERT INTO send_queue(id,user_id,mailbox_id,sent_message_id,message_id,source,mail_from,header_from,recipients_json,mime_base64,status,attempt_count,max_attempts,next_attempt_at,last_error,lease_owner,lease_token,lease_until,created_at,updated_at)
		VALUES(?,?,?,'','','webmail','admin@lanqin.local','admin@lanqin.local','["person@example.com"]',?,?,?,?,?,?,?,?,?,?,?)`,
		id, userID, mailboxID, payload, sendQueueStatusSending, attempts, maxAttempts, old, "", "test-worker", "lease-"+id, old, old, old)
	if err != nil {
		t.Fatalf("insert send queue row: %v", err)
	}
}

// 最后一次尝试时进程崩溃的行以前会永远停在 sending：恢复查询里带着
// attempt_count<max_attempts 过滤，谁也看不到它。现在必须收尾成 failed。
func TestRecoverStaleSendQueueFailsExhaustedRows(t *testing.T) {
	a := newTestApp(t)
	insertStuckSendQueueRow(t, a, "snd_stuck_exhausted", 3, 3, base64.StdEncoding.EncodeToString([]byte("Subject: x\r\n\r\nbody")))

	if err := a.recoverStaleSendQueueItems(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	var status string
	var lastError string
	if err := a.db.QueryRow(`SELECT status,last_error FROM send_queue WHERE id=?`, "snd_stuck_exhausted").Scan(&status, &lastError); err != nil {
		t.Fatalf("load row: %v", err)
	}
	if status != sendQueueStatusFailed {
		t.Fatalf("status=%q, want %q (last_error=%q)", status, sendQueueStatusFailed, lastError)
	}
}

// 恢复只处理租约，不读正文；坏载荷在 claim 那一步收尾成失败，
// 而且不能因为一行坏数据让整轮恢复失败。
func TestRecoverStaleSendQueueSkipsCorruptPayload(t *testing.T) {
	a := newTestApp(t)
	insertStuckSendQueueRow(t, a, "snd_corrupt", 1, 5, "!!!not-base64!!!")
	insertStuckSendQueueRow(t, a, "snd_healthy", 1, 5, base64.StdEncoding.EncodeToString([]byte("Subject: ok\r\n\r\nbody")))

	if err := a.recoverStaleSendQueueItems(context.Background()); err != nil {
		t.Fatalf("recover should not fail on a corrupt row: %v", err)
	}
	var corruptStatus, healthyStatus string
	if err := a.db.QueryRow(`SELECT status FROM send_queue WHERE id=?`, "snd_corrupt").Scan(&corruptStatus); err != nil {
		t.Fatalf("load corrupt row: %v", err)
	}
	if err := a.db.QueryRow(`SELECT status FROM send_queue WHERE id=?`, "snd_healthy").Scan(&healthyStatus); err != nil {
		t.Fatalf("load healthy row: %v", err)
	}
	// 恢复阶段不看载荷，两行都回到队列。
	if corruptStatus != sendQueueStatusQueued {
		t.Fatalf("corrupt row status=%q, want queued", corruptStatus)
	}
	if healthyStatus != sendQueueStatusQueued {
		t.Fatalf("healthy row status=%q, want queued", healthyStatus)
	}

	// claim 时才发现载荷坏了 —— 就地收尾成失败，不再无限重试。
	a.processSendQueueItem(context.Background(), "snd_corrupt")
	var status, lastError string
	if err := a.db.QueryRow(`SELECT status,last_error FROM send_queue WHERE id=?`, "snd_corrupt").Scan(&status, &lastError); err != nil {
		t.Fatalf("load corrupt row: %v", err)
	}
	if status != sendQueueStatusFailed {
		t.Fatalf("corrupt row status=%q, want failed (last_error=%q)", status, lastError)
	}
	if !strings.Contains(lastError, "corrupt") {
		t.Fatalf("last_error=%q, want corrupt payload reason", lastError)
	}
}

func TestPermanentSMTPFailureClassification(t *testing.T) {
	cases := []struct {
		code      int
		permanent bool
	}{
		{550, true},
		{553, true},
		{554, true},
		{450, false},
		{451, false},
		{452, false},
		{421, false},
	}
	for _, tc := range cases {
		err := &textproto.Error{Code: tc.code, Msg: "smtp failure"}
		if got := permanentSMTPFailure(err); got != tc.permanent {
			t.Fatalf("code %d: permanent=%v, want %v", tc.code, got, tc.permanent)
		}
	}
	if permanentSMTPFailure(context.DeadlineExceeded) {
		t.Fatal("non-SMTP errors must stay retryable")
	}
}

func insertQueuedSendQueueRow(t *testing.T, a *App, id string) {
	t.Helper()
	ctx := context.Background()
	var mailboxID, userID string
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM mailboxes LIMIT 1`).Scan(&mailboxID); err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Fatalf("load user: %v", err)
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `INSERT INTO send_queue(id,user_id,mailbox_id,sent_message_id,message_id,source,mail_from,header_from,recipients_json,mime_base64,status,attempt_count,max_attempts,next_attempt_at,last_error,created_at,updated_at)
		VALUES(?,?,?,'','','webmail','admin@lanqin.local','admin@lanqin.local','["person@example.com"]',?,?,0,5,?,'',?,?)`,
		id, userID, mailboxID, base64.StdEncoding.EncodeToString([]byte("Subject: x\r\n\r\nbody")), sendQueueStatusQueued, now, now, now)
	if err != nil {
		t.Fatalf("insert queued row: %v", err)
	}
}

// 5xx 重试也没用，标记失败时不应该再安排 6 小时后的重试。
func TestMarkSendQueueFailedStopsRetryingPermanentErrors(t *testing.T) {
	a := newTestApp(t)
	insertQueuedSendQueueRow(t, a, "snd_perm")
	item, err := a.claimSendQueueItem(context.Background(), "snd_perm")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	a.markSendQueueFailed(context.Background(), item, &textproto.Error{Code: 550, Msg: "no such user"})

	var status, nextAttempt string
	if err := a.db.QueryRow(`SELECT status,next_attempt_at FROM send_queue WHERE id=?`, "snd_perm").Scan(&status, &nextAttempt); err != nil {
		t.Fatalf("load row: %v", err)
	}
	if status != sendQueueStatusFailed {
		t.Fatalf("status=%q, want failed", status)
	}
	parsed, err := time.Parse(time.RFC3339Nano, nextAttempt)
	if err != nil {
		t.Fatalf("parse next_attempt_at %q: %v", nextAttempt, err)
	}
	if parsed.Sub(a.now().UTC()) < 24*time.Hour {
		t.Fatalf("next_attempt_at=%s, want a far-future parking time", nextAttempt)
	}
}
