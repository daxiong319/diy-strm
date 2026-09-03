package tgchannel

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// [ENCRYPTED_LINK] AES 解密向量
// 被测实现按 tgto123 算法解密（AES-256-CBC、密钥 "123456" 右侧补 ASCII '0'
// 至 32 字节、iv+密文 url-safe base64 去 '='）。算法参数已另用 PyCryptodome
// 按 tgto123 还原源码交叉互解验证一致；此处用同参数固定 IV 加密明文做往返校验。
// ---------------------------------------------------------------------------

var testEncIV = []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}

// testEncKey 与 encryptedLinkKey 同参数：默认密钥 "123456" 右侧补 ASCII '0' 到 32 字节
func testEncKey() []byte {
	b := []byte("123456")
	padded := make([]byte, 32)
	copy(padded, b)
	for i := len(b); i < 32; i++ {
		padded[i] = '0'
	}
	return padded
}

// encForTest 用固定 IV 按被测同款算法加密明文，生成稳定测试密文
func encForTest(plaintext string) string {
	block, _ := aes.NewCipher(testEncKey())
	data := []byte(plaintext)
	if pad := aes.BlockSize - len(data)%aes.BlockSize; pad != aes.BlockSize {
		p := make([]byte, pad)
		for i := range p {
			p[i] = byte(pad)
		}
		data = append(data, p...)
	}
	mode := cipher.NewCBCEncrypter(block, testEncIV)
	mode.CryptBlocks(data, data)
	raw := append(append([]byte{}, testEncIV...), data...)
	return strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(base64.StdEncoding.EncodeToString(raw))
}

func TestDecryptEncryptedLinks(t *testing.T) {
	// 样例明文与 tgto123 频道资源帖格式一致（123FSLinkV2 秒传暗号）
	plain1 := "123FSLinkV2$a90299f35176b5e758c97d13633b4fcc#1739435946#3年Z组银八老师.2025.S01E07.1080p.friDay.WEB-DL.H264.AAC 2.0 {tmdb-222624}.mkv"
	plain2 := "123FSLinkV1$abcdef1234567890#1710000000#Test.Movie.2024.2160p.WEB-DL.DDP5.1.HDR.mkv"

	cipher1 := encForTest(plain1)
	cipher2 := encForTest(plain2)

	wrapped := "[ENCRYPTED_LINK_START]" + cipher1 + "[ENCRYPTED_LINK_END] " +
		"含普通文本 [ENCRYPTED_LINK_START]" + cipher2 + "[ENCRYPTED_LINK_END]"
	got := DecryptEncryptedLinks(wrapped)
	if !strings.Contains(got, plain1) || !strings.Contains(got, plain2) {
		t.Fatalf("解密结果缺少明文：\n输入: %s\n输出: %s", wrapped, got)
	}
	if !strings.Contains(got, "含普通文本") {
		t.Fatalf("解密丢失周边普通文本：%s", got)
	}
	if strings.Contains(got, "[ENCRYPTED_LINK_START]") {
		t.Fatalf("解密后仍残留 ENCRYPTED_LINK 标记：%s", got)
	}
}

func TestDecryptEncryptedLinksKeepInvalid(t *testing.T) {
	// 无法解密的暗号（长度不对/非法 base64）应保留原文不 panic
	in := "前缀[ENCRYPTED_LINK_START]not-a-valid-cipher[ENCRYPTED_LINK_END]后缀"
	got := DecryptEncryptedLinks(in)
	if got != in {
		t.Fatalf("非法暗号应保留原文：%s != %s", got, in)
	}
}

