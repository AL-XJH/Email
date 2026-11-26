package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	smtpserver "github.com/emersion/go-smtp"
)

func TestOutboundSMTPSizeContract(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	user, mb := defaultAdminUserAndMailbox(t, a)
	for _, tc := range []struct {
		configured int
		want       int64
	}{{0, 35 << 20}, {1, 1 << 20}, {35, 35 << 20}, {100, 37748736}} {
		a.updateConfig(func(c *Config) { c.SubmissionMaxMessageMB = tc.configured })
		if got := outboundMessageSizeLimit(*a.config()); got != tc.want {
			t.Fatalf("limit(%d)=%d want%d", tc.configured, got, tc.want)
		}
		if err := validateOutboundMessageSize(*a.config(), tc.want); err != nil {
			t.Fatal(err)
		}
		if err := validateOutboundMessageSize(*a.config(), tc.want+1); !errors.Is(err, errMessageTooLarge) {
			t.Fatalf("over-boundary=%v", err)
		}
		s := a.newSubmissionServer(":0", nil)
		if s.MaxMessageBytes != tc.want {
			t.Fatalf("SMTP advertised limit=%d want%d", s.MaxMessageBytes, tc.want)
		}
		_ = s.Close()
	}
	a.updateConfig(func(c *Config) { c.SubmissionMaxMessageMB = 1 })
	var before int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM smtp_send_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err := a.submitSMTPMessage(t.Context(), user, mb, mb.Address, []string{"recipient@example.test"}, strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	var smtpErr *smtpserver.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 552 {
		t.Fatalf("SMTP oversize=%v", err)
	}
	var after int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM smtp_send_events`).Scan(&after); err != nil || after != before {
		t.Fatalf("SMTP rejected message charged: %d->%d err=%v", before, after, err)
	}
}

func TestOutboundRejectedRegularUserDoesNotConsumeAllowance(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	a.updateConfig(func(c *Config) { c.SubmissionMaxMessageMB = 1; c.SMTPHost = "127.0.0.1"; c.SMTPPort = "1" })
	client := audit118Client(t, a)
	mb := createTestMailbox(t, client, mustDefaultDomainID(t, a), "size-limit", "Size Limit", "Password123!", nil)
	user, err := a.userByID(t.Context(), mb.UserID)
	if err != nil {
		t.Fatal(err)
	}
	user.Limits.SMTPDailyLimit = 10
	user.Limits.SMTPMinuteLimit = 10
	req := mailComposeInput{To: []string{"recipient@example.test"}, Text: strings.Repeat("x", 1<<20)}
	if _, err := a.sendMailNow(t.Context(), user, &mb, req); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("large=%v", err)
	}
	req.Text = "small"
	if _, err := a.sendMailNow(t.Context(), user, &mb, req); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM smtp_send_events WHERE user_id=?`, user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("allowance count=%d want1 err=%v", count, err)
	}
}

func TestOutboundSizeRejectsBeforeStateAndPreservesDraft(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	a.updateConfig(func(c *Config) { c.SMTPHost = "127.0.0.1"; c.SMTPPort = "1"; c.MaildirRoot = t.TempDir() })
	client := audit118Client(t, a)
	_, mb := defaultAdminUserAndMailbox(t, a)
	var draft MailMessage
	if code := client.do("POST", "/api/mail/drafts", map[string]any{"mailboxId": mb.ID, "subject": "keep draft", "text": "unchanged"}, &draft); code != 201 {
		t.Fatal(code)
	}
	count := func(table string) int {
		var n int
		if err := a.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := []int{count("messages"), count("smtp_send_events"), count("send_queue"), count("scheduled_sends")}
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("z"), 14<<20))
	input := map[string]any{"mailboxId": mb.ID, "to": []string{"recipient@example.test"}, "subject": "too big", "text": "body", "attachments": []map[string]string{{"filename": "one.bin", "contentBase64": encoded}, {"filename": "two.bin", "contentBase64": encoded}}}
	var failure map[string]any
	if code := client.do("POST", "/api/mail/send", input, &failure); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized send=%d want413 response=%v", code, failure)
	}
	input["sendAt"] = a.now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	input["draftId"] = draft.ID
	if code := client.do("POST", "/api/mail/schedule-send", input, &failure); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized schedule=%d want413 response=%v", code, failure)
	}
	for i, table := range []string{"messages", "smtp_send_events", "send_queue", "scheduled_sends"} {
		if got := count(table); got != before[i] {
			t.Errorf("%s changed %d ->%d on size rejection", table, before[i], got)
		}
	}
	var kept MailMessage
	if code := client.do("GET", "/api/mail/messages/"+draft.ID+"?markRead=0", nil, &kept); code != 200 || kept.BodyText != "unchanged" {
		t.Fatalf("draft not preserved code=%d body=%q", code, kept.BodyText)
	}
}

