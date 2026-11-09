package app

import (
	"net/http"
	"testing"
	"time"
)

func TestAdminDeliveryQueueListAndMutationsRedactAndGuardState(t *testing.T) {
	a := newTelegramTestApp(t)
	server, admin := loginTelegramTestAdmin(t, a)
	defer server.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var adminID string
	if err := a.db.QueryRow(`SELECT id FROM users WHERE email=?`, a.config().AdminEmail).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO telegram_notification_outbox(id,event_key,user_id,mailbox_id,message_id,rule_id,payload_json,attempt_count,next_attempt_at,last_error,created_at,updated_at,lease_owner,lease_token,lease_until)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "queue-admin-test", "event-admin-test", adminID, "mb", "msg", "rule", `{"text":"secret body https://private.example.test"}`, 10, now, "token 123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijk", now, now, "", "", nil); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Items []AdminDeliveryQueueItem `json:"items"`
	}
	if code := admin.do("GET", "/api/admin/delivery-queue?queueType=telegram&status=failed", nil, &listed); code != http.StatusOK || len(listed.Items) != 1 {
		t.Fatalf("list code=%d items=%+v", code, listed.Items)
	}
	if listed.Items[0].LastError == "" || listed.Items[0].LastError == "token 123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijk" || listed.Items[0].LastError == "secret body https://private.example.test" {
		t.Fatalf("sensitive queue error was not redacted: %q", listed.Items[0].LastError)
	}
	if code := admin.do("POST", "/api/admin/delivery-queue/telegram/queue-admin-test/retry", nil, nil); code != http.StatusOK {
		t.Fatalf("retry code=%d", code)
	}
	var attempts int
	if err := a.db.QueryRow(`SELECT attempt_count FROM telegram_notification_outbox WHERE id=?`, "queue-admin-test").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("retry attempts=%d err=%v", attempts, err)
	}
	if code := admin.do("POST", "/api/admin/delivery-queue/telegram/queue-admin-test/retry", nil, nil); code != http.StatusConflict {
		t.Fatalf("retry pending code=%d", code)
	}
	if code := admin.do("DELETE", "/api/admin/delivery-queue/telegram/queue-admin-test", nil, nil); code != http.StatusOK {
		t.Fatalf("cancel code=%d", code)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM telegram_notification_outbox WHERE id=?`, "queue-admin-test").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("cancel did not remove item count=%d err=%v", attempts, err)
	}
}

func TestAdminDeliveryQueueRequiresAdmin(t *testing.T) {
	a := newTelegramTestApp(t)
	ts, admin := loginTelegramTestAdmin(t, a)
	defer ts.Close()
	_ = admin
	unauth := (&testClient{t: t, server: ts}).do("GET", "/api/admin/delivery-queue", nil, nil)
	if unauth != http.StatusUnauthorized {
		t.Fatalf("unauthenticated code=%d", unauth)
	}
}

// 管理端投递队列要把 send_queue / webhook / telegram 三张表按 updated_at 归并，
// 分页时不能只截断某一张表，也不能漏掉来源之间的先后顺序。
func TestAdminDeliveryQueueMergesSourcesInUpdatedOrder(t *testing.T) {
	a := newTelegramTestApp(t)
	ts, admin := loginTelegramTestAdmin(t, a)
	defer ts.Close()
	var adminID string
	if err := a.db.QueryRow(`SELECT id FROM users WHERE email=?`, a.config().AdminEmail).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	var mailboxID string
	if err := a.db.QueryRow(`SELECT id FROM mailboxes WHERE user_id=? LIMIT 1`, adminID).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stamp := func(offset int) string { return base.Add(time.Duration(offset) * time.Second).Format(time.RFC3339Nano) }
	insertSend := func(id, status, updatedAt string, deliveredAt any) {
		t.Helper()
		if _, err := a.db.Exec(`INSERT INTO send_queue(id,user_id,mailbox_id,sent_message_id,message_id,source,mail_from,header_from,recipients_json,mime_base64,status,attempt_count,max_attempts,next_attempt_at,last_error,created_at,updated_at,delivered_at,lease_owner,lease_token,lease_until)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, adminID, mailboxID, "", "", "webmail", "a@example.test", "a@example.test", `["a@example.test"]`, "", status, 0, 5, updatedAt, "", updatedAt, updatedAt, deliveredAt, "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	insertSend("queue-send-old", "queued", stamp(1), nil)
	insertSend("queue-send-new", "delivered", stamp(3), stamp(3))
	if _, err := a.db.Exec(`INSERT INTO telegram_notification_outbox(id,event_key,user_id,mailbox_id,message_id,rule_id,payload_json,attempt_count,next_attempt_at,last_error,created_at,updated_at,lease_owner,lease_token,lease_until)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "queue-tg-mid", "event-tg-mid", adminID, mailboxID, "msg", "rule", `{}`, 0, stamp(2), "", stamp(2), stamp(2), "", "", nil); err != nil {
		t.Fatal(err)
	}

	var page1 struct {
		Items      []AdminDeliveryQueueItem `json:"items"`
		TotalCount int                      `json:"totalCount"`
	}
	if code := admin.do("GET", "/api/admin/delivery-queue?limit=2&page=1", nil, &page1); code != http.StatusOK {
		t.Fatalf("page1 code=%d", code)
	}
	if page1.TotalCount != 3 {
		t.Fatalf("totalCount = %d, want 3", page1.TotalCount)
	}
	if len(page1.Items) != 2 || page1.Items[0].ID != "queue-send-new" || page1.Items[1].ID != "queue-tg-mid" {
		t.Fatalf("page1 order = %+v", page1.Items)
	}

	var page2 struct {
		Items []AdminDeliveryQueueItem `json:"items"`
	}
	if code := admin.do("GET", "/api/admin/delivery-queue?limit=2&page=2", nil, &page2); code != http.StatusOK {
		t.Fatalf("page2 code=%d", code)
	}
	if len(page2.Items) != 1 || page2.Items[0].ID != "queue-send-old" {
		t.Fatalf("page2 = %+v", page2.Items)
	}

	var delivered struct {
		Items []AdminDeliveryQueueItem `json:"items"`
	}
	if code := admin.do("GET", "/api/admin/delivery-queue?status=delivered", nil, &delivered); code != http.StatusOK {
		t.Fatalf("delivered code=%d", code)
	}
	if len(delivered.Items) != 1 || delivered.Items[0].ID != "queue-send-new" || delivered.Items[0].Status != "delivered" {
		t.Fatalf("delivered filter = %+v", delivered.Items)
	}
}
