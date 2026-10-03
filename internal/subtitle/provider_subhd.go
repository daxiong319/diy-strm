package subtitle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"resty.dev/v3"
)

// subhdBase 是 SubHD 站点地址。
//
// 站点限制（改动前必读）：SubHD 在 Cloudflare 之后，无有效 Cookie 通常返回 403
// 或 JS 挑战页（HTTP 200 但正文是 challenge 脚本）。本实现不做 CF 挑战绕过
// （不引无头浏览器、不用第三方 bypass 服务），只提供"内置 HTTP 抓取 + 可配置 Cookie"：
// Cookie 失效返回明确 403 而非静默失败；站点结构/类名随改版变化时返回"页面结构变化"错误。
// 该来源无需付费 key。
const subhdBase = "https://subhd.tv"

// 用宽松正则而非完整 HTML 解析，类名微调时不至整体失效；
// CF 挑战标记由 checkSubhdResponse 前置拦下。
var subhdSearchPattern = regexp.MustCompile(`(?is)<a[^>]+href="(/d/[A-Za-z0-9]+)"[^>]*>(.*?)</a>`)
var subhdDownloadPattern = regexp.MustCompile(`(?is)<a[^>]+href="(/down/[A-Za-z0-9]+)"`)
var tagStripPattern = regexp.MustCompile(`(?is)<[^>]+>`)
var htmlSpacePattern = regexp.MustCompile(`\s+`)

type subhdProvider struct {
	client *resty.Client
	cookie string
}

