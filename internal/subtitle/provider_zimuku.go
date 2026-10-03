package subtitle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"resty.dev/v3"
)

// zimukuBase 是字幕库站点地址。
//
// 域名历史上多次更换（zimuku.net → zimuku.la → zimuku.org）；若检索全部失败且报
// 404/连接错误，应优先怀疑域名变更——只需改 zimukuBase 一处。
//
// 站点限制：对高频访问有限流，短时间大量检索会被要求人机校验；本实现不绕过人机校验，
// 被拦时返回明确错误并提示填写 Cookie。站点是服务端渲染页面 + 打包下载（zip），
// 走"详情页解析直链 → 下载 zip"，zip 解包与字幕挑选交给 unpackSubtitleArchive。
// 无需付费 key，但可能需要 Cookie。
const zimukuBase = "https://zimuku.org"

var zimukuSearchPattern = regexp.MustCompile(`(?is)<a[^>]+href="(/detail/[A-Za-z0-9_\-]+\.html)"[^>]*>(.*?)</a>`)
var zimukuDownloadPattern = regexp.MustCompile(`(?is)<a[^>]+href="(/(?:dld|download)/[A-Za-z0-9_\-/\.]+)"`)

var zimukuChallengeMarkers = []string{
	"人机验证",
	"cf-browser-verification",
	"Just a moment",
	"滑动验证",
	"security check",
}

type zimukuProvider struct {
	client *resty.Client
	cookie string
}

// NewZimukuProvider 构造字幕库字幕源。
func NewZimukuProvider(opts httpClientOptions) (Provider, error) {
	cookie := opts.Cookie
	client, err := newHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	client.SetHeader("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	return &zimukuProvider{client: client, cookie: cookie}, nil
}

func (p *zimukuProvider) Name() string             { return "zimuku" }
func (p *zimukuProvider) DisplayName() string      { return "字幕库(zimuku)" }
func (p *zimukuProvider) RequiresCredential() bool { return false }

func (p *zimukuProvider) Search(ctx context.Context, req SearchRequest) ([]Candidate, error) {
	ctx, cancel := newRequestContext(ctx, 30*time.Second)
	defer cancel()

	keyword := firstNonEmpty(req.Title, req.OriginalTitle)
	if keyword == "" {
		return nil, errors.New("字幕库检索需要标题")
	}
	// 剧集条目通常按季划分，检索词带季号显著提高命中率。
	if req.MediaType == "tvshow" && req.Season > 0 {
		keyword = fmt.Sprintf("%s %d", keyword, req.Season)
	}

	resp, err := p.client.R().
		SetContext(ctx).
		Get(zimukuBase + "/search?s=" + url.QueryEscape(keyword))
	if err != nil {
		return nil, fmt.Errorf("字幕库检索失败：%w", err)
	}
	if err := checkZimukuResponse(resp); err != nil {
		return nil, err
	}

	body := resp.String()
	if !strings.Contains(body, "/detail/") {
		return nil, nil
	}
	matches := zimukuSearchPattern.FindAllStringSubmatch(body, 30)
	if len(matches) == 0 {
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
			Provider: "zimuku",
			Slug:     strings.TrimSuffix(strings.TrimPrefix(path, "/detail/"), ".html"),
			Title:    title,
			Language: detectLanguageFromTitle(title),
			Format:   detectFormatFromTitle(title),
			PageURL:  zimukuBase + path,
		})
	}
	return out, nil
}

func (p *zimukuProvider) Download(ctx context.Context, candidate Candidate, destPath string) error {
	ctx, cancel := newRequestContext(ctx, 90*time.Second)
	defer cancel()

	detailURL := fmt.Sprintf("%s/detail/%s.html", zimukuBase, candidate.Slug)
	detailResp, err := p.client.R().SetContext(ctx).Get(detailURL)
	if err != nil {
		return fmt.Errorf("字幕库获取详情页失败：%w", err)
	}
	if err := checkZimukuResponse(detailResp); err != nil {
		return err
	}

	matches := zimukuDownloadPattern.FindAllStringSubmatch(detailResp.String(), 20)
	if len(matches) == 0 {
		return errors.New("字幕库详情页未解析到下载链接，页面结构可能已变化")
	}

	fileResp, err := p.client.R().
		SetContext(ctx).
		SetHeader("Referer", detailURL).
		Get(zimukuBase + matches[0][1])
	if err != nil {
		return fmt.Errorf("字幕库下载字幕失败：%w", err)
	}
	if fileResp.StatusCode() != 200 {
		return fmt.Errorf("字幕库下载字幕失败：HTTP %d", fileResp.StatusCode())
	}
	if looksLikeHTML(fileResp.String()) {
		return errors.New("字幕库下载返回的是网页而非字幕文件，Cookie 可能已失效或需要人机校验")
	}

	// 先读进内存再按魔数决定解包还是直写，避免把 zip 当字幕写进媒体库。
	raw, err := io.ReadAll(io.LimitReader(fileResp.RawResponse.Body, maxSubtitleFileSize))
	if err != nil {
		return fmt.Errorf("读取字幕库响应失败：%w", err)
	}
	return writeSubtitlePayload(raw, destPath, candidate.Format)
}

func (p *zimukuProvider) HealthCheck(ctx context.Context) error {
	ctx, cancel := newRequestContext(ctx, 15*time.Second)
	defer cancel()

	resp, err := p.client.R().SetContext(ctx).Get(zimukuBase + "/")
	if err != nil {
		return fmt.Errorf("字幕库连通性检查失败：%w（若为连接错误，可能是站点域名已变更）", err)
	}
	return checkZimukuResponse(resp)
}

// checkZimukuResponse 把状态码与校验页归一成可读错误。
func checkZimukuResponse(resp *resty.Response) error {
	if resp == nil {
		return errors.New("字幕库响应为空")
	}
	switch resp.StatusCode() {
	case 200:
	case 403:
		return errors.New("字幕库返回 403：访问被拒绝，请填入有效的站点 Cookie")
	case 404:
		return errors.New("字幕库返回 404：站点域名可能已变更或页面不存在")
	case 429:
		return errors.New("字幕库返回 429：请求过于频繁，已触发限流")
	case 503:
		return errors.New("字幕库返回 503：站点维护中或正在下发校验页")
	default:
		return fmt.Errorf("字幕库请求失败：HTTP %d", resp.StatusCode())
	}
	body := resp.String()
	for _, marker := range zimukuChallengeMarkers {
		if strings.Contains(body, marker) {
			return errors.New("字幕库返回了人机校验页：请在浏览器通过校验后把 Cookie 填入配置")
		}
	}
	return nil
}
