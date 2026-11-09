package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	netmail "net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	smtpserver "github.com/emersion/go-smtp"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultSubmissionMaxRecipients = 200
)

type SubmissionServers struct {
	Plain *smtpserver.Server
	TLS   *smtpserver.Server
}

func (s *SubmissionServers) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.Plain != nil {
		if err := s.Plain.Shutdown(ctx); err != nil && !errors.Is(err, smtpserver.ErrServerClosed) {
			errs = append(errs, err)
		}
	}
	if s.TLS != nil {
		if err := s.TLS.Shutdown(ctx); err != nil && !errors.Is(err, smtpserver.ErrServerClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *App) NewSubmissionServers(tlsConfig *tls.Config) *SubmissionServers {
	return &SubmissionServers{
		Plain: a.newSubmissionServer(a.config().SubmissionAddr, tlsConfig),
		TLS:   a.newSubmissionServer(a.config().SubmissionTLSAddr, tlsConfig),
	}
}

func (a *App) newSubmissionServer(addr string, tlsConfig *tls.Config) *smtpserver.Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	s := smtpserver.NewServer(submissionBackend{app: a})
	s.Addr = addr
	s.Domain = a.config().PublicHostname
	s.TLSConfig = tlsConfig
	s.AllowInsecureAuth = false
	s.MaxRecipients = defaultSubmissionMaxRecipients
	s.MaxMessageBytes = int64(a.config().SubmissionMaxMessageMB) * 1024 * 1024
	s.ReadTimeout = smtpSessionTimeout
	s.WriteTimeout = smtpSessionTimeout
	s.ErrorLog = log.New(submissionLogWriter{log: a.log}, "smtp/submission ", 0)
	return s
}

func LoadServerTLSConfig(cfg Config) (*tls.Config, error) {
	certFile, keyFile := strings.TrimSpace(cfg.TLSCertFile), strings.TrimSpace(cfg.TLSKeyFile)
	if certFile == "" || keyFile == "" {
		return nil, errors.New("LANQIN_TLS_CERT_FILE and LANQIN_TLS_KEY_FILE are required when SMTP submission is enabled")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}, nil
}

type submissionLogWriter struct {
	log slogLogger
}

func (w submissionLogWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		w.log.Warn(strings.TrimSpace(string(p)))
	}
	return len(p), nil
}

type slogLogger interface {
	Warn(msg string, args ...any)
}

type submissionBackend struct {
	app *App
}

func (b submissionBackend) NewSession(conn *smtpserver.Conn) (smtpserver.Session, error) {
	remoteIP := ""
	if conn != nil && conn.Conn() != nil {
		remoteIP = conn.Conn().RemoteAddr().String()
		if host, _, err := net.SplitHostPort(remoteIP); err == nil {
			remoteIP = host
		}
	}
	return &submissionSession{app: b.app, remoteIP: remoteIP}, nil
}

type submissionSession struct {
	app        *App
	remoteIP   string
	user       *User
	mailbox    *Mailbox
	mailFrom   string
	recipients []string
}

// SMTP 会话本身没有 context，但每个命令背后都是数据库查询和网络往返。
// 给每条命令一个上限，避免一个卡住的查询把会话（和它的 goroutine）永久占住；
// 客户端断开时 go-smtp 会关掉连接，命令超时后这里也会自己收尾。
const submissionCommandTimeout = 90 * time.Second

func submissionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), submissionCommandTimeout)
}

func (s *submissionSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}

func (s *submissionSession) Auth(mech string) (sasl.Server, error) {
	if !strings.EqualFold(mech, sasl.Plain) {
		return nil, smtpserver.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		ctx, cancel := submissionContext()
		defer cancel()
		account := normalizeEmail(username)
		// SMTP 提交口是公网可达的，必须和网页登录共用同一套限速：
		// 否则这里就是一个不限次数、也不进审计的密码校验接口。
		if retryAfter, err := s.app.checkLoginRateLimit(ctx, account, s.remoteIP); err != nil {
			return smtpserver.ErrAuthFailed
		} else if retryAfter > 0 {
			s.app.auditLoginAttempt("smtp", "rate_limited", account, s.remoteIP)
			return smtpserver.ErrAuthFailed
		}
		user, mailbox, err := s.app.authenticateSubmission(ctx, username, password)
		if err != nil {
			if _, recordErr := s.app.recordLoginFailure(ctx, account, s.remoteIP); recordErr == nil {
				s.app.auditLoginAttempt("smtp", "failed", account, s.remoteIP)
			}
			return smtpserver.ErrAuthFailed
		}
		_ = s.app.clearLoginAccountFailures(ctx, account)
		s.app.auditLoginAttempt("smtp", "success", account, s.remoteIP)
		s.user, s.mailbox = user, mailbox
		return nil
	}), nil
}

