package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type SystemSettings struct {
	PublicHostname                     string   `json:"publicHostname"`
	PublicBaseURL                      string   `json:"publicBaseUrl"`
	SiteTitle                          string   `json:"siteTitle"`
	SMTPHost                           string   `json:"smtpHost"`
	SMTPPort                           string   `json:"smtpPort"`
	SMTPUsername                       string   `json:"smtpUsername"`
	SMTPPasswordSet                    bool     `json:"smtpPasswordSet"`
	SMTPRequireTLS                     bool     `json:"smtpRequireTls"`
	MaildirRoot                        string   `json:"maildirRoot"`
	MaildirScanSeconds                 int      `json:"maildirScanSeconds"`
	SessionTTLHours                    int      `json:"sessionTtlHours"`
	AllowInsecureHTTP                  bool     `json:"allowInsecureHttp"`
	OpenRegistration                   bool     `json:"openRegistration"`
	InviteRegistrationEnabled          bool     `json:"inviteRegistrationEnabled"`
	TwoFactorEnabled                   bool     `json:"twoFactorEnabled"`
	TurnstileEnabled                   bool     `json:"turnstileEnabled"`
	TurnstileSiteKey                   string   `json:"turnstileSiteKey"`
	TurnstileSecretSet                 bool     `json:"turnstileSecretSet"`
	LinuxDoSSOEnabled                  bool     `json:"linuxDoSSOEnabled"`
	LinuxDoRegistrationEnabled         bool     `json:"linuxDoRegistrationEnabled"`
	LinuxDoClientID                    string   `json:"linuxDoClientId"`
	LinuxDoClientSecretSet             bool     `json:"linuxDoClientSecretSet"`
	LinuxDoCallbackURL                 string   `json:"linuxDoCallbackUrl"`
	LinuxDoRegistrationGroupIDs        []string `json:"linuxDoRegistrationGroupIds"`
	CatchAllEnabled                    bool     `json:"catchAllEnabled"`
	MailAutoRefresh                    bool     `json:"mailAutoRefresh"`
	MailRefreshSeconds                 int      `json:"mailRefreshSeconds"`
	UserMailboxApplyEnabled            bool     `json:"userMailboxApplyEnabled"`
	UserMailboxDomainIDs               []string `json:"userMailboxDomainIds"`
	ReservedMailboxPrefixes            string   `json:"reservedMailboxPrefixes"`
	ExternalIMAPEnabled                bool     `json:"externalImapEnabled"`
	ExternalIMAPSecretSet              bool     `json:"externalImapSecretSet"`
	ExternalIMAPSyncSeconds            int      `json:"externalImapSyncSeconds"`
	ExternalIMAPAllowPrivateHosts      bool     `json:"externalImapAllowPrivateHosts"`
	ExternalIMAPGmailClientID          string   `json:"externalImapGmailClientId"`
	ExternalIMAPGmailClientSecretSet   bool     `json:"externalImapGmailClientSecretSet"`
	ExternalIMAPOutlookClientID        string   `json:"externalImapOutlookClientId"`
	ExternalIMAPOutlookClientSecretSet bool     `json:"externalImapOutlookClientSecretSet"`
}

type systemSettingsUpdate struct {
	PublicHostname                  string   `json:"publicHostname"`
	PublicBaseURL                   string   `json:"publicBaseUrl"`
	SiteTitle                       string   `json:"siteTitle"`
	SMTPHost                        string   `json:"smtpHost"`
	SMTPPort                        string   `json:"smtpPort"`
	SMTPUsername                    string   `json:"smtpUsername"`
	SMTPPassword                    string   `json:"smtpPassword"`
	SMTPRequireTLS                  bool     `json:"smtpRequireTls"`
	MaildirRoot                     string   `json:"maildirRoot"`
	MaildirScanSeconds              int      `json:"maildirScanSeconds"`
	SessionTTLHours                 int      `json:"sessionTtlHours"`
	AllowInsecureHTTP               bool     `json:"allowInsecureHttp"`
	OpenRegistration                bool     `json:"openRegistration"`
	InviteRegistrationEnabled       bool     `json:"inviteRegistrationEnabled"`
	TwoFactorEnabled                bool     `json:"twoFactorEnabled"`
	TurnstileEnabled                bool     `json:"turnstileEnabled"`
	TurnstileSiteKey                string   `json:"turnstileSiteKey"`
	TurnstileSecretKey              string   `json:"turnstileSecretKey"`
	LinuxDoSSOEnabled               bool     `json:"linuxDoSSOEnabled"`
	LinuxDoRegistrationEnabled      bool     `json:"linuxDoRegistrationEnabled"`
	LinuxDoClientID                 string   `json:"linuxDoClientId"`
	LinuxDoClientSecret             string   `json:"linuxDoClientSecret"`
	LinuxDoRegistrationGroupIDs     []string `json:"linuxDoRegistrationGroupIds"`
	CatchAllEnabled                 bool     `json:"catchAllEnabled"`
	MailAutoRefresh                 bool     `json:"mailAutoRefresh"`
	MailRefreshSeconds              int      `json:"mailRefreshSeconds"`
	UserMailboxApplyEnabled         bool     `json:"userMailboxApplyEnabled"`
	UserMailboxDomainIDs            []string `json:"userMailboxDomainIds"`
	ReservedMailboxPrefixes         string   `json:"reservedMailboxPrefixes"`
	ExternalIMAPEnabled             bool     `json:"externalImapEnabled"`
	ExternalIMAPSecretKey           string   `json:"externalImapSecretKey"`
	ExternalIMAPSyncSeconds         int      `json:"externalImapSyncSeconds"`
	ExternalIMAPAllowPrivateHosts   bool     `json:"externalImapAllowPrivateHosts"`
	ExternalIMAPGmailClientID       string   `json:"externalImapGmailClientId"`
	ExternalIMAPGmailClientSecret   string   `json:"externalImapGmailClientSecret"`
	ExternalIMAPOutlookClientID     string   `json:"externalImapOutlookClientId"`
	ExternalIMAPOutlookClientSecret string   `json:"externalImapOutlookClientSecret"`

	// present records which keys the client actually sent. 缺省值不能当成 false/空：
	// 一个只带 siteTitle 的 POST 以前会把 twoFactorEnabled、turnstileEnabled 等
	// 全部写成 false，等于静默关闭安全功能。
	present map[string]bool
}

