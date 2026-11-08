package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

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
