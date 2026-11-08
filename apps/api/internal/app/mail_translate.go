package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"golang.org/x/net/html"
)

// 三个上游按顺序尝试，第一个成功即返回。
//
//  1. clients5.google.com 的 dict-chrome-ex 接口——Chrome 划词翻译用的那个，
//     能一次带多个 q 批量翻，且数据中心 IP 上仍然可用（gtx 接口在云厂商 IP 上
//     一律返回 429，就是「点击翻译会翻译失败」的根因）。
//  2. translate.googleapis.com 的 gtx 接口——老接口，住宅 IP 上很稳，云上常 429。
//  3. MyMemory——完全独立的第三方，单次请求有长度限制，只用来兜底短文本。
var (
	googleDictEndpoint = "https://clients5.google.com/translate_a/t"
	googleGTXEndpoint  = "https://translate.googleapis.com/translate_a/single"
	myMemoryEndpoint   = "https://api.mymemory.translated.net/get"

	translateHTTPClient = &http.Client{Timeout: 12 * time.Second}
)

const (
	translateBatchTexts       = 16   // 一次批量请求最多带多少段文本
	translateBatchRunes       = 4000 // 一次批量请求最多多少字符（URL/表单都别撑爆）
	translateMyMemoryMax      = 450  // MyMemory 单次上限经验值
	translateMyMemoryMaxItems = 40   // 兜底逐条翻译的条数上限，别让一封 HTML 邮件打成几千次外呼
	translateCacheSize        = 2000 // 译文缓存条数
)

type translateMailMessageRequest struct {
	TargetLanguage string `json:"targetLanguage"`
}

type translateMailMessageResponse struct {
	TranslatedText string `json:"translatedText"`
	TranslatedHTML string `json:"translatedHtml,omitempty"`
	SourceLanguage string `json:"sourceLanguage,omitempty"`
	TargetLanguage string `json:"targetLanguage"`
	Truncated      bool   `json:"truncated"`
}

// 译文缓存：同一封邮件来回切原文/译文、或多人看同一封时不用重复打上游。
var translateCache = struct {
	sync.Mutex
	items map[string]string
}{items: make(map[string]string)}

func translateCacheKey(target, text string) string {
	return target + "\x00" + text
}

func translateCacheGet(target, text string) (string, bool) {
	translateCache.Lock()
	defer translateCache.Unlock()
	value, ok := translateCache.items[translateCacheKey(target, text)]
	return value, ok
}

func translateCachePut(target, text, translated string) {
	translateCache.Lock()
	defer translateCache.Unlock()
	if len(translateCache.items) >= translateCacheSize {
		translateCache.items = make(map[string]string)
	}
	translateCache.items[translateCacheKey(target, text)] = translated
}

func (a *App) handleTranslateMailMessage(w http.ResponseWriter, r *http.Request) {
	if !a.config().MailTranslateEnabled {
		respondError(w, http.StatusForbidden, "mail translation is disabled")
		return
	}
	var req translateMailMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	target := normalizeTranslateTarget(req.TargetLanguage)
	if target == "" {
		respondError(w, http.StatusBadRequest, "unsupported target language")
		return
	}
	msg, err := a.loadMessageForReadRequest(r, chi.URLParam(r, "id"), true)
	if err != nil {
		respondError(w, http.StatusNotFound, "message not found")
		return
	}
	a.translateMailContent(w, r, msg.BodyText, msg.BodyHTML, msg.Snippet, target)
}