func (u *systemSettingsUpdate) UnmarshalJSON(data []byte) error {
	type plain systemSettingsUpdate
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*u = systemSettingsUpdate(decoded)
	u.present = make(map[string]bool, len(raw))
	for key := range raw {
		u.present[key] = true
	}
	return nil
}

func (u systemSettingsUpdate) has(key string) bool {
	return u.present[key]
}

type PublicSettings struct {
	OpenRegistration           bool           `json:"openRegistration"`
	InviteRegistrationEnabled  bool           `json:"inviteRegistrationEnabled"`
	TurnstileEnabled           bool           `json:"turnstileEnabled"`
	TurnstileSiteKey           string         `json:"turnstileSiteKey"`
	PublicHostname             string         `json:"publicHostname"`
	SiteTitle                  string         `json:"siteTitle"`
	MailAutoRefresh            bool           `json:"mailAutoRefresh"`
	MailRefreshMs              int            `json:"mailRefreshMs"`
	ExternalIMAPEnabled        bool           `json:"externalImapEnabled"`
	LinuxDoSSOEnabled          bool           `json:"linuxDoSSOEnabled"`
	LinuxDoRegistrationEnabled bool           `json:"linuxDoRegistrationEnabled"`
	MailboxDomains             []PublicDomain `json:"mailboxDomains,omitempty"`
}

type PublicDomain struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type smtpTestRequest struct {
	To string `json:"to"`
}

func (a *App) handleGetSystemSettings(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, a.systemSettingsSnapshot())
}

func (a *App) handlePublicSettings(w http.ResponseWriter, r *http.Request) {
	enabled := a.config().TurnstileEnabled && strings.TrimSpace(a.config().TurnstileSiteKey) != "" && strings.TrimSpace(a.config().TurnstileSecretKey) != ""
	refreshSeconds := a.config().MailRefreshSeconds
	if refreshSeconds <= 0 {
		refreshSeconds = 30
	}
	linuxDoEnabled := a.linuxDoEnabled()
	settings := PublicSettings{OpenRegistration: a.config().OpenRegistration, InviteRegistrationEnabled: a.config().InviteRegistrationEnabled, TurnstileEnabled: enabled, TurnstileSiteKey: a.config().TurnstileSiteKey, PublicHostname: a.config().PublicHostname, SiteTitle: a.config().SiteTitle, MailAutoRefresh: a.config().MailAutoRefresh, MailRefreshMs: refreshSeconds * 1000, ExternalIMAPEnabled: a.config().ExternalIMAPEnabled, LinuxDoSSOEnabled: linuxDoEnabled, LinuxDoRegistrationEnabled: linuxDoEnabled && a.config().LinuxDoRegistrationEnabled}

	// Include available domains for mailbox creation during registration
	if a.config().OpenRegistration || a.config().InviteRegistrationEnabled || (linuxDoEnabled && a.config().LinuxDoRegistrationEnabled) {
		rows, err := a.db.QueryContext(r.Context(), `SELECT id, name FROM domains WHERE status='active' ORDER BY name`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var d PublicDomain
				if err := rows.Scan(&d.ID, &d.Name); err != nil {
					continue
				}
				settings.MailboxDomains = append(settings.MailboxDomains, d)
			}
		}
	}

	respondJSON(w, http.StatusOK, settings)
}

