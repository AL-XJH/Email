package app

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// Use a short client read deadline to exercise a real missing final SMTP reply
// without waiting for the production 45-second idle timeout.
func TestSMTPFinalDATAReplyClassification(t *testing.T) {
	for _, tc := range []struct {
		name, reply        string
		code               int
		unknown, permanent bool
	}{
		{"temporary-451", "451 4.3.0 Temporary failure\r\n", 451, false, false},
		{"temporary-452", "452 4.3.1 Storage full\r\n", 452, false, false},
		{"permanent-554", "554 5.7.1 Rejected\r\n", 554, false, true},
		{"eof", "", 0, true, true},
		{"timeout", "timeout", 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			t.Cleanup(func() { clientConn.Close(); serverConn.Close() })
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			go func() {
				defer serverConn.Close()
				rd := textproto.NewReader(bufio.NewReader(serverConn))
				io.WriteString(serverConn, "220 test.example.test ESMTP\r\n")
				for {
					line, err := rd.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						io.WriteString(serverConn, "250 test.example.test\r\n")
					case line == "DATA":
						io.WriteString(serverConn, "354 Send data\r\n")
						if _, err := rd.ReadDotBytes(); err != nil {
							return
						}
						if tc.reply == "timeout" {
							<-release
							return
						}
						if tc.reply != "" {
							io.WriteString(serverConn, tc.reply)
						}
						return
					default:
						io.WriteString(serverConn, "250 OK\r\n")
					}
				}
			}()
			client, err := smtp.NewClient(&idleConn{Conn: clientConn, d: 150 * time.Millisecond}, "test.example.test")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = sendSMTPMessage(client, nil, "sender@example.test", []string{"recipient@example.test"}, []byte("Subject: hello\r\n\r\nsmall body\r\n"))
			if err == nil {
				t.Fatal("expected final DATA failure")
			}
			if got := errors.Is(err, errDeliveryUnknown); got != tc.unknown {
				t.Fatalf("unknown=%v want=%v: %v", got, tc.unknown, err)
			}
			if tc.code != 0 {
				var reply *textproto.Error
				if !errors.As(err, &reply) || reply.Code != tc.code {
					t.Fatalf("lost explicit SMTP error: %v", err)
				}
			}
			if got := permanentSMTPFailure(err); got != tc.permanent {
				t.Fatalf("permanent=%v want=%v: %v", got, tc.permanent, err)
			}
			t.Logf("unknown=%v permanent=%v error=%v", tc.unknown, tc.permanent, err)
		})
	}
}
