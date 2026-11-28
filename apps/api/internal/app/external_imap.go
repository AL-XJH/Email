package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
)

const (
	externalIMAPStorageLocal     = "local"
	externalIMAPStorageRemote    = "remote"
	externalIMAPTLS              = "tls"
	externalIMAPStartTLS         = "starttls"
	externalIMAPPlain            = "plain"
	externalIMAPMaxFetch         = 30
	externalIMAPAuthPassword     = "password"
	externalIMAPAuthOAuth2       = "oauth2"
	externalIMAPOAuthGmail       = "gmail"
	externalIMAPOAuthOutlook     = "outlook"
	externalIMAPOAuthNonceCookie = "external_imap_oauth_nonce"
	// Gmail 的 \All（「所有邮件」）角色：导入时跨文件夹按 Message-ID 去重。
	externalIMAPFolderRoleAll = "All"
	externalIMAPSyncBatchSize = 10
	externalIMAPSyncMaxPerRun = 100

	// 单封超过这个体积就不跟着整批拉了：批量取正文时 10 封 20MB 的邮件
	// 会同时驻留内存。超过的改用 FetchRaw 逐封取，峰值只跟最大的一封有关。
	externalIMAPInlineBodyMaxBytes = 2 << 20
)

type externalIMAPClientFactory interface {
	openExternalIMAPClient(ctx context.Context, account externalIMAPAccountRecord) (externalIMAPClient, error)
}

type externalIMAPClient interface {
	Close() error
	ListFolders(ctx context.Context) ([]externalIMAPRemoteFolder, error)
	FetchSummaries(ctx context.Context, folder string, query string, cursor string, limit int) ([]externalIMAPRemoteMessage, string, int, error)
	FetchNew(ctx context.Context, folder string, afterUID uint32, limit int) ([]externalIMAPRemoteMessage, error)
	FetchRaw(ctx context.Context, folder string, uid uint32) ([]byte, externalIMAPRemoteMessage, error)
	FetchAttachments(ctx context.Context, folder string, uid uint32) ([]Attachment, error)
	FetchPart(ctx context.Context, folder string, uid uint32, partID string) ([]byte, Attachment, error)
	SetRead(ctx context.Context, folder string, uid uint32, read bool) error
}

type externalIMAPAccountRecord struct {
	ExternalIMAPAccount
	PasswordCiphertext          string
	OAuthAccessTokenCiphertext  string
	OAuthRefreshTokenCiphertext string
}

type externalIMAPRemoteFolder struct {
	Name        string `json:"name"`
	Role        string `json:"role,omitempty"`
	UnreadCount int    `json:"unreadCount,omitempty"`
	TotalCount  int    `json:"totalCount,omitempty"`
	UIDValidity uint32 `json:"uidValidity,omitempty"`
}

type externalIMAPRemoteMessage struct {
	UID         uint32
	UIDValidity uint32
	Folder      string
	MessageID   string
	Subject     string
	From        string
	FromName    string
	To          []string
	CC          []string
	SentAt      time.Time
	ReceivedAt  time.Time
	Snippet     string
	IsRead      bool
	SizeBytes   int64
	Raw         []byte
	Attachments []Attachment
}

type externalIMAPAttachmentPart struct {
	Attachment
	Encoding string
}

type externalIMAPPayload struct {
	MailboxID     string `json:"mailboxId"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	TLSMode       string `json:"tlsMode"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	StorageMode   string `json:"storageMode"`
	SyncReadState *bool  `json:"syncReadState"`
	Enabled       *bool  `json:"enabled"`
}

type externalIMAPOAuthStartPayload struct {
	MailboxID     string `json:"mailboxId"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	StorageMode   string `json:"storageMode"`
	SyncReadState *bool  `json:"syncReadState"`
	Enabled       *bool  `json:"enabled"`
}

type externalIMAPOAuthState struct {
	UserID        string `json:"userId"`
	MailboxID     string `json:"mailboxId"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	StorageMode   string `json:"storageMode"`
	SyncReadState bool   `json:"syncReadState"`
	Enabled       bool   `json:"enabled"`
	Nonce         string `json:"nonce"`
	ExpiresAt     int64  `json:"expiresAt"`
}

func (a *App) externalIMAPWorker(ctx context.Context) {
	interval := time.Duration(a.config().ExternalIMAPSyncSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.syncDueExternalIMAPAccounts(ctx)
		}
	}
}

func (a *App) syncDueExternalIMAPAccounts(ctx context.Context) {
	if !a.config().ExternalIMAPEnabled {
		return
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM external_imap_accounts WHERE enabled=1 AND storage_mode=? ORDER BY COALESCE(last_sync_at, created_at) ASC LIMIT 10`, externalIMAPStorageLocal)
	if err != nil {
		a.log.Warn("failed to list external imap accounts", "error", err)
		return
	}
	defer rows.Close()
	var accountIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			a.log.Warn("failed to scan external imap account", "error", err)
			return
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Err(); err != nil {
		a.log.Warn("failed to list external imap accounts", "error", err)
		return
	}
	// Release the result set before synchronization queries the database again.
	// SQLite uses a single connection, so retaining rows would block all DB access.
	if err := rows.Close(); err != nil {
		a.log.Warn("failed to close external imap accounts", "error", err)
		return
	}
	for _, id := range accountIDs {
		if ctx.Err() != nil {
			return
		}
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, _ = a.syncExternalIMAPAccount(runCtx, id)
		cancel()
	}
}

func (a *App) requireExternalIMAPEnabled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.config().ExternalIMAPEnabled {
			respondError(w, http.StatusForbidden, "external imap is disabled")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleListExternalIMAPAccounts(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	mailboxID := strings.TrimSpace(r.URL.Query().Get("mailboxId"))
	args := []any{user.ID}
	where := "user_id=?"
	if mailboxID != "" {
		if _, err := a.mailboxForUserByID(r.Context(), user.ID, mailboxID); err != nil {
			respondError(w, http.StatusNotFound, "mailbox not found")
			return
		}
		where += " AND mailbox_id=?"
		args = append(args, mailboxID)
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,auth_mode,oauth_provider,oauth_email,oauth_access_token_ciphertext,oauth_refresh_token_ciphertext,oauth_expiry,storage_mode,sync_read_state,enabled,last_sync_at,last_status,last_error,created_at,updated_at FROM external_imap_accounts WHERE `+where+` ORDER BY created_at DESC`, args...)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load external accounts")
		return
	}
	defer rows.Close()
	items := []ExternalIMAPAccount{}
	for rows.Next() {
		item, err := scanExternalIMAPAccount(rows)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to scan external accounts")
			return
		}
		items = append(items, item.ExternalIMAPAccount)
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleCreateExternalIMAPAccount(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req externalIMAPPayload
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	mb, err := a.mailboxForUserByID(r.Context(), user.ID, strings.TrimSpace(req.MailboxID))
	if err != nil {
		respondError(w, http.StatusNotFound, "mailbox not found")
		return
	}
	req.MailboxID = mb.ID
	if strings.TrimSpace(req.Password) == "" {
		badRequest(w, errors.New("password is required"))
		return
	}
	normalized, err := a.normalizeExternalIMAPPayload(r.Context(), req, true)
	if err != nil {
		badRequest(w, err)
		return
	}
	ciphertext, err := a.encryptExternalIMAPPassword(normalized.Password)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 先真连一次再落库：账号存进去才发现连不上，用户看到的是「添加成功但用不了」。
	// LANQIN_EXTERNAL_IMAP_VERIFY_ON_CREATE=0 可关掉（测试/内网环境）。
	var createdProbeFolders []externalIMAPRemoteFolder
	if a.config().ExternalIMAPVerifyOnCreate {
		probeCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		probe := externalIMAPAccountRecord{
			ExternalIMAPAccount: ExternalIMAPAccount{
				ID:          "probe",
				MailboxID:   normalized.MailboxID,
				Name:        normalized.Name,
				Host:        normalized.Host,
				Port:        normalized.Port,
				TLSMode:     normalized.TLSMode,
				Username:    normalized.Username,
				AuthMode:    externalIMAPAuthPassword,
				StorageMode: normalized.StorageMode,
				Enabled:     true,
			},
			PasswordCiphertext: ciphertext,
		}
		probeClient, err := a.externalIMAP.openExternalIMAPClient(probeCtx, probe)
		if err != nil {
			a.log.Warn("external imap create probe failed", "host", probe.Host, "port", probe.Port, "tls", probe.TLSMode, "error", err)
			respondError(w, http.StatusBadRequest, "连接失败："+err.Error())
			return
		}
		probeFolders, err := probeClient.ListFolders(probeCtx)
		if err != nil {
			probeClient.Close()
			respondError(w, http.StatusBadRequest, "连接失败："+err.Error())
			return
		}
		probeClient.Close()
		createdProbeFolders = probeFolders
	}

	now := a.now().UTC().Format(time.RFC3339Nano)
	id := newID("ximap")
	if _, err := a.db.ExecContext(r.Context(), `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, user.ID, normalized.MailboxID, normalized.Name, normalized.Host, normalized.Port, normalized.TLSMode, normalized.Username, ciphertext, normalized.StorageMode, boolInt(*normalized.SyncReadState), boolInt(*normalized.Enabled), "idle", now, now); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create external account")
		return
	}
	// 探活已经 LIST 过一遍，顺手存成快照：用户加完账号立刻展开下拉不用再等一次现连。
	if len(createdProbeFolders) > 0 {
		a.saveExternalIMAPFolderSnapshot(r.Context(), id, createdProbeFolders)
	}
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load external account")
		return
	}
	respondJSON(w, http.StatusCreated, account.ExternalIMAPAccount)
}

func (a *App) handleUpdateExternalIMAPAccount(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := chi.URLParam(r, "id")
	current, err := a.externalIMAPAccountForUser(r.Context(), user.ID, id)
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	var req externalIMAPPayload
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	if strings.TrimSpace(req.MailboxID) == "" {
		req.MailboxID = current.MailboxID
	}
	if _, err := a.mailboxForUserByID(r.Context(), user.ID, strings.TrimSpace(req.MailboxID)); err != nil {
		respondError(w, http.StatusNotFound, "mailbox not found")
		return
	}
	normalized, err := a.normalizeExternalIMAPPayload(r.Context(), req, false)
	if err != nil {
		badRequest(w, err)
		return
	}
	ciphertext := current.PasswordCiphertext
	if strings.TrimSpace(normalized.Password) != "" {
		ciphertext, err = a.encryptExternalIMAPPassword(normalized.Password)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.ExecContext(r.Context(), `UPDATE external_imap_accounts SET mailbox_id=?,name=?,host=?,port=?,tls_mode=?,username=?,password_ciphertext=?,storage_mode=?,sync_read_state=?,enabled=?,updated_at=? WHERE id=? AND user_id=?`,
		normalized.MailboxID, normalized.Name, normalized.Host, normalized.Port, normalized.TLSMode, normalized.Username, ciphertext, normalized.StorageMode, boolInt(*normalized.SyncReadState), boolInt(*normalized.Enabled), now, id, user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update external account")
		return
	}
	// 换了服务器/账号/绑定邮箱/存储模式，旧的 UID 游标和已导入邮件都不再对应
	// 同一个空间：留着会让新服务器上 UID 更小的邮件被永久跳过，或者让旧邮件
	// 挂在新邮箱下永远看不见。直接按「重新接入」处理，清了重新同步。
	// 必须放在 UPDATE 成功之后：更新失败时不能先把用户数据删了。
	if normalized.Host != current.Host || normalized.Port != current.Port ||
		normalized.TLSMode != current.TLSMode || normalized.Username != current.Username ||
		normalized.MailboxID != current.MailboxID || normalized.StorageMode != current.StorageMode {
		a.log.Info("external imap account connection changed, clearing imported data",
			"account", current.ID, "host", normalized.Host, "mailbox", normalized.MailboxID)
		if err := a.deleteExternalIMAPLocalData(r.Context(), current.ID, user.ID); err != nil {
			a.log.Warn("failed to clear old external imap data after reconnect", "account", current.ID, "error", err)
		}
	}
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load external account")
		return
	}
	respondJSON(w, http.StatusOK, account.ExternalIMAPAccount)
}

func (a *App) handleDeleteExternalIMAPAccount(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	accountID := chi.URLParam(r, "id")
	// 先清掉这个账号导入到本地的邮件和「External …」文件夹。
	// 只删账号行的话，external_imap_messages 级联删除后这些邮件就失去了
	// 「属于外部账号」的标记，整批历史外部邮件会突然出现在主邮箱列表里。
	if err := a.deleteExternalIMAPLocalData(r.Context(), accountID, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to remove external account messages")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM external_imap_accounts WHERE id=? AND user_id=?`, accountID, user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete external account")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleTestExternalIMAPAccount(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
	if err != nil {
		a.updateExternalIMAPStatus(r.Context(), account.ID, "error", err.Error())
		respondError(w, http.StatusBadRequest, "connection failed: "+err.Error())
		return
	}
	defer client.Close()
	folders, err := client.ListFolders(r.Context())
	if err != nil {
		a.updateExternalIMAPStatus(r.Context(), account.ID, "error", err.Error())
		respondError(w, http.StatusBadRequest, "list folders failed: "+err.Error())
		return
	}
	a.updateExternalIMAPStatus(r.Context(), account.ID, "ok", "")
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "folders": len(folders)})
}

func (a *App) handleSyncExternalIMAPAccount(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	if account.StorageMode != externalIMAPStorageLocal {
		badRequest(w, errors.New("remote storage accounts do not sync into local mailbox"))
		return
	}
	run, err := a.syncExternalIMAPAccount(r.Context(), account.ID)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, run)
}

func (a *App) handleExternalIMAPSyncRuns(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,account_id,folder,status,imported,skipped,failed,error,started_at,finished_at FROM external_imap_sync_runs WHERE account_id=? ORDER BY started_at DESC LIMIT 20`, account.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load sync runs")
		return
	}
	defer rows.Close()
	items := []ExternalIMAPSyncRun{}
	for rows.Next() {
		run, err := scanExternalIMAPSyncRun(rows)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to scan sync runs")
			return
		}
		items = append(items, run)
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleSyncExternalIMAPFolder(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	if account.StorageMode != externalIMAPStorageLocal {
		badRequest(w, errors.New("remote storage accounts do not sync into local mailbox"))
		return
	}
	var req struct {
		Folder string `json:"folder"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	folder := strings.TrimSpace(req.Folder)
	if folder == "" {
		badRequest(w, errors.New("folder is required"))
		return
	}
	run, err := a.syncExternalIMAPAccountFolder(r.Context(), account.ID, folder)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, run)
}

