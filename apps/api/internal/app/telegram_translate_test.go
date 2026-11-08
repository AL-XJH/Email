package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 译文缓存是全局的：同一段文本在别的用例里翻过，这里就会直接命中缓存，
// 拿到那个用例的假译文。每个用例先清一遍。
func resetTranslateCache(t *testing.T) {
	t.Helper()
	translateCache.Lock()
	translateCache.items = make(map[string]string)
	translateCache.Unlock()
}

func TestTelegramNeedsTranslation(t *testing.T) {
	cases := []struct {
		name string
		text string
		min  int
		want bool
	}{
		{"纯英文", "Your invoice for November is available. Thanks for your business.", 40, true},
		{"纯中文", "您的订单已签收，感谢您在京东购物，期待再次为您服务。", 60, false},
		{
			"中文正文 + 一堆追踪链接",
			"点击下面的按钮开始设置。\nhttps://c.gle/AGSJxQxHyhbP9QWg-81LooUkNYWws-joXbQR2ebAIbOhxihF-eBYE5pHjnwvEATZoUxMJoQU8rm0g\nhttps://c.gle/AGSJxQxHyhbP9QWg-81LooUkNYWws-joXbQR2ebAIbOhxihF-eBYE5pHjnwvEATZoUxMJoQU8rm0h",
			60,
			false,
		},
		{"中英混排但以中文为主", "这是 Gemini 的周报：本周新增 12 个功能，详见 Release Notes 页面。", 60, false},
		{"短英文不需要翻", "Re: ok", 60, false},
	}
	for _, tc := range cases {
		if got := telegramNeedsTranslation(tc.text, tc.min); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestTranslateTelegramPayloadTranslatesEnglish(t *testing.T) {
	resetTranslateCache(t)
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = r.ParseForm()
		out := make([][]string, 0, len(r.Form["q"]))
		for range r.Form["q"] {
			out = append(out, []string{"这是机器翻译的中文内容。", "en"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	old := googleDictEndpoint
	googleDictEndpoint = server.URL
	defer func() { googleDictEndpoint = old }()

	a := &App{}
	payload := telegramNotificationPayload{
		Text:    "old",
		Subject: "Your invoice for November is available",
		Body:    "Your invoice for November is available. Thanks for your business and see you next month.",
		Mailbox: "xjh@example.test",
		From:    "Stripe <no-reply@stripe.test>",
	}
	got := a.translateTelegramPayload(context.Background(), payload)
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("英文邮件应该打翻译接口")
	}
	if !strings.Contains(got.Subject, "机器翻译") {
		t.Fatalf("主题没翻译: %q", got.Subject)
	}
	if len(got.Parts) == 0 || !strings.Contains(got.Parts[0], "机器翻译") {
		t.Fatalf("正文没翻译: %#v", got.Parts)
	}
	if !strings.HasPrefix(got.Parts[0], "<b>") {
		t.Fatalf("消息头第一行仍然要是加粗主题: %q", got.Parts[0])
	}
	// 收件人/发件人这些结构化字段不能被翻掉。
	if !strings.Contains(got.Parts[0], "xjh@example.test") {
		t.Fatalf("收件人被翻没了: %q", got.Parts[0])
	}
}

func TestTranslateTelegramPayloadLeavesChineseAlone(t *testing.T) {
	resetTranslateCache(t)
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	old := googleDictEndpoint
	googleDictEndpoint = server.URL
	defer func() { googleDictEndpoint = old }()

	a := &App{}
	payload := telegramNotificationPayload{
		Text:    "原样",
		Subject: "【京东】您的订单已签收",
		Body:    "订单 2837461928 已由本人签收，感谢您在京东购物，期待再次为您服务。",
		Mailbox: "xjh@example.test",
		From:    "京东 <no-reply@jd.test>",
	}
	got := a.translateTelegramPayload(context.Background(), payload)
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("中文邮件不该打翻译接口，实际打了 %d 次", hits)
	}
	if got.Parts != nil || got.Text != "原样" {
		t.Fatalf("中文邮件应原样返回: %#v", got)
	}
}

// 端到端：worker 取到一封英文邮件，发给 Telegram 之前应该已经翻成中文。
func TestTelegramWorkerSendsTranslatedText(t *testing.T) {
	resetTranslateCache(t)
	a := newTelegramTestApp(t)
	var sent []string
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if text, ok := body["text"].(string); ok {
			sent = append(sent, text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer telegram.Close()
	a.telegramAPIBaseURL = telegram.URL

	dict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		out := make([][]string, 0, len(r.Form["q"]))
		for range r.Form["q"] {
			out = append(out, []string{"这是译文正文。", "en"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer dict.Close()
	old := googleDictEndpoint
	googleDictEndpoint = dict.URL
	defer func() { googleDictEndpoint = old }()

	user, mailbox := defaultAdminUserAndMailbox(t, a)
	ciphertext, err := a.encryptNotificationSecret(testTelegramBotToken)
	if err != nil {
		t.Fatal(err)
	}
	nowText := a.now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`INSERT INTO telegram_notification_settings(user_id,bot_token_ciphertext,bot_username,chat_id,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)`, user.ID, ciphertext, "lanqin_test_bot", "123456789", 1, nowText, nowText); err != nil {
		t.Fatal(err)
	}
	payload := telegramNotificationPayload{
		Text:    "<b>Your invoice for November is available</b>\n收件人: xjh@example.test",
		Subject: "Your invoice for November is available",
		Body:    "Your monthly invoice is now available. We charged your card ending in 4242 for $20.00 on November 1. Thanks for your business.",
		Mailbox: "xjh@example.test",
		From:    "Stripe Billing no-reply@stripe.test",
	}
	if _, err := a.db.Exec(`INSERT INTO telegram_notification_outbox(id,event_key,user_id,mailbox_id,message_id,rule_id,payload_json,next_attempt_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, "tgn_translate", "telegram:translate", user.ID, mailbox.ID, "mail", "rule", jsonEncode(payload), nowText, nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if err := a.processDueTelegramNotifications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sent) == 0 {
		t.Fatal("没有发出任何消息")
	}
	first := sent[0]
	if !strings.Contains(first, "这是译文正文。") {
		t.Fatalf("推送正文没翻译: %q", first)
	}
	if strings.Contains(first, "Your monthly invoice is now available") {
		t.Fatalf("原文没被替换掉: %q", first)
	}
	if !strings.HasPrefix(first, "<b>这是译文正文。</b>") {
		t.Fatalf("第一行应该是加粗的译文主题: %q", first)
	}
	if !strings.Contains(first, "xjh@example.test") {
		t.Fatalf("收件人丢了: %q", first)
	}
	var delivered string
	if err := a.db.QueryRow(`SELECT COALESCE(delivered_at,'') FROM telegram_notification_outbox WHERE id=?`, "tgn_translate").Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered == "" {
		t.Fatal("推送没有标记为已送达")
	}
}
