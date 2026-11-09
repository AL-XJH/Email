package app

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// 起一个内存 IMAP 服务器，验证 FetchNew 对「小信批量带正文、大信只带元数据」
// 的行为。这条路径以前是整批 BODY.PEEK[]，大附件会把内存顶穿。
func TestFetchNewInlinesSmallBodiesOnly(t *testing.T) {
	memServer := imapmemserver.New()
	user := imapmemserver.NewUser("tester", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create inbox: %v", err)
	}
	memServer.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memServer.NewSession(), nil, nil
		},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})

	client, err := imapclient.DialInsecure(ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Login("tester", "secret").Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}

	small := []byte("From: small@example.com\r\nSubject: small\r\n\r\nshort body\r\n")
	bigBody := bytes.Repeat([]byte("x"), int(externalIMAPInlineBodyMaxBytes)+1024)
	big := append([]byte("From: big@example.com\r\nSubject: big\r\n\r\n"), bigBody...)
	for _, raw := range [][]byte{small, big} {
		cmd := client.Append("INBOX", int64(len(raw)), nil)
		if _, err := cmd.Write(raw); err != nil {
			t.Fatalf("append write: %v", err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatalf("append close: %v", err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("append wait: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wrapper := &goExternalIMAPClient{client: client}
	messages, err := wrapper.FetchNew(ctx, "INBOX", 0, 10)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	for _, msg := range messages {
		switch msg.Subject {
		case "small":
			if len(msg.Raw) == 0 {
				t.Fatal("small message should be fetched inline with its body")
			}
		case "big":
			if len(msg.Raw) != 0 {
				t.Fatalf("big message must not be fetched inline, got %d bytes", len(msg.Raw))
			}
		default:
			t.Fatalf("unexpected subject %q", msg.Subject)
		}
	}

	// 大邮件应该能通过 FetchRaw 单独取回，且内容完整。
	var bigUID uint32
	for _, msg := range messages {
		if msg.Subject == "big" {
			bigUID = msg.UID
		}
	}
	raw, _, err := wrapper.FetchRaw(ctx, "INBOX", bigUID)
	if err != nil {
		t.Fatalf("FetchRaw: %v", err)
	}
	if len(raw) != len(big) {
		t.Fatalf("FetchRaw got %d bytes, want %d", len(raw), len(big))
	}
}
