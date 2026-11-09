package app

import (
	"strings"
	"testing"
)

func TestReadMessageHeaderSlicesBodyWithoutCopy(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"crlf", "From: a@example.com\r\nSubject: hi\r\n\r\nbody line\r\n", "body line\r\n"},
		{"lf", "From: a@example.com\nSubject: hi\n\nbody line\n", "body line\n"},
		{"empty body", "From: a@example.com\r\nSubject: hi\r\n\r\n", ""},
		{"multiline header", "From: a@example.com\r\nTo: b@example.com,\r\n c@example.com\r\n\r\nhello", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			header, body, err := readMessageHeader(raw)
			if err != nil {
				t.Fatalf("readMessageHeader: %v", err)
			}
			if got := string(body); got != tc.want {
				t.Fatalf("body = %q, want %q", got, tc.want)
			}
			if header.Get("From") != "a@example.com" {
				t.Fatalf("From = %q", header.Get("From"))
			}
			// 正文必须是 raw 的切片，不能再复制一份。
			if len(body) > 0 && &body[0] != &raw[len(raw)-len(body)] {
				t.Fatalf("body was copied instead of sliced")
			}
		})
	}
}

// 大于一个 bufio 缓冲区（4096 字节）的邮件曾经只切出末尾一小段正文，
// 这里用 8KB 正文锁住这个回归。
func TestReadMessageHeaderSlicesLargeBody(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 512) // 8192 字节
	raw := []byte("From: a@example.com\r\nSubject: big\r\nTo: b@example.com\r\n\r\n" + body)
	if len(raw) <= 4096 {
		t.Fatalf("test payload must exceed one bufio buffer, got %d bytes", len(raw))
	}
	header, got, err := readMessageHeader(raw)
	if err != nil {
		t.Fatalf("readMessageHeader: %v", err)
	}
	if header.Get("Subject") != "big" {
		t.Fatalf("Subject = %q", header.Get("Subject"))
	}
	if string(got) != body {
		t.Fatalf("body length = %d, want %d (head=%q)", len(got), len(body), string(got[:min(32, len(got))]))
	}
	if &got[0] != &raw[len(raw)-len(body)] {
		t.Fatal("body was copied instead of sliced")
	}
}

func TestSerializeMessageKeepsBodyIntact(t *testing.T) {
	raw := []byte("From: a@example.com\r\nSubject: hi\r\n\r\n" + "x")
	header, body, err := readMessageHeader(raw)
	if err != nil {
		t.Fatalf("readMessageHeader: %v", err)
	}
	out := serializeMessage(header, body)
	if string(out[len(out)-1:]) != "x" {
		t.Fatalf("body lost in serialize: %q", string(out))
	}
	if header.Get("Subject") != "hi" {
		t.Fatalf("Subject = %q", header.Get("Subject"))
	}
}
