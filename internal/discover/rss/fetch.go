package rss

// T16 · feed 抓取层。
//
// 只做三件事：发起 GET、按字节上限读 body、把响应的 Content-Type 字符集
// 传给 Parse。解析策略、HTML 清理、去重键全部在 parse.go / clean.go。
//
// 为什么单列一个文件而不是塞进 service：抓取需要可注入（测试用 httptest，
// 生产用 http.Client），而解析是纯函数。把两者混在一起会让「解析一个
// 本地字符串」也必须准备一个 http.Client，单元测试会很难写。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxFeedBytes 单次抓取的字节上限。
//
// 取 4MB 与 参考实现 的 RSS 侧常量一致（参考实现-capability-map.md:167）。
//
// ⚠️ **超限必须报错，不能静默截断**：截断产生的是半个 XML，encoding/xml
// 会解析失败（表现为「解析失败」），而真实原因其实是「这个 feed 太大了」。
// 更糟的情况是 XML 恰好在前 4MB 内闭合、后面的条目被丢掉 —— 那时解析成功、
// 结果看起来正常，但用户会莫名其妙发现某些条目永远抓不到。
// 报错至少让 last_message 里写明「超过 4MB」。
const MaxFeedBytes int64 = 4 << 20 // 4 MiB

// ErrFeedTooLarge feed 超过字节上限。
var ErrFeedTooLarge = errors.New("feed 体积超过上限")

// Fetcher 抓取一个 feed URL。
type Fetcher struct {
	// Client HTTP 客户端。nil 时用 DefaultClient。
	Client *http.Client
	// MaxBytes 字节上限。<= 0 时用 MaxFeedBytes。
	MaxBytes int64
}

// NewFetcher 构造抓取器。
//
// timeout 走 Client.Timeout，不在 context 上加 —— 因为 sync 单源失败
// 不该影响其它源（验收 ⑦），超时必须只作用于这一个请求。
// 调用方（service）给每次请求独立 ctx。
func NewFetcher(client *http.Client) *Fetcher {
	return &Fetcher{Client: client}
}

// FetchOptions 抓取器可调项。
//
// 单列一个结构体而不是继续加 NewFetcherXxx 变体：上限与超时都是用户设置项
// （mo_rss_max_feed_bytes / mo_rss_http_timeout_seconds），生产路径要按设置构造，
// 而测试只想改其中一个。变体函数会多到没人记得哪个是哪个。
type FetchOptions struct {
	// MaxBytes 字节上限。<= 0 时用 MaxFeedBytes。
	MaxBytes int64
}

// NewFetcherWithOptions 按选项构造抓取器。
func NewFetcherWithOptions(client *http.Client, opt FetchOptions) *Fetcher {
	return &Fetcher{Client: client, MaxBytes: opt.MaxBytes}
}

func (f *Fetcher) client() *http.Client {
	if f != nil && f.Client != nil {
		return f.Client
	}
	return defaultHTTPClient()
}

func (f *Fetcher) maxBytes() int64 {
	if f != nil && f.MaxBytes > 0 {
		return f.MaxBytes
	}
	return MaxFeedBytes
}

// defaultHTTPClient 默认客户端。
//
// 不设全局 DefaultClient（改全局会影响别的出网调用），每次新建。
// 30s 与 connector.DefaultConnectorTimeout 一致，保持单源超时口径统一。
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// FetchResult 一次成功抓取的结果。
type FetchResult struct {
	// Feed 解析后的 feed。
	Feed *Feed
	// Bytes 实际读到的字节数（用于诊断，不写业务逻辑）。
	Bytes int64
	// ContentType 原始响应头，原样返回给上层做诊断展示。
	ContentType string
}

// Fetch 抓取并解析一个 feed URL。
//
// ctx 取消、网络错误、非 2xx、超限、非 UTF-8 全部返回错误 ——
// 每一种错误都被上层转成 last_message 让用户在 UI 上看到，
// **没有任何一种被静默吞掉**（验收 ⑤）。
func (f *Fetcher) Fetch(ctx context.Context, feedURL string) (*FetchResult, error) {
	url := strings.TrimSpace(feedURL)
	if url == "" {
		return nil, errors.New("RSS 地址为空")
	}
	if !isHTTPScheme(url) {
		return nil, fmt.Errorf("RSS 地址必须是 http/https：%s", url)
	}

	// UA 必须给：不少 PT 站对空 UA 直接返回 403 或一段 HTML 落地页，
	// 那样用户看到的是「不是 RSS」，而真实原因是 UA。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("User-Agent", defaultFeedUserAgent)
	req.Header.Set("Accept", `application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8`)
	// 有些站用 Accept-Language 决定返回的编码。
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := f.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("抓取失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 读一点点 body 便于诊断（限 512 字节，别把大 HTML 灌进日志）。
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if s := strings.TrimSpace(string(snippet)); s != "" {
			return nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncateForMessage(s))
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 上限 +1 字节用于区分「正好等于上限」与「超了上限」。
	limit := f.maxBytes()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败：%w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w：超过 %d MB，请换一个条目更少或分页的 feed",
			ErrFeedTooLarge, limit/(1<<20))
	}

	// 字符集优先级：HTTP 头 > XML 声明（Parse 内部再取后者）。
	charset := CharsetFromContentType(resp.Header.Get("Content-Type"))

	feed, err := Parse(data, charset)
	if err != nil {
		return nil, err
	}
	return &FetchResult{Feed: feed, Bytes: int64(len(data)), ContentType: resp.Header.Get("Content-Type")}, nil
}

// defaultFeedUserAgent 抓取用的 UA。
//
// 显式给一个可识别的值而不是留空：留空时不少站点返回一段登录页 HTML，
// 解析结果变成「不是 RSS」—— 用户看到的是错误的原因而不是现象。
const defaultFeedUserAgent = "litepan-rss/1.0 (+https://github.com/litepan)"

func truncateForMessage(s string) string {
	const limit = 120
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}

// ValidateFeedURL 只做 URL 形态校验，不发起请求。
// 保存源之前用它挡掉明显的错误输入，比等第一次轮询失败再报错友好。
func ValidateFeedURL(raw string) error {
	url := strings.TrimSpace(raw)
	if url == "" {
		return errors.New("请填写 RSS 地址")
	}
	if !isHTTPScheme(url) {
		return errors.New("RSS 地址必须是 http/https")
	}
	return nil
}