func (s *submissionSession) Mail(from string, _ *smtpserver.MailOptions) error {
	if s.user == nil || s.mailbox == nil {
		return smtpserver.ErrAuthRequired
	}
	from = normalizeEmail(from)
	ctx, cancel := submissionContext()
	defer cancel()
	authorized, _, err := s.app.authorizedSender(ctx, s.mailbox, from)
	if err != nil || from == "" || from != authorized {
		return smtpError(553, smtpserver.EnhancedCode{5, 7, 1}, "sender must match authenticated mailbox")
	}
	s.mailFrom = from
	s.recipients = nil
	return nil
}

func (s *submissionSession) Rcpt(to string, _ *smtpserver.RcptOptions) error {
	if s.user == nil || s.mailbox == nil {
		return smtpserver.ErrAuthRequired
	}
	to = normalizeEmail(to)
	if to == "" || !strings.Contains(to, "@") {
		return smtpError(501, smtpserver.EnhancedCode{5, 1, 3}, "invalid recipient")
	}
	// The same cap the compose paths apply, refused at RCPT so the client never gets
	// to stream a message that would only be rejected afterwards.
	if len(s.recipients) >= maxRecipientsPerMessage {
		return smtpError(452, smtpserver.EnhancedCode{4, 5, 3}, fmt.Sprintf("too many recipients: max %d", maxRecipientsPerMessage))
	}
	s.recipients = append(s.recipients, to)
	return nil
}

func (s *submissionSession) Data(r io.Reader) error {
	if s.user == nil || s.mailbox == nil {
		return smtpserver.ErrAuthRequired
	}
	if s.mailFrom == "" || len(s.recipients) == 0 {
		return smtpError(503, smtpserver.EnhancedCode{5, 5, 1}, "missing sender or recipients")
	}
	ctx, cancel := submissionContext()
	defer cancel()
	if err := s.app.submitSMTPMessage(ctx, s.user, s.mailbox, s.mailFrom, s.recipients, r); err != nil {
		var smtpErr *smtpserver.SMTPError
		if errors.As(err, &smtpErr) {
			return smtpErr
		}
		return smtpError(451, smtpserver.EnhancedCode{4, 0, 0}, "message submission failed")
	}
	s.Reset()
	return nil
}

func (s *submissionSession) Reset() {
	s.mailFrom = ""
	s.recipients = nil
}

func (s *submissionSession) Logout() error {
	s.Reset()
	return nil
}

func (a *App) authenticateSubmission(ctx context.Context, username, password string) (*User, *Mailbox, error) {
	address := normalizeEmail(username)
	if address == "" {
		return nil, nil, errors.New("missing username")
	}
	var mb Mailbox
	var passwordHash, created string
	row := a.db.QueryRowContext(ctx, `SELECT id,user_id,domain_id,local_part,address,display_name,password_hash,quota_mb,status,created_at
		FROM mailboxes WHERE address=? AND status='active'`, address)
	if err := row.Scan(&mb.ID, &mb.UserID, &mb.DomainID, &mb.LocalPart, &mb.Address, &mb.DisplayName, &passwordHash, &mb.QuotaMB, &mb.Status, &created); err != nil {
		return nil, nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return nil, nil, err
	}
	mb.CreatedAt = parseTime(created)
	user, err := a.userByID(ctx, mb.UserID)
	if err != nil {
		return nil, nil, err
	}
	if user.Disabled {
		return nil, nil, errors.New("user disabled")
	}
	if !userHasPermission(user, PermissionMailSend) {
		return nil, nil, errors.New("send permission required")
	}
	return user, &mb, nil
}

