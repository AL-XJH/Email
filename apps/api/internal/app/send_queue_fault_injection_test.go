package app

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// 发信队列的故障注入：用真实 TCP 的假 SMTP 服务器，覆盖重试、部分拒收、
// 投递结果未知、崩溃后靠投递标记收尾这四条路径。这些分支只在对方 MTA
// 异常时才会走到，平时的手工测试永远碰不到。

type fakeSMTPAttempt struct {
	Rcpts  []string
	Final  int
	Closed bool
}

type queueFakeSMTP struct {
	listener net.Listener
	mu       sync.Mutex
	rcptCode map[string]int
	final    int // DATA 之后返回的状态码；0 表示直接断开（模拟结果未知）
	attempts []fakeSMTPAttempt
	accepted [][]byte
	wg       sync.WaitGroup
	closed   bool
}

func startQueueFakeSMTP(t *testing.T) *queueFakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &queueFakeSMTP{listener: ln, rcptCode: map[string]int{}, final: 250}
	t.Cleanup(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		_ = ln.Close()
		f.wg.Wait()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				f.serve(conn)
			}()
		}
	}()
	return f
}

func (f *queueFakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	rd := textproto.NewReader(bufio.NewReader(conn))
	fmt.Fprint(conn, "220 fake.example.test ESMTP\r\n")
	f.mu.Lock()
	attempt := fakeSMTPAttempt{}
	f.mu.Unlock()
	var body []byte
	for {
		line, err := rd.ReadLine()
		if err != nil {
			f.record(attempt, nil)
			return
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			fmt.Fprint(conn, "250-fake.example.test\r\n250-8BITMIME\r\n250 OK\r\n")
		case strings.HasPrefix(line, "MAIL FROM"):
			fmt.Fprint(conn, "250 OK\r\n")
		case strings.HasPrefix(line, "RCPT TO"):
			addr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "RCPT TO:"), ">"))
			addr = strings.TrimPrefix(addr, "<")
			attempt.Rcpts = append(attempt.Rcpts, addr)
			f.mu.Lock()
			code := f.rcptCode[addr]
			f.mu.Unlock()
			if code == 0 {
				code = 250
			}
			fmt.Fprintf(conn, "%d rcpt %s\r\n", code, addr)
		case line == "DATA":
			fmt.Fprint(conn, "354 go ahead\r\n")
			data, readErr := rd.ReadDotBytes()
			if readErr != nil {
				f.record(attempt, nil)
				return
			}
			f.mu.Lock()
			final := f.final
			f.mu.Unlock()
			if final == 0 {
				attempt.Closed = true
				f.record(attempt, nil)
				return
			}
			fmt.Fprintf(conn, "%d done\r\n", final)
			body = data
			attempt.Final = final
			f.record(attempt, body)
			return
		case line == "QUIT":
			fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

func (f *queueFakeSMTP) record(attempt fakeSMTPAttempt, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, attempt)
	if attempt.Final == 250 && body != nil {
		f.accepted = append(f.accepted, body)
	}
}

func (f *queueFakeSMTP) setRcptCode(addr string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rcptCode[addr] = code
}

func (f *queueFakeSMTP) setFinal(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.final = code
}

func (f *queueFakeSMTP) snapshot() (attempts []fakeSMTPAttempt, accepted int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSMTPAttempt(nil), f.attempts...), len(f.accepted)
}

func (f *queueFakeSMTP) addr() (host, port string) {
	host, port, _ = net.SplitHostPort(f.listener.Addr().String())
	return host, port
}