func (a *App) translateMailContent(w http.ResponseWriter, r *http.Request, bodyText, bodyHTML, snippet, target string) {
	text := strings.TrimSpace(bodyText)
	if text == "" {
		text = strings.TrimSpace(snippet)
	}
	if text == "" {
		respondError(w, http.StatusBadRequest, "message has no translatable text")
		return
	}
	maxChars := a.config().MailTranslateMaxChars
	if maxChars <= 0 {
		maxChars = 8000
	}
	text, truncated := truncateRunes(text, maxChars)
	translated, source, err := a.translateTexts(r.Context(), []string{text}, target)
	if err != nil || len(translated) == 0 {
		a.log.Warn("mail translation failed", "target", target, "error", err)
		respondError(w, http.StatusBadGateway, "translation service unavailable, please retry later")
		return
	}
	translatedHTML := ""
	if strings.TrimSpace(bodyHTML) != "" {
		translatedHTML, _ = a.translateHTMLTextNodes(r.Context(), a.policy, bodyHTML, target, maxChars)
	}
	respondJSON(w, http.StatusOK, translateMailMessageResponse{
		TranslatedText: translated[0],
		TranslatedHTML: translatedHTML,
		SourceLanguage: source,
		TargetLanguage: target,
		Truncated:      truncated,
	})
}

func (a *App) handleTranslateExternalIMAPMessage(w http.ResponseWriter, r *http.Request) {
	if !a.config().MailTranslateEnabled {
		respondError(w, http.StatusForbidden, "mail translation is disabled")
		return
	}
	var req translateMailMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	target := normalizeTranslateTarget(req.TargetLanguage)
	if target == "" {
		respondError(w, http.StatusBadRequest, "unsupported target language")
		return
	}
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
		a.translateMailContent(w, r, msg.BodyText, msg.BodyHTML, msg.Snippet, target)
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
		respondError(w, http.StatusBadRequest, "failed to load remote message")
		return
	}
	stored, _, err := a.parseMaildirMessage(raw, account.Username)
	text := ""
	bodyHTML := ""
	if err == nil {
		text = strings.TrimSpace(stored.BodyText)
		if text == "" {
			text = strings.TrimSpace(stored.Snippet)
		}
		bodyHTML = stored.BodyHTML
	}
	if text == "" {
		text = strings.TrimSpace(remote.Snippet)
	}
	a.translateMailContent(w, r, text, bodyHTML, remote.Snippet, target)
}