func (a *App) submitSMTPMessage(ctx context.Context, user *User, mb *Mailbox, mailFrom string, recipients []string, r io.Reader) error {
	if err := a.recordSMTPRate(ctx, user, mb, len(recipients)); err != nil {
		if errors.Is(err, errSMTPRateLimited) {
			return smtpError(452, smtpserver.EnhancedCode{4, 7, 0}, err.Error())
		}
		return err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	prepared, msg, attachments, err := a.prepareSubmittedMessage(ctx, raw, mb, mailFrom, recipients)
	if err != nil {
		return err
	}
	msg.MailboxID = mb.ID
	sentID, insertedSent, err := a.insertSentMessageOnce(ctx, msg, attachments)
	if err != nil {
		return err
	}
	if insertedSent {
		if err := a.rewriteMessageMaildir(ctx, sentID); err != nil {
			a.deleteMessage(ctx, sentID)
			if sentFolderID, ferr := a.ensureFolder(ctx, mb.ID, "Sent"); ferr == nil {
				a.deleteSentDedupeKey(ctx, mb.ID, sentFolderID, msg.MessageID)
			}
			return err
		}
	}
	a.recordSendAudit(ctx, sendAuditAccepted, sendQueueStatusQueued, sendAuditInput{UserID: user.ID, MailboxID: mb.ID, SentMessageID: sentID, Source: sendSourceSubmission, MailFrom: mailFrom, HeaderFrom: msg.From, Recipients: recipients})
	// 同一封邮件（Message-ID 相同）在 SMTP 上再来一次，常见原因是客户端没收到
	// 250 响应后的自动重投，而不是用户想重发。已经投递成功/还在队列里的，
	// 直接当成功处理；只有上一轮失败或被取消才真的重新入队。
	requeue := insertedSent
	if !requeue && sentID != "" {
		requeue, err = a.smtpResubmitNeedsRequeue(ctx, sentID)
		if err != nil {
			return err
		}
	}
	if sentID != "" && requeue {
		if _, err := a.enqueueSend(ctx, sendQueueInput{UserID: user.ID, MailboxID: mb.ID, SentMessageID: sentID, MessageID: msg.MessageID, Source: sendSourceSubmission, MailFrom: mailFrom, HeaderFrom: msg.From, Recipients: recipients, MIMEBytes: prepared, Now: a.now().UTC()}); err != nil {
			if insertedSent {
				a.deleteMessage(ctx, sentID)
				if sentFolderID, ferr := a.ensureFolder(ctx, mb.ID, "Sent"); ferr == nil {
					a.deleteSentDedupeKey(ctx, mb.ID, sentFolderID, msg.MessageID)
				}
			}
			return err
		}
	}
	if sentID != "" && strings.TrimSpace(a.config().SMTPHost) == "" {
		return a.deliverLocalRecipients(ctx, sentID, recipients, msg, attachments)
	}
	return nil
}

func (a *App) prepareSubmittedMessage(ctx context.Context, raw []byte, mb *Mailbox, mailFrom string, recipients []string) ([]byte, storedMessage, []AttachmentInput, error) {
	header, body, err := readMessageHeader(raw)
	if err != nil {
		return nil, storedMessage{}, nil, smtpError(554, smtpserver.EnhancedCode{5, 6, 0}, "invalid message")
	}
	fromAddress, fromName, ok := singleHeaderAddress(header.Get("From"))
	if !ok || fromAddress == "" {
		return nil, storedMessage{}, nil, smtpError(550, smtpserver.EnhancedCode{5, 7, 1}, "From header must contain exactly one address")
	}
	authAddress, fromName, err := a.authorizedSender(ctx, mb, fromAddress)
	if err != nil || normalizeEmail(mailFrom) != authAddress || normalizeEmail(fromAddress) != authAddress {
		return nil, storedMessage{}, nil, smtpError(553, smtpserver.EnhancedCode{5, 7, 1}, "sender must match authenticated mailbox")
	}
	now := a.now().UTC()
	messageID := strings.TrimSpace(header.Get("Message-Id"))
	if messageID == "" {
		messageID = fmt.Sprintf("<%s@%s>", newID("msg"), domainPart(authAddress))
		header.Set("Message-ID", messageID)
	} else {
		header.Set("Message-ID", messageID)
	}
	sentAt := parseMailDate(header.Get("Date"))
	if sentAt.IsZero() {
		sentAt = now
		header.Set("Date", sentAt.Format(time.RFC1123Z))
	}
	header.Del("Bcc")
	prepared := serializeMessage(header, body)
	msg, attachments, err := a.parseMaildirMessage(prepared, authAddress)
	if err != nil {
		return nil, storedMessage{}, nil, smtpError(554, smtpserver.EnhancedCode{5, 6, 0}, "invalid message")
	}
	if msg.MessageID == "" {
		msg.MessageID = messageID
	}
	msg.InReplyTo = strings.TrimSpace(header.Get("In-Reply-To"))
	msg.References = strings.TrimSpace(header.Get("References"))
	if msg.SentAt.IsZero() {
		msg.SentAt = sentAt
	}
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = sentAt
	}
	msg.From = authAddress
	msg.FromName = fromName
	msg.To = dedupeEmails(msg.To)
	msg.CC = dedupeEmails(msg.CC)
	msg.BCC = deduceBCCRecipients(recipients, addressList(header.Get("To")), addressList(header.Get("Cc")))
	msg.IsRead = true
	msg.RawPath = ""
	if msg.Subject == "" {
		msg.Subject = "(no subject)"
	}
	if msg.Snippet == "" {
		msg.Snippet = snippetFrom(msg.BodyText, msg.BodyHTML)
	}
	return prepared, msg, attachments, nil
}

