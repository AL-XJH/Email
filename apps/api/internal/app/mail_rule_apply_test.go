package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 「应用到已有邮件」改成按批取详情之后，仍要匹配并作用于所有命中邮件。
func TestApplyRuleToExistingMatchesWholeMailboxInBatches(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = t.TempDir() })
	ts := httptest.NewServer(a.Router())
	defer ts.Close()
	client := &testClient{t: t, server: ts}
	var login map[string]any
	if code := client.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, &login); code != http.StatusOK {
		t.Fatalf("login code=%d", code)
	}
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)
	mb, err := a.mailboxByID(ctx, mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	inboxID, err := a.ensureFolder(ctx, mailboxID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 4)
	for _, from := range []string{"bulk@example.test", "bulk@example.test", "bulk@example.test", "other@example.test"} {
		now := a.now().UTC()
		msg := storedMessage{
			MailboxID:  mailboxID,
			FolderID:   inboxID,
			MessageUID: newID("uid"),
			MessageID:  "<rule-apply-" + newID("x") + "@example.test>",
			Subject:    "hello",
			From:       from,
			To:         []string{mb.Address},
			SentAt:     now,
			ReceivedAt: now,
			Snippet:    "body",
			BodyText:   "body",
		}
		id, err := a.insertMessage(ctx, msg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.writeStoredMessageToMaildir(ctx, id, msg, nil); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	var rule MailRule
	if code := client.do("POST", "/api/me/rules", map[string]any{
		"mailboxId":       mailboxID,
		"name":            "star bulk mail",
		"matchMode":       "all",
		"conditions":      []map[string]string{{"field": "from", "operator": "contains", "value": "bulk@example.test"}},
		"actions":         []map[string]string{{"type": "star"}},
		"applyToExisting": true,
	}, &rule); code != http.StatusCreated {
		t.Fatalf("create rule code=%d rule=%+v", code, rule)
	}
	if rule.AppliedExistingCount != 3 {
		t.Fatalf("appliedExistingCount = %d, want 3", rule.AppliedExistingCount)
	}
	for index, id := range ids {
		var starred int
		if err := a.db.QueryRow(`SELECT is_starred FROM messages WHERE id=?`, id).Scan(&starred); err != nil {
			t.Fatal(err)
		}
		want := 0
		if index < 3 {
			want = 1
		}
		if starred != want {
			t.Fatalf("message %d is_starred=%d, want %d", index, starred, want)
		}
	}
}
