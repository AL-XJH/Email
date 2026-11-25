package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func audit118Client(t *testing.T, a *App) *testClient {
	t.Helper()
	s := httptest.NewServer(a.Router())
	t.Cleanup(s.Close)
	c := &testClient{t: t, server: s}
	if code := c.do("POST", "/api/auth/login", map[string]string{"email": "admin@lanqin.local", "password": "ChangeMe123!"}, nil); code != http.StatusOK {
		t.Fatalf("login=%d", code)
	}
	return c
}

func TestAudit118DataTemporaryRejectRemainsRetryable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	completed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		rd := bufio.NewReader(conn)
		io.WriteString(conn, "220 audit.example.test ESMTP\r\n")
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				io.WriteString(conn, "250 audit.example.test\r\n")
			case strings.HasPrefix(line, "DATA"):
				io.WriteString(conn, "354 Send message\r\n")
				for {
					line, err = rd.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
				}
				io.WriteString(conn, "452 4.3.1 Insufficient system storage, try again later\r\n")
				close(completed)
			case strings.HasPrefix(line, "QUIT"):
				io.WriteString(conn, "221 bye\r\n")
				return
			default:
				io.WriteString(conn, "250 OK\r\n")
			}
		}
	}()
	a := newTestApp(t)
	stopTestWorkers(a)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	a.updateConfig(func(c *Config) {
		c.SMTPHost = host
		c.SMTPPort = port
		c.SMTPRequireTLS = false
		c.MaildirRoot = t.TempDir()
	})
	client := audit118Client(t, a)
	_, mb := defaultAdminUserAndMailbox(t, a)
	var msg MailMessage
	code := client.do("POST", "/api/mail/send", map[string]any{"mailboxId": mb.ID, "to": []string{"recipient@example.test"}, "subject": "small normal email", "text": "hello", "clientToken": "audit-452"}, &msg)
	if code != http.StatusCreated {
		t.Fatalf("send=%d", code)
	}
	var id string
	if err := a.db.QueryRow(`SELECT id FROM send_queue WHERE sent_message_id=?`, msg.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	a.processSendQueueItem(context.Background(), id)
	select {
	case <-completed:
	default:
		t.Fatal("SMTP did not reach final 452")
	}
	var status, next, last string
	var attempts int
	if err := a.db.QueryRow(`SELECT status,next_attempt_at,last_error,attempt_count FROM send_queue WHERE id=?`, id).Scan(&status, &next, &last, &attempts); err != nil {
		t.Fatal(err)
	}
	delay := parseTime(next).Sub(a.now())
	t.Logf("after actual SMTP final 452: status=%s attempts=%d retry_delay=%s error=%q", status, attempts, delay.Round(time.Second), last)
	if delay > time.Hour {
		t.Fatalf("temporary DATA rejection lost automatic retry: delay=%s, last_error=%q", delay.Round(time.Second), last)
	}
}