// NewSubhdProvider 构造 SubHD 字幕源。
func NewSubhdProvider(opts httpClientOptions) (Provider, error) {
	cookie := opts.Cookie
	client, err := newHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	client.SetHeader("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	return &subhdProvider{client: client, cookie: cookie}, nil
}

func (p *subhdProvider) Name() string             { return "subhd" }
func (p *subhdProvider) DisplayName() string      { return "SubHD" }
func (p *subhdProvider) RequiresCredential() bool { return false }

// subhdSearchResult 是搜索页解析出的中间结果。
type subhdSearchResult struct {
	DetailPath string
	Title      string
	Language   string
	Format     SubtitleFormat
}

func (p *subhdProvider) Search(ctx context.Context, req SearchRequest) ([]Candidate, error) {
	ctx, cancel := newRequestContext(ctx, 30*time.Second)
	defer cancel()

	keyword := firstNonEmpty(req.Title, req.OriginalTitle)
	if keyword == "" {
		return nil, errors.New("SubHD 检索需要标题")
	}
	// 站点按"名称 第x季"聚合，季集信息在详情页内，因此检索词只带季不带集。
	if req.MediaType == "tvshow" && req.Season > 0 {
		keyword = fmt.Sprintf("%s 第%d季", keyword, req.Season)
	}

	resp, err := p.client.R().
		SetContext(ctx).
		Get(subhdBase + "/search?keyword=" + url.QueryEscape(keyword))
	if err != nil {
		return nil, fmt.Errorf("SubHD 检索失败：%w", err)
	}
	if err := checkSubhdResponse(resp); err != nil {
		return nil, err
	}

	body := resp.String()
	matches := subhdSearchPattern.FindAllStringSubmatch(body, 30)
	if len(matches) == 0 {
		if !strings.Contains(body, "/d/") {
			return nil, errors.New("SubHD 搜索页结构可能已变化，未解析到任何字幕条目")
		}
		return nil, nil
	}

	seen := map[string]bool{}
	var out []Candidate
	for _, m := range matches {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		title := cleanHTMLText(m[2])
		out = append(out, Candidate{
			Provider: "subhd",
			Slug:     strings.TrimPrefix(path, "/d/"),
			Title:    title,
			Language: detectLanguageFromTitle(title),
			Format:   detectFormatFromTitle(title),
			PageURL:  subhdBase + path,
		})
	}
	return out, nil
}

func (p *subhdProvider) Download(ctx context.Context, candidate Candidate, destPath string) error {
	ctx, cancel := newRequestContext(ctx, 90*time.Second)
	defer cancel()

	detailURL := subhdBase + "/d/" + candidate.Slug
	detailResp, err := p.client.R().SetContext(ctx).Get(detailURL)
	if err != nil {
		return fmt.Errorf("SubHD 获取详情页失败：%w", err)
	}
	if err := checkSubhdResponse(detailResp); err != nil {
		return err
	}

	matches := subhdDownloadPattern.FindAllStringSubmatch(detailResp.String(), 20)
	if len(matches) == 0 {
		return errors.New("SubHD 详情页未解析到下载链接，页面结构可能已变化")
	}
	downloadPath := matches[0][1]
	if candidate.Format != "" {
		for _, m := range matches {
			if DetectFormat(m[1]) == candidate.Format {
				downloadPath = m[1]
				break
			}
		}
	}

	fileResp, err := p.client.R().
		SetContext(ctx).
		SetHeader("Referer", detailURL).
		Get(subhdBase + downloadPath)
	if err != nil {
		return fmt.Errorf("SubHD 下载字幕失败：%w", err)
	}
	if fileResp.StatusCode() != 200 {
		return fmt.Errorf("SubHD 下载字幕失败：HTTP %d", fileResp.StatusCode())
	}
	if looksLikeHTML(fileResp.String()) {
		return errors.New("SubHD 下载返回的是网页而非字幕文件，Cookie 可能已失效")
	}
	return writeResponseToFile(fileResp, destPath)
}

func (p *subhdProvider) HealthCheck(ctx context.Context) error {
	ctx, cancel := newRequestContext(ctx, 15*time.Second)
	defer cancel()

	resp, err := p.client.R().SetContext(ctx).Get(subhdBase + "/")
	if err != nil {
		return fmt.Errorf("SubHD 连通性检查失败：%w", err)
	}
	if err := checkSubhdResponse(resp); err != nil {
		return err
	}
	if p.cookie == "" {
		// 不阻断，只是提醒：首页能打开不代表检索页不被拦。
		return errors.New("SubHD 未配置 Cookie，检索页可能被反爬拦截（首页可访问）")
	}
	return nil
}

// checkSubhdResponse 把状态码与 Cloudflare 挑战页归一成可读错误。
func checkSubhdResponse(resp *resty.Response) error {
	if resp == nil {
		return errors.New("SubHD 响应为空")
	}
	switch resp.StatusCode() {
	case 200:
	case 403:
		return errors.New("SubHD 返回 403：被 Cloudflare 反爬拦截，请在配置中填入有效的站点 Cookie")
	case 404:
		return errors.New("SubHD 返回 404：目标页面不存在")
	case 429:
		return errors.New("SubHD 返回 429：请求过于频繁")
	case 503:
		return errors.New("SubHD 返回 503：站点正在维护或正在下发挑战页")
	default:
		return fmt.Errorf("SubHD 请求失败：HTTP %d", resp.StatusCode())
	}
	body := resp.String()
	for _, marker := range []string{"cf-browser-verification", "Just a moment", "Enable JavaScript and cookies"} {
		if strings.Contains(body, marker) {
			return errors.New("SubHD 返回了 Cloudflare 挑战页：请在浏览器通过验证后把 Cookie 填入配置")
		}
	}
	return nil
}

// looksLikeHTML 判断响应体是否其实是网页。
func looksLikeHTML(body string) bool {
	sample := body
	if len(sample) > 512 {
		sample = sample[:512]
	}
	lower := strings.ToLower(sample)
	return strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html")
}

// cleanHTMLText 去掉标签与常见实体，折叠空白。
func cleanHTMLText(raw string) string {
	s := tagStripPattern.ReplaceAllString(raw, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
	).Replace(s)
	return strings.TrimSpace(htmlSpacePattern.ReplaceAllString(s, " "))
}

// detectLanguageFromTitle 从标题文本猜语言；猜不到返回空串，
// 由匹配层按"语言未知"处理而不是瞎猜。
func detectLanguageFromTitle(title string) string {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(title, "双语") || strings.Contains(lower, "chs&eng") || strings.Contains(lower, "gb&eng"):
		return "zh-cn"
	case strings.Contains(title, "简体") || strings.Contains(lower, "chs") || strings.Contains(lower, "gb"):
		return "zh-cn"
	case strings.Contains(title, "繁体") || strings.Contains(lower, "cht") || strings.Contains(lower, "big5"):
		return "zh-tw"
	case strings.Contains(title, "中英") || strings.Contains(lower, "eng"):
		return "zh-cn"
	case strings.Contains(title, "英文"):
		return "en"
	}
	return ""
}

// detectFormatFromTitle 从标题文本猜格式，兜底 srt。
func detectFormatFromTitle(title string) SubtitleFormat {
	lower := strings.ToLower(title)
	for _, pair := range []struct {
		token  string
		format SubtitleFormat
	}{
		{"ass", FormatASS},
		{"ssa", FormatSSA},
		{"srt", FormatSRT},
		{"sup", FormatSUP},
		{"sub", FormatSUB},
		{"vtt", FormatVTT},
	} {
		if strings.Contains(lower, pair.token) {
			return pair.format
		}
	}
	return FormatSRT
}
