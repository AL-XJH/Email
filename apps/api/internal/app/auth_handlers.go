package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var dummyLoginPasswordHash = func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte(randomToken()), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return hash
}()

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken"`
		ChallengeToken string `json:"challengeToken"`
		TwoFactorCode  string `json:"twoFactorCode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	clientIP := r.RemoteAddr
	if strings.TrimSpace(req.ChallengeToken) != "" {
		if retryAfter, err := a.checkLoginRateLimit(r.Context(), "", clientIP); err != nil {
			a.log.Warn("failed to check authentication rate limit", "stage", "totp", "error", err)
			respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
			return
		} else if retryAfter > 0 {
			a.auditLoginAttempt("totp", "rate_limited", "", clientIP)
			respondLoginRateLimited(w, retryAfter)
			return
		}
		challenge, err := a.loginChallengeByToken(r.Context(), req.ChallengeToken)
		if err != nil {
			retryAfter, recordErr := a.recordLoginFailure(r.Context(), "", clientIP)
			if recordErr != nil {
				a.log.Warn("failed to record authentication failure", "stage", "totp", "error", recordErr)
				respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
				return
			}
			a.auditLoginAttempt("totp", "failed", "", clientIP)
			if retryAfter > 0 {
				respondLoginRateLimited(w, retryAfter)
				return
			}
			respondError(w, http.StatusUnauthorized, "验证已过期，请重新登录")
			return
		}
		user, secret, err := a.loadUserAuthByID(r.Context(), challenge.UserID)
		if err != nil || user.Disabled || !user.TwoFactorEnabled || strings.TrimSpace(secret) == "" {
			a.deleteLoginChallenge(r.Context(), challenge.ID)
			account := ""
			if user != nil {
				account = user.Email
			}
			retryAfter, recordErr := a.recordLoginFailure(r.Context(), account, clientIP)
			if recordErr != nil {
				a.log.Warn("failed to record authentication failure", "stage", "totp", "error", recordErr)
				respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
				return
			}
			a.auditLoginAttempt("totp", "failed", account, clientIP)
			if retryAfter > 0 {
				respondLoginRateLimited(w, retryAfter)
				return
			}
			respondError(w, http.StatusUnauthorized, "验证已过期，请重新登录")
			return
		}
		if retryAfter, err := a.checkLoginRateLimit(r.Context(), user.Email, clientIP); err != nil {
			a.log.Warn("failed to check authentication rate limit", "stage", "totp", "error", err)
			respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
			return
		} else if retryAfter > 0 {
			a.auditLoginAttempt("totp", "rate_limited", user.Email, clientIP)
			respondLoginRateLimited(w, retryAfter)
			return
		}
		if !a.consumeTOTP(r.Context(), user.ID, secret, req.TwoFactorCode) {
			retryAfter, recordErr := a.recordLoginFailure(r.Context(), user.Email, clientIP)
			if recordErr != nil {
				a.log.Warn("failed to record authentication failure", "stage", "totp", "error", recordErr)
				respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
				return
			}
			a.auditLoginAttempt("totp", "failed", user.Email, clientIP)
			if retryAfter > 0 {
				respondLoginRateLimited(w, retryAfter)
				return
			}
			respondError(w, http.StatusUnauthorized, "验证码错误")
			return
		}
		a.deleteLoginChallenge(r.Context(), challenge.ID)
		if err := a.issueSession(w, r, user.ID); err != nil {
			respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
			return
		}
		if err := a.clearLoginAccountFailures(r.Context(), user.Email); err != nil {
			a.log.Warn("failed to clear authentication failures", "stage", "totp", "error", err)
		}
		a.auditLoginAttempt("totp", "success", user.Email, clientIP)
		respondJSON(w, http.StatusOK, map[string]any{"user": user})
		return
	}
	email := normalizeEmail(req.Email)
	if retryAfter, err := a.checkLoginRateLimit(r.Context(), email, clientIP); err != nil {
		a.log.Warn("failed to check authentication rate limit", "stage", "password", "error", err)
		respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	} else if retryAfter > 0 {
		a.auditLoginAttempt("password", "rate_limited", email, clientIP)
		respondLoginRateLimited(w, retryAfter)
		return
	}
	if err := a.verifyTurnstile(r.Context(), req.TurnstileToken, r.RemoteAddr); err != nil {
		a.auditLoginAttempt("turnstile", "failed", email, clientIP)
		respondError(w, http.StatusUnauthorized, "人机验证失败，请重试")
		return
	}
	user, passwordHash, err := a.loadLoginUserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, errNotFound) {
		respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	validUser := err == nil && user != nil && !user.Disabled
	hash := dummyLoginPasswordHash
	if err == nil && user != nil {
		hash = []byte(passwordHash)
	}
	passwordValid := bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) == nil
	if !validUser || !passwordValid {
		retryAfter, recordErr := a.recordLoginFailure(r.Context(), email, clientIP)
		if recordErr != nil {
			a.log.Warn("failed to record authentication failure", "stage", "password", "error", recordErr)
			respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
			return
		}
		a.auditLoginAttempt("password", "failed", email, clientIP)
		if retryAfter > 0 {
			respondLoginRateLimited(w, retryAfter)
			return
		}
		respondError(w, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	if err := a.attachUserAuthorization(r.Context(), user); err != nil {
		respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	if user.TwoFactorEnabled {
		// 用户已经绑定 2FA 就必须挑战，不能因为全局开关被关掉（或运维误关）
		// 只凭密码发会话；密钥缺失时失败关闭，绝不降级成单因子。
		_, secret, secretErr := a.loadUserAuthByID(r.Context(), user.ID)
		if secretErr != nil || strings.TrimSpace(secret) == "" {
			a.log.Error("two-factor enabled but secret is unavailable", "user", user.ID, "error", secretErr)
			respondError(w, http.StatusInternalServerError, "验证码配置异常，请联系管理员")
			return
		}
		challengeToken, err := a.createLoginChallenge(r.Context(), user.ID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "验证码生成失败，请稍后重试")
			return
		}
		a.auditPasswordStageSuccess(user.Email, clientIP, true)
		respondJSON(w, http.StatusOK, map[string]any{"twoFactorRequired": true, "challengeToken": challengeToken})
		return
	}
	if err := a.issueSession(w, r, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	if err := a.clearLoginAccountFailures(r.Context(), user.Email); err != nil {
		a.log.Warn("failed to clear authentication failures", "stage", "password", "error", err)
	}
	a.auditPasswordStageSuccess(user.Email, clientIP, false)
	respondJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (a *App) loadLoginUserByEmail(ctx context.Context, email string) (*User, string, error) {
	row := a.db.QueryRowContext(ctx, `SELECT id,email,display_name,role,password_hash,disabled,two_factor_enabled,created_at FROM users WHERE email=?`, email)
	var user User
	var passwordHash, created string
	var disabled, twoFactorEnabled int
	if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &passwordHash, &disabled, &twoFactorEnabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", errNotFound
		}
		return nil, "", err
	}
	user.Disabled = intBool(disabled)
	user.TwoFactorEnabled = intBool(twoFactorEnabled)
	user.CreatedAt = parseTime(created)
	return &user, passwordHash, nil
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !a.config().OpenRegistration && !a.config().InviteRegistrationEnabled {
		respondError(w, http.StatusForbidden, "当前未开放注册")
		return
	}
	var req struct {
		Email          string `json:"email"`
		DisplayName    string `json:"displayName"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken"`
		DomainID       string `json:"domainId"`
		LocalPart      string `json:"localPart"`
		InviteCode     string `json:"inviteCode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	if err := a.verifyTurnstile(r.Context(), req.TurnstileToken, r.RemoteAddr); err != nil {
		respondError(w, http.StatusUnauthorized, "人机验证失败，请重试")
		return
	}
	if !a.config().OpenRegistration {
		if err := a.validateRegistrationInvite(r.Context(), req.InviteCode); err != nil {
			if errors.Is(err, errRegistrationInviteInvalid) {
				respondError(w, http.StatusForbidden, "邀请码无效或可用次数已耗尽")
			} else {
				respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
			}
			return
		}
	}
	email := normalizeEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		badRequest(w, errors.New("邮箱地址无效"))
		return
	}
	if err := validatePasswordLength(req.Password); err != nil {
		badRequest(w, err)
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = strings.Split(email, "@")[0]
	}
	if len([]rune(displayName)) > 80 {
		badRequest(w, errors.New("显示名称不能超过 80 个字符"))
		return
	}
	var mailboxDomainID string
	var mailboxLocalPart string
	if strings.TrimSpace(req.DomainID) != "" && strings.TrimSpace(req.LocalPart) != "" {
		mailboxDomainID = strings.TrimSpace(req.DomainID)
		cleaned, err := requireCleanLocalPart(req.LocalPart)
		if err != nil {
			badRequest(w, err)
			return
		}
		mailboxLocalPart = cleaned
	} else {
		if err := a.db.QueryRowContext(r.Context(), `SELECT id FROM domains WHERE status='active' ORDER BY created_at ASC LIMIT 1`).Scan(&mailboxDomainID); err != nil {
			mailboxDomainID = ""
		}
		if mailboxDomainID != "" {
			mailboxLocalPart = strings.SplitN(email, "@", 2)[0]
		}
	}
	if mailboxDomainID != "" && mailboxLocalPart != "" {
		for _, reserved := range parseReservedPrefixes(a.config().ReservedMailboxPrefixes) {
			if mailboxLocalPart == reserved {
				respondError(w, http.StatusForbidden, "该前缀已被保留，请使用其他前缀")
				return
			}
		}
	}
	if _, _, err := a.userByEmail(r.Context(), email); err == nil {
		respondError(w, http.StatusConflict, "该邮箱已被注册")
		return
	} else if !errors.Is(err, errNotFound) {
		respondError(w, http.StatusInternalServerError, "failed to check user")
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	userID := newID("usr")
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
		return
	}
	defer tx.Rollback()
	var inviteGroupIDs []string
	if !a.config().OpenRegistration {
		granted, err := a.consumeRegistrationInviteTx(r.Context(), tx, req.InviteCode)
		if err != nil {
			switch {
			case errors.Is(err, errRegistrationInviteInvalid):
				respondError(w, http.StatusForbidden, "邀请码无效或可用次数已耗尽")
			case errors.Is(err, errRegistrationInviteGroupUnavailable):
				respondError(w, http.StatusConflict, "该邀请码绑定的用户组已不存在，请联系管理员")
			default:
				respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
			}
			return
		}
		inviteGroupIDs = granted
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO users(id,email,display_name,role,password_hash,disabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, userID, email, displayName, "user", string(passwordHash), 0, now, now); err != nil {
		if isUniqueViolation(err) {
			respondError(w, http.StatusConflict, "该邮箱已被注册")
			return
		}
		respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
		return
	}
	// Self-registration has no actor, so the actor-based grant checks in
	// setUserPermissionGroups cannot apply here. The groups were validated when
	// the invite was created and again when it was consumed.
	if len(inviteGroupIDs) > 0 {
		if err := a.writeUserPermissionGroups(r.Context(), tx, userID, inviteGroupIDs); err != nil {
			respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
			return
		}
	}
	// The mailbox is created in the same transaction as the user.
	//
	// Doing it after the commit meant that an inactive domain, a taken prefix or a
	// quota rejection left the caller with 201, a live session and no mailbox, with
	// nothing but a log line to show for it. Rolling the whole registration back
	// instead keeps the account and its mailbox from ever disagreeing.
	if mailboxDomainID != "" && mailboxLocalPart != "" {
		limits, err := a.permissionLimitsForGroupsTx(r.Context(), tx, inviteGroupIDs)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
			return
		}
		// attachUserAuthorization cannot run against the uncommitted user, so the
		// quota subject is assembled by hand. consumeMailboxQuotaTx only reads the
		// limits from it; the counters it compares against come from the transaction.
		quotaUser := &User{ID: userID, Limits: limits}
		if _, err := a.createMailboxWithPasswordHashTx(r.Context(), tx, userID, mailboxDomainID, mailboxLocalPart, displayName, string(passwordHash), 1024, "active", quotaUser); err != nil {
			switch {
			case errors.Is(err, errInactiveDomain):
				respondError(w, http.StatusBadRequest, "所选域名不可用，请选择其他域名")
			case errors.Is(err, errMailboxCountLimitReached), errors.Is(err, errMailboxDailyLimitReached):
				respondError(w, http.StatusForbidden, "邮箱数量已达上限，请联系管理员")
			case isUniqueViolation(err):
				respondError(w, http.StatusConflict, "该邮箱前缀已被使用，请换一个前缀")
			default:
				a.log.Warn("failed to create mailbox for registered user", "error", err, "email", email)
				respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
			}
			return
		}
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, "注册失败，请稍后重试")
		return
	}
	user, err := a.userByID(r.Context(), userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if err := a.issueSession(w, r, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"user": user})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(a.config().CookieName); err == nil {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=?`, hashToken(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: a.config().CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

const userPreferenceShowRemoteImages = "showRemoteImages"

func userPreferenceShowRemoteImagesKey(userID string) string {
	return "userPreference:" + userID + ":" + userPreferenceShowRemoteImages
}

func (a *App) userPreferences(ctx context.Context, userID string) (UserPreferences, error) {
	var value string
	err := a.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key=?`, userPreferenceShowRemoteImagesKey(userID)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return UserPreferences{}, nil
	}
	if err != nil {
		return UserPreferences{}, err
	}
	return UserPreferences{ShowRemoteImages: value == "true"}, nil
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	preferences, err := a.userPreferences(r.Context(), currentUser(r).ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load preferences")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"user": currentUser(r), "preferences": preferences})
}

func (a *App) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShowRemoteImages *bool `json:"showRemoteImages"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	if req.ShowRemoteImages == nil {
		badRequest(w, errors.New("showRemoteImages is required"))
		return
	}
	value := "false"
	if *req.ShowRemoteImages {
		value = "true"
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(r.Context(), `INSERT INTO system_settings(key,value,updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
		userPreferenceShowRemoteImagesKey(currentUser(r).ID), value, now)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update preferences")
		return
	}
	preferences, err := a.userPreferences(r.Context(), currentUser(r).ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load preferences")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"preferences": preferences})
}

func (a *App) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		badRequest(w, errors.New("请输入显示名称"))
		return
	}
	if len([]rune(displayName)) > 80 {
		badRequest(w, errors.New("显示名称不能超过 80 个字符"))
		return
	}
	_, err := a.db.ExecContext(r.Context(), `UPDATE users SET display_name=?, updated_at=? WHERE id=?`,
		displayName, a.now().UTC().Format(time.RFC3339Nano), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update profile")
		return
	}
	updated, err := a.userByID(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load profile")
		return
	}
	preferences, err := a.userPreferences(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load preferences")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"user": updated, "preferences": preferences})
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	if err := validatePasswordLength(req.NewPassword); err != nil {
		badRequest(w, err)
		return
	}
	clientIP := r.RemoteAddr
	if err := a.beginReauth(r.Context(), user.Email, clientIP); err != nil {
		if errors.Is(err, errReauthRateLimited) {
			respondError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
			return
		}
		respondError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	row := a.db.QueryRowContext(r.Context(), `SELECT password_hash FROM users WHERE id=?`, user.ID)
	var currentHash string
	if err := row.Scan(&currentHash); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(req.CurrentPassword)); err != nil {
		if recordErr := a.recordReauthFailure(r.Context(), user.Email, clientIP); errors.Is(recordErr, errReauthRateLimited) {
			respondError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
			return
		}
		respondError(w, http.StatusUnauthorized, "当前密码错误")
		return
	}
	if err := a.clearLoginAccountFailures(r.Context(), user.Email); err != nil {
		a.log.Warn("failed to clear reauthentication failures", "error", err)
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE users SET password_hash=?, updated_at=? WHERE id=?`, string(newHash), now, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update password")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE mailboxes SET password_hash=?, updated_at=? WHERE user_id=?`, string(newHash), now, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update mailbox password")
		return
	}
	// Changing a password is the one recovery action a user knows to take after a
	// suspected compromise, so it has to end the attacker's other sessions. The
	// current session is kept alive to avoid logging the user out of the page they
	// are on.
	keep := ""
	if cookie, err := r.Cookie(a.config().CookieName); err == nil {
		keep = hashToken(cookie.Value)
	}
	if err := revokeUserSessionsTx(r.Context(), tx, user.ID, keep); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to revoke other sessions")
		return
	}
	// 自助改密同样是「怀疑账号被盗」后的恢复动作：长期 API token 也必须失效。
	// 管理员重置那条路径已经这么做了，这里以前只清 session，等于留着后门。
	if _, err := tx.ExecContext(r.Context(), `UPDATE api_tokens SET disabled=1, updated_at=? WHERE user_id=? AND disabled=0`, now, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to revoke API tokens")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save password")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}