func TestAudit118InboundReplyToSurvivesImport(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	root := t.TempDir()
	a.updateConfig(func(c *Config) { c.MaildirRoot = root })
	client := audit118Client(t, a)
	_, mb := defaultAdminUserAndMailbox(t, a)
	path := filepath.Join(root, "lanqin.local", "admin", "Maildir", "new", "audit-replyto.eml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: no-reply@example.test\r\nReply-To: support@example.test\r\nTo: admin@lanqin.local\r\nMessage-ID: <audit-reply-to@example.test>\r\nSubject: Please reply to support\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSmall normal message\r\n")
	parsed, _, parseErr := a.parseMaildirMessage(raw, mb.Address)
	if parseErr != nil || parsed.ReplyTo != "support@example.test" {
		t.Fatalf("parser ReplyTo=%q err=%v", parsed.ReplyTo, parseErr)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncMaildirOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var id, replyTo string
	if err := a.db.QueryRow(`SELECT id,reply_to FROM messages WHERE mailbox_id=? AND message_id=?`, mb.ID, "<audit-reply-to@example.test>").Scan(&id, &replyTo); err != nil {
		t.Fatal(err)
	}
	var detail MailMessage
	if code := client.do("GET", "/api/mail/messages/"+id, nil, &detail); code != http.StatusOK {
		t.Fatalf("detail=%d", code)
	}
	t.Logf("inbound raw Reply-To=support@example.test; stored reply_to=%q; GET detail replyTo=%q from=%q", replyTo, detail.ReplyTo, detail.From)
	if detail.ReplyTo != "support@example.test" {
		t.Fatalf("normal reply targets wrong address: API replyTo=%q falls back to From=%q; want support@example.test", detail.ReplyTo, detail.From)
	}
}

func TestAudit118SmallDraftAttachmentSendMoveDelete(t *testing.T) {
	a := newTestApp(t)
	stopTestWorkers(a)
	host, port, received := startCapturingSMTP(t, 4)
	a.updateConfig(func(c *Config) {
		c.SMTPHost = host
		c.SMTPPort = port
		c.SMTPRequireTLS = false
		c.MaildirRoot = t.TempDir()
	})
	client := audit118Client(t, a)
	_, mb := defaultAdminUserAndMailbox(t, a)
	attachments := func(name, body string) []map[string]string {
		return []map[string]string{{"filename": name, "contentType": "text/plain", "contentBase64": base64.StdEncoding.EncodeToString([]byte(body))}}
	}
	p := map[string]any{"mailboxId": mb.ID, "to": []string{"recipient@example.test"}, "bcc": []string{"hidden@example.test"}, "subject": "draft one", "text": "first", "clientToken": "audit-draft", "attachments": attachments("one.txt", "first file")}
	var draft MailMessage
	if code := client.do("POST", "/api/mail/drafts", p, &draft); code != http.StatusCreated {
		t.Fatalf("create=%d", code)
	}
	p["subject"] = "draft two"
	p["text"] = "second"
	p["attachments"] = attachments("two.txt", "second file")
	if code := client.do("POST", "/api/mail/drafts/"+draft.ID, p, &draft); code != http.StatusOK {
		t.Fatalf("update=%d", code)
	}
	raw, err := os.ReadFile(maildirRawPathForTest(t, a, draft.ID))
	if err != nil {
		t.Fatal(err)
	}
	parsed, atts, err := a.parseMaildirMessage(raw, mb.Address)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject != "draft two" || !strings.Contains(parsed.BodyText, "second") || len(atts) != 1 || atts[0].Filename != "two.txt" {
		t.Fatalf("draft raw mismatch: subject=%s body=%s attachments=%+v", parsed.Subject, parsed.BodyText, atts)
	}
	data, err := atts[0].contentBytes()
	if err != nil || string(data) != "second file" {
		t.Fatalf("attachment=%q %v", data, err)
	}
	var sent MailMessage
	p["clientToken"] = "audit-send-happy"
	if code := client.do("POST", "/api/mail/send", p, &sent); code != http.StatusCreated {
		t.Fatalf("send=%d", code)
	}
	var queueID string
	if err := a.db.QueryRow(`SELECT id FROM send_queue WHERE sent_message_id=?`, sent.ID).Scan(&queueID); err != nil {
		t.Fatal(err)
	}
	// Removing the Sent copy while queued must not cancel the actual SMTP job.
	for _, folder := range []string{"Audit-folder", "Archive"} {
		if code := client.do("POST", "/api/mail/messages/"+sent.ID+"/move", map[string]string{"folder": folder}, nil); code != http.StatusOK {
			t.Fatalf("move %s=%d", folder, code)
		}
	}
	for i := 0; i < 2; i++ {
		if code := client.do("DELETE", "/api/mail/messages/"+sent.ID, nil, nil); code != http.StatusOK {
			t.Fatalf("delete %d=%d", i, code)
		}
	}
	a.processSendQueueItem(t.Context(), queueID)
	select {
	case raw := <-received:
		msg, atts, err := a.parseMaildirMessage([]byte(raw), mb.Address)
		if err != nil || msg.Subject != "draft two" || len(atts) != 1 {
			t.Fatalf("SMTP payload subject=%s atts=%d err=%v", msg.Subject, len(atts), err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deleted Sent copy stopped queued SMTP delivery")
	}
	var status string
	if err := a.db.QueryRow(`SELECT status FROM send_queue WHERE id=?`, queueID).Scan(&status); err != nil || status != "delivered" {
		t.Fatalf("queue status=%s err=%v", status, err)
	}
	if code := client.do("DELETE", "/api/mail/drafts/"+draft.ID, nil, nil); code != http.StatusOK {
		t.Fatalf("delete draft=%d", code)
	}
	t.Log(fmt.Sprintf("draft create/update attachment + queued send + move/archive/trash/permanent delete + SMTP delivery: PASS"))
}