func (a *App) insertSentMessageOnce(ctx context.Context, msg storedMessage, attachments []AttachmentInput) (string, bool, error) {
	sentFolderID, err := a.ensureFolder(ctx, msg.MailboxID, "Sent")
	if err != nil {
		return "", false, err
	}
	msg.FolderID = sentFolderID
	if msg.MessageUID == "" {
		msg.MessageUID = newID("uid")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	committed := false
	messageIDForCleanup := ""
	defer func() {
		if !committed {
			_ = tx.Rollback()
			if messageIDForCleanup != "" {
				a.deleteMessageFiles(ctx, messageIDForCleanup)
			}
		}
	}()
	if msg.MessageID != "" {
		existing, err := sentMessageIDByMessageID(ctx, tx, msg.MailboxID, sentFolderID, msg.MessageID)
		if err == nil {
			if err := a.insertSentDedupeKeyWithDB(ctx, tx, msg.MailboxID, sentFolderID, msg.MessageID); err != nil && !errors.Is(err, errSentDedupeExists) {
				return "", false, err
			}
			if err := tx.Commit(); err != nil {
				return "", false, err
			}
			committed = true
			return existing, false, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if err := a.insertSentDedupeKeyWithDB(ctx, tx, msg.MailboxID, sentFolderID, msg.MessageID); err != nil {
			if !errors.Is(err, errSentDedupeExists) {
				return "", false, err
			}
			existing, qerr := sentMessageIDByMessageID(ctx, tx, msg.MailboxID, sentFolderID, msg.MessageID)
			if qerr == nil {
				if err := tx.Commit(); err != nil {
					return "", false, err
				}
				committed = true
				return existing, false, nil
			}
			if !errors.Is(qerr, sql.ErrNoRows) {
				return "", false, qerr
			}
			// 去重键还在、对应的已发邮件已经被移走或删掉（去重键不会跟着删）。
			// 这时不能一直报错：把孤儿键刷新后继续，否则这个 Message-ID
			// 会永久返回 451，用户再也发不出这封信。
			if _, insErr := tx.ExecContext(ctx, `INSERT INTO sent_message_dedupe_keys(mailbox_id,folder_id,message_id,created_at) VALUES(?,?,?,?)
				ON CONFLICT(mailbox_id,folder_id,message_id) DO UPDATE SET created_at=excluded.created_at`,
				msg.MailboxID, sentFolderID, msg.MessageID, a.now().UTC().Format(time.RFC3339Nano)); insErr != nil {
				return "", false, insErr
			}
		}
	}
	id, err := a.insertMessageWithDB(ctx, tx, msg, attachments)
	if err != nil {
		return "", false, err
	}
	messageIDForCleanup = id
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	committed = true
	return id, true, nil
}

var errSentDedupeExists = errors.New("sent message already exists")

func (a *App) insertSentDedupeKey(ctx context.Context, mailboxID, folderID, messageID string) error {
	return a.insertSentDedupeKeyWithDB(ctx, a.db, mailboxID, folderID, messageID)
}

func (a *App) insertSentDedupeKeyWithDB(ctx context.Context, db dbExecutor, mailboxID, folderID, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return nil
	}
	query := insertIgnoreSQL(databaseDriverSQLite, `INSERT INTO sent_message_dedupe_keys(mailbox_id,folder_id,message_id,created_at) VALUES(?,?,?,?)`, `(mailbox_id,folder_id,message_id)`)
	res, err := db.ExecContext(ctx, query, mailboxID, folderID, messageID, a.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return errSentDedupeExists
	}
	return nil
}

func sentMessageIDByMessageID(ctx context.Context, db dbQueryer, mailboxID, folderID, messageID string) (string, error) {
	var existing string
	err := db.QueryRowContext(ctx, `SELECT id FROM messages WHERE mailbox_id=? AND folder_id=? AND message_id=? AND message_id <> '' LIMIT 1`, mailboxID, folderID, messageID).Scan(&existing)
	return existing, err
}

func (a *App) deleteSentDedupeKey(ctx context.Context, mailboxID, folderID, messageID string) {
	if strings.TrimSpace(messageID) == "" {
		return
	}
	_, _ = a.db.ExecContext(ctx, `DELETE FROM sent_message_dedupe_keys WHERE mailbox_id=? AND folder_id=? AND message_id=?`, mailboxID, folderID, messageID)
}

func readMessageHeader(raw []byte) (textproto.MIMEHeader, []byte, error) {
	// 用 bufio 精确记录头结束位置，正文直接切片复用 raw 的底层数组，
	// 避免再把整封正文复制一份（大附件时这一份就是几十 MB）。
	//
	// 偏移必须同时减掉「reader 里还没读的」和「bufio 缓冲里还没交出去的」：
	// 只看 br.Buffered() 的话，凡是超过一个 bufio 缓冲区（4096 字节）的邮件，
	// 切出来的正文会变成末尾那一小段，整封信的内容就没了。
	rd := bytes.NewReader(raw)
	br := bufio.NewReader(rd)
	header, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		return nil, nil, err
	}
	consumed := len(raw) - rd.Len() - br.Buffered()
	body := raw[consumed:]
	return header, body, nil
}