// translateTexts 批量翻译：返回与入参等长的切片（失败的位置保留原文）。
func (a *App) translateTexts(ctx context.Context, texts []string, target string) ([]string, string, error) {
	out := make([]string, len(texts))
	copy(out, texts)

	// 先吃缓存，命中的不再打上游
	pending := make([]int, 0, len(texts))
	source := ""
	for i, text := range texts {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		if cached, ok := translateCacheGet(target, trimmed); ok {
			out[i] = cached
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return out, source, nil
	}

	// 1. dict-chrome-ex，可批量
	translated, src, err := googleDictTranslate(ctx, gather(texts, pending), target)
	if err == nil {
		source = src
		for n, i := range pending {
			if n < len(translated) && strings.TrimSpace(translated[n]) != "" {
				out[i] = translated[n]
				translateCachePut(target, strings.TrimSpace(texts[i]), translated[n])
			}
		}
		return out, source, nil
	}
	var failures []string
	failures = append(failures, "dict: "+err.Error())

	// 2. gtx，逐条
	for n, i := range pending {
		value, src, gerr := googleGTXTranslate(ctx, strings.TrimSpace(texts[i]), target)
		if gerr != nil {
			failures = append(failures, "gtx: "+gerr.Error())
			break
		}
		if src != "" && source == "" {
			source = src
		}
		out[i] = value
		translateCachePut(target, strings.TrimSpace(texts[i]), value)
		if n == len(pending)-1 {
			return out, source, nil
		}
	}

	// 3. MyMemory，逐条（短文本才行）。
	//
	// 必须设上限：一封 HTML 邮件能切出几千个文本节点，每个节点一次串行外呼，
	// 落到这一级（云厂商 IP 上 gtx 基本全 429）时就是几千次请求、几十分钟，
	// 一个前台请求占死一个 goroutine，还会连带拖慢 Telegram 翻译。
	attempted := 0
	for _, i := range pending {
		if attempted >= translateMyMemoryMaxItems {
			break
		}
		trimmed := strings.TrimSpace(texts[i])
		if utf8.RuneCountInString(trimmed) > translateMyMemoryMax {
			continue
		}
		attempted++
		value, merr := myMemoryTranslate(ctx, trimmed, target)
		if merr != nil {
			failures = append(failures, "mymemory: "+merr.Error())
			continue
		}
		out[i] = value
		translateCachePut(target, trimmed, value)
	}
	// 只要有一段真的翻出来了就算成功。以前用「译文 == 原文」判断失败，
	// 但专有名词、`Re:` 这种短句翻完本来就等于原文，会被误判成整单失败（502）。
	translatedAny := false
	for _, i := range pending {
		if out[i] != texts[i] {
			translatedAny = true
			break
		}
	}
	if !translatedAny {
		return out, source, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return out, source, nil
}

func gather(texts []string, indexes []int) []string {
	out := make([]string, 0, len(indexes))
	for _, i := range indexes {
		out = append(out, strings.TrimSpace(texts[i]))
	}
	return out
}

// googleDictTranslate 批量调用 dict-chrome-ex，返回按入参顺序排列的译文。
func googleDictTranslate(ctx context.Context, texts []string, target string) ([]string, string, error) {
	if len(texts) == 0 {
		return nil, "", errors.New("empty input")
	}
	results := make([]string, 0, len(texts))
	source := ""
	for start := 0; start < len(texts); {
		end, runes := start, 0
		for end < len(texts) {
			n := utf8.RuneCountInString(texts[end])
			if end > start && (end-start >= translateBatchTexts || runes+n > translateBatchRunes) {
				break
			}
			runes += n
			end++
		}
		part, src, err := googleDictBatch(ctx, texts[start:end], target)
		if err != nil {
			return nil, "", err
		}
		if source == "" {
			source = src
		}
		results = append(results, part...)
		start = end
	}
	return results, source, nil
}

func googleDictBatch(ctx context.Context, texts []string, target string) ([]string, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	form := url.Values{}
	for _, text := range texts {
		form.Add("q", text)
	}
	endpoint := googleDictEndpoint + "?" + url.Values{
		"client": {"dict-chrome-ex"},
		"sl":     {"auto"},
		"tl":     {target},
	}.Encode()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/plain,*/*")

	res, err := translateHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 512))
		return nil, "", fmt.Errorf("dict endpoint status %d", res.StatusCode)
	}
	var raw any
	if err := json.NewDecoder(io.LimitReader(res.Body, 8*1024*1024)).Decode(&raw); err != nil {
		return nil, "", err
	}
	return parseChromeDictResponse(raw)
}

// parseChromeDictResponse 解析 [["译文","源语言"], ...]；单条时也可能是 ["译文","源语言"]。
func parseChromeDictResponse(raw any) ([]string, string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, "", errors.New("unexpected response type")
	}
	// 扁平形态：["译文","en"]
	if len(items) == 2 {
		if first, ok := items[0].(string); ok {
			if lang, ok := items[1].(string); ok {
				return []string{first}, lang, nil
			}
		}
	}
	out := make([]string, 0, len(items))
	source := ""
	for _, item := range items {
		pair, ok := item.([]any)
		if !ok || len(pair) == 0 {
			continue
		}
		text, ok := pair[0].(string)
		if !ok {
			continue
		}
		if len(pair) > 1 && source == "" {
			if lang, ok := pair[1].(string); ok {
				source = lang
			}
		}
		out = append(out, text)
	}
	if len(out) == 0 {
		return nil, "", errors.New("empty dict response")
	}
	return out, source, nil
}

func googleGTXTranslate(ctx context.Context, text, target string) (string, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	params := url.Values{}
	params.Set("client", "gtx")
	params.Set("sl", "auto")
	params.Set("tl", target)
	params.Set("dt", "t")
	params.Set("q", text)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, googleGTXEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := translateHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return "", "", fmt.Errorf("gtx status %d", res.StatusCode)
	}
	var raw any
	if err := json.NewDecoder(io.LimitReader(res.Body, 8*1024*1024)).Decode(&raw); err != nil {
		return "", "", err
	}
	translated, source := parseGoogleTranslateResponse(raw)
	translated = strings.TrimSpace(translated)
	if translated == "" {
		return "", source, errors.New("empty translation")
	}
	return translated, source, nil
}

func myMemoryTranslate(ctx context.Context, text, target string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	params := url.Values{}
	params.Set("q", text)
	params.Set("langpair", "autodetect|"+myMemoryLang(target))
	params.Set("de", "lanqin@localhost")
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, myMemoryEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := translateHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return "", fmt.Errorf("mymemory status %d", res.StatusCode)
	}
	var payload struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus any `json:"responseStatus"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return "", err
	}
	value := strings.TrimSpace(payload.ResponseData.TranslatedText)
	if value == "" {
		return "", errors.New("empty mymemory translation")
	}
	return value, nil
}

func myMemoryLang(target string) string {
	switch target {
	case "zh-CN":
		return "zh-CN"
	case "zh-TW":
		return "zh-TW"
	default:
		return target
	}
}

// translateHTMLTextNodes 批量翻译正文里的文本节点：先把要翻的节点收集起来，
// 一次请求翻完再写回，避免一封长邮件打几十个请求被上游限流。
func (a *App) translateHTMLTextNodes(ctx context.Context, policy *HTMLPolicy, bodyHTML, target string, maxChars int) (string, error) {
	nodes, err := html.ParseFragment(strings.NewReader(bodyHTML), nil)
	if err != nil {
		return "", err
	}
	remaining := maxChars
	var targets []*html.Node
	var snippets []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && shouldSkipHTMLTranslationElement(n.Data) {
			return
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" && containsTranslatableLetter(text) && remaining > 0 {
				limited, _ := truncateRunes(text, remaining)
				remaining -= utf8.RuneCountInString(limited)
				targets = append(targets, n)
				snippets = append(snippets, limited)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	if len(snippets) > 0 {
		translated, _, err := a.translateTexts(ctx, snippets, target)
		if err != nil {
			return "", err
		}
		for i, n := range targets {
			if i >= len(translated) {
				break
			}
			value := translated[i]
			if strings.TrimSpace(value) == "" {
				continue
			}
			n.Data = strings.Replace(n.Data, snippets[i], value, 1)
		}
	}
	var b bytes.Buffer
	for _, n := range nodes {
		if err := html.Render(&b, n); err != nil {
			return "", err
		}
	}
	if policy != nil {
		return policy.Sanitize(b.String()), nil
	}
	return b.String(), nil
}

func shouldSkipHTMLTranslationElement(tag string) bool {
	switch strings.ToLower(tag) {
	case "script", "style", "code", "pre", "textarea":
		return true
	default:
		return false
	}
}

func containsTranslatableLetter(value string) bool {
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '\u4e00' && r <= '\u9fff') {
			return true
		}
	}
	return false
}

func normalizeTranslateTarget(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "zh", "zh-cn", "zh-hans", "zh_cn":
		return "zh-CN"
	case "zh-tw", "zh-hant", "zh_hk", "zh-hk", "zh-mo":
		return "zh-TW"
	case "en", "en-us", "en-gb":
		return "en"
	default:
		return ""
	}
}

func truncateRunes(value string, max int) (string, bool) {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value, false
	}
	out := make([]rune, 0, max)
	for i, r := range value {
		if len(out) >= max {
			return string(out), i < len(value)
		}
		out = append(out, r)
	}
	return string(out), false
}

func parseGoogleTranslateResponse(raw any) (string, string) {
	root, _ := raw.([]any)
	var b strings.Builder
	if len(root) > 0 {
		if sentences, ok := root[0].([]any); ok {
			for _, item := range sentences {
				parts, ok := item.([]any)
				if !ok || len(parts) == 0 {
					continue
				}
				if s, ok := parts[0].(string); ok {
					b.WriteString(s)
				}
			}
		}
	}
	source := ""
	if len(root) > 2 {
		if s, ok := root[2].(string); ok {
			source = s
		}
	}
	return b.String(), source
}
