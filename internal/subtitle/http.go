package subtitle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"resty.dev/v3"
)

// DefaultUserAgent 是访问字幕站点的默认 UA。
// 字幕站点对空 UA 或 Go 默认 UA 敏感，统一伪装成常见浏览器。
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// maxSubtitleFileSize 是单个字幕文件的大小上限。
// 正常文本字幕几百 KB，sup 也很少超 100MB；设上限是防止被误导下载视频/超大响应耗尽磁盘。
const maxSubtitleFileSize = 128 << 20

// httpClientOptions 是构造 resty 客户端的可选项。
type httpClientOptions struct {
	Timeout   time.Duration
	Proxy     string
	UserAgent string
	Cookie    string
	Referer   string
}

// newHTTPClient 按选项构造 resty 客户端。
func newHTTPClient(opts httpClientOptions) (*resty.Client, error) {
	client := resty.New()
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	client.SetTimeout(opts.Timeout)
	// 字幕源多为小站点，自动重试会放大压力，是否重试交给上层决定。
	client.SetRetryCount(0)

	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}
	client.SetHeader("User-Agent", ua)
	client.SetHeader("Accept", "*/*")
	if opts.Referer != "" {
		client.SetHeader("Referer", opts.Referer)
	}
	if opts.Cookie != "" {
		client.SetHeader("Cookie", opts.Cookie)
	}
	if strings.TrimSpace(opts.Proxy) != "" {
		proxyURL, err := url.Parse(opts.Proxy)
		if err != nil {
			return nil, fmt.Errorf("解析字幕代理地址失败：%w", err)
		}
		client.SetProxy(proxyURL.String())
	}
	return client, nil
}

// clientOptionsFromConfig 从字幕配置派生 HTTP 选项。
func clientOptionsFromConfig(cfg *Config) httpClientOptions {
	opts := httpClientOptions{Timeout: 30 * time.Second}
	if cfg == nil {
		return opts
	}
	if cfg.TimeoutSeconds > 0 {
		opts.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	opts.Proxy = cfg.Proxy
	opts.UserAgent = cfg.UserAgent
	return opts
}

// ErrProviderDisabled 表示字幕源未启用或缺少必需凭证。
// 明确拒绝而不是静默降级，让调用方能区分"来源未启用"和"来源查询失败"。
var ErrProviderDisabled = errors.New("字幕源未启用或缺少必需凭证")

// ProviderStatus 是单个字幕源的可用状态。
type ProviderStatus struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Configured  bool   `json:"configured"`
	Healthy     bool   `json:"healthy"`
	Message     string `json:"message"`
	LatencyMs   int64  `json:"latency_ms"`
}

// providerSpec 描述一个字幕源的开关、凭证与构造方式。
type providerSpec struct {
	name        string
	credential  string
	enabled     bool
	requiresKey bool
	build       func(cfg *Config, opts httpClientOptions) (Provider, error)
}

// buildEnabledProviders 构造全部已启用的字幕源。
// 单个来源构造失败只跳过并记日志，不阻断其它来源；返回的 error 汇总构建期错误。
func buildEnabledProviders(cfg *Config, log Logger) ([]Provider, error) {
	specs := providerSpecs(cfg)
	if len(specs) == 0 {
		return nil, nil
	}
	var (
		providers []Provider
		errs      []string
	)
	for _, spec := range specs {
		if !spec.enabled {
			continue
		}
		if spec.requiresKey && strings.TrimSpace(spec.credential) == "" {
			log.Warnf("字幕源 %s 已启用但缺少凭证，已跳过", spec.name)
			continue
		}
		p, err := spec.build(cfg, clientOptionsFromConfig(cfg))
		if err != nil {
			log.Errorf("构建字幕源 %s 失败：%v", spec.name, err)
			errs = append(errs, fmt.Sprintf("%s: %v", spec.name, err))
			continue
		}
		providers = append(providers, p)
	}
	if len(errs) > 0 {
		return providers, errors.New("部分字幕源构建失败：" + strings.Join(errs, "; "))
	}
	return providers, nil
}