func TestHasFastShareMarker(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"123FSLinkV2$a90299f35176b5e758c97d13633b4fcc#1739435946#影片.mkv", true},
		{"123FLCPV1$abc#123#合集", true},
		{"123FSLinkV1$abcd", true},
		{"下载地址：https://www.123pan.com/s/abc123", false},
		{"普通文字没有暗号", false},
	}
	for _, c := range cases {
		if got := HasFastShareMarker(c.text); got != c.want {
			t.Errorf("HasFastShareMarker(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 分页 mock：用可替换 transport 拦截 https://t.me/s/ 请求，按 ?before= 返回预设页
// ---------------------------------------------------------------------------

// mockTMPageHTML 生成一页 t.me/s 风格的帖子 HTML（ids 按新→旧排列）
func mockTMPageHTML(ids ...int) string {
	var sb strings.Builder
	for _, id := range ids {
		sb.WriteString(fmt.Sprintf(
			`<div class="tgme_widget_message" data-post="mockchan/%d"><div class="tgme_widget_message_text">post %d</div><time datetime="2026-01-01T00:00:00Z"></time></div>`,
			id, id))
	}
	return sb.String()
}

type pageRoundTripper struct {
	pages map[string][]int // key: ?before= 值（"" 为首页）
	urls  []string         // 记录的请求 URL（含 before 参数）
}

func (h *pageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	h.urls = append(h.urls, req.URL.String())
	ids := h.pages[req.URL.Query().Get("before")]
	body := mockTMPageHTML(ids...)
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// mockChannelPages 构造 4 段历史：1100..1089 共 12 帖（每页 4 帖），之后空页
func mockChannelPages() *pageRoundTripper {
	return &pageRoundTripper{pages: map[string][]int{
		"":       {1100, 1099, 1098, 1097},
		"1097":   {1096, 1095, 1094, 1093},
		"1093":   {1092, 1091, 1090, 1089},
		"1089":   {}, // 空页：无更多历史
	}}
}

func withMockChannel(t *testing.T, rt *pageRoundTripper) func() {
	t.Helper()
	orig := channelHTTPClient
	channelHTTPClient = &http.Client{Transport: rt}
	return func() { channelHTTPClient = orig }
}

func idsOf(posts []ChannelPost) []string {
	out := make([]string, 0, len(posts))
	for _, p := range posts {
		out = append(out, p.PostID)
	}
	return out
}

func wantIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// 单页模式（stopID="" 只抓首页）—— 与 ParseChannelPage / 预览行为一致
func TestParseChannelPageRangeSinglePage(t *testing.T) {
	rt := mockChannelPages()
	restore := withMockChannel(t, rt)
	defer restore()

	posts, pages, err := ParseChannelPageRange(context.Background(), "@mockchan", "", 1)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if pages != 1 {
		t.Fatalf("pages = %d, want 1", pages)
	}
	if !wantIDs(idsOf(posts), "1100", "1099", "1098", "1097") {
		t.Fatalf("单页模式帖子不符：%v", idsOf(posts))
	}
}

// 命中游标边界截断（游标 1095 落在第 2 页中间）
func TestParseChannelPageRangeHitCursor(t *testing.T) {
	rt := mockChannelPages()
	restore := withMockChannel(t, rt)
	defer restore()

	posts, pages, err := ParseChannelPageRange(context.Background(), "mockchan", "1095", 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want 2", pages)
	}
	if !wantIDs(idsOf(posts), "1100", "1099", "1098", "1097", "1096") {
		t.Fatalf("截断结果不符：%v", idsOf(posts))
	}
}

// 翻到底（游标极旧）：直到空页才停
func TestParseChannelPageRangeUntilEmpty(t *testing.T) {
	rt := mockChannelPages()
	restore := withMockChannel(t, rt)
	defer restore()

	posts, pages, err := ParseChannelPageRange(context.Background(), "mockchan", "1", 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if pages != 4 {
		t.Fatalf("pages = %d, want 4（3 页内容 + 1 空页）", pages)
	}
	if len(posts) != 12 {
		t.Fatalf("共 %d 帖, want 12", len(posts))
	}
}

// maxPages 限制：超限即停，不翻到底
func TestParseChannelPageRangeMaxPages(t *testing.T) {
	rt := mockChannelPages()
	restore := withMockChannel(t, rt)
	defer restore()

	posts, pages, err := ParseChannelPageRange(context.Background(), "mockchan", "1", 2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want 2", pages)
	}
	if len(posts) != 8 {
		t.Fatalf("共 %d 帖, want 8", len(posts))
	}
	// 不得发出超过 maxPages 的请求
	if len(rt.urls) != 2 {
		t.Fatalf("实际请求 %d 次, want 2", len(rt.urls))
	}
}

// 频道失效：t.me 301 重定向回首页 → 报错（引擎据此提示频道失效）
func TestParseChannelPageRangeChannelGone(t *testing.T) {
	orig := channelHTTPClient
	channelHTTPClient = &http.Client{
		Timeout:       45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusMovedPermanently,
				Header:     http.Header{"Location": []string{"https://t.me/"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
	}
	defer func() { channelHTTPClient = orig }()

	_, _, err := ParseChannelPageRange(context.Background(), "gonechan", "", 1)
	if err == nil || !strings.Contains(err.Error(), "频道已失效") {
		t.Fatalf("err = %v, want 频道已失效提示", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// 频道名归一化：@ 前缀 / t.me 链接 / 尾斜杠都能解析到同一页
func TestParseChannelPageRangeNormalizeName(t *testing.T) {
	rt := mockChannelPages()
	restore := withMockChannel(t, rt)
	defer restore()

	for _, name := range []string{"mockchan", "@mockchan", "https://t.me/mockchan", "https://t.me/s/mockchan/", "t.me/mockchan"} {
		posts, _, err := ParseChannelPageRange(context.Background(), name, "", 1)
		if err != nil {
			t.Fatalf("name %q err = %v", name, err)
		}
		if len(posts) != 4 {
			t.Fatalf("name %q 帖子数 = %d, want 4", name, len(posts))
		}
	}
	// 全部请求应落在同一 URL（不带参数）
	for _, u := range rt.urls {
		if !strings.HasPrefix(u, "https://t.me/s/mockchan") {
			t.Fatalf("请求 URL 异常：%s", u)
		}
	}
}