func (a *App) handleUpdateSystemSettings(w http.ResponseWriter, r *http.Request) {
	var req systemSettingsUpdate
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	next := *a.config()
	// 未提交的字段一律沿用当前值；否则部分 POST 会把安全开关/连接参数清空。
	if !req.has("publicHostname") {
		req.PublicHostname = next.PublicHostname
	}
	if !req.has("publicBaseUrl") {
		req.PublicBaseURL = next.PublicBaseURL
	}
	if !req.has("siteTitle") {
		req.SiteTitle = next.SiteTitle
	}
	if !req.has("smtpHost") {
		req.SMTPHost = next.SMTPHost
	}
	if !req.has("smtpPort") {
		req.SMTPPort = next.SMTPPort
	}
	if !req.has("smtpUsername") {
		req.SMTPUsername = next.SMTPUsername
	}
	if !req.has("smtpRequireTls") {
		req.SMTPRequireTLS = next.SMTPRequireTLS
	}
	if !req.has("maildirRoot") {
		req.MaildirRoot = next.MaildirRoot
	}
	if !req.has("maildirScanSeconds") {
		req.MaildirScanSeconds = next.MaildirScanSeconds
	}
	if !req.has("sessionTtlHours") {
		req.SessionTTLHours = next.SessionTTLHours
	}
	if !req.has("allowInsecureHttp") {
		req.AllowInsecureHTTP = next.AllowInsecureHTTP
	}
	if !req.has("openRegistration") {
		req.OpenRegistration = next.OpenRegistration
	}
	if !req.has("inviteRegistrationEnabled") {
		req.InviteRegistrationEnabled = next.InviteRegistrationEnabled
	}
	if !req.has("twoFactorEnabled") {
		req.TwoFactorEnabled = next.TwoFactorEnabled
	}
	if !req.has("turnstileEnabled") {
		req.TurnstileEnabled = next.TurnstileEnabled
	}
	if !req.has("turnstileSiteKey") {
		req.TurnstileSiteKey = next.TurnstileSiteKey
	}
	if !req.has("linuxDoSSOEnabled") {
		req.LinuxDoSSOEnabled = next.LinuxDoSSOEnabled
	}
	if !req.has("linuxDoRegistrationEnabled") {
		req.LinuxDoRegistrationEnabled = next.LinuxDoRegistrationEnabled
	}
	if !req.has("linuxDoClientId") {
		req.LinuxDoClientID = next.LinuxDoClientID
	}
	if !req.has("linuxDoRegistrationGroupIds") {
		req.LinuxDoRegistrationGroupIDs = cleanIDList(strings.Split(next.LinuxDoRegistrationGroupIDs, ","))
	}
	if !req.has("catchAllEnabled") {
		req.CatchAllEnabled = next.CatchAllEnabled
	}
	if !req.has("mailAutoRefresh") {
		req.MailAutoRefresh = next.MailAutoRefresh
	}
	if !req.has("mailRefreshSeconds") {
		req.MailRefreshSeconds = next.MailRefreshSeconds
	}
	if !req.has("userMailboxApplyEnabled") {
		req.UserMailboxApplyEnabled = next.UserMailboxApplyEnabled
	}
	if !req.has("userMailboxDomainIds") {
		req.UserMailboxDomainIDs = cleanIDList(strings.Split(next.UserMailboxDomainIDs, ","))
	}
	if !req.has("reservedMailboxPrefixes") {
		req.ReservedMailboxPrefixes = next.ReservedMailboxPrefixes
	}
	if !req.has("externalImapEnabled") {
		req.ExternalIMAPEnabled = next.ExternalIMAPEnabled
	}
	if !req.has("externalImapSyncSeconds") {
		req.ExternalIMAPSyncSeconds = next.ExternalIMAPSyncSeconds
	}
	if !req.has("externalImapAllowPrivateHosts") {
		req.ExternalIMAPAllowPrivateHosts = next.ExternalIMAPAllowPrivateHosts
	}
	if !req.has("externalImapGmailClientId") {
		req.ExternalIMAPGmailClientID = next.ExternalIMAPGmailClientID
	}
	if !req.has("externalImapOutlookClientId") {
		req.ExternalIMAPOutlookClientID = next.ExternalIMAPOutlookClientID
	}
	next.PublicHostname = normalizeHostname(req.PublicHostname)
	if next.PublicHostname == "" {
		badRequest(w, errors.New("publicHostname is required"))
		return
	}
	next.PublicBaseURL = strings.TrimSpace(req.PublicBaseURL)
	if next.PublicBaseURL != "" {
		parsed, parseErr := url.Parse(next.PublicBaseURL)
		validScheme := parsed != nil && parsed.Scheme == "https"
		if parsed != nil && parsed.Scheme == "http" && req.AllowInsecureHTTP {
			validScheme = true
		}
		if parseErr != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || !validScheme {
			badRequest(w, errors.New("publicBaseUrl 必须是 https 地址（明文 http 需同时开启 allowInsecureHttp）"))
			return
		}
	}
	next.SiteTitle = strings.TrimSpace(req.SiteTitle)
	if next.SiteTitle == "" {
		next.SiteTitle = "LanQin Email"
	}
	if len([]rune(next.SiteTitle)) > 80 {
		badRequest(w, errors.New("siteTitle must be 80 characters or fewer"))
		return
	}
	next.SMTPHost = strings.TrimSpace(req.SMTPHost)
	next.SMTPPort = strings.TrimSpace(req.SMTPPort)
	if next.SMTPPort == "" {
		next.SMTPPort = "25"
	}
	if port, err := strconv.Atoi(next.SMTPPort); err != nil || port < 1 || port > 65535 {
		badRequest(w, errors.New("smtpPort must be between 1 and 65535"))
		return
	} else {
		next.SMTPPort = strconv.Itoa(port)
	}
	next.SMTPUsername = strings.TrimSpace(req.SMTPUsername)
	if strings.TrimSpace(req.SMTPPassword) != "" {
		next.SMTPPassword = req.SMTPPassword
	}
	next.SMTPRequireTLS = req.SMTPRequireTLS
	if next.SMTPUsername != "" && !next.SMTPRequireTLS && !strings.EqualFold(next.SMTPHost, "localhost") && next.SMTPHost != "127.0.0.1" && next.SMTPHost != "::1" {
		// Go 的 PlainAuth 拒绝在明文连接上发送口令，这里提前拦住，
		// 否则每次发信都会在 AUTH 阶段失败并重试到永久失败。
		badRequest(w, errors.New("smtpHost 不是本机时必须开启 smtpRequireTls，否则无法发送认证信息"))
		return
	}
	next.MaildirRoot = strings.TrimSpace(req.MaildirRoot)
	if req.MaildirScanSeconds <= 0 {
		req.MaildirScanSeconds = defaultMaildirScanSeconds
	}
	next.MaildirScanSeconds = req.MaildirScanSeconds
	if req.SessionTTLHours <= 0 {
		req.SessionTTLHours = 24 * 7
	}
	// time.Duration(hours)*time.Hour 在 ~256 万小时会溢出成负数，
	// 结果是登录成功但 cookie 一写出来就过期。上限一年足够用。
	if req.SessionTTLHours > 24*365 {
		req.SessionTTLHours = 24 * 365
	}
	next.SessionTTLHours = req.SessionTTLHours
	next.AllowInsecureHTTP = req.AllowInsecureHTTP
	next.OpenRegistration = req.OpenRegistration
	next.InviteRegistrationEnabled = req.InviteRegistrationEnabled
	next.TwoFactorEnabled = req.TwoFactorEnabled
	next.TurnstileEnabled = req.TurnstileEnabled
	next.TurnstileSiteKey = strings.TrimSpace(req.TurnstileSiteKey)
	if strings.TrimSpace(req.TurnstileSecretKey) != "" {
		next.TurnstileSecretKey = strings.TrimSpace(req.TurnstileSecretKey)
	}
	if next.TurnstileEnabled && (next.TurnstileSiteKey == "" || next.TurnstileSecretKey == "") {
		badRequest(w, errors.New("turnstile keys are required when enabled"))
		return
	}
	next.LinuxDoSSOEnabled = req.LinuxDoSSOEnabled
	next.LinuxDoRegistrationEnabled = req.LinuxDoRegistrationEnabled
	next.LinuxDoClientID = strings.TrimSpace(req.LinuxDoClientID)
	if strings.TrimSpace(req.LinuxDoClientSecret) != "" {
		next.LinuxDoClientSecret = strings.TrimSpace(req.LinuxDoClientSecret)
	}
	// Assigning a permission group is an authorization decision, not a settings
	// tweak: Linux.do sign-ups join this group through writeUserPermissionGroups,
	// which by design skips the actor-based grant checks. Left on
	// admin.settings.update, a settings operator could point it at a group holding
	// permissions they lack and then register a Linux.do account into it. The same
	// reasoning already restricts invite codes to super administrators.
	requestedLinuxDoGroups := strings.Join(cleanIDList(req.LinuxDoRegistrationGroupIDs), ",")
	if requestedLinuxDoGroups != next.LinuxDoRegistrationGroupIDs {
		actor := currentUser(r)
		if actor == nil || actor.Role != "admin" {
			respondError(w, http.StatusForbidden, "只有超级管理员可以修改 Linux.do 注册用户组")
			return
		}
		if err := a.validateAssignableGroupIDsTx(r.Context(), nil, cleanIDList(req.LinuxDoRegistrationGroupIDs)); err != nil {
			badRequest(w, err)
			return
		}
		next.LinuxDoRegistrationGroupIDs = requestedLinuxDoGroups
	}
	if err := validateLinuxDoSettings(next); err != nil {
		badRequest(w, err)
		return
	}
	next.CatchAllEnabled = req.CatchAllEnabled
	next.MailAutoRefresh = req.MailAutoRefresh
	if req.MailRefreshSeconds <= 0 {
		req.MailRefreshSeconds = 30
	}
	next.MailRefreshSeconds = req.MailRefreshSeconds
	next.UserMailboxApplyEnabled = req.UserMailboxApplyEnabled
	next.UserMailboxDomainIDs = strings.Join(cleanIDList(req.UserMailboxDomainIDs), ",")
	next.ReservedMailboxPrefixes = strings.Join(parseReservedPrefixes(req.ReservedMailboxPrefixes), ",")
	next.ExternalIMAPEnabled = req.ExternalIMAPEnabled
	if strings.TrimSpace(req.ExternalIMAPSecretKey) != "" {
		next.ExternalIMAPSecretKey = strings.TrimSpace(req.ExternalIMAPSecretKey)
	}
	if req.ExternalIMAPSyncSeconds <= 0 {
		req.ExternalIMAPSyncSeconds = 300
	}
	next.ExternalIMAPSyncSeconds = req.ExternalIMAPSyncSeconds
	next.ExternalIMAPAllowPrivateHosts = req.ExternalIMAPAllowPrivateHosts
	next.ExternalIMAPGmailClientID = strings.TrimSpace(req.ExternalIMAPGmailClientID)
	if strings.TrimSpace(req.ExternalIMAPGmailClientSecret) != "" {
		next.ExternalIMAPGmailClientSecret = strings.TrimSpace(req.ExternalIMAPGmailClientSecret)
	}
	next.ExternalIMAPOutlookClientID = strings.TrimSpace(req.ExternalIMAPOutlookClientID)
	if strings.TrimSpace(req.ExternalIMAPOutlookClientSecret) != "" {
		next.ExternalIMAPOutlookClientSecret = strings.TrimSpace(req.ExternalIMAPOutlookClientSecret)
	}
	if next.ExternalIMAPEnabled && strings.TrimSpace(next.ExternalIMAPSecretKey) == "" {
		badRequest(w, errors.New("外部 IMAP 加密密钥未设置"))
		return
	}
	// 换密钥不会自动重加密已存的口令/OAuth token：直接保存会让所有外部账号
	// 立刻解不开，且原密钥一丢就永久无法恢复。有账号时只允许沿用原密钥。
	if next.ExternalIMAPSecretKey != a.config().ExternalIMAPSecretKey {
		var existing int
		if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM external_imap_accounts`).Scan(&existing); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to check external accounts")
			return
		}
		if existing > 0 {
			badRequest(w, errors.New("已有外部邮箱账号，不能更换加密密钥；请先删除这些账号"))
			return
		}
	}

	if err := a.saveSystemSettings(r.Context(), next); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save settings")
		return
	}
	a.setConfig(next)
	respondJSON(w, http.StatusOK, a.systemSettingsSnapshot())
}

func (a *App) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	var req smtpTestRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	cfg := *a.config()
	if strings.TrimSpace(cfg.SMTPHost) == "" {
		badRequest(w, errors.New("SMTP 主机未设置"))
		return
	}
	if strings.TrimSpace(cfg.SMTPPort) == "" {
		cfg.SMTPPort = "25"
	}
	if _, err := strconv.Atoi(cfg.SMTPPort); err != nil {
		badRequest(w, errors.New("SMTP 端口无效"))
		return
	}
	to := normalizeEmail(req.To)
	if to == "" || !strings.Contains(to, "@") {
		badRequest(w, errors.New("收件邮箱无效"))
		return
	}
	from := cfg.AdminEmail
	if user := currentUser(r); user != nil && strings.Contains(user.Email, "@") {
		from = user.Email
	}
	if strings.TrimSpace(from) == "" || !strings.Contains(from, "@") {
		badRequest(w, errors.New("发件邮箱无效"))
		return
	}
	domain := cfg.PublicHostname
	if parts := strings.SplitN(from, "@", 2); len(parts) == 2 && parts[1] != "" {
		domain = parts[1]
	}
	if domain == "" {
		domain = "lanqin.local"
	}
	now := a.now().UTC()
	subject := "LanQin Email SMTP 测试"
	bodyText := "这是一封 SMTP 测试邮件。"
	bodyHTML := "<p>这是一封 SMTP 测试邮件。</p>"
	if tpl, err := a.mailTemplate(r.Context(), smtpTestTemplateKey); err == nil {
		rendered := renderMailTemplate(tpl, templateRenderData{
			To:             to,
			From:           from,
			PublicHostname: cfg.PublicHostname,
			PublicBaseURL:  cfg.PublicBaseURL,
			Time:           now,
		})
		subject, bodyText, bodyHTML = rendered.Subject, rendered.Text, rendered.HTML
	}
	mimeBytes, err := BuildMIME(MIMEMessage{
		From:      from,
		To:        []string{to},
		Subject:   subject,
		Text:      bodyText,
		HTML:      bodyHTML,
		MessageID: "<" + newID("msg") + "@" + domain + ">",
		Date:      now,
	})
	if err != nil {
		badRequest(w, err)
		return
	}
	if err := sendSMTPWithConfig(cfg, from, []string{to}, mimeBytes); err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) systemSettingsSnapshot() SystemSettings {
	return SystemSettings{
		PublicHostname:                     a.config().PublicHostname,
		PublicBaseURL:                      a.config().PublicBaseURL,
		SiteTitle:                          a.config().SiteTitle,
		SMTPHost:                           a.config().SMTPHost,
		SMTPPort:                           a.config().SMTPPort,
		SMTPUsername:                       a.config().SMTPUsername,
		SMTPPasswordSet:                    strings.TrimSpace(a.config().SMTPPassword) != "",
		SMTPRequireTLS:                     a.config().SMTPRequireTLS,
		MaildirRoot:                        a.config().MaildirRoot,
		MaildirScanSeconds:                 a.config().MaildirScanSeconds,
		SessionTTLHours:                    a.config().SessionTTLHours,
		AllowInsecureHTTP:                  a.config().AllowInsecureHTTP,
		OpenRegistration:                   a.config().OpenRegistration,
		InviteRegistrationEnabled:          a.config().InviteRegistrationEnabled,
		TwoFactorEnabled:                   a.config().TwoFactorEnabled,
		TurnstileEnabled:                   a.config().TurnstileEnabled,
		TurnstileSiteKey:                   a.config().TurnstileSiteKey,
		TurnstileSecretSet:                 strings.TrimSpace(a.config().TurnstileSecretKey) != "",
		LinuxDoSSOEnabled:                  a.config().LinuxDoSSOEnabled,
		LinuxDoRegistrationEnabled:         a.config().LinuxDoRegistrationEnabled,
		LinuxDoClientID:                    a.config().LinuxDoClientID,
		LinuxDoClientSecretSet:             strings.TrimSpace(a.config().LinuxDoClientSecret) != "",
		LinuxDoCallbackURL:                 a.linuxDoCallbackURL(),
		LinuxDoRegistrationGroupIDs:        cleanIDList(strings.Split(a.config().LinuxDoRegistrationGroupIDs, ",")),
		CatchAllEnabled:                    a.config().CatchAllEnabled,
		MailAutoRefresh:                    a.config().MailAutoRefresh,
		MailRefreshSeconds:                 a.config().MailRefreshSeconds,
		UserMailboxApplyEnabled:            a.config().UserMailboxApplyEnabled,
		UserMailboxDomainIDs:               cleanIDList(strings.Split(a.config().UserMailboxDomainIDs, ",")),
		ReservedMailboxPrefixes:            strings.Join(parseReservedPrefixes(a.config().ReservedMailboxPrefixes), "\n"),
		ExternalIMAPEnabled:                a.config().ExternalIMAPEnabled,
		ExternalIMAPSecretSet:              strings.TrimSpace(a.config().ExternalIMAPSecretKey) != "",
		ExternalIMAPSyncSeconds:            a.config().ExternalIMAPSyncSeconds,
		ExternalIMAPAllowPrivateHosts:      a.config().ExternalIMAPAllowPrivateHosts,
		ExternalIMAPGmailClientID:          a.config().ExternalIMAPGmailClientID,
		ExternalIMAPGmailClientSecretSet:   strings.TrimSpace(a.config().ExternalIMAPGmailClientSecret) != "",
		ExternalIMAPOutlookClientID:        a.config().ExternalIMAPOutlookClientID,
		ExternalIMAPOutlookClientSecretSet: strings.TrimSpace(a.config().ExternalIMAPOutlookClientSecret) != "",
	}
}

func (a *App) loadPersistedSystemSettings(ctx context.Context) error {
	keyColumn := keyColumnSQL(databaseDriverSQLite)
	rows, err := a.db.QueryContext(ctx, `SELECT `+keyColumn+`,value FROM system_settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		switch key {
		case "publicHostname":
			a.updateConfig(func(cfg *Config) { cfg.PublicHostname = value })
		case "publicBaseUrl":
			a.updateConfig(func(cfg *Config) { cfg.PublicBaseURL = value })
		case "siteTitle":
			a.updateConfig(func(cfg *Config) { cfg.SiteTitle = value })
		case "smtpHost":
			a.updateConfig(func(cfg *Config) { cfg.SMTPHost = value })
		case "smtpPort":
			a.updateConfig(func(cfg *Config) { cfg.SMTPPort = value })
		case "smtpUsername":
			a.updateConfig(func(cfg *Config) { cfg.SMTPUsername = value })
		case "smtpPassword":
			a.updateConfig(func(cfg *Config) { cfg.SMTPPassword = a.openSettingSecret(value) })
		case "smtpRequireTls":
			a.updateConfig(func(cfg *Config) { cfg.SMTPRequireTLS = value == "true" })
		case "maildirRoot":
			a.updateConfig(func(cfg *Config) { cfg.MaildirRoot = value })
		case "maildirScanSeconds":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				a.updateConfig(func(cfg *Config) { cfg.MaildirScanSeconds = n })
			}
		case "sessionTtlHours":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				a.updateConfig(func(cfg *Config) { cfg.SessionTTLHours = n })
			}
		case "allowInsecureHttp":
			a.updateConfig(func(cfg *Config) { cfg.AllowInsecureHTTP = value == "true" })
		case "openRegistration":
			a.updateConfig(func(cfg *Config) { cfg.OpenRegistration = value == "true" })
		case "inviteRegistrationEnabled":
			a.updateConfig(func(cfg *Config) { cfg.InviteRegistrationEnabled = value == "true" })
		case "twoFactorEnabled":
			a.updateConfig(func(cfg *Config) { cfg.TwoFactorEnabled = value == "true" })
		case "turnstileEnabled":
			a.updateConfig(func(cfg *Config) { cfg.TurnstileEnabled = value == "true" })
		case "turnstileSiteKey":
			a.updateConfig(func(cfg *Config) { cfg.TurnstileSiteKey = value })
		case "turnstileSecretKey":
			a.updateConfig(func(cfg *Config) { cfg.TurnstileSecretKey = a.openSettingSecret(value) })
		case "linuxDoSSOEnabled":
			a.updateConfig(func(cfg *Config) { cfg.LinuxDoSSOEnabled = value == "true" })
		case "linuxDoRegistrationEnabled":
			a.updateConfig(func(cfg *Config) { cfg.LinuxDoRegistrationEnabled = value == "true" })
		case "linuxDoRegistrationGroupIds":
			a.updateConfig(func(cfg *Config) { cfg.LinuxDoRegistrationGroupIDs = value })
		case "linuxDoClientId":
			a.updateConfig(func(cfg *Config) { cfg.LinuxDoClientID = value })
		case "linuxDoClientSecret":
			a.updateConfig(func(cfg *Config) { cfg.LinuxDoClientSecret = a.openSettingSecret(value) })
		case "catchAllEnabled":
			a.updateConfig(func(cfg *Config) { cfg.CatchAllEnabled = value == "true" })
		case "mailAutoRefresh":
			a.updateConfig(func(cfg *Config) { cfg.MailAutoRefresh = value == "true" })
		case "mailRefreshSeconds":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				a.updateConfig(func(cfg *Config) { cfg.MailRefreshSeconds = n })
			}
		case "userMailboxApplyEnabled":
			a.updateConfig(func(cfg *Config) { cfg.UserMailboxApplyEnabled = value == "true" })
		case "userMailboxDomainIds":
			a.updateConfig(func(cfg *Config) { cfg.UserMailboxDomainIDs = value })
		case "reservedMailboxPrefixes":
			a.updateConfig(func(cfg *Config) { cfg.ReservedMailboxPrefixes = value })
		case "externalImapEnabled":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPEnabled = value == "true" })
		case "externalImapSecretKey":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPSecretKey = a.openSettingSecret(value) })
		case "externalImapSyncSeconds":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPSyncSeconds = n })
			}
		case "externalImapAllowPrivateHosts":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPAllowPrivateHosts = value == "true" })
		case "externalImapGmailClientId":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPGmailClientID = value })
		case "externalImapGmailClientSecret":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPGmailClientSecret = a.openSettingSecret(value) })
		case "externalImapOutlookClientId":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPOutlookClientID = value })
		case "externalImapOutlookClientSecret":
			a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPOutlookClientSecret = a.openSettingSecret(value) })
		}
	}
	return rows.Err()
}

