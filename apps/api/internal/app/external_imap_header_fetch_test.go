package app

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// QQ/163 的 ENVELOPE 会让 go-imap 解析失败并断开连接，改走头部字段。
func TestExternalIMAPNeedsHeaderFetch(t *testing.T) {
	for _, host := range []string{"imap.qq.com", "imap.163.com", "imap.126.com", "yeah.net"} {
		if !externalIMAPNeedsHeaderFetch(host) {
			t.Fatalf("host %s should use header fetch", host)
		}
	}
	for _, host := range []string{"imap.gmail.com", "outlook.office365.com", "mail.example.com"} {
		if externalIMAPNeedsHeaderFetch(host) {
			t.Fatalf("host %s should not use header fetch", host)
		}
	}
}

func TestFetchBufferHeadersToExternalMessage(t *testing.T) {
	header := []byte("From: =?UTF-8?B?5byg5LiJ?= <zhangsan@qq.com>\r\n" +
		"To: bob@example.com\r\n" +
		"Cc: carol@example.com\r\n" +
		"Subject: =?UTF-8?B?5rWL6K+V6YKu5Lu2?=\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 -0700\r\n" +
		"Message-ID: <abc123@qq.com>\r\n\r\n")
	received := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	buf := &imapclient.FetchMessageBuffer{UID: 42, InternalDate: received, RFC822Size: 2048, Flags: []imap.Flag{imap.FlagSeen}}
	msg := fetchBufferHeadersToExternalMessage("INBOX", 7, buf, header)
	if msg.UID != 42 || msg.UIDValidity != 7 || msg.SizeBytes != 2048 {
		t.Fatalf("meta=%+v", msg)
	}
	if !msg.IsRead {
		t.Fatal("expected seen flag")
	}
	if msg.Subject != "测试邮件" {
		t.Fatalf("subject=%q", msg.Subject)
	}
	if msg.MessageID != "abc123@qq.com" {
		t.Fatalf("message id=%q", msg.MessageID)
	}
	if msg.From != "zhangsan@qq.com" || msg.FromName != "张三" {
		t.Fatalf("from=%q name=%q", msg.From, msg.FromName)
	}
	if len(msg.To) != 1 || msg.To[0] != "bob@example.com" || len(msg.CC) != 1 || msg.CC[0] != "carol@example.com" {
		t.Fatalf("to=%v cc=%v", msg.To, msg.CC)
	}
	if !msg.ReceivedAt.Equal(received) {
		t.Fatalf("received=%v", msg.ReceivedAt)
	}
}