func queueFixture(t *testing.T, smtp *queueFakeSMTP) (a *App, queueID string) {
	t.Helper()
	a = newTestApp(t)
	host, port := smtp.addr()
	a.updateConfig(func(cfg *Config) {
		cfg.SMTPHost = host
		cfg.SMTPPort = port
		cfg.SMTPUsername = ""
		cfg.SMTPRequireTLS = false
	})
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)
	var userID string
	if err := a.db.QueryRow(`SELECT user_id FROM mailboxes WHERE id=?`, mailboxID).Scan(&userID); err != nil {
		t.Fatalf("load mailbox owner: %v", err)
	}
	id, err := a.enqueueSend(ctx, sendQueueInput{
		UserID:        userID,
		MailboxID:     mailboxID,
		SentMessageID: "",
		MessageID:     "<queue-fault@example.test>",
		Source:        "webmail",
		MailFrom:      "sender@example.test",
		HeaderFrom:    "Sender <sender@example.test>",
		Recipients:    []string{"ok@example.test"},
		MIMEBytes:     []byte("From: sender@example.test\r\nTo: ok@example.test\r\nSubject: fault\r\n\r\nbody\r\n"),
		Now:           a.now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return a, id
}

func queueRow(t *testing.T, a *App, id string) (status string, attempts int, lastError string, nextAttempt time.Time) {
	t.Helper()
	var next string
	if err := a.db.QueryRow(`SELECT status,attempt_count,COALESCE(last_error,''),next_attempt_at FROM send_queue WHERE id=?`, id).
		Scan(&status, &attempts, &lastError, &next); err != nil {
		t.Fatalf("load queue row: %v", err)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, next); err == nil {
		nextAttempt = parsed
	}
	return
}

// 4xx 是临时性错误：必须重试、且重试成功后收件人只收到一封。
func TestSendQueueRetriesTemporaryFailureAndDeliversOnce(t *testing.T) {
	smtp := startQueueFakeSMTP(t)
	smtp.setRcptCode("ok@example.test", 451)
	a, id := queueFixture(t, smtp)
	ctx := context.Background()

	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	status, attempts, lastError, next := queueRow(t, a, id)
	if status != sendQueueStatusFailed {
		t.Fatalf("status=%q want failed after 451", status)
	}
	if attempts != 1 {
		t.Fatalf("attempt_count=%d want 1", attempts)
	}
	if !strings.Contains(lastError, "451") {
		t.Fatalf("last_error=%q, want the 451 reply", lastError)
	}
	if !next.After(a.now().UTC()) {
		t.Fatalf("next_attempt_at=%v, want a future retry", next)
	}
	if _, accepted := smtp.snapshot(); accepted != 0 {
		t.Fatalf("server accepted %d messages after 451, want 0", accepted)
	}

	// 对方恢复：把重试时间提前，下一轮应当投递成功，且只有一封。
	smtp.setRcptCode("ok@example.test", 250)
	if _, err := a.db.Exec(`UPDATE send_queue SET next_attempt_at=? WHERE id=?`, a.now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), id); err != nil {
		t.Fatalf("rewind retry: %v", err)
	}
	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	status, attempts, _, _ = queueRow(t, a, id)
	if status != sendQueueStatusDelivered {
		t.Fatalf("status=%q want delivered", status)
	}
	if attempts != 2 {
		t.Fatalf("attempt_count=%d want 2", attempts)
	}
	if _, accepted := smtp.snapshot(); accepted != 1 {
		t.Fatalf("server accepted %d messages, want exactly 1（重试不能变成重复投递）", accepted)
	}
}