func TestOutboundTwoTwelveMiBAttachmentsAndSchedule(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	host, port, received := startCapturingSMTP(t, 2)
	a.updateConfig(func(c *Config) {
		c.SMTPHost = host
		c.SMTPPort = port
		c.SMTPRequireTLS = false
		c.MaildirRoot = t.TempDir()
	})
	client := audit118Client(t, a)
	_, mb := defaultAdminUserAndMailbox(t, a)
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("z"), 12<<20))
	input := map[string]any{"mailboxId": mb.ID, "to": []string{"recipient@example.test"}, "subject": "fits", "text": "body", "attachments": []map[string]string{{"filename": "one.bin", "contentBase64": encoded}, {"filename": "two.bin", "contentBase64": encoded}}}
	var sent MailMessage
	if code := client.do("POST", "/api/mail/send", input, &sent); code != 201 {
		t.Fatal(code)
	}
	if err := a.processDueSendQueue(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case wire := <-received:
		if len(wire) > 37748736 {
			t.Fatal("oversized wire")
		}
		t.Logf("12+12MiB accepted and relayed: MIME bytes=%d", len(wire))
	case <-time.After(3 * time.Second):
		t.Fatal("no SMTP")
	}
	due := a.now().Add(time.Hour)
	input["sendAt"] = due.UTC().Format(time.RFC3339Nano)
	var scheduled ScheduledSend
	if code := client.do("POST", "/api/mail/schedule-send", input, &scheduled); code != 201 {
		t.Fatal(code)
	}
	a.setNow(func() time.Time { return due.Add(time.Second) })
	if err := a.processDueScheduledSends(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := a.db.QueryRow(`SELECT status FROM scheduled_sends WHERE id=?`, scheduled.ID).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("scheduled status=%s err=%v", status, err)
	}
}

func TestPrivateBCCDraftCreateUpdateSentAndBackfill(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	host, port, received := startCapturingSMTP(t, 2)
	a.updateConfig(func(c *Config) {
		c.SMTPHost = host
		c.SMTPPort = port
		c.SMTPRequireTLS = false
		c.MaildirRoot = t.TempDir()
	})
	client := audit118Client(t, a)
	_, mb := defaultAdminUserAndMailbox(t, a)
	input := map[string]any{"mailboxId": mb.ID, "to": []string{"visible@example.test"}, "bcc": []string{"hidden@example.test"}, "subject": "private", "text": "body"}
	check := func(id, want string) {
		t.Helper()
		raw, err := os.ReadFile(maildirRawPathForTest(t, a, id))
		if err != nil {
			t.Fatal(err)
		}
		m, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Header.Get("Bcc"); got != want {
			t.Errorf("private Bcc=%q want%q", got, want)
		}
	}
	var draft MailMessage
	if code := client.do("POST", "/api/mail/drafts", input, &draft); code != 201 {
		t.Fatal(code)
	}
	check(draft.ID, "hidden@example.test")
	input["bcc"] = []string{"updated@example.test"}
	if code := client.do("POST", "/api/mail/drafts/"+draft.ID, input, &draft); code != 200 {
		t.Fatal(code)
	}
	check(draft.ID, "updated@example.test")
	var sent MailMessage
	if code := client.do("POST", "/api/mail/send", input, &sent); code != 201 {
		t.Fatal(code)
	}
	check(sent.ID, "updated@example.test")
	path := maildirRawPathForTest(t, a, sent.ID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE messages SET raw_path='' WHERE id=?`, sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.backfillSQLiteMessagesToMaildir(context.Background()); err != nil {
		t.Fatal(err)
	}
	check(sent.ID, "updated@example.test")
	if err := a.processDueSendQueue(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case wire := <-received:
		m, err := mail.ReadMessage(strings.NewReader(wire))
		if err != nil {
			t.Fatal(err)
		}
		if m.Header.Get("Bcc") != "" {
			t.Fatal("wire leaks Bcc")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no SMTP")
	}
	// Reusing the stored writer for a recipient copy must never emit Bcc.
	inbox, err := a.ensureFolder(t.Context(), mb.ID, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	msg := storedMessage{MailboxID: mb.ID, FolderID: inbox, MessageUID: newID("uid"), MessageID: "<recipient-bcc@example.test>", From: mb.Address, To: []string{mb.Address}, BCC: []string{"hidden@example.test"}, Subject: "recipient copy", BodyText: "body", SentAt: a.now(), ReceivedAt: a.now()}
	id, err := a.insertMessage(t.Context(), msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.writeStoredMessageToMaildir(t.Context(), id, msg, nil); err != nil {
		t.Fatal(err)
	}
	check(id, "")
}

func TestOutboundConfiguredLimitBoundary(t *testing.T) {
	// Body base64 is deterministic in length; vary Subject by single ASCII bytes
	// to hit the configured 1MiB boundary without relying on multipart randomness.
	a := newTestApp(t)
	stopTestWorkers(a)
	a.updateConfig(func(c *Config) { c.SubmissionMaxMessageMB = 1; c.SMTPHost = "127.0.0.1"; c.SMTPPort = "1" })
	user, mb := defaultAdminUserAndMailbox(t, a)
	req := mailComposeInput{To: []string{"recipient@example.test"}, Subject: "boundary", Text: strings.Repeat("x", 700000), HTML: "<p>x</p>"}
	raw, err := BuildMIME(MIMEMessage{From: mb.Address, FromName: mb.DisplayName, To: req.To, Subject: req.Subject, Text: req.Text, HTML: req.HTML, MessageID: "<" + "msg_" + strings.Repeat("x", 22) + "@lanqin.local>", Date: a.now()})
	if err != nil {
		t.Fatal(err)
	}
	gap := (1 << 20) - len(raw)
	if gap < 1 {
		t.Fatal(gap)
	}
	req.Subject += strings.Repeat("s", gap)
	if _, err := a.sendMailNow(t.Context(), user, mb, req); err != nil {
		t.Fatalf("exact configured limit rejected: %v (gap=%s)", err, strconv.Itoa(gap))
	}
	req.Subject += "s"
	if _, err := a.sendMailNow(t.Context(), user, mb, req); err == nil {
		t.Fatal("one byte over configured limit accepted")
	}
}
