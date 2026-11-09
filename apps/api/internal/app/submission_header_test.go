package app

import "testing"

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