func (a *App) handleStartExternalIMAPOAuth(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	provider := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))
	conf, profile, err := a.externalIMAPOAuthConfig(provider)
	if err != nil {
		badRequest(w, err)
		return
	}
	var req externalIMAPOAuthStartPayload
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	mb, err := a.mailboxForUserByID(r.Context(), user.ID, strings.TrimSpace(req.MailboxID))
	if err != nil {
		respondError(w, http.StatusNotFound, "mailbox not found")
		return
	}
	email := normalizeEmail(req.Email)
	if email != "" && !strings.Contains(email, "@") {
		badRequest(w, errors.New("invalid oauth email"))
		return
	}
	req.StorageMode = strings.ToLower(strings.TrimSpace(req.StorageMode))
	if req.StorageMode == "" {
		req.StorageMode = externalIMAPStorageLocal
	}
	if req.StorageMode != externalIMAPStorageLocal && req.StorageMode != externalIMAPStorageRemote {
		badRequest(w, errors.New("invalid storage mode"))
		return
	}
	syncRead := true
	if req.SyncReadState != nil {
		syncRead = *req.SyncReadState
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	state := externalIMAPOAuthState{
		UserID:        user.ID,
		MailboxID:     mb.ID,
		Provider:      provider,
		Name:          strings.Join(strings.Fields(req.Name), " "),
		Email:         email,
		StorageMode:   req.StorageMode,
		SyncReadState: syncRead,
		Enabled:       enabled,
		Nonce:         newID("oauth"),
		ExpiresAt:     a.now().Add(10 * time.Minute).Unix(),
	}
	if state.Name == "" {
		state.Name = profile.Name
	}
	stateValue, err := a.encryptExternalIMAPOAuthState(state)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 把 nonce 绑到发起请求的浏览器：否则攻击者可以把自己的 state 链接发给
	// 受害者，受害者授权后 Gmail token 会落到攻击者账号下。
	http.SetCookie(w, &http.Cookie{
		Name:     externalIMAPOAuthNonceCookie,
		Value:    state.Nonce,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(strings.ToLower(a.config().PublicBaseURL), "https://"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   10 * 60,
	})
	// 必须显式 prompt=consent：Google 只在「首次同意」返回 refresh_token，
	// 之前授权过的账号再走一遍只会拿到一次性 access token，一小时后同步就废了。
	respondJSON(w, http.StatusOK, map[string]any{"url": conf.AuthCodeURL(stateValue,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("include_granted_scopes", "false"),
	)})
}

// pickExternalIMAPOAuthUsername 用候选地址逐个试 XOAUTH2 登录，返回第一个能连上的。
// 授权返回的身份可能是登录别名，IMAP 只认邮箱主地址（或 UPN），必须实测。
func (a *App) pickExternalIMAPOAuthUsername(ctx context.Context, provider string, profile externalIMAPOAuthProvider, candidates []string, accessCipher, refreshCipher string) (string, error) {
	seen := map[string]bool{}
	var lastErr error
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[strings.ToLower(candidate)] {
			continue
		}
		seen[strings.ToLower(candidate)] = true
		account := externalIMAPAccountRecord{
			ExternalIMAPAccount: ExternalIMAPAccount{
				ID:            "probe-oauth",
				Host:          profile.Host,
				Port:          profile.Port,
				TLSMode:       externalIMAPTLS,
				Username:      candidate,
				AuthMode:      externalIMAPAuthOAuth2,
				OAuthProvider: provider,
				Enabled:       true,
			},
			OAuthAccessTokenCiphertext:  accessCipher,
			OAuthRefreshTokenCiphertext: refreshCipher,
		}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		client, err := a.externalIMAP.openExternalIMAPClient(probeCtx, account)
		if err == nil {
			_, err = client.ListFolders(probeCtx)
			client.Close()
		}
		cancel()
		if err == nil {
			return candidate, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的邮箱地址")
	}
	return "", lastErr
}

func (a *App) handleExternalIMAPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))
	conf, profile, err := a.externalIMAPOAuthConfig(provider)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := a.decryptExternalIMAPOAuthState(r.URL.Query().Get("state"))
	if err != nil || state.Provider != provider || state.ExpiresAt < a.now().Unix() {
		respondError(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	nonceCookie, cookieErr := r.Cookie(externalIMAPOAuthNonceCookie)
	if cookieErr != nil || subtle.ConstantTimeCompare([]byte(nonceCookie.Value), []byte(state.Nonce)) != 1 {
		respondError(w, http.StatusBadRequest, "oauth state does not belong to this browser")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: externalIMAPOAuthNonceCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
		respondError(w, http.StatusBadRequest, "oauth failed: "+oauthErr)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		respondError(w, http.StatusBadRequest, "missing oauth code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	token, err := conf.Exchange(ctx, code)
	if err != nil {
		respondError(w, http.StatusBadRequest, "oauth exchange failed")
		return
	}
	accessCipher, err := a.encryptExternalIMAPPassword(token.AccessToken)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 没有 refresh token 的账号只有 1 小时寿命（access token 一过期就再也续不上），
	// 与其存进去过一小时再报错，不如当场拒绝，让用户重新授权。
	if strings.TrimSpace(token.RefreshToken) == "" {
		respondError(w, http.StatusBadRequest, "服务商未返回 refresh token，请重新授权（授权页需勾选全部权限）")
		return
	}
	refreshCipher, err := a.encryptExternalIMAPPassword(token.RefreshToken)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := state.Name
	if name == "" {
		name = profile.Name
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	expiry := ""
	if !token.Expiry.IsZero() {
		expiry = token.Expiry.UTC().Format(time.RFC3339Nano)
	}
	authorizedEmail, err := externalIMAPOAuthEmail(provider, token)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 微软 id_token 里的 email/preferred_username 经常只是登录别名（例如登录名
	// xjh@alxjh.cn，真正的邮箱是 al-xjh@outlook.com），拿它当 IMAP 用户名会得到
	// "User is authenticated but not connected"。把用户填的地址排在前面，claims 兜底，
	// 逐个用 XOAUTH2 真连一次，谁能登进去就用谁。
	candidates := make([]string, 0, 2)
	if state.Email != "" {
		candidates = append(candidates, normalizeEmail(state.Email))
	}
	if authorizedEmail != "" {
		candidates = append(candidates, normalizeEmail(authorizedEmail))
	}
	username, probeErr := a.pickExternalIMAPOAuthUsername(r.Context(), provider, profile, candidates, accessCipher, refreshCipher)
	if username == "" {
		msg := "授权成功但邮箱登录失败"
		if probeErr != nil {
			msg += "：" + probeErr.Error()
		}
		respondError(w, http.StatusBadRequest, msg)
		return
	}
	// OAuth 窗口有 10 分钟：期间邮箱可能被转走，落库前再确认一次归属。
	if _, err := a.mailboxForUserByID(r.Context(), state.UserID, state.MailboxID); err != nil {
		respondError(w, http.StatusBadRequest, "mailbox not found for this account")
		return
	}
	id := newID("ximap")
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO external_imap_accounts(id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,auth_mode,oauth_provider,oauth_email,oauth_access_token_ciphertext,oauth_refresh_token_ciphertext,oauth_expiry,storage_mode,sync_read_state,enabled,last_status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, state.UserID, state.MailboxID, name, profile.Host, profile.Port, externalIMAPTLS, username, "", externalIMAPAuthOAuth2, provider, username, accessCipher, refreshCipher, expiry, state.StorageMode, boolInt(state.SyncReadState), boolInt(state.Enabled), "idle", now, now)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save oauth account")
		return
	}
	http.Redirect(w, r, strings.TrimRight(a.config().PublicBaseURL, "/")+"/profile?tab=mailboxes&externalImapOauth=ok", http.StatusFound)
}

func (a *App) handleMailExternalAccounts(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,auth_mode,oauth_provider,oauth_email,oauth_access_token_ciphertext,oauth_refresh_token_ciphertext,oauth_expiry,storage_mode,sync_read_state,enabled,last_sync_at,last_status,last_error,created_at,updated_at FROM external_imap_accounts WHERE user_id=? AND enabled=1 ORDER BY name`, user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load external accounts")
		return
	}
	defer rows.Close()
	items := []ExternalIMAPAccount{}
	for rows.Next() {
		item, err := scanExternalIMAPAccount(rows)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to scan external accounts")
			return
		}
		items = append(items, item.ExternalIMAPAccount)
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

// externalIMAPFolderSnapshotTTL 决定文件夹快照多久后重新向服务端 LIST。
// 定时同步每轮都会刷新快照，所以正常情况下这里几乎不会触发现连。
const externalIMAPFolderSnapshotTTL = 30 * time.Minute

func (a *App) saveExternalIMAPFolderSnapshot(ctx context.Context, accountID string, folders []externalIMAPRemoteFolder) {
	payload, err := json.Marshal(folders)
	if err != nil {
		return
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO external_imap_folder_snapshots(account_id,payload,fetched_at) VALUES(?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET payload=excluded.payload,fetched_at=excluded.fetched_at`, accountID, string(payload), now); err != nil {
		a.log.Debug("external imap folder snapshot save failed", "account", accountID, "error", err)
	}
}

func (a *App) loadExternalIMAPFolderSnapshot(ctx context.Context, accountID string) ([]externalIMAPRemoteFolder, time.Time, bool) {
	var payload, fetched string
	if err := a.db.QueryRowContext(ctx, `SELECT payload,fetched_at FROM external_imap_folder_snapshots WHERE account_id=?`, accountID).Scan(&payload, &fetched); err != nil {
		return nil, time.Time{}, false
	}
	var folders []externalIMAPRemoteFolder
	if err := json.Unmarshal([]byte(payload), &folders); err != nil {
		return nil, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, fetched)
	if err != nil {
		return nil, time.Time{}, false
	}
	return folders, at, true
}

func (a *App) handleExternalIMAPFolders(w http.ResponseWriter, r *http.Request) {
	account, ok := a.externalIMAPAccountForMailRequest(w, r)
	if !ok {
		return
	}
	// 列文件夹要新建一条 IMAP 连接并 LIST，Gmail/Outlook 这类海外服务单程就要
	// 几百毫秒到几秒；下拉菜单每展开一次都现连，用户体感就是卡。本地存储模式的
	// 账号同步时已经 LIST 过一遍，这里优先用快照，没有或过期才现连。
	folders, fetchedAt, hasSnapshot := a.loadExternalIMAPFolderSnapshot(r.Context(), account.ID)
	fresh := hasSnapshot && a.now().Sub(fetchedAt) < externalIMAPFolderSnapshotTTL
	if !fresh {
		client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
		if err != nil {
			if hasSnapshot {
				a.log.Warn("external imap folders live list failed, serving stale snapshot", "account", account.ID, "error", err)
			} else {
				respondError(w, http.StatusBadRequest, "connection failed: "+err.Error())
				return
			}
		} else {
			live, listErr := client.ListFolders(r.Context())
			_ = client.Close()
			if listErr != nil {
				if hasSnapshot {
					a.log.Warn("external imap folders live list failed, serving stale snapshot", "account", account.ID, "error", listErr)
				} else {
					respondError(w, http.StatusBadRequest, "failed to list folders")
					return
				}
			} else {
				folders = live
				a.saveExternalIMAPFolderSnapshot(r.Context(), account.ID, folders)
			}
		}
	}
	// 默认只回可见文件夹；设置页要用 ?all=1 拿全集，才能把隐藏的再打开。
	showAll := strings.TrimSpace(r.URL.Query().Get("all")) == "1" || strings.EqualFold(r.URL.Query().Get("all"), "true")
	hidden := a.externalIMAPHiddenFolderSet(r.Context(), account.ID)
	items := make([]ExternalIMAPFolder, 0, len(folders))
	for _, folder := range folders {
		isHidden := hidden[folder.Name]
		if isHidden && !showAll {
			continue
		}
		if account.StorageMode == externalIMAPStorageLocal {
			// 计数口径要和列表一致：只算仍在这个镜像文件夹里的邮件，已移走的
			//（删到 Trash、归档到别处）不再计入。
			var total, unread int
			_ = a.db.QueryRowContext(r.Context(), `SELECT COUNT(m.id),COALESCE(SUM(CASE WHEN m.is_read=0 THEN 1 ELSE 0 END),0)
				FROM external_imap_messages em
				JOIN messages m ON m.id=em.local_message_id
				JOIN external_imap_folder_states fs ON fs.account_id=em.account_id AND fs.remote_folder=em.remote_folder
				WHERE em.account_id=? AND em.remote_folder=? AND m.mailbox_id=? AND m.folder_id=fs.local_folder_id`, account.ID, folder.Name, account.MailboxID).Scan(&total, &unread)
			folder.TotalCount = total
			folder.UnreadCount = unread
		}
		items = append(items, ExternalIMAPFolder{Name: folder.Name, DisplayName: externalIMAPFolderDisplayName(folder.Name), Hidden: isHidden, Role: folder.Role, UnreadCount: folder.UnreadCount, TotalCount: folder.TotalCount})
	}
	// 用户在「文件夹显示」里拖出来的顺序优先，没排过的保持服务端顺序排后面。
	if order := a.externalIMAPFolderOrder(r.Context(), account.ID); len(order) > 0 {
		position := make(map[string]int, len(order))
		for index, remoteFolder := range order {
			position[remoteFolder] = index
		}
		sort.SliceStable(items, func(i, j int) bool {
			left, leftOK := position[items[i].Name]
			right, rightOK := position[items[j].Name]
			if leftOK != rightOK {
				return leftOK
			}
			if leftOK && left != right {
				return left < right
			}
			return false
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleExternalIMAPMessages(w http.ResponseWriter, r *http.Request) {
	account, ok := a.externalIMAPAccountForMailRequest(w, r)
	if !ok {
		return
	}
	folder := strings.TrimSpace(r.URL.Query().Get("folder"))
	if folder == "" {
		folder = "INBOX"
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	limit, err := mailMessagePageLimit(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	if account.StorageMode == externalIMAPStorageLocal {
		// 必须按「当前所在的本地文件夹」过滤，不能只看映射里的 remote_folder：
		// 删除只是把本地邮件移进 Trash，映射仍指向 INBOX，只按映射过滤会让删掉的
		// 邮件继续留在列表里，用户以为没删掉、反复点删除。
		access := &mailboxReadAccess{Mailbox: &Mailbox{ID: account.MailboxID, UserID: account.UserID}, Owner: true}
		// 「所有邮件」跨文件夹去重后，一封邮件只有一份本地副本，不能按 folder_id
		// 过滤；但它也不该显示已经删进 Trash/Spam 的邮件。
		if externalIMAPFolderIsAllMail(externalIMAPRemoteFolder{Name: folder}) {
			a.respondMailMessageListInternal(w, r, access,
				`m.mailbox_id=? AND m.folder_id NOT IN (SELECT id FROM folders WHERE mailbox_id=? AND lower(name) IN ('trash','spam')) AND EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id AND em.account_id=?)`,
				[]any{account.MailboxID, account.MailboxID, account.ID}, true, account.ID)
			return
		}
		localFolderID := a.externalIMAPLocalFolderID(r.Context(), account.ID, folder)
		if localFolderID == "" {
			respondJSON(w, http.StatusOK, map[string]any{"items": []MailMessage{}, "nextCursor": "", "totalCount": 0})
			return
		}
		a.respondMailMessageListInternal(w, r, access,
			`m.mailbox_id=? AND m.folder_id=? AND EXISTS (SELECT 1 FROM external_imap_messages em WHERE em.local_message_id=m.id AND em.account_id=?)`,
			[]any{account.MailboxID, localFolderID, account.ID}, true, account.ID)
		return
	}
	client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
	if err != nil {
		respondError(w, http.StatusBadRequest, "connection failed: "+err.Error())
		return
	}
	defer client.Close()
	remote, next, totalCount, err := client.FetchSummaries(r.Context(), folder, query, cursor, limit)
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to load remote messages")
		return
	}
	items := make([]MailMessage, 0, len(remote))
	for _, msg := range remote {
		items = append(items, externalRemoteMessageToMailMessage(account, msg, false))
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next, "totalCount": totalCount})
}

func (a *App) handleExternalIMAPMessage(w http.ResponseWriter, r *http.Request) {
	account, ok := a.externalIMAPAccountForMailRequest(w, r)
	if !ok {
		return
	}
	if account.StorageMode == externalIMAPStorageLocal {
		messageID := chi.URLParam(r, "remoteId")
		var mappedID string
		if err := a.db.QueryRowContext(r.Context(), `SELECT em.local_message_id FROM external_imap_messages em JOIN messages m ON m.id=em.local_message_id
			WHERE em.account_id=? AND em.local_message_id=? AND m.mailbox_id=? LIMIT 1`, account.ID, messageID, account.MailboxID).Scan(&mappedID); err != nil {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		msg, err := a.messageByID(r.Context(), mappedID, true)
		if err != nil {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		msg.ExternalAccountID = account.ID
		if len(msg.Attachments) == 0 {
			var rawPath string
			if err := a.db.QueryRowContext(r.Context(), `SELECT raw_path FROM messages WHERE id=? AND mailbox_id=?`, mappedID, account.MailboxID).Scan(&rawPath); err == nil {
				if info, err := os.Stat(rawPath); err == nil {
					msg.Attachments = []Attachment{{ID: "raw", MessageID: msg.ID, Filename: safeExternalEMLFilename(msg.Subject), ContentType: "message/rfc822", SizeBytes: info.Size(), CreatedAt: a.now().UTC()}}
					msg.HasAttachments = true
				}
			}
		}
		respondJSON(w, http.StatusOK, msg)
		return
	}
	folder, uid, ok := decodeExternalRemoteID(w, chi.URLParam(r, "remoteId"))
	if !ok {
		return
	}
	client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
	if err != nil {
		respondError(w, http.StatusBadRequest, "connection failed: "+err.Error())
		return
	}
	defer client.Close()
	raw, remote, err := client.FetchRaw(r.Context(), folder, uid)
	if err != nil {
		if errors.Is(err, errExternalIMAPMessageTooLarge) {
			respondError(w, http.StatusRequestEntityTooLarge, "邮件超过单封大小上限，无法在网页打开")
			return
		}
		respondError(w, http.StatusBadRequest, "failed to load remote message")
		return
	}
	stored, attachments, err := a.parseMaildirMessage(raw, account.Username)
	msg := externalRemoteMessageToMailMessage(account, remote, true)
	if err == nil {
		msg.BodyText = stored.BodyText
		msg.BodyHTML = stored.BodyHTML
		msg.Snippet = stored.Snippet
		if len(attachments) > 0 {
			msg.HasAttachments = true
		}
	}
	if parts, err := client.FetchAttachments(r.Context(), folder, uid); err == nil {
		for i := range parts {
			parts[i].MessageID = msg.ID
		}
		msg.Attachments = parts
		msg.HasAttachments = len(parts) > 0
	}
	if len(msg.Attachments) == 0 {
		msg.Attachments = []Attachment{{ID: "raw", MessageID: msg.ID, Filename: safeExternalEMLFilename(msg.Subject), ContentType: "message/rfc822", SizeBytes: int64(len(raw)), CreatedAt: a.now().UTC()}}
	}
	respondJSON(w, http.StatusOK, msg)
}

func (a *App) handleExternalIMAPAttachment(w http.ResponseWriter, r *http.Request) {
	account, ok := a.externalIMAPAccountForMailRequest(w, r)
	if !ok {
		return
	}
	partID := chi.URLParam(r, "partId")
	if account.StorageMode == externalIMAPStorageLocal {
		messageID := chi.URLParam(r, "remoteId")
		var exists int
		if err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM external_imap_messages em JOIN messages m ON m.id=em.local_message_id
			WHERE em.account_id=? AND em.local_message_id=? AND m.mailbox_id=? LIMIT 1`, account.ID, messageID, account.MailboxID).Scan(&exists); err != nil {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		if partID != "raw" {
			var filename, contentType, path string
			var size int64
			if err := a.db.QueryRowContext(r.Context(), `SELECT att.filename,att.content_type,att.storage_path,att.size_bytes
				FROM attachments att JOIN external_imap_messages em ON em.local_message_id=att.message_id
				WHERE att.id=? AND em.account_id=? AND em.local_message_id=?`, partID, account.ID, messageID).Scan(&filename, &contentType, &path, &size); err != nil {
				respondError(w, http.StatusNotFound, "attachment not found")
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				respondError(w, http.StatusNotFound, "attachment file missing")
				return
			}
			writeAttachmentHeaders(w, contentType, filename, size)
			_, _ = w.Write(data)
			return
		}
		var rawPath, subject string
		if err := a.db.QueryRowContext(r.Context(), `SELECT m.raw_path,m.subject FROM messages m
			JOIN external_imap_messages em ON em.local_message_id=m.id
			WHERE m.id=? AND m.mailbox_id=? AND em.account_id=?`, messageID, account.MailboxID, account.ID).Scan(&rawPath, &subject); err != nil {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		raw, err := os.ReadFile(rawPath)
		if err != nil {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		writeAttachmentHeaders(w, "message/rfc822", safeExternalEMLFilename(subject), int64(len(raw)))
		_, _ = w.Write(raw)
		return
	}
	folder, uid, ok := decodeExternalRemoteID(w, chi.URLParam(r, "remoteId"))
	if !ok {
		return
	}
	client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
	if err != nil {
		respondError(w, http.StatusBadRequest, "connection failed: "+err.Error())
		return
	}
	defer client.Close()
	if partID != "raw" {
		data, att, err := client.FetchPart(r.Context(), folder, uid, partID)
		if err != nil {
			if errors.Is(err, errExternalIMAPMessageTooLarge) {
				respondError(w, http.StatusRequestEntityTooLarge, "附件超过单个大小上限")
				return
			}
			respondError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeAttachmentHeaders(w, att.ContentType, att.Filename, int64(len(data)))
		_, _ = w.Write(data)
		return
	}
	raw, remote, err := client.FetchRaw(r.Context(), folder, uid)
	if err != nil {
		if errors.Is(err, errExternalIMAPMessageTooLarge) {
			respondError(w, http.StatusRequestEntityTooLarge, "邮件超过单封大小上限")
			return
		}
		respondError(w, http.StatusBadRequest, "failed to load remote message")
		return
	}
	writeAttachmentHeaders(w, "message/rfc822", safeExternalEMLFilename(remote.Subject), int64(len(raw)))
	_, _ = w.Write(raw)
}

func (a *App) handleExternalIMAPMarkRead(w http.ResponseWriter, r *http.Request) {
	account, ok := a.externalIMAPAccountForMailRequest(w, r)
	if !ok {
		return
	}
	if account.StorageMode != externalIMAPStorageLocal && !account.SyncReadState {
		badRequest(w, errors.New("read state sync is disabled"))
		return
	}
	var req struct {
		Read *bool `json:"read"`
	}
	_ = decodeJSON(r, &req)
	read := true
	if req.Read != nil {
		read = *req.Read
	}
	if account.StorageMode == externalIMAPStorageLocal {
		messageID := chi.URLParam(r, "remoteId")
		var folder string
		var uid uint32
		if err := a.db.QueryRowContext(r.Context(), `SELECT em.remote_folder,em.uid FROM external_imap_messages em JOIN messages m ON m.id=em.local_message_id
			WHERE em.account_id=? AND em.local_message_id=? AND m.mailbox_id=? LIMIT 1`, account.ID, messageID, account.MailboxID).Scan(&folder, &uid); err != nil {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		msg, err := a.loadMessageForRequest(r, messageID, false)
		if err != nil || msg.MailboxID != account.MailboxID {
			respondError(w, http.StatusNotFound, "message not found")
			return
		}
		if err := a.updateMessageMaildirFlags(r.Context(), msg.ID, &read, nil); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to update message")
			return
		}
		modSeq, err := a.updateMessageModSeq(r.Context(), msg.ID, msg.FolderID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to update message")
			return
		}
		if _, err := a.db.ExecContext(r.Context(), `UPDATE messages SET is_read=?, imap_modseq=CASE WHEN ? > 0 THEN ? ELSE imap_modseq END,updated_at=? WHERE id=?`, boolInt(read), modSeq, modSeq, a.now().UTC().Format(time.RFC3339Nano), msg.ID); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to update message")
			return
		}
		remoteSynced := false
		if account.SyncReadState {
			client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
			if err == nil {
				if err = client.SetRead(r.Context(), folder, uid, read); err == nil {
					remoteSynced = true
				}
				_ = client.Close()
			}
			if err != nil {
				a.log.Warn("failed to sync external imap read state", "account", account.ID, "error", err)
			}
		}
		respondJSON(w, http.StatusOK, map[string]any{"ok": true, "read": read, "remoteSynced": remoteSynced})
		return
	}
	folder, uid, ok := decodeExternalRemoteID(w, chi.URLParam(r, "remoteId"))
	if !ok {
		return
	}
	client, err := a.externalIMAP.openExternalIMAPClient(r.Context(), account)
	if err != nil {
		respondError(w, http.StatusBadRequest, "connection failed: "+err.Error())
		return
	}
	defer client.Close()
	if err := client.SetRead(r.Context(), folder, uid, read); err != nil {
		respondError(w, http.StatusBadRequest, "failed to update remote message")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "read": read})
}

func (a *App) normalizeExternalIMAPPayload(ctx context.Context, req externalIMAPPayload, create bool) (externalIMAPPayload, error) {
	req.Name = strings.Join(strings.Fields(req.Name), " ")
	if req.Name == "" {
		req.Name = req.Username
	}
	if req.Name == "" {
		req.Name = req.Host
	}
	if len([]rune(req.Name)) > 80 {
		return req, errors.New("name is too long")
	}
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	if req.Host == "" {
		return req, errors.New("host is required")
	}
	if err := a.validateExternalIMAPHost(ctx, req.Host); err != nil {
		return req, err
	}
	req.TLSMode = strings.ToLower(strings.TrimSpace(req.TLSMode))
	if req.TLSMode == "" {
		req.TLSMode = externalIMAPTLS
	}
	if req.TLSMode != externalIMAPTLS && req.TLSMode != externalIMAPStartTLS && req.TLSMode != externalIMAPPlain {
		return req, errors.New("invalid TLS mode")
	}
	// 明文 IMAP 等于把邮箱口令和整箱邮件交给同一网段：只允许回环地址
	// （测试用），自建服务器请用 TLS 或 STARTTLS，确需放开时显式设
	// LANQIN_EXTERNAL_IMAP_ALLOW_INSECURE=true。
	if req.TLSMode == externalIMAPPlain && !a.plaintextIMAPAllowed(req.Host) {
		return req, errors.New("plaintext IMAP is only allowed for loopback hosts; use TLS or STARTTLS")
	}
	if req.Port <= 0 {
		if req.TLSMode == externalIMAPTLS {
			req.Port = 993
		} else {
			req.Port = 143
		}
	}
	if req.Port <= 0 || req.Port > 65535 {
		return req, errors.New("invalid port")
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		return req, errors.New("username is required")
	}
	req.StorageMode = strings.ToLower(strings.TrimSpace(req.StorageMode))
	if req.StorageMode == "" {
		req.StorageMode = externalIMAPStorageLocal
	}
	if req.StorageMode != externalIMAPStorageLocal && req.StorageMode != externalIMAPStorageRemote {
		return req, errors.New("invalid storage mode")
	}
	if req.SyncReadState == nil {
		v := true
		req.SyncReadState = &v
	}
	if req.Enabled == nil {
		v := true
		req.Enabled = &v
	}
	return req, nil
}

func (a *App) validateExternalIMAPHost(ctx context.Context, host string) error {
	if a.config().ExternalIMAPAllowPrivateHosts {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return errors.New("localhost is not allowed")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			return fmt.Errorf("failed to resolve host: %w", err)
		}
	}
	for _, ip := range ips {
		if !isPublicUnicastIP(ip) {
			return errors.New("private or local IMAP hosts are not allowed")
		}
	}
	return nil
}

// externalIMAPDialer returns a dialer that re-checks the destination after DNS
// resolution.
//
// validateExternalIMAPHost resolves the hostname itself, so on its own it leaves
// a DNS rebinding window: a hostile resolver can answer with a public address
// for the check and a private one for the connection that follows. Dialer.Control
// runs once per connection attempt with the address actually being dialed, which
// is the only point where the check cannot be raced.
func (a *App) externalIMAPDialer() *net.Dialer {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if a.config().ExternalIMAPAllowPrivateHosts {
		return dialer
	}
	dialer.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if !isPublicUnicastIP(net.ParseIP(host)) {
			return errors.New("private or local IMAP hosts are not allowed")
		}
		return nil
	}
	return dialer
}

func (a *App) encryptExternalIMAPPassword(password string) (string, error) {
	key, err := a.externalIMAPKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(nonce, gcm.Seal(nil, nonce, []byte(password), nil)...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (a *App) decryptExternalIMAPPassword(ciphertext string) (string, error) {
	key, err := a.externalIMAPKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted password")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (a *App) externalIMAPKey() ([]byte, error) {
	secret := strings.TrimSpace(a.config().ExternalIMAPSecretKey)
	if secret == "" {
		return nil, errors.New("LANQIN_EXTERNAL_IMAP_SECRET_KEY is required")
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:], nil
}

type externalIMAPOAuthProvider struct {
	Name string
	Host string
	Port int
}

func (a *App) externalIMAPOAuthConfig(provider string) (*oauth2.Config, externalIMAPOAuthProvider, error) {
	callback := strings.TrimRight(a.config().PublicBaseURL, "/") + "/api/external-imap-oauth/" + provider + "/callback"
	switch provider {
	case externalIMAPOAuthGmail:
		if a.config().ExternalIMAPGmailClientID == "" || a.config().ExternalIMAPGmailClientSecret == "" {
			return nil, externalIMAPOAuthProvider{}, errors.New("gmail oauth is not configured")
		}
		return &oauth2.Config{
			ClientID:     a.config().ExternalIMAPGmailClientID,
			ClientSecret: a.config().ExternalIMAPGmailClientSecret,
			RedirectURL:  callback,
			Scopes:       []string{"openid", "email", "profile", "https://mail.google.com/"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL: "https://oauth2.googleapis.com/token",
			},
		}, externalIMAPOAuthProvider{Name: "Gmail", Host: "imap.gmail.com", Port: 993}, nil
	case externalIMAPOAuthOutlook:
		if a.config().ExternalIMAPOutlookClientID == "" || a.config().ExternalIMAPOutlookClientSecret == "" {
			return nil, externalIMAPOAuthProvider{}, errors.New("outlook oauth is not configured")
		}
		return &oauth2.Config{
			ClientID:     a.config().ExternalIMAPOutlookClientID,
			ClientSecret: a.config().ExternalIMAPOutlookClientSecret,
			RedirectURL:  callback,
			Scopes:       []string{"openid", "email", "profile", "offline_access", "https://outlook.office.com/IMAP.AccessAsUser.All"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
				TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			},
		}, externalIMAPOAuthProvider{Name: "Outlook", Host: "outlook.office365.com", Port: 993}, nil
	default:
		return nil, externalIMAPOAuthProvider{}, errors.New("unsupported oauth provider")
	}
}

func externalIMAPOAuthEmail(provider string, token *oauth2.Token) (string, error) {
	if token == nil {
		return "", errors.New("oauth token is missing")
	}
	idToken, _ := token.Extra("id_token").(string)
	claims, err := externalIMAPOIDCClaims(idToken)
	if err != nil {
		return "", err
	}
	fields := []string{"email"}
	if provider == externalIMAPOAuthOutlook {
		fields = []string{"email", "preferred_username", "upn"}
	}
	for _, field := range fields {
		value, _ := claims[field].(string)
		email := normalizeEmail(value)
		if email != "" && strings.Contains(email, "@") {
			return email, nil
		}
	}
	return "", errors.New("oauth provider did not return an email address")
}

func externalIMAPOIDCClaims(idToken string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return nil, errors.New("oauth provider did not return an id token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid oauth id token")
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("invalid oauth id token")
	}
	return claims, nil
}

func (a *App) encryptExternalIMAPOAuthState(state externalIMAPOAuthState) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	ciphertext, err := a.encryptExternalIMAPPassword(string(raw))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString([]byte(ciphertext)), nil
}

func (a *App) decryptExternalIMAPOAuthState(value string) (externalIMAPOAuthState, error) {
	var state externalIMAPOAuthState
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return state, err
	}
	plain, err := a.decryptExternalIMAPPassword(string(raw))
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(plain), &state)
	return state, err
}

func (a *App) externalIMAPAccountForUser(ctx context.Context, userID, id string) (externalIMAPAccountRecord, error) {
	row := a.db.QueryRowContext(ctx, `SELECT id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,auth_mode,oauth_provider,oauth_email,oauth_access_token_ciphertext,oauth_refresh_token_ciphertext,oauth_expiry,storage_mode,sync_read_state,enabled,last_sync_at,last_status,last_error,created_at,updated_at FROM external_imap_accounts WHERE id=? AND user_id=?`, id, userID)
	return scanExternalIMAPAccount(row)
}

func (a *App) externalIMAPAccountForMailRequest(w http.ResponseWriter, r *http.Request) (externalIMAPAccountRecord, bool) {
	user := currentUser(r)
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, chi.URLParam(r, "id"))
	if err != nil || !account.Enabled {
		respondError(w, http.StatusNotFound, "external account not found")
		return externalIMAPAccountRecord{}, false
	}
	return account, true
}

type externalIMAPScanner interface {
	Scan(dest ...any) error
}

func scanExternalIMAPAccount(row externalIMAPScanner) (externalIMAPAccountRecord, error) {
	var item externalIMAPAccountRecord
	var syncRead, enabled int
	var lastSync, oauthExpiry sql.NullString
	var created, updated string
	err := row.Scan(&item.ID, &item.UserID, &item.MailboxID, &item.Name, &item.Host, &item.Port, &item.TLSMode, &item.Username, &item.PasswordCiphertext, &item.AuthMode, &item.OAuthProvider, &item.OAuthEmail, &item.OAuthAccessTokenCiphertext, &item.OAuthRefreshTokenCiphertext, &oauthExpiry, &item.StorageMode, &syncRead, &enabled, &lastSync, &item.LastStatus, &item.LastError, &created, &updated)
	if err != nil {
		return item, err
	}
	if item.AuthMode == "" {
		item.AuthMode = externalIMAPAuthPassword
	}
	item.OAuthConfigured = item.AuthMode == externalIMAPAuthOAuth2 && item.OAuthAccessTokenCiphertext != ""
	item.SyncReadState = syncRead != 0
	item.Enabled = enabled != 0
	if lastSync.Valid && strings.TrimSpace(lastSync.String) != "" {
		t := parseTime(lastSync.String)
		item.LastSyncAt = &t
	}
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (a *App) updateExternalIMAPStatus(ctx context.Context, accountID, status, errText string) {
	_, _ = a.db.ExecContext(ctx, `UPDATE external_imap_accounts SET last_status=?,last_error=?,updated_at=? WHERE id=?`, status, trimExternalIMAPError(errText), a.now().UTC().Format(time.RFC3339Nano), accountID)
}

func (a *App) migrateExternalIMAP(ctx context.Context) error {
	columns := []struct {
		table string
		name  string
		sql   string
	}{
		{"external_imap_sync_runs", "folder", `ALTER TABLE external_imap_sync_runs ADD COLUMN folder TEXT NOT NULL DEFAULT ''`},
		{"external_imap_accounts", "auth_mode", `ALTER TABLE external_imap_accounts ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'password'`},
		{"external_imap_accounts", "oauth_provider", `ALTER TABLE external_imap_accounts ADD COLUMN oauth_provider TEXT NOT NULL DEFAULT ''`},
		{"external_imap_accounts", "oauth_email", `ALTER TABLE external_imap_accounts ADD COLUMN oauth_email TEXT NOT NULL DEFAULT ''`},
		{"external_imap_accounts", "oauth_access_token_ciphertext", `ALTER TABLE external_imap_accounts ADD COLUMN oauth_access_token_ciphertext TEXT NOT NULL DEFAULT ''`},
		{"external_imap_accounts", "oauth_refresh_token_ciphertext", `ALTER TABLE external_imap_accounts ADD COLUMN oauth_refresh_token_ciphertext TEXT NOT NULL DEFAULT ''`},
		{"external_imap_accounts", "oauth_expiry", `ALTER TABLE external_imap_accounts ADD COLUMN oauth_expiry TEXT`},
	}
	for _, column := range columns {
		if err := a.ensureTableColumn(ctx, column.table, column.name, column.sql); err != nil {
			return err
		}
	}
	// 老库升级：隐藏文件夹表后加，建表语句两种驱动都吃 TEXT。
	if _, err := a.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_imap_hidden_folders (
		account_id TEXT NOT NULL,
		remote_folder TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(account_id, remote_folder)
	)`); err != nil {
		return err
	}
	// 用户在本地删掉的外部邮件，记 UID 墓碑：同步游标被重置（自愈）时也不能
	// 把这些信再拉回来。否则「删 50 封 → 下一轮同步全回来」。
	if _, err := a.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_imap_deleted_messages (
		account_id TEXT NOT NULL,
		remote_folder TEXT NOT NULL,
		uid_validity INTEGER NOT NULL DEFAULT 0,
		uid INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		PRIMARY KEY(account_id, remote_folder, uid_validity, uid)
	)`); err != nil {
		return err
	}
	// 用户手动拖出来的文件夹顺序，未记录的排在后面。
	if _, err := a.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_imap_folder_order (
		account_id TEXT NOT NULL,
		remote_folder TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		PRIMARY KEY(account_id, remote_folder)
	)`); err != nil {
		return err
	}
	return nil
}

// externalIMAPHiddenFolderSet 返回某账号被隐藏的远端文件夹集合。
// hiddenExternalLocalFolderIDs 找出被隐藏的远端文件夹对应的本地文件夹 id，
// 本地存储模式下这些文件夹也不该出现在侧栏。
func (a *App) hiddenExternalLocalFolderIDs(ctx context.Context, mailboxID string) map[string]bool {
	out := map[string]bool{}
	rows, err := a.db.QueryContext(ctx, `SELECT s.local_folder_id
		FROM external_imap_hidden_folders h
		JOIN external_imap_accounts acc ON acc.id=h.account_id
		JOIN external_imap_folder_states s ON s.account_id=h.account_id AND s.remote_folder=h.remote_folder
		WHERE acc.mailbox_id=? AND s.local_folder_id<>''`, mailboxID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			out[id] = true
		}
	}
	return out
}

func (a *App) externalIMAPHiddenFolderSet(ctx context.Context, accountID string) map[string]bool {
	out := map[string]bool{}
	rows, err := a.db.QueryContext(ctx, `SELECT remote_folder FROM external_imap_hidden_folders WHERE account_id=?`, accountID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			out[name] = true
		}
	}
	return out
}

func (a *App) replaceExternalIMAPHiddenFolders(ctx context.Context, accountID string, folders []string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM external_imap_hidden_folders WHERE account_id=?`, accountID); err != nil {
		return err
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	seen := map[string]bool{}
	for _, name := range folders {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_imap_hidden_folders(account_id,remote_folder,created_at) VALUES(?,?,?)`, accountID, name, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// externalIMAPLocalFolderID 取某远端文件夹对应的本地镜像文件夹 id。
func (a *App) externalIMAPLocalFolderID(ctx context.Context, accountID, remoteFolder string) string {
	var folderID string
	if err := a.db.QueryRowContext(ctx, `SELECT local_folder_id FROM external_imap_folder_states WHERE account_id=? AND remote_folder=?`, accountID, remoteFolder).Scan(&folderID); err != nil {
		return ""
	}
	return folderID
}

// externalIMAPFolderOrder 返回用户保存的文件夹顺序（按 position 升序）。
func (a *App) externalIMAPFolderOrder(ctx context.Context, accountID string) []string {
	rows, err := a.db.QueryContext(ctx, `SELECT remote_folder FROM external_imap_folder_order WHERE account_id=? ORDER BY position,created_at`, accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// replaceExternalIMAPFolderOrder 覆盖式保存顺序；本地存储模式顺带把镜像文件夹的
// sort_order 按同一顺序重排，侧栏才会跟着变。
func (a *App) replaceExternalIMAPFolderOrder(ctx context.Context, account externalIMAPAccountRecord, folders []string) error {
	normalized := make([]string, 0, len(folders))
	seen := map[string]bool{}
	for _, name := range folders {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		normalized = append(normalized, name)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM external_imap_folder_order WHERE account_id=?`, account.ID); err != nil {
		return err
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	for index, name := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO external_imap_folder_order(account_id,remote_folder,position,created_at) VALUES(?,?,?,?)`, account.ID, name, index, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if account.StorageMode == externalIMAPStorageLocal {
		a.reorderExternalIMAPLocalFolders(ctx, account, normalized)
	}
	return nil
}

// reorderExternalIMAPLocalFolders 只在这些镜像文件夹原本占用的 sort_order 值之间
// 互换，不把外部文件夹整体挪到侧栏最前/最后。
func (a *App) reorderExternalIMAPLocalFolders(ctx context.Context, account externalIMAPAccountRecord, ordered []string) {
	if len(ordered) == 0 {
		return
	}
	rows, err := a.db.QueryContext(ctx, `SELECT fs.remote_folder,f.id,f.sort_order FROM external_imap_folder_states fs
		JOIN folders f ON f.id=fs.local_folder_id WHERE fs.account_id=?`, account.ID)
	if err != nil {
		return
	}
	localByRemote := map[string]string{}
	orders := []int{}
	for rows.Next() {
		var remote, folderID string
		var sortOrder int
		if err := rows.Scan(&remote, &folderID, &sortOrder); err != nil {
			continue
		}
		localByRemote[remote] = folderID
		orders = append(orders, sortOrder)
	}
	rows.Close()
	if len(localByRemote) == 0 {
		return
	}
	sort.Ints(orders)
	for index, remote := range ordered {
		folderID, ok := localByRemote[remote]
		if !ok || index >= len(orders) {
			continue
		}
		_, _ = a.db.ExecContext(ctx, `UPDATE folders SET sort_order=? WHERE id=?`, orders[index], folderID)
	}
}

// handleSetExternalIMAPFolderOrder 保存用户拖出来的文件夹顺序。
func (a *App) handleSetExternalIMAPFolderOrder(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	accountID := chi.URLParam(r, "id")
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, accountID)
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	var req struct {
		Folders []string `json:"folders"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	if len(req.Folders) > 500 {
		badRequest(w, errors.New("too many folders"))
		return
	}
	if err := a.replaceExternalIMAPFolderOrder(r.Context(), account, req.Folders); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save folder order")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "folders": req.Folders})
}

func (a *App) handleSetExternalIMAPHiddenFolders(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	accountID := chi.URLParam(r, "id")
	account, err := a.externalIMAPAccountForUser(r.Context(), user.ID, accountID)
	if err != nil {
		respondError(w, http.StatusNotFound, "external account not found")
		return
	}
	var req struct {
		Folders []string `json:"folders"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	if err := a.replaceExternalIMAPHiddenFolders(r.Context(), account.ID, req.Folders); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save hidden folders")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "hiddenFolders": req.Folders})
}

func (a *App) syncExternalIMAPAccount(ctx context.Context, accountID string) (ExternalIMAPSyncRun, error) {
	account, err := a.externalIMAPAccountByID(ctx, accountID)
	if err != nil {
		return ExternalIMAPSyncRun{}, err
	}
	if account.StorageMode != externalIMAPStorageLocal {
		return ExternalIMAPSyncRun{}, errors.New("account is not configured for local storage")
	}
	a.pruneOrphanExternalIMAPMessages(ctx, account.ID)
	run := ExternalIMAPSyncRun{ID: newID("ximrun"), AccountID: account.ID, Status: "running", StartedAt: a.now().UTC()}
	_, _ = a.db.ExecContext(ctx, `INSERT INTO external_imap_sync_runs(id,account_id,folder,status,started_at) VALUES(?,?,?,?,?)`, run.ID, run.AccountID, run.Folder, run.Status, run.StartedAt.Format(time.RFC3339Nano))
	client, err := a.externalIMAP.openExternalIMAPClient(ctx, account)
	if err != nil {
		return a.finishExternalIMAPRun(ctx, run, "failed", err)
	}
	defer client.Close()
	folders, err := client.ListFolders(ctx)
	if err != nil {
		return a.finishExternalIMAPRun(ctx, run, "failed", err)
	}
	a.saveExternalIMAPFolderSnapshot(ctx, account.ID, folders)
	// 客户端自己带命令级超时（响应 30s、字面量 5min、写 30s），但一次同步可能遍历
	// 几十个文件夹，每轮都在超时内也可能跑很久。这里按 context 的 deadline 在文件夹之间
	// 主动收手，别让 worker 的一个账号卡住整条定时链路。
	deadlineHit := false
	hidden := a.externalIMAPHiddenFolderSet(ctx, account.ID)
	for _, folder := range folders {
		if hidden[folder.Name] {
			// 用户把这个文件夹隐藏了：不导入、不建本地文件夹，省流量也省通知。
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			a.log.Warn("external imap sync stopped early", "account", account.ID, "error", ctxErr)
			deadlineHit = true
			break
		}
		imported, skipped, failed, err := a.syncExternalIMAPFolder(ctx, account, client, folder)
		run.Imported += imported
		run.Skipped += skipped
		run.Failed += failed
		if err != nil {
			run.Failed++
			a.log.Warn("external imap folder sync failed", "account", account.ID, "folder", folder.Name, "error", err)
		}
	}
	status := "ok"
	if run.Failed > 0 || deadlineHit {
		status = "partial"
	}
	return a.finishExternalIMAPRun(ctx, run, status, nil)
}

func (a *App) syncExternalIMAPAccountFolder(ctx context.Context, accountID, folderName string) (ExternalIMAPSyncRun, error) {
	account, err := a.externalIMAPAccountByID(ctx, accountID)
	if err != nil {
		return ExternalIMAPSyncRun{}, err
	}
	if account.StorageMode != externalIMAPStorageLocal {
		return ExternalIMAPSyncRun{}, errors.New("account is not configured for local storage")
	}
	a.pruneOrphanExternalIMAPMessages(ctx, account.ID)
	run := ExternalIMAPSyncRun{ID: newID("ximrun"), AccountID: account.ID, Folder: folderName, Status: "running", StartedAt: a.now().UTC()}
	_, _ = a.db.ExecContext(ctx, `INSERT INTO external_imap_sync_runs(id,account_id,folder,status,started_at) VALUES(?,?,?,?,?)`, run.ID, run.AccountID, run.Folder, run.Status, run.StartedAt.Format(time.RFC3339Nano))
	client, err := a.externalIMAP.openExternalIMAPClient(ctx, account)
	if err != nil {
		return a.finishExternalIMAPRun(ctx, run, "failed", err)
	}
	defer client.Close()
	folders, err := client.ListFolders(ctx)
	if err != nil {
		return a.finishExternalIMAPRun(ctx, run, "failed", err)
	}
	var selected externalIMAPRemoteFolder
	for _, folder := range folders {
		if strings.EqualFold(folder.Name, folderName) {
			selected = folder
			break
		}
	}
	if strings.TrimSpace(selected.Name) == "" {
		return a.finishExternalIMAPRun(ctx, run, "failed", errors.New("remote folder not found"))
	}
	imported, skipped, failed, err := a.syncExternalIMAPFolder(ctx, account, client, selected)
	run.Imported = imported
	run.Skipped = skipped
	run.Failed = failed
	status := "ok"
	if failed > 0 {
		status = "partial"
	}
	if err != nil {
		status = "failed"
	}
	return a.finishExternalIMAPRun(ctx, run, status, err)
}

// externalIMAPDeletedUIDs 返回某文件夹在指定 UIDVALIDITY 下被本地删掉的 UID。
// UIDVALIDITY 变了以后 UID 空间重建，旧墓碑不再适用。
func (a *App) externalIMAPDeletedUIDs(ctx context.Context, accountID, folder string, uidValidity uint32) map[uint32]bool {
	out := map[uint32]bool{}
	if uidValidity == 0 {
		return out
	}
	rows, err := a.db.QueryContext(ctx, `SELECT uid FROM external_imap_deleted_messages
		WHERE account_id=? AND remote_folder=? AND uid_validity=?`, accountID, folder, uidValidity)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err == nil {
			out[uid] = true
		}
	}
	return out
}

// recordExternalIMAPDeletion 在本地彻底删除一封外部导入的邮件时，把它的远端
// UID 记成墓碑并移除映射。映射留着会在下一轮 prune 里被当成孤儿、连带把游标
// 归零，已删的邮件就被重新导入了。本地删除不代表远端删除，墓碑只用于阻止回填。
func (a *App) recordExternalIMAPDeletion(ctx context.Context, messageID string) {
	rows, err := a.db.QueryContext(ctx, `SELECT account_id,remote_folder,uid_validity,uid FROM external_imap_messages WHERE local_message_id=?`, messageID)
	if err != nil {
		a.log.Warn("failed to load external imap mapping for deletion", "message", messageID, "error", err)
		return
	}
	type tombstone struct {
		accountID, folder string
		uidValidity, uid  uint32
	}
	items := []tombstone{}
	for rows.Next() {
		var item tombstone
		if err := rows.Scan(&item.accountID, &item.folder, &item.uidValidity, &item.uid); err == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	if len(items) == 0 {
		return
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	for _, item := range items {
		if _, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO external_imap_deleted_messages(account_id,remote_folder,uid_validity,uid,created_at) VALUES(?,?,?,?,?)`,
			item.accountID, item.folder, item.uidValidity, item.uid, now); err != nil {
			a.log.Warn("failed to record external imap tombstone", "message", messageID, "folder", item.folder, "uid", item.uid, "error", err)
		}
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM external_imap_messages WHERE local_message_id=?`, messageID); err != nil {
		a.log.Warn("failed to drop external imap mapping after deletion", "message", messageID, "error", err)
	}
}

func (a *App) pruneOrphanExternalIMAPMessages(ctx context.Context, accountID string) {
	// 早先版本切换存储模式/换服务器时只删 messages 没删映射，这些 UID 之后
	// 一直被当成「已导入」跳过，外部邮箱在本地模式下永远空白。同步前自愈。
	folderRows, err := a.db.QueryContext(ctx, `SELECT DISTINCT remote_folder FROM external_imap_messages
		WHERE account_id=? AND local_message_id<>'' AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.id=external_imap_messages.local_message_id)`, accountID)
	if err != nil {
		a.log.Warn("failed to inspect orphan external imap mappings", "account", accountID, "error", err)
		return
	}
	orphanFolders := []string{}
	for folderRows.Next() {
		var folder string
		if err := folderRows.Scan(&folder); err == nil && folder != "" {
			orphanFolders = append(orphanFolders, folder)
		}
	}
	folderRows.Close()
	res, err := a.db.ExecContext(ctx, `DELETE FROM external_imap_messages WHERE account_id=? AND local_message_id<>'' AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.id=external_imap_messages.local_message_id)`, accountID)
	if err != nil {
		a.log.Warn("failed to prune orphan external imap mappings", "account", accountID, "error", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return
	}
	// 只把真正有孤儿的文件夹游标归零重扫；以前是整账号重置，一个文件夹出问题
	// 会把所有文件夹都倒回去重拉一遍。用户删掉的邮件已记墓碑，重扫也不会回来。
	for _, folder := range orphanFolders {
		if _, err := a.db.ExecContext(ctx, `UPDATE external_imap_folder_states SET last_uid=0 WHERE account_id=? AND remote_folder=?`, accountID, folder); err != nil {
			a.log.Warn("failed to reset external imap folder cursor after prune", "account", accountID, "folder", folder, "error", err)
		}
	}
	a.log.Info("pruned orphan external imap mappings", "account", accountID, "rows", n, "folders", len(orphanFolders))
}

func (a *App) externalIMAPAccountByID(ctx context.Context, id string) (externalIMAPAccountRecord, error) {
	row := a.db.QueryRowContext(ctx, `SELECT id,user_id,mailbox_id,name,host,port,tls_mode,username,password_ciphertext,auth_mode,oauth_provider,oauth_email,oauth_access_token_ciphertext,oauth_refresh_token_ciphertext,oauth_expiry,storage_mode,sync_read_state,enabled,last_sync_at,last_status,last_error,created_at,updated_at FROM external_imap_accounts WHERE id=? AND enabled=1`, id)
	return scanExternalIMAPAccount(row)
}

func (a *App) syncExternalIMAPFolder(ctx context.Context, account externalIMAPAccountRecord, client externalIMAPClient, folder externalIMAPRemoteFolder) (int, int, int, error) {
	state := a.loadExternalIMAPFolderState(ctx, account.ID, folder.Name)
	localFolderName := a.externalIMAPLocalFolderName(ctx, account, folder.Name)
	localFolderID := state.LocalFolderID
	if localFolderID != "" {
		// 已有映射的文件夹就地改名（旧命名是 External <键> <哈希>），
		// 不要新建一个空文件夹再把几百封邮件搬过去。
		if _, err := a.db.ExecContext(ctx, `UPDATE folders SET name=? WHERE id=? AND mailbox_id=? AND name<>?`,
			localFolderName, localFolderID, account.MailboxID, localFolderName); err != nil {
			a.log.Warn("failed to rename external imap local folder", "folder", localFolderID, "name", localFolderName, "error", err)
		}
		var exists string
		if a.db.QueryRowContext(ctx, `SELECT id FROM folders WHERE id=? AND mailbox_id=?`, localFolderID, account.MailboxID).Scan(&exists) != nil {
			localFolderID = ""
		}
	}
	if localFolderID == "" {
		var err error
		localFolderID, err = a.ensureFolder(ctx, account.MailboxID, localFolderName)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	imported, skipped, failed := 0, 0, 0
	lastUID := state.LastUID
	uidValidityChanged := state.UIDValidity != 0 && folder.UIDValidity != 0 && state.UIDValidity != folder.UIDValidity
	if uidValidityChanged {
		// 远端重建了 UID 空间（迁移/重建索引）。旧的 last_uid 在新空间里没有意义，
		// 继续用它会导致「新邮件 UID 比旧值小」而被永久跳过。从头扫一遍，
		// 重复的邮件靠 Message-ID 去重。
		a.log.Info("external imap uidvalidity changed, rescanning folder",
			"account", account.ID, "folder", folder.Name,
			"old", state.UIDValidity, "new", folder.UIDValidity)
		lastUID = 0
	}
	processed := 0
	deletedUIDs := a.externalIMAPDeletedUIDs(ctx, account.ID, folder.Name, folder.UIDValidity)
	now := a.now().UTC().Format(time.RFC3339Nano)
	for processed < externalIMAPSyncMaxPerRun {
		batchLimit := min(externalIMAPSyncBatchSize, externalIMAPSyncMaxPerRun-processed)
		remote, err := client.FetchNew(ctx, folder.Name, lastUID, batchLimit)
		if err != nil {
			if processed == 0 {
				return 0, 0, 0, err
			}
			failed++
			break
		}
		if len(remote) == 0 {
			break
		}
		stop := false
		for _, item := range remote {
			// 本地删过的不再导回来；UIDVALIDITY 换了的话墓碑不适用（map 为空）。
			if deletedUIDs[item.UID] {
				skipped++
				lastUID = item.UID
				processed++
				continue
			}
			if a.externalIMAPRemoteMessageExists(ctx, account.ID, folder.Name, item.UIDValidity, item.UID) {
				skipped++
				lastUID = item.UID
				processed++
				continue
			}
			// 外部 IMAP 的整封信当前只能整封读进内存（BODY[] 落在 []byte）。
			// 超大附件会把进程内存顶穿，连带 SMTP/IMAP/网页全挂；这里先按
			// RFC822.SIZE 拦住，宁可跳过这一封并留日志，也不要整个服务 OOM。
			if limit := a.externalIMAPMaxMessageBytes(); limit > 0 && item.SizeBytes > limit {
				a.log.Warn("external imap message exceeds size limit, skipped",
					"account", account.ID, "folder", folder.Name, "uid", item.UID,
					"size_bytes", item.SizeBytes, "limit_bytes", limit)
				skipped++
				lastUID = item.UID
				processed++
				continue
			}
			raw := item.Raw
			if len(raw) == 0 {
				var fetched externalIMAPRemoteMessage
				raw, fetched, err = client.FetchRaw(ctx, folder.Name, item.UID)
				if err == nil {
					item = fetched
				}
			}
			if err != nil {
				// 单封取不下来（远端异常、超过大小上限）不能卡住整个文件夹：
				// 以前这里 break 且不推进 last_uid，下一轮从同一 UID 重来，
				// 后面所有邮件永远同步不到。
				failed++
				a.log.Warn("failed to fetch external imap message, skipped", "account", account.ID, "folder", folder.Name, "uid", item.UID, "error", err)
				lastUID = item.UID
				processed++
				continue
			}
			stored, attachments, err := a.parseMaildirMessage(raw, account.Username)
			if err != nil {
				failed++
				a.log.Warn("failed to parse external imap message, skipped", "account", account.ID, "folder", folder.Name, "uid", item.UID, "error", err)
				lastUID = item.UID
				processed++
				continue
			}
			stored.MailboxID = account.MailboxID
			stored.FolderID = localFolderID
			stored.RecipientAddr = account.Username
			// 排序用 IMAP 的 InternalDate（真实到达时间），不要用发件人可伪造的 Date 头。
			if !item.ReceivedAt.IsZero() {
				stored.ReceivedAt = item.ReceivedAt
			}
			if stored.MessageID == "" && item.MessageID != "" {
				stored.MessageID = item.MessageID
			}
			if strings.TrimSpace(stored.Subject) == "" && strings.TrimSpace(item.Subject) != "" {
				stored.Subject = item.Subject
			}
			stored.IsRead = item.IsRead
			msgID, err := a.insertExternalIMAPMessageOnce(ctx, account, folder.Name, item, stored, attachments, uidValidityChanged, externalIMAPFolderIsAllMail(folder))
			if err != nil {
				failed++
				if errors.Is(err, errMailboxQuotaExceeded) {
					// 配额是用户可恢复的：不要吞邮件，停下来等用户清理。
					stop = true
					break
				}
				a.log.Warn("failed to import external imap message, skipped", "account", account.ID, "folder", folder.Name, "uid", item.UID, "error", err)
				lastUID = item.UID
				processed++
				continue
			}
			if msgID != "" {
				if err := a.writeStoredMessageToMaildir(ctx, msgID, stored, attachments); err != nil {
					a.log.Warn("failed to write external imap message to maildir", "message", msgID, "error", err)
				}
				if strings.EqualFold(folder.Name, "INBOX") {
					a.applyInboundControls(ctx, msgID, account.MailboxID, stored.From, stored.Subject)
				}
				imported++
			} else {
				skipped++
			}
			lastUID = item.UID
			processed++
		}
		if stop || len(remote) < batchLimit {
			break
		}
	}
	// UIDVALIDITY 变了就不能再对 last_uid 取 max：新空间里的 UID 可能更小，
	// 取 max 会把游标永久钉在旧值上，之后的邮件全部漏掉。
	maxLastUID := scalarMaxSQL(databaseDriverSQLite, "last_uid", "excluded.last_uid")
	query := upsertSQL(databaseDriverSQLite, `INSERT INTO external_imap_folder_states(account_id,remote_folder,local_folder_id,uid_validity,last_uid,last_sync_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, `(account_id,remote_folder)`,
		`local_folder_id=excluded.local_folder_id,uid_validity=excluded.uid_validity,last_uid=CASE WHEN external_imap_folder_states.uid_validity=excluded.uid_validity THEN `+maxLastUID+` ELSE excluded.last_uid END,last_sync_at=excluded.last_sync_at,updated_at=excluded.updated_at`,
		`local_folder_id=VALUES(local_folder_id),uid_validity=VALUES(uid_validity),last_uid=CASE WHEN uid_validity=VALUES(uid_validity) THEN GREATEST(last_uid,VALUES(last_uid)) ELSE VALUES(last_uid) END,last_sync_at=VALUES(last_sync_at),updated_at=VALUES(updated_at)`)
	_, _ = a.db.ExecContext(ctx, query,
		account.ID, folder.Name, localFolderID, folder.UIDValidity, lastUID, now, now, now)
	return imported, skipped, failed, ctx.Err()
}

type externalIMAPFolderState struct {
	LocalFolderID string
	UIDValidity   uint32
	LastUID       uint32
}

func (a *App) loadExternalIMAPFolderState(ctx context.Context, accountID, folder string) externalIMAPFolderState {
	var state externalIMAPFolderState
	_ = a.db.QueryRowContext(ctx, `SELECT local_folder_id,uid_validity,last_uid FROM external_imap_folder_states WHERE account_id=? AND remote_folder=?`, accountID, folder).Scan(&state.LocalFolderID, &state.UIDValidity, &state.LastUID)
	return state
}

// externalIMAPFolderDisplayName 去掉 Gmail 的层级前缀，并把常见的英文系统
// 文件夹名换成中文，避免侧栏出现「[Gmail]/已删除邮件」这种夹杂层级路径的名字。
func externalIMAPFolderDisplayName(remoteFolder string) string {
	name := strings.TrimSpace(remoteFolder)
	for _, prefix := range []string{"[Gmail]/", "[Google Mail]/"} {
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			name = strings.TrimSpace(name[len(prefix):])
			break
		}
	}
	// 逐段翻译：Outlook 返回的是 INBOX/Junk/Sent/Trash/Deleted/Outbox/Notes
	// 这类英文系统名，Gmail 中文界面则已经给中文；自定义子文件夹保持原样。
	segments := strings.Split(name, "/")
	for i, segment := range segments {
		if label, ok := externalIMAPSystemFolderNames[strings.ToLower(strings.TrimSpace(segment))]; ok {
			segments[i] = label
		}
	}
	return strings.Join(segments, "/")
}

// externalIMAPSystemFolderNames 各家 IMAP 服务端对系统文件夹的叫法不统一，
// 统一翻成中文，免得设置页里出现 Junk/Sent/Trash 混排。
var externalIMAPSystemFolderNames = map[string]string{
	"inbox":                  "收件箱",
	"all mail":               "所有邮件",
	"sent":                   "已发送",
	"sent mail":              "已发送",
	"sent messages":          "已发送",
	"sent items":             "已发送",
	"drafts":                 "草稿",
	"draft":                  "草稿",
	"spam":                   "垃圾邮件",
	"junk":                   "垃圾邮件",
	"junk e-mail":            "垃圾邮件",
	"junk email":             "垃圾邮件",
	"trash":                  "已删除邮件",
	"deleted":                "已删除邮件",
	"deleted items":          "已删除邮件",
	"deleted messages":       "已删除邮件",
	"bin":                    "已删除邮件",
	"outbox":                 "发件箱",
	"notes":                  "备忘录",
	"starred":                "已加星标",
	"important":              "重要",
	"archive":                "归档",
	"journal":                "日记",
	"conversation history":   "会话历史",
	"synchronization issues": "同步问题",
	"sync issues":            "同步问题",
	"conflicts":              "冲突",
	"server failures":        "服务器故障",
	"local failures":         "本地故障",
}

// externalIMAPLocalFolderName 起一个能认出来的本地文件夹名。
// 以前是「External <账号键> <远端名哈希>」，界面上完全看不出对应哪个文件夹。
// 现在带上账号名与远端名；两个账号撞名时补 4 位账号键，保证 UNIQUE(mailbox,name)。
func (a *App) externalIMAPLocalFolderName(ctx context.Context, account externalIMAPAccountRecord, remoteFolder string) string {
	label := strings.TrimSpace(account.Name)
	if label == "" {
		label = strings.TrimSpace(account.Username)
	}
	if label == "" {
		label = strings.TrimPrefix(account.ID, "ximap_")
		if len(label) > 8 {
			label = label[:8]
		}
	}
	base := fmt.Sprintf("外部 · %s · %s", label, externalIMAPFolderDisplayName(remoteFolder))
	var owner string
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE(fs.account_id,'') FROM folders f
		LEFT JOIN external_imap_folder_states fs ON fs.local_folder_id=f.id
		WHERE f.mailbox_id=? AND lower(f.name)=lower(?)`, account.MailboxID, base).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (owner == "" || owner == account.ID)) {
		return base
	}
	key := strings.TrimPrefix(account.ID, "ximap_")
	if len(key) > 4 {
		key = key[:4]
	}
	return fmt.Sprintf("外部 · %s（%s） · %s", label, key, externalIMAPFolderDisplayName(remoteFolder))
}

func (a *App) moveExternalIMAPMessagesToFolder(ctx context.Context, accountID, mailboxID, remoteFolder, targetFolderID string) error {
	rows, err := a.db.QueryContext(ctx, `SELECT m.id FROM messages m JOIN external_imap_messages em ON em.local_message_id=m.id
		WHERE em.account_id=? AND em.remote_folder=? AND m.mailbox_id=? AND m.folder_id<>?`, accountID, remoteFolder, mailboxID, targetFolderID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := a.moveMessageMaildir(ctx, id, targetFolderID); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) externalIMAPRemoteMessageExists(ctx context.Context, accountID, folder string, uidValidity, uid uint32) bool {
	var exists int
	_ = a.db.QueryRowContext(ctx, `SELECT 1 FROM external_imap_messages WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`, accountID, folder, uidValidity, uid).Scan(&exists)
	return exists == 1
}

func (a *App) insertExternalIMAPMessageOnce(ctx context.Context, account externalIMAPAccountRecord, folder string, item externalIMAPRemoteMessage, msg storedMessage, attachments []AttachmentInput, dedupeByMessageID, dedupeAnyCopy bool) (string, error) {
	now := a.now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	query := insertIgnoreSQL(databaseDriverSQLite, `INSERT INTO external_imap_messages(account_id,remote_folder,uid_validity,uid,message_id,is_read,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, `(account_id,remote_folder,uid_validity,uid)`)
	res, err := tx.ExecContext(ctx, query, account.ID, folder, item.UIDValidity, item.UID, msg.MessageID, boolInt(item.IsRead), now, now)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", tx.Commit()
	}
	if msg.MessageID != "" {
		// Gmail 的标签和「所有邮件」是同一封信的不同视图，第一次同步时哪个
		// 文件夹先跑不确定：只给「所有邮件」单向去重时，反方向（INBOX 后跑）
		// 仍会再插一份。这里改成账号内跨文件夹双向认：只要这封信已经落在本
		// 账号的别的远端文件夹里，就复用那一行，只补映射。
		var existingID string
		err := tx.QueryRowContext(ctx, `SELECT m.id FROM messages m
			JOIN external_imap_messages em ON em.local_message_id=m.id
			WHERE em.account_id=? AND em.remote_folder<>? AND m.message_id=? LIMIT 1`,
			account.ID, folder, msg.MessageID).Scan(&existingID)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `UPDATE external_imap_messages SET local_message_id=?,updated_at=? WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`,
				existingID, now, account.ID, folder, item.UIDValidity, item.UID); err != nil {
				return "", err
			}
			return "", tx.Commit()
		case !errors.Is(err, sql.ErrNoRows):
			return "", err
		}
		if dedupeAnyCopy {
			// 「所有邮件」再兜一层：早期版本导入的副本可能没有映射行（切换过
			// 存储模式、手工清理过），只按映射找不到它们。
			var anyID string
			err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE mailbox_id=? AND message_id=? ORDER BY received_at DESC, id LIMIT 1`,
				msg.MailboxID, msg.MessageID).Scan(&anyID)
			switch {
			case err == nil:
				if _, err := tx.ExecContext(ctx, `UPDATE external_imap_messages SET local_message_id=?,updated_at=? WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`,
					anyID, now, account.ID, folder, item.UIDValidity, item.UID); err != nil {
					return "", err
				}
				return "", tx.Commit()
			case !errors.Is(err, sql.ErrNoRows):
				return "", err
			}
		}
		if dedupeByMessageID {
			// UIDVALIDITY 重置后的重扫只在同文件夹内去重：同一文件夹里两封不同
			// 邮件复用同一个 Message-ID（营销邮件、坏客户端）不能吞掉一封。
			var sameFolderID string
			err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE mailbox_id=? AND folder_id=? AND message_id=? LIMIT 1`,
				msg.MailboxID, msg.FolderID, msg.MessageID).Scan(&sameFolderID)
			switch {
			case err == nil:
				if _, err := tx.ExecContext(ctx, `UPDATE external_imap_messages SET local_message_id=?,updated_at=? WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`,
					sameFolderID, now, account.ID, folder, item.UIDValidity, item.UID); err != nil {
					return "", err
				}
				return "", tx.Commit()
			case !errors.Is(err, sql.ErrNoRows):
				return "", err
			}
		}
	}
	id, err := a.insertMessageWithDB(ctx, tx, msg, attachments)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE external_imap_messages SET local_message_id=?,updated_at=? WHERE account_id=? AND remote_folder=? AND uid_validity=? AND uid=?`, id, now, account.ID, folder, item.UIDValidity, item.UID)
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (a *App) finishExternalIMAPRun(ctx context.Context, run ExternalIMAPSyncRun, status string, err error) (ExternalIMAPSyncRun, error) {
	// 收尾写库不能用会被取消的 ctx：关机/超时把同步打断后，这些 UPDATE 会
	// 全部失败，同步记录永远停在 running，账号的 last_status 也不再更新。
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = finishCtx
	run.Status = status
	if err != nil {
		run.Error = trimExternalIMAPError(err.Error())
	}
	finished := a.now().UTC()
	run.FinishedAt = &finished
	_, _ = a.db.ExecContext(ctx, `UPDATE external_imap_sync_runs SET status=?,imported=?,skipped=?,failed=?,error=?,finished_at=? WHERE id=?`,
		run.Status, run.Imported, run.Skipped, run.Failed, run.Error, finished.Format(time.RFC3339Nano), run.ID)
	lastStatus := status
	if status == "failed" {
		lastStatus = "error"
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE external_imap_accounts SET last_sync_at=?,last_status=?,last_error=?,updated_at=? WHERE id=?`,
		finished.Format(time.RFC3339Nano), lastStatus, run.Error, finished.Format(time.RFC3339Nano), run.AccountID)
	if run.Imported > 0 {
		// The account names the mailbox the messages landed in, so only that mailbox's
		// audience needs waking rather than every open stream.
		var mailboxID string
		if scanErr := a.db.QueryRowContext(ctx, `SELECT mailbox_id FROM external_imap_accounts WHERE id=?`, run.AccountID).Scan(&mailboxID); scanErr != nil {
			a.log.Warn("failed to resolve external IMAP sync audience", "account", run.AccountID, "error", scanErr)
			a.publishSyncEventToAll()
		} else {
			a.publishSyncEventForMailboxes(ctx, []string{mailboxID})
		}
	}
	return run, err
}

func scanExternalIMAPSyncRun(row externalIMAPScanner) (ExternalIMAPSyncRun, error) {
	var run ExternalIMAPSyncRun
	var started, finished sql.NullString
	if err := row.Scan(&run.ID, &run.AccountID, &run.Folder, &run.Status, &run.Imported, &run.Skipped, &run.Failed, &run.Error, &started, &finished); err != nil {
		return run, err
	}
	if started.Valid && strings.TrimSpace(started.String) != "" {
		run.StartedAt = parseTime(started.String)
	}
	if finished.Valid && strings.TrimSpace(finished.String) != "" {
		t := parseTime(finished.String)
		run.FinishedAt = &t
	}
	return run, nil
}

func trimExternalIMAPError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func (a *App) plaintextIMAPAllowed(host string) bool {
	return a.config().ExternalIMAPAllowInsecure || isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeExternalIMAPFolderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "inbox":
		return "Inbox"
	case "sent", "sent items", "sent messages":
		return "Sent"
	case "drafts", "draft":
		return "Drafts"
	case "archive", "archives":
		return "Archive"
	case "spam", "junk", "junk email":
		return "Spam"
	case "trash", "deleted", "deleted items":
		return "Trash"
	default:
		folder, err := normalizeCustomFolderName(name)
		if err != nil {
			return "Imported"
		}
		return folder
	}
}

func encodeExternalRemoteID(folder string, uid uint32) string {
	return base64.RawURLEncoding.EncodeToString([]byte(folder + "\x00" + strconv.FormatUint(uint64(uid), 10)))
}

func decodeExternalRemoteID(w http.ResponseWriter, raw string) (string, uint32, bool) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid remote id")
		return "", 0, false
	}
	parts := strings.SplitN(string(data), "\x00", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		respondError(w, http.StatusBadRequest, "invalid remote id")
		return "", 0, false
	}
	uid, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || uid == 0 {
		respondError(w, http.StatusBadRequest, "invalid remote id")
		return "", 0, false
	}
	return parts[0], uint32(uid), true
}

func externalRemoteMessageToMailMessage(account externalIMAPAccountRecord, msg externalIMAPRemoteMessage, includeBody bool) MailMessage {
	id := encodeExternalRemoteID(msg.Folder, msg.UID)
	messageID := msg.MessageID
	if messageID != "" && !strings.HasPrefix(messageID, "<") {
		messageID = "<" + messageID + ">"
	}
	return MailMessage{
		ID:                id,
		MailboxID:         account.MailboxID,
		MailboxAddress:    account.Name,
		FolderID:          msg.Folder,
		Folder:            msg.Folder,
		MessageUID:        id,
		IMAPUID:           int64(msg.UID),
		MessageID:         messageID,
		Subject:           decodeMIMEHeader(msg.Subject),
		From:              msg.From,
		FromName:          msg.FromName,
		To:                msg.To,
		CC:                msg.CC,
		SentAt:            msg.SentAt,
		ReceivedAt:        msg.ReceivedAt,
		Snippet:           msg.Snippet,
		IsRead:            msg.IsRead,
		SizeBytes:         msg.SizeBytes,
		HasAttachments:    includeBody,
		ExternalAccountID: account.ID,
	}
}

func safeExternalEMLFilename(subject string) string {
	name := strings.TrimSpace(subject)
	if name == "" {
		name = "message"
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`\/:*?"<>|`, r) {
			return '-'
		}
		return r
	}, name)
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	return name + ".eml"
}

func externalIMAPAttachmentsFromBodyStructure(body imap.BodyStructure) []Attachment {
	parts := externalIMAPAttachmentPartsFromBodyStructure(body)
	items := make([]Attachment, 0, len(parts))
	for _, part := range parts {
		items = append(items, part.Attachment)
	}
	return items
}

func externalIMAPAttachmentPartsFromBodyStructure(body imap.BodyStructure) []externalIMAPAttachmentPart {
	now := time.Now().UTC()
	items := []externalIMAPAttachmentPart{}
	body.Walk(func(path []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		filename := strings.TrimSpace(single.Filename())
		disposition := ""
		if disp := single.Disposition(); disp != nil {
			disposition = strings.ToLower(strings.TrimSpace(disp.Value))
		}
		if filename == "" && disposition != "attachment" {
			return true
		}
		if filename == "" {
			filename = "attachment"
		}
		contentType := single.MediaType()
		if contentType == "/" || contentType == "" {
			contentType = "application/octet-stream"
		}
		items = append(items, externalIMAPAttachmentPart{
			Attachment: Attachment{
				ID:          encodeExternalIMAPPartID(path),
				Filename:    filename,
				ContentType: contentType,
				SizeBytes:   int64(single.Size),
				CreatedAt:   now,
			},
			Encoding: strings.ToLower(strings.TrimSpace(single.Encoding)),
		})
		return true
	})
	return items
}

func encodeExternalIMAPPartID(path []int) string {
	parts := make([]string, 0, len(path))
	for _, n := range path {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ".")
}

func decodeExternalIMAPPartID(value string) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "raw" {
		return nil, errors.New("invalid part id")
	}
	rawParts := strings.Split(value, ".")
	path := make([]int, 0, len(rawParts))
	for _, part := range rawParts {
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, errors.New("invalid part id")
		}
		path = append(path, n)
	}
	return path, nil
}

func decodeExternalIMAPPartData(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		compact := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(string(data))
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(compact)))
		n, err := base64.StdEncoding.Decode(decoded, []byte(compact))
		if err != nil {
			return nil, err
		}
		return decoded[:n], nil
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(data))))
	default:
		return data, nil
	}
}

func (a *App) openExternalIMAPClient(ctx context.Context, account externalIMAPAccountRecord) (externalIMAPClient, error) {
	if err := a.validateExternalIMAPHost(ctx, account.Host); err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(account.Host, strconv.Itoa(account.Port))
	options := &imapclient.Options{
		Dialer:    a.externalIMAPDialer(),
		TLSConfig: &tls.Config{ServerName: account.Host, MinVersion: tls.VersionTLS12},
	}
	var c *imapclient.Client
	var err error
	switch account.TLSMode {
	case externalIMAPTLS:
		c, err = imapclient.DialTLS(addr, options)
	case externalIMAPStartTLS:
		c, err = imapclient.DialStartTLS(addr, options)
	default:
		c, err = imapclient.DialInsecure(addr, options)
	}
	if err != nil {
		return nil, err
	}
	// 腾讯 QQ 邮箱的 IMAP（QQMail XMIMAP4Server）在 greeting 里已经带 CAPABILITY
	// 时不会响应随后的第一个 LOGIN，连接就那样挂着直到超时；必须显式再发一次
	// CAPABILITY 把命令通道打开。其它服务端多这一问也没坏处，失败就继续。
	if _, capErr := c.Capability().Wait(); capErr != nil {
		a.log.Debug("external imap capability probe failed", "host", account.Host, "error", capErr)
	}
	if account.AuthMode == externalIMAPAuthOAuth2 {
		token, err := a.externalIMAPOAuthAccessToken(ctx, account)
		if err != nil {
			c.Close()
			return nil, err
		}
		if err := c.Authenticate(newExternalIMAPXOAUTH2Client(account.Username, token)); err != nil {
			c.Close()
			return nil, fmt.Errorf("oauth xoauth2 authenticate failed: %w", err)
		}
	} else {
		password, err := a.decryptExternalIMAPPassword(account.PasswordCiphertext)
		if err != nil {
			c.Close()
			return nil, err
		}
		if err := c.Login(account.Username, password).Wait(); err != nil {
			c.Close()
			return nil, err
		}
	}
	return &goExternalIMAPClient{client: c, maxMessageBytes: a.externalIMAPMaxMessageBytes()}, nil
}

type externalIMAPXOAUTH2Client struct {
	username string
	token    string
}

func newExternalIMAPXOAUTH2Client(username, token string) externalIMAPXOAUTH2Client {
	return externalIMAPXOAUTH2Client{username: username, token: token}
}

func (c externalIMAPXOAUTH2Client) Start() (string, []byte, error) {
	return "XOAUTH2", externalIMAPXOAUTH2Response(c.username, c.token), nil
}

func (c externalIMAPXOAUTH2Client) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

func externalIMAPXOAUTH2Response(username, token string) []byte {
	return []byte("user=" + username + "\x01auth=Bearer " + token + "\x01\x01")
}

func (a *App) externalIMAPOAuthAccessToken(ctx context.Context, account externalIMAPAccountRecord) (string, error) {
	access, err := a.decryptExternalIMAPPassword(account.OAuthAccessTokenCiphertext)
	if err != nil {
		return "", err
	}
	var expiry time.Time
	var expiryRaw sql.NullString
	_ = a.db.QueryRowContext(ctx, `SELECT oauth_expiry FROM external_imap_accounts WHERE id=?`, account.ID).Scan(&expiryRaw)
	if expiryRaw.Valid && strings.TrimSpace(expiryRaw.String) != "" {
		expiry = parseTime(expiryRaw.String)
	}
	if expiry.IsZero() || expiry.After(a.now().Add(2*time.Minute)) || account.OAuthRefreshTokenCiphertext == "" {
		return access, nil
	}
	refresh, err := a.decryptExternalIMAPPassword(account.OAuthRefreshTokenCiphertext)
	if err != nil {
		return "", err
	}
	conf, _, err := a.externalIMAPOAuthConfig(account.OAuthProvider)
	if err != nil {
		return "", err
	}
	token, err := conf.TokenSource(ctx, &oauth2.Token{AccessToken: access, RefreshToken: refresh, Expiry: expiry}).Token()
	if err != nil {
		return "", err
	}
	accessCipher, err := a.encryptExternalIMAPPassword(token.AccessToken)
	if err != nil {
		return "", err
	}
	refreshCipher := account.OAuthRefreshTokenCiphertext
	if token.RefreshToken != "" && token.RefreshToken != refresh {
		refreshCipher, err = a.encryptExternalIMAPPassword(token.RefreshToken)
		if err != nil {
			return "", err
		}
	}
	expiryText := ""
	if !token.Expiry.IsZero() {
		expiryText = token.Expiry.UTC().Format(time.RFC3339Nano)
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE external_imap_accounts SET oauth_access_token_ciphertext=?,oauth_refresh_token_ciphertext=?,oauth_expiry=?,updated_at=? WHERE id=?`, accessCipher, refreshCipher, expiryText, a.now().UTC().Format(time.RFC3339Nano), account.ID)
	return token.AccessToken, nil
}

type goExternalIMAPClient struct {
	client *imapclient.Client
	// 单封邮件允许整封载入内存的上限。UI 的 FetchRaw/FetchPart 也要走这个闸门：
	// 只给同步循环加限制挡不住「打开一封 2GB 的远端邮件」。
	maxMessageBytes int64
}

func (c *goExternalIMAPClient) Close() error {
	if c.client == nil {
		return nil
	}
	_ = c.client.Logout().Wait()
	return c.client.Close()
}

func (c *goExternalIMAPClient) ListFolders(ctx context.Context) ([]externalIMAPRemoteFolder, error) {
	caps := c.client.Caps()
	var options *imap.ListOptions
	if caps.Has(imap.CapIMAP4rev2) || caps.Has(imap.CapListStatus) {
		options = &imap.ListOptions{ReturnStatus: &imap.StatusOptions{NumMessages: true, NumUnseen: true, UIDValidity: true}}
	}
	list, err := c.client.List("", "*", options).Collect()
	if err != nil && options != nil {
		// Some Outlook/Exchange IMAP deployments advertise extended LIST
		// capabilities but reject LIST RETURN (STATUS ...) with
		// "BAD Command Argument Error. 12". Fall back to a plain LIST and
		// fetch counts with STATUS separately.
		options = nil
		list, err = c.client.List("", "*", nil).Collect()
	}
	if err != nil {
		return nil, err
	}
	folders := []externalIMAPRemoteFolder{}
	for _, item := range list {
		if strings.TrimSpace(item.Mailbox) == "" || mailboxHasNoSelect(item.Attrs) {
			continue
		}
		f := externalIMAPRemoteFolder{Name: item.Mailbox, Role: normalizeExternalIMAPFolderName(item.Mailbox)}
		// Gmail 的 \All（「所有邮件」）把 INBOX/Sent 的邮件再列一遍，
		// 导入时必须跨文件夹按 Message-ID 去重，否则每封本地存两份。
		if mailboxHasAttr(item.Attrs, imap.MailboxAttrAll) || externalIMAPFolderIsAllMail(f) {
			f.Role = externalIMAPFolderRoleAll
		}
		if item.Status != nil {
			if item.Status.NumMessages != nil {
				f.TotalCount = int(*item.Status.NumMessages)
			}
			if item.Status.NumUnseen != nil {
				f.UnreadCount = int(*item.Status.NumUnseen)
			}
			f.UIDValidity = item.Status.UIDValidity
		}
		if strings.EqualFold(item.Mailbox, "INBOX") {
			f.Name = "INBOX"
		}
		folders = append(folders, f)
	}
	if options == nil {
		statusOptions := &imap.StatusOptions{NumMessages: true, NumUnseen: true, UIDValidity: true}
		for i := range folders {
			status, err := c.client.Status(folders[i].Name, statusOptions).Wait()
			if err != nil {
				continue
			}
			if status.NumMessages != nil {
				folders[i].TotalCount = int(*status.NumMessages)
			}
			if status.NumUnseen != nil {
				folders[i].UnreadCount = int(*status.NumUnseen)
			}
			folders[i].UIDValidity = status.UIDValidity
		}
	}
	if len(folders) == 0 {
		folders = append(folders, externalIMAPRemoteFolder{Name: "INBOX", Role: "Inbox"})
	}
	return folders, nil
}

// externalIMAPFolderIsAllMail 判断是不是 Gmail 的「所有邮件」。
// 不能只看 \All：部分服务器（含中文界面的 Gmail）LIST 不带 SPECIAL-USE，
// 只回名字，靠属性判断会退化成普通文件夹，于是每封邮件在本地存两份。
func externalIMAPFolderIsAllMail(folder externalIMAPRemoteFolder) bool {
	if folder.Role == externalIMAPFolderRoleAll {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(externalIMAPFolderDisplayName(folder.Name)))
	switch name {
	case "all mail", "all", "所有邮件", "所有邮件夹", "全部邮件":
		return true
	}
	return false
}

func mailboxHasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	// 属性值是单个反斜杠加名字，必须和库里的常量比；写成原始字符串里的两个
	// 反斜杠会永远匹配不上。
	for _, attr := range attrs {
		if strings.EqualFold(string(attr), string(want)) {
			return true
		}
	}
	return false
}

func mailboxHasNoSelect(attrs []imap.MailboxAttr) bool {
	return mailboxHasAttr(attrs, imap.MailboxAttrNoSelect) || mailboxHasAttr(attrs, imap.MailboxAttrNonExistent)
}

func (c *goExternalIMAPClient) FetchSummaries(ctx context.Context, folder string, query string, cursor string, limit int) ([]externalIMAPRemoteMessage, string, int, error) {
	selected, err := c.client.Select(folder, nil).Wait()
	if err != nil {
		return nil, "", 0, err
	}
	if limit != 20 && limit != 30 && limit != 50 {
		limit = externalIMAPMaxFetch
	}
	if selected.NumMessages == 0 {
		return nil, "", 0, nil
	}
	maxUID := uint32(0)
	if strings.TrimSpace(cursor) != "" {
		parsed, _ := strconv.ParseUint(strings.TrimPrefix(cursor, "uid:"), 10, 32)
		maxUID = uint32(parsed)
	}
	if maxUID == 0 {
		if selected.UIDNext == 0 {
			return nil, "", 0, nil
		}
		maxUID = uint32(selected.UIDNext - 1)
	}
	query = strings.TrimSpace(query)
	totalCount := int(selected.NumMessages)
	if query != "" {
		totalData, err := c.client.UIDSearch(&imap.SearchCriteria{Text: []string{query}}, c.uidSearchAllOptions()).Wait()
		if err != nil {
			return nil, "", 0, err
		}
		totalCount = staticUIDSetCount(totalData.All)
	}
	criteria := &imap.SearchCriteria{}
	var uidRange imap.UIDSet
	uidRange.AddRange(1, imap.UID(maxUID))
	criteria.UID = []imap.UIDSet{uidRange}
	if query != "" {
		criteria.Text = []string{query}
	}
	data, err := c.client.UIDSearch(criteria, c.uidSearchAllOptions()).Wait()
	if err != nil {
		return nil, "", 0, err
	}
	uids := boundedSearchUIDs(data.All, limit+1, true)
	if len(uids) == 0 {
		return nil, "", totalCount, nil
	}
	hasMore := len(uids) > limit
	if hasMore {
		uids = uids[:limit]
	}
	var set imap.UIDSet
	set.AddNum(uids...)
	bodySection := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}
	messages, err := collectFetchBuffersLimited(c.client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{bodySection}}), c.maxMessageBytes)
	if err != nil {
		return nil, "", 0, err
	}
	out := []externalIMAPRemoteMessage{}
	for _, message := range messages {
		out = append(out, fetchBufferToExternalMessage(folder, selected.UIDValidity, message, nil))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID > out[j].UID })
	next := ""
	if hasMore {
		last := uint32(uids[len(uids)-1])
		if last > 1 {
			next = "uid:" + strconv.FormatUint(uint64(last-1), 10)
		}
	}
	return out, next, totalCount, nil
}

func staticUIDSetCount(numSet imap.NumSet) int {
	uidSet, ok := numSet.(imap.UIDSet)
	if !ok || uidSet.Dynamic() {
		return 0
	}
	total := uint64(0)
	for _, uidRange := range uidSet {
		if uidRange.Start == 0 || uidRange.Stop < uidRange.Start {
			continue
		}
		total += uint64(uidRange.Stop-uidRange.Start) + 1
	}
	maxInt := uint64(^uint(0) >> 1)
	if total > maxInt {
		return int(maxInt)
	}
	return int(total)
}

func (c *goExternalIMAPClient) FetchNew(ctx context.Context, folder string, afterUID uint32, limit int) ([]externalIMAPRemoteMessage, error) {
	selected, err := c.client.Select(folder, nil).Wait()
	if err != nil {
		return nil, err
	}
	if afterUID == ^uint32(0) {
		return nil, nil
	}
	if selected.NumMessages == 0 || selected.UIDNext <= imap.UID(afterUID+1) {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var searchRange imap.UIDSet
	searchRange.AddRange(imap.UID(afterUID+1), selected.UIDNext-1)
	data, err := c.client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{searchRange}}, c.uidSearchAllOptions()).Wait()
	if err != nil {
		return nil, err
	}
	uids := boundedSearchUIDs(data.All, limit, false)
	if len(uids) == 0 {
		return nil, nil
	}
	var set imap.UIDSet
	set.AddNum(uids...)
	// 先只拉元数据（含 RFC822Size），再决定哪些信整封下载。
	// 以前这里对整批 100 封直接 BODY.PEEK[]，碰上大附件就是几百 MB 同时驻留。
	meta, err := c.client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true}).Collect()
	if err != nil {
		return nil, err
	}
	inlineUIDs := externalIMAPInlineBodyUIDs(meta, externalIMAPInlineBodyMaxBytes)
	bodies := make(map[imap.UID][]byte, len(meta))
	if staticUIDSetCount(inlineUIDs) > 0 {
		bodySection := &imap.FetchItemBodySection{Peek: true}
		withBody, err := collectFetchBuffersLimited(c.client.Fetch(inlineUIDs, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{bodySection}}), externalIMAPInlineBodyMaxBytes)
		if err != nil {
			return nil, err
		}
		for _, message := range withBody {
			if raw := message.FindBodySection(bodySection); raw != nil {
				bodies[message.UID] = raw
			}
		}
	}
	out := []externalIMAPRemoteMessage{}
	for _, message := range meta {
		// 超过阈值的信这里不带正文，同步循环会用 FetchRaw 一封一封取。
		out = append(out, fetchBufferToExternalMessage(folder, selected.UIDValidity, message, bodies[message.UID]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// externalIMAPInlineBodyUIDs 挑出体积在阈值内的邮件，允许批量连正文一起取。
// 超过阈值的留给逐封 FetchRaw，避免整批大附件把进程内存顶穿。
func externalIMAPInlineBodyUIDs(meta []*imapclient.FetchMessageBuffer, maxBytes int64) imap.UIDSet {
	var set imap.UIDSet
	for _, message := range meta {
		if message == nil {
			continue
		}
		if maxBytes > 0 && message.RFC822Size > maxBytes {
			continue
		}
		set.AddNum(message.UID)
	}
	return set
}

func (c *goExternalIMAPClient) uidSearchAllOptions() *imap.SearchOptions {
	caps := c.client.Caps()
	if caps.Has(imap.CapIMAP4rev2) || caps.Has(imap.CapESearch) {
		return &imap.SearchOptions{ReturnAll: true}
	}
	return nil
}

// boundedSearchUIDs reads only the requested edge of a possibly large,
// range-compressed SEARCH result. SEARCH results are always static UID sets.
func boundedSearchUIDs(numSet imap.NumSet, limit int, newestFirst bool) []imap.UID {
	uidSet, ok := numSet.(imap.UIDSet)
	if !ok || uidSet.Dynamic() || limit <= 0 {
		return nil
	}
	uids := make([]imap.UID, 0, min(limit, len(uidSet)))
	appendRange := func(start, stop imap.UID, descending bool) bool {
		if start == 0 || stop == 0 {
			return false
		}
		if descending {
			for uid := stop; ; uid-- {
				uids = append(uids, uid)
				if len(uids) == limit || uid == start {
					break
				}
			}
		} else {
			for uid := start; ; uid++ {
				uids = append(uids, uid)
				if len(uids) == limit || uid == stop {
					break
				}
			}
		}
		return len(uids) == limit
	}
	if newestFirst {
		for i := len(uidSet) - 1; i >= 0; i-- {
			if appendRange(uidSet[i].Start, uidSet[i].Stop, true) {
				break
			}
		}
	} else {
		for _, uidRange := range uidSet {
			if appendRange(uidRange.Start, uidRange.Stop, false) {
				break
			}
		}
	}
	return uids
}

var errExternalIMAPMessageTooLarge = errors.New("external imap message exceeds size limit")

// collectFetchBuffersLimited 替代 Collect：先看服务器声明的 literal 大小，
// 超过上限立即拒绝，再 LimitReader 读取。恶意 IMAP 服务器可以声明 RFC822.SIZE=1
// 却返回几 GB 的 literal，Collect 会直接 io.ReadAll 把进程打爆。
func collectFetchBuffersLimited(cmd *imapclient.FetchCommand, maxBytes int64) ([]*imapclient.FetchMessageBuffer, error) {
	var out []*imapclient.FetchMessageBuffer
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		buf := &imapclient.FetchMessageBuffer{SeqNum: msg.SeqNum}
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch value := item.(type) {
			case imapclient.FetchItemDataBodySection:
				if value.Literal == nil {
					continue
				}
				if maxBytes > 0 && value.Literal.Size() > maxBytes {
					_ = cmd.Close()
					return nil, errExternalIMAPMessageTooLarge
				}
				reader := io.Reader(value.Literal)
				if maxBytes > 0 {
					reader = io.LimitReader(value.Literal, maxBytes+1)
				}
				data, err := io.ReadAll(reader)
				if err != nil {
					_ = cmd.Close()
					return nil, err
				}
				if maxBytes > 0 && int64(len(data)) > maxBytes {
					_ = cmd.Close()
					return nil, errExternalIMAPMessageTooLarge
				}
				buf.BodySection = append(buf.BodySection, imapclient.FetchBodySectionBuffer{Section: value.Section, Bytes: data})
			case imapclient.FetchItemDataFlags:
				buf.Flags = value.Flags
			case imapclient.FetchItemDataEnvelope:
				buf.Envelope = value.Envelope
			case imapclient.FetchItemDataInternalDate:
				buf.InternalDate = value.Time
			case imapclient.FetchItemDataRFC822Size:
				buf.RFC822Size = value.Size
			case imapclient.FetchItemDataUID:
				buf.UID = value.UID
			case imapclient.FetchItemDataBodyStructure:
				buf.BodyStructure = value.BodyStructure
			}
		}
		out = append(out, buf)
	}
	return out, cmd.Close()
}

func (c *goExternalIMAPClient) FetchRaw(ctx context.Context, folder string, uid uint32) ([]byte, externalIMAPRemoteMessage, error) {
	selected, err := c.client.Select(folder, nil).Wait()
	if err != nil {
		return nil, externalIMAPRemoteMessage{}, err
	}
	if c.maxMessageBytes > 0 {
		// 先只取 RFC822.SIZE，确认体积再用 BODY[]；否则 Collect() 会在返回前
		// 就把整封读进内存，限制写在后面已经来不及。
		metas, err := c.client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
		if err != nil {
			return nil, externalIMAPRemoteMessage{}, err
		}
		if len(metas) == 0 {
			return nil, externalIMAPRemoteMessage{}, sql.ErrNoRows
		}
		if metas[0].RFC822Size > c.maxMessageBytes {
			return nil, externalIMAPRemoteMessage{}, errExternalIMAPMessageTooLarge
		}
	}
	bodySection := &imap.FetchItemBodySection{Peek: true}
	messages, err := collectFetchBuffersLimited(c.client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{bodySection}}), c.maxMessageBytes)
	if err != nil {
		return nil, externalIMAPRemoteMessage{}, err
	}
	if len(messages) == 0 {
		return nil, externalIMAPRemoteMessage{}, sql.ErrNoRows
	}
	raw := messages[0].FindBodySection(bodySection)
	return raw, fetchBufferToExternalMessage(folder, selected.UIDValidity, messages[0], raw), nil
}

func (c *goExternalIMAPClient) FetchAttachments(ctx context.Context, folder string, uid uint32) ([]Attachment, error) {
	parts, err := c.fetchAttachmentParts(ctx, folder, uid)
	if err != nil {
		return nil, err
	}
	out := make([]Attachment, 0, len(parts))
	for _, part := range parts {
		out = append(out, part.Attachment)
	}
	return out, nil
}

func (c *goExternalIMAPClient) fetchAttachmentParts(ctx context.Context, folder string, uid uint32) ([]externalIMAPAttachmentPart, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, err
	}
	messages, err := c.client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 || messages[0].BodyStructure == nil {
		return nil, sql.ErrNoRows
	}
	return externalIMAPAttachmentPartsFromBodyStructure(messages[0].BodyStructure), nil
}

func (c *goExternalIMAPClient) FetchPart(ctx context.Context, folder string, uid uint32, partID string) ([]byte, Attachment, error) {
	path, err := decodeExternalIMAPPartID(partID)
	if err != nil {
		return nil, Attachment{}, err
	}
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, Attachment{}, err
	}
	attachments, err := c.fetchAttachmentParts(ctx, folder, uid)
	if err != nil {
		return nil, Attachment{}, err
	}
	var att externalIMAPAttachmentPart
	for _, item := range attachments {
		if item.ID == partID {
			att = item
			break
		}
	}
	if att.ID == "" {
		return nil, Attachment{}, sql.ErrNoRows
	}
	if c.maxMessageBytes > 0 && att.SizeBytes > c.maxMessageBytes {
		return nil, Attachment{}, errExternalIMAPMessageTooLarge
	}
	bodySection := &imap.FetchItemBodySection{Part: path, Peek: true}
	messages, err := collectFetchBuffersLimited(c.client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{bodySection}}), c.maxMessageBytes)
	if err != nil {
		return nil, Attachment{}, err
	}
	if len(messages) == 0 {
		return nil, Attachment{}, sql.ErrNoRows
	}
	data := messages[0].FindBodySection(bodySection)
	if data == nil {
		return nil, Attachment{}, sql.ErrNoRows
	}
	data, err = decodeExternalIMAPPartData(data, att.Encoding)
	if err != nil {
		return nil, Attachment{}, err
	}
	att.SizeBytes = int64(len(data))
	return data, att.Attachment, nil
}

func (c *goExternalIMAPClient) SetRead(ctx context.Context, folder string, uid uint32, read bool) error {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return err
	}
	op := imap.StoreFlagsDel
	if read {
		op = imap.StoreFlagsAdd
	}
	return c.client.Store(imap.UIDSetNum(imap.UID(uid)), &imap.StoreFlags{Op: op, Flags: []imap.Flag{imap.FlagSeen}, Silent: true}, nil).Close()
}

func fetchBufferToExternalMessage(folder string, uidValidity uint32, msg *imapclient.FetchMessageBuffer, raw []byte) externalIMAPRemoteMessage {
	out := externalIMAPRemoteMessage{Folder: folder, UIDValidity: uidValidity, UID: uint32(msg.UID), ReceivedAt: time.Now().UTC(), Raw: raw}
	if msg.Envelope != nil {
		out.MessageID = msg.Envelope.MessageID
		out.Subject = msg.Envelope.Subject
		out.SentAt = msg.Envelope.Date
		out.From, out.FromName = firstIMAPAddress(msg.Envelope.From)
		out.To = imapAddresses(msg.Envelope.To)
		out.CC = imapAddresses(msg.Envelope.Cc)
	}
	if out.SentAt.IsZero() {
		out.SentAt = out.ReceivedAt
	}
	if !msg.InternalDate.IsZero() {
		out.ReceivedAt = msg.InternalDate
	}
	out.SizeBytes = msg.RFC822Size
	for _, flag := range msg.Flags {
		if flag == imap.FlagSeen {
			out.IsRead = true
			break
		}
	}
	if len(raw) > 0 {
		if stored, _, err := parseExternalIMAPRawForSnippet(raw); err == nil {
			if out.Subject == "" {
				out.Subject = stored.Subject
			}
			if out.MessageID == "" {
				out.MessageID = strings.Trim(stored.MessageID, "<>")
			}
			out.Snippet = stored.Snippet
		}
	}
	return out
}

func parseExternalIMAPRawForSnippet(raw []byte) (storedMessage, []AttachmentInput, error) {
	tmp := &App{policy: NewHTMLPolicy()}
	tmp.setNow(time.Now)
	return tmp.parseMaildirMessage(raw, "")
}

func firstIMAPAddress(addrs []imap.Address) (string, string) {
	for _, addr := range addrs {
		if email := addr.Addr(); email != "" {
			return normalizeEmail(email), addr.Name
		}
	}
	return "", ""
}

func imapAddresses(addrs []imap.Address) []string {
	out := []string{}
	for _, addr := range addrs {
		if email := addr.Addr(); email != "" {
			out = append(out, normalizeEmail(email))
		}
	}
	return out
}

// deleteExternalIMAPLocalData 删除某外部账号在本地的全部痕迹：
// 导入的邮件（含 maildir 文件与附件）、对应的本地文件夹、文件夹同步状态。
func (a *App) deleteExternalIMAPLocalData(ctx context.Context, accountID, userID string) error {
	rows, err := a.db.QueryContext(ctx, `SELECT em.local_message_id FROM external_imap_messages em
		JOIN external_imap_accounts ea ON ea.id=em.account_id
		WHERE em.account_id=? AND ea.user_id=? AND em.local_message_id<>''`, accountID, userID)
	if err != nil {
		a.log.Warn("failed to list external imap local messages", "account", accountID, "error", err)
		return err
	}
	messageIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			messageIDs = append(messageIDs, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var deleteErr error
	for _, id := range messageIDs {
		if err := a.deleteMessage(ctx, id); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				// 文件删不掉（路径不在 maildir 根下、权限不足等）不能拖住整个
				// 账号删除：行必须清掉，孤儿文件留给运维看日志处理。
				a.log.Warn("failed to delete external imap local message, forcing row removal", "message", id, "error", err)
			}
			if _, rowErr := a.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id); rowErr != nil {
				a.log.Warn("failed to force remove external imap message row", "message", id, "error", rowErr)
				if deleteErr == nil {
					deleteErr = rowErr
				}
			}
		}
	}
	// 映射行必须一起清：只删 messages 会留下指向已消失邮件的孤儿映射，
	// 之后同步会认为这些 UID 已经导入而永远跳过（列表空白），删账号时也会
	// 反复在旧引用上失败。
	if _, err := a.db.ExecContext(ctx, `DELETE FROM external_imap_messages WHERE account_id=?`, accountID); err != nil {
		a.log.Warn("failed to clear external imap message mappings", "account", accountID, "error", err)
		if deleteErr == nil {
			deleteErr = err
		}
	}

	folderRows, err := a.db.QueryContext(ctx, `SELECT fs.local_folder_id FROM external_imap_folder_states fs
		JOIN external_imap_accounts ea ON ea.id=fs.account_id
		WHERE fs.account_id=? AND ea.user_id=? AND fs.local_folder_id<>''`, accountID, userID)
	if err != nil {
		a.log.Warn("failed to list external imap local folders", "account", accountID, "error", err)
		return err
	}
	folderIDs := []string{}
	for folderRows.Next() {
		var id string
		if err := folderRows.Scan(&id); err == nil && id != "" {
			folderIDs = append(folderIDs, id)
		}
	}
	folderRows.Close()
	if err := folderRows.Err(); err != nil {
		return err
	}
	for _, id := range folderIDs {
		// 只删空文件夹：里面如果还有用户自己拖进去的邮件，保留更安全。
		if _, err := a.db.ExecContext(ctx, `DELETE FROM folders WHERE id=? AND NOT EXISTS (SELECT 1 FROM messages WHERE folder_id=?)`, id, id); err != nil {
			a.log.Warn("failed to delete external imap local folder", "folder", id, "error", err)
		}
	}
	// UID 游标必须一起清：换服务器/换账号后如果沿用旧 last_uid，
	// 新服务器 UID 更低的所有邮件都会被永久跳过。
	if _, err := a.db.ExecContext(ctx, `DELETE FROM external_imap_folder_states WHERE account_id=?`, accountID); err != nil {
		a.log.Warn("failed to clear external imap folder state", "account", accountID, "error", err)
		if deleteErr == nil {
			deleteErr = err
		}
	}
	return deleteErr
}

// externalIMAPMaxMessageBytes 是单封外部邮件允许整封载入内存的上限。
// 0 表示不限制（不推荐）。默认 50MB。
func (a *App) externalIMAPMaxMessageBytes() int64 {
	mb := a.config().ExternalIMAPMaxMessageMB
	if mb <= 0 {
		return 0
	}
	return int64(mb) << 20
}