// 一个收件人被 550：其余收件人照常投递，被拒的地址要留回执。
func TestSendQueueRecordsPermanentRejectionPerRecipient(t *testing.T) {
	smtp := startQueueFakeSMTP(t)
	smtp.setRcptCode("bad@example.test", 550)
	a := newTestApp(t)
	host, port := smtp.addr()
	a.updateConfig(func(cfg *Config) {
		cfg.SMTPHost = host
		cfg.SMTPPort = port
		cfg.SMTPUsername = ""
		cfg.SMTPRequireTLS = false
	})
	ctx := context.Background()
	mailboxID := adminMailboxIDForTest(t, a)
	var userID string
	if err := a.db.QueryRow(`SELECT user_id FROM mailboxes WHERE id=?`, mailboxID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	id, err := a.enqueueSend(ctx, sendQueueInput{
		UserID: userID, MailboxID: mailboxID, MessageID: "<partial@example.test>", Source: "webmail",
		MailFrom: "sender@example.test", HeaderFrom: "Sender <sender@example.test>",
		Recipients: []string{"ok@example.test", "bad@example.test"},
		MIMEBytes:  []byte("From: sender@example.test\r\nTo: ok@example.test\r\nSubject: p\r\n\r\nb\r\n"),
		Now:        a.now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("pass: %v", err)
	}
	status, _, _, _ := queueRow(t, a, id)
	if status != sendQueueStatusDelivered {
		t.Fatalf("status=%q want delivered（还有人被接受就不该整封失败）", status)
	}
	attempts, accepted := smtp.snapshot()
	if accepted != 1 {
		t.Fatalf("accepted=%d want 1", accepted)
	}
	if len(attempts) != 1 || len(attempts[0].Rcpts) != 2 {
		t.Fatalf("attempts=%+v want one session with two RCPT", attempts)
	}
	var reason string
	if err := a.db.QueryRow(`SELECT COALESCE(reason,'') FROM delivery_events WHERE queue_id=? AND recipient=? AND status='rejected'`, id, "bad@example.test").Scan(&reason); err != nil {
		t.Fatalf("missing rejection event for bad@example.test: %v", err)
	}
	if !strings.Contains(reason, "550") && !strings.Contains(reason, "5.1.1") && !strings.Contains(reason, "rcpt") {
		t.Fatalf("rejection reason %q does not describe the failure", reason)
	}
}

// 写完正文但没读到最终应答：对方很可能已经收下，不能自动重试（会重复投递）。
func TestSendQueueDoesNotRetryUnknownDelivery(t *testing.T) {
	smtp := startQueueFakeSMTP(t)
	smtp.setFinal(0) // 收到 DATA 后直接断开
	a, id := queueFixture(t, smtp)
	ctx := context.Background()

	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("pass: %v", err)
	}
	status, attempts, lastError, next := queueRow(t, a, id)
	if status != sendQueueStatusFailed {
		t.Fatalf("status=%q want failed", status)
	}
	if attempts != 1 {
		t.Fatalf("attempt_count=%d want 1", attempts)
	}
	if next.Sub(a.now().UTC()) < 24*time.Hour {
		t.Fatalf("next_attempt_at=%v: 结果未知的行不应在短时间内自动重投", next)
	}
	if lastError == "" {
		t.Fatal("last_error empty: 用户看不到为什么没发出去")
	}

	// 再跑两轮也不许重新连接（否则就是重复投递）。
	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	if attemptsNow, accepted := smtp.snapshot(); len(attemptsNow) != 1 || accepted != 0 {
		t.Fatalf("attempts=%d accepted=%d, want 1 attempt and 0 accepted (no retry after unknown delivery)", len(attemptsNow), accepted)
	}
}

// 进程在「SMTP 已接受、DB 还没标记 delivered」之间被杀：投递标记让恢复流程收尾，
// 不能再发一次。
func TestSendQueueMarkedDeliveredAfterCrashIsNotResent(t *testing.T) {
	smtp := startQueueFakeSMTP(t)
	a, id := queueFixture(t, smtp)
	ctx := context.Background()

	item, err := a.claimSendQueueItem(ctx, id)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := a.writeSendQueueDeliveredMarker(item.ID); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// 租约过期 = 这个 worker 已经不在了
	if _, err := a.db.Exec(`UPDATE send_queue SET lease_until=? WHERE id=?`, a.now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), id); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	status, _, _, _ := queueRow(t, a, id)
	if status != sendQueueStatusDelivered {
		t.Fatalf("status=%q want delivered via marker", status)
	}
	if attempts, accepted := smtp.snapshot(); len(attempts) != 0 || accepted != 0 {
		t.Fatalf("恢复流程发出了 %d 次连接（accepted=%d），投递标记没起作用", len(attempts), accepted)
	}
}

// 收件人全被 5xx：整封失败且不再重试（重试也不会有不同结果）。
func TestSendQueueAllRecipientsRejectedIsPermanent(t *testing.T) {
	smtp := startQueueFakeSMTP(t)
	smtp.setRcptCode("ok@example.test", 550)
	a, id := queueFixture(t, smtp)
	ctx := context.Background()

	if err := a.processDueSendQueue(ctx); err != nil {
		t.Fatalf("pass: %v", err)
	}
	status, attempts, _, next := queueRow(t, a, id)
	if status != sendQueueStatusFailed || attempts != 1 {
		t.Fatalf("status=%q attempts=%d, want failed/1", status, attempts)
	}
	if next.Sub(a.now().UTC()) < 24*time.Hour {
		t.Fatalf("next_attempt_at=%v: 全 5xx 不该退避重试", next)
	}
}