func serializeMessage(header textproto.MIMEHeader, body []byte) []byte {
	var buf bytes.Buffer
	buf.Grow(len(body) + 512)
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return textproto.CanonicalMIMEHeaderKey(keys[i]) < textproto.CanonicalMIMEHeaderKey(keys[j])
	})
	for _, key := range keys {
		values := header[key]
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", canonical, strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", " "))
		}
	}
	buf.WriteString("\r\n")
	buf.Write(body)
	return buf.Bytes()
}

func singleHeaderAddress(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	items, err := netmail.ParseAddressList(value)
	if err != nil || len(items) != 1 {
		decoded := decodeMIMEHeader(value)
		items, err = netmail.ParseAddressList(decoded)
		if err != nil || len(items) != 1 {
			return "", "", false
		}
	}
	item := items[0]
	return normalizeEmail(item.Address), strings.TrimSpace(decodeMIMEHeader(item.Name)), true
}

func deduceBCCRecipients(envelope, to, cc []string) []string {
	visible := map[string]bool{}
	for _, item := range append(to, cc...) {
		if email := normalizeEmail(item); email != "" {
			visible[email] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range envelope {
		email := normalizeEmail(item)
		if email == "" || visible[email] || seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, email)
	}
	return out
}

func domainPart(email string) string {
	parts := strings.SplitN(normalizeEmail(email), "@", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "lanqin.local"
	}
	return parts[1]
}

func smtpError(code int, enhanced smtpserver.EnhancedCode, message string) *smtpserver.SMTPError {
	return &smtpserver.SMTPError{Code: code, EnhancedCode: enhanced, Message: message}
}

// smtpResubmitNeedsRequeue 判断同一 Message-ID 再次通过 SMTP 提交时要不要重新入队。
// 队列里没有记录、或上一轮失败/被取消 → 需要；已投递或还在排队/发送中 → 不需要。
func (a *App) smtpResubmitNeedsRequeue(ctx context.Context, sentMessageID string) (bool, error) {
	var status string
	err := a.db.QueryRowContext(ctx, `SELECT status FROM send_queue WHERE sent_message_id=? ORDER BY created_at DESC LIMIT 1`, sentMessageID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	switch status {
	case sendQueueStatusFailed, sendQueueStatusCanceled:
		return true, nil
	default:
		return false, nil
	}
}
