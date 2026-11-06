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

func TestParseGoogleTranslateResponse(t *testing.T) {
	raw := []any{
		[]any{
			[]any{"你好", "Hello", nil, nil, float64(3)},
			[]any{"，世界", ", world", nil, nil, float64(3)},
		},
		nil,
		"en",
	}
	translated, source := parseGoogleTranslateResponse(raw)
	if translated != "你好，世界" {
		t.Fatalf("translated = %q", translated)
	}
	if source != "en" {
		t.Fatalf("source = %q", source)
	}
}

func TestParseChromeDictResponseMulti(t *testing.T) {
	raw := []any{
		[]any{"你好", "en"},
		[]any{"世界", "en"},
	}
	out, source, err := parseChromeDictResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "你好" || out[1] != "世界" {
		t.Fatalf("out = %#v", out)
	}
	if source != "en" {
		t.Fatalf("source = %q", source)
	}
}

func TestParseChromeDictResponseFlat(t *testing.T) {
	out, source, err := parseChromeDictResponse([]any{"你好", "en"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "你好" || source != "en" {
		t.Fatalf("out = %#v source = %q", out, source)
	}
}

// overrideEndpoints 把三个上游指到测试服务器，测试结束自动还原。
func overrideEndpoints(t *testing.T, dict, gtx, mymemory string) {
	t.Helper()
	oldDict, oldGTX, oldMy := googleDictEndpoint, googleGTXEndpoint, myMemoryEndpoint
	oldClient := translateHTTPClient
	translateHTTPClient = &http.Client{}
	t.Cleanup(func() {
		googleDictEndpoint, googleGTXEndpoint, myMemoryEndpoint = oldDict, oldGTX, oldMy
		translateHTTPClient = oldClient
	})
	googleDictEndpoint, googleGTXEndpoint, myMemoryEndpoint = dict, gtx, mymemory
}

func TestTranslateTextsUsesBatchProvider(t *testing.T) {
	var hits int32
	dict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := len(r.Form["q"]); got != 2 {
			t.Errorf("q 参数个数 = %d, want 2", got)
		}
		_ = json.NewEncoder(w).Encode([]any{
			[]any{"你好", "en"},
			[]any{"世界", "en"},
		})
	}))
	defer dict.Close()
	overrideEndpoints(t, dict.URL, "http://127.0.0.1:1/unused", "http://127.0.0.1:1/unused")

	app := &App{}
	out, source, err := app.translateTexts(context.Background(), []string{"Hello", "World"}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "你好" || out[1] != "世界" {
		t.Fatalf("out = %#v", out)
	}
	if source != "en" {
		t.Fatalf("source = %q", source)
	}

	// 第二次同样的文本应该全部命缓存，不再打上游
	if _, _, err := app.translateTexts(context.Background(), []string{"Hello", "World"}, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("上游请求次数 = %d, want 1（第二次应命中缓存）", got)
	}
}

func TestTranslateTextsFallsBackWhenDictBlocked(t *testing.T) {
	dict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Sorry...", http.StatusTooManyRequests) // 云厂商 IP 上的真实表现
	}))
	defer dict.Close()
	gtx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{
			[]any{[]any{"节点离线", "Node offline", nil, nil, float64(3)}},
			nil,
			"en",
		})
	}))
	defer gtx.Close()
	overrideEndpoints(t, dict.URL, gtx.URL, "http://127.0.0.1:1/unused")

	app := &App{}
	out, source, err := app.translateTexts(context.Background(), []string{"Node offline"}, "zh-CN")
	if err != nil {
		t.Fatalf("应该退化到 gtx 成功，却报错: %v", err)
	}
	if out[0] != "节点离线" || source != "en" {
		t.Fatalf("out = %#v source = %q", out, source)
	}
}

func TestTranslateTextsReportsWhenAllProvidersFail(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTooManyRequests)
	}))
	defer blocked.Close()
	overrideEndpoints(t, blocked.URL, blocked.URL, blocked.URL)

	app := &App{}
	if _, _, err := app.translateTexts(context.Background(), []string{"Brand new sentence " + t.Name()}, "zh-CN"); err == nil {
		t.Fatal("三个上游全挂时必须返回错误，否则前端会显示一段没翻译的原文")
	} else if !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("错误里应带上上游状态码，实际: %v", err)
	}
}

func TestTruncateRunes(t *testing.T) {
	got, truncated := truncateRunes("你好world", 4)
	if got != "你好wo" || !truncated {
		t.Fatalf("truncateRunes() = %q, %v", got, truncated)
	}
	got, truncated = truncateRunes("你好", 4)
	if got != "你好" || truncated {
		t.Fatalf("truncateRunes() = %q, %v", got, truncated)
	}
}