func (a *App) sealSettingSecret(value string) string {
	if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "enc:v1:") {
		return value
	}
	if strings.TrimSpace(a.config().NotificationSecretKey) == "" {
		return value
	}
	sealed, err := a.encryptNotificationSecret(value)
	if err != nil {
		a.log.Warn("failed to encrypt setting secret", "error", err)
		return value
	}
	return "enc:v1:" + sealed
}

func (a *App) openSettingSecret(value string) string {
	if !strings.HasPrefix(value, "enc:v1:") {
		return value
	}
	plain, err := a.decryptNotificationSecret(strings.TrimPrefix(value, "enc:v1:"))
	if err != nil {
		a.log.Warn("failed to decrypt setting secret", "error", err)
		return ""
	}
	return plain
}

func (a *App) saveSystemSettings(ctx context.Context, cfg Config) error {
	values := map[string]string{
		"publicHostname":                  cfg.PublicHostname,
		"publicBaseUrl":                   cfg.PublicBaseURL,
		"siteTitle":                       cfg.SiteTitle,
		"smtpHost":                        cfg.SMTPHost,
		"smtpPort":                        cfg.SMTPPort,
		"smtpUsername":                    cfg.SMTPUsername,
		"smtpPassword":                    cfg.SMTPPassword,
		"smtpRequireTls":                  strconv.FormatBool(cfg.SMTPRequireTLS),
		"maildirRoot":                     cfg.MaildirRoot,
		"maildirScanSeconds":              strconv.Itoa(cfg.MaildirScanSeconds),
		"sessionTtlHours":                 strconv.Itoa(cfg.SessionTTLHours),
		"allowInsecureHttp":               strconv.FormatBool(cfg.AllowInsecureHTTP),
		"openRegistration":                strconv.FormatBool(cfg.OpenRegistration),
		"inviteRegistrationEnabled":       strconv.FormatBool(cfg.InviteRegistrationEnabled),
		"twoFactorEnabled":                strconv.FormatBool(cfg.TwoFactorEnabled),
		"turnstileEnabled":                strconv.FormatBool(cfg.TurnstileEnabled),
		"turnstileSiteKey":                cfg.TurnstileSiteKey,
		"turnstileSecretKey":              cfg.TurnstileSecretKey,
		"linuxDoSSOEnabled":               strconv.FormatBool(cfg.LinuxDoSSOEnabled),
		"linuxDoRegistrationEnabled":      strconv.FormatBool(cfg.LinuxDoRegistrationEnabled),
		"linuxDoRegistrationGroupIds":     strings.Join(cleanIDList(strings.Split(cfg.LinuxDoRegistrationGroupIDs, ",")), ","),
		"linuxDoClientId":                 cfg.LinuxDoClientID,
		"linuxDoClientSecret":             cfg.LinuxDoClientSecret,
		"catchAllEnabled":                 strconv.FormatBool(cfg.CatchAllEnabled),
		"mailAutoRefresh":                 strconv.FormatBool(cfg.MailAutoRefresh),
		"mailRefreshSeconds":              strconv.Itoa(cfg.MailRefreshSeconds),
		"userMailboxApplyEnabled":         strconv.FormatBool(cfg.UserMailboxApplyEnabled),
		"userMailboxDomainIds":            strings.Join(cleanIDList(strings.Split(cfg.UserMailboxDomainIDs, ",")), ","),
		"reservedMailboxPrefixes":         strings.Join(parseReservedPrefixes(cfg.ReservedMailboxPrefixes), ","),
		"externalImapEnabled":             strconv.FormatBool(cfg.ExternalIMAPEnabled),
		"externalImapSecretKey":           cfg.ExternalIMAPSecretKey,
		"externalImapSyncSeconds":         strconv.Itoa(cfg.ExternalIMAPSyncSeconds),
		"externalImapAllowPrivateHosts":   strconv.FormatBool(cfg.ExternalIMAPAllowPrivateHosts),
		"externalImapGmailClientId":       cfg.ExternalIMAPGmailClientID,
		"externalImapGmailClientSecret":   cfg.ExternalIMAPGmailClientSecret,
		"externalImapOutlookClientId":     cfg.ExternalIMAPOutlookClientID,
		"externalImapOutlookClientSecret": a.sealSettingSecret(cfg.ExternalIMAPOutlookClientSecret),
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	keyColumn := keyColumnSQL(databaseDriverSQLite)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range values {
		query := upsertSQL(databaseDriverSQLite, `INSERT INTO system_settings(`+keyColumn+`,value,updated_at) VALUES(?,?,?)`, `(`+keyColumn+`)`,
			`value=excluded.value,updated_at=excluded.updated_at`,
			`value=VALUES(value),updated_at=VALUES(updated_at)`)
		if _, err := tx.ExecContext(ctx, query, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func cleanIDList(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func parseReservedPrefixes(value string) []string {
	value = strings.NewReplacer("\r", "\n", ",", "\n", ";", "\n", "，", "\n", "；", "\n").Replace(value)
	seen := map[string]bool{}
	out := []string{}
	for _, item := range strings.Split(value, "\n") {
		item = normalizeLocalPart(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func normalizeHostname(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	if i := strings.Index(value, "/"); i >= 0 {
		value = value[:i]
	}
	return value
}