// providerSpecs 声明全部字幕源。
// 集中在一处，使"新增来源"= 加一个文件 + 这里加一行。
func providerSpecs(cfg *Config) []providerSpec {
	if cfg == nil {
		return nil
	}
	return []providerSpec{
		{
			name:        "assrt",
			credential:  cfg.AssrtAPIKey,
			enabled:     cfg.AssrtEnabled,
			requiresKey: true,
			build: func(c *Config, opts httpClientOptions) (Provider, error) {
				return NewAssrtProvider(c.AssrtAPIKey, opts)
			},
		},
		{
			name:        "opensubtitles",
			credential:  cfg.OpenSubtitlesAPIKey,
			enabled:     cfg.OpenSubtitlesEnabled,
			requiresKey: true,
			build: func(c *Config, opts httpClientOptions) (Provider, error) {
				return NewOpenSubtitlesProvider(OpenSubtitlesOptions{
					ApiKey:    c.OpenSubtitlesAPIKey,
					UserAgent: c.OpenSubtitlesUserAgent,
					Username:  c.OpenSubtitlesUsername,
					Password:  c.OpenSubtitlesPassword,
				}, opts)
			},
		},
		{
			name:        "subhd",
			credential:  cfg.SubhdCookie,
			enabled:     cfg.SubhdEnabled,
			requiresKey: false,
			build: func(c *Config, opts httpClientOptions) (Provider, error) {
				opts.Cookie = c.SubhdCookie
				opts.Referer = "https://subhd.tv/"
				return NewSubhdProvider(opts)
			},
		},
		{
			name:        "zimuku",
			credential:  cfg.ZimukuCookie,
			enabled:     cfg.ZimukuEnabled,
			requiresKey: false,
			build: func(c *Config, opts httpClientOptions) (Provider, error) {
				opts.Cookie = c.ZimukuCookie
				opts.Referer = "https://zimuku.org/"
				return NewZimukuProvider(opts)
			},
		},
	}
}

// writeResponseToFile 把 HTTP 响应体写到目标路径。
// 先落到 .part 再 rename，避免中断留下半截字幕被后续流程误认为"已有字幕"。
func writeResponseToFile(resp *resty.Response, destPath string) error {
	if resp == nil || resp.RawResponse == nil || resp.RawResponse.Body == nil {
		return errors.New("字幕下载响应为空")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("创建字幕目录失败：%w", err)
	}
	tmp := destPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建临时字幕文件失败：%w", err)
	}
	written, copyErr := io.Copy(f, io.LimitReader(resp.RawResponse.Body, maxSubtitleFileSize))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("写入字幕文件失败：%w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭字幕文件失败：%w", closeErr)
	}
	if written == 0 {
		_ = os.Remove(tmp)
		return errors.New("字幕内容为空")
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存字幕文件失败：%w", err)
	}
	return nil
}

// ComputeOpenSubtitlesHash 计算 OpenSubtitles 的 moviehash。
func ComputeOpenSubtitlesHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开视频文件失败：%w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("读取视频文件信息失败：%w", err)
	}
	size := info.Size()
	if size < 2*64*1024 {
		return "", errors.New("视频文件小于 128KB，无法计算 moviehash")
	}

	hash := uint64(size)
	buf := make([]byte, 64*1024)
	for i := 0; i < 2; i++ {
		offset := int64(0)
		if i == 1 {
			offset = size - int64(len(buf))
		}
		if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("读取视频文件分片失败：%w", err)
		}
		for j := 0; j+8 <= len(buf); j += 8 {
			hash += uint64(buf[j]) | uint64(buf[j+1])<<8 | uint64(buf[j+2])<<16 | uint64(buf[j+3])<<24 |
				uint64(buf[j+4])<<32 | uint64(buf[j+5])<<40 | uint64(buf[j+6])<<48 | uint64(buf[j+7])<<56
		}
	}
	return fmt.Sprintf("%016x", hash), nil
}

// newRequestContext 给外部请求加超时。
// 每个 Provider 都必须自己加一层超时，禁止出现无超时的外部请求。
// ctx 已有 deadline 且剩余时间更短时不叠加，避免把可用窗口截断得更短。
func newRequestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if time.Until(deadline) <= timeout {
			return context.WithCancel(ctx)
		}
	}
	return context.WithTimeout(ctx, timeout)
}

// isNetworkError 判断是否为网络层错误。
func isNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
