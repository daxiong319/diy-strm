package subtitle

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"resty.dev/v3"
)

// openSubtitlesAPIBase 是 OpenSubtitles v1 REST 接口地址。
// 旧版 XML-RPC（api.opensubtitles.org）已被官方逐步下线且不再发放新 key，因此只对接 v1。
const openSubtitlesAPIBase = "https://api.opensubtitles.com/api/v1"

// OpenSubtitlesOptions 是构造 OpenSubtitles 字幕源的参数。
// ApiKey 必需；UserAgent 是站点强制要求的调用方标识；Username/Password 仅在用下载配额时必需。
type OpenSubtitlesOptions struct {
	ApiKey    string
	UserAgent string
	Username  string
	Password  string
}

// 站点限制：必需 API Key（无 key 全部 401）；未登录匿名下载配额极低（每日约 5 次），
// 配账号密码后登录换取更高配额；站点强制要求 User-Agent 声明用途，缺失返回 403；
// 下载接口返回站内临时直链（几十秒内有效），拿到链接必须立刻下载不能缓存。
type openSubtitlesProvider struct {
	apiKey      string
	userAgent   string
	client      *resty.Client
	credentials *openSubtitlesCredentials

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// openSubtitlesCredentials 保存登录凭证。
// 只用于登录换 token，不把明文长期留在 provider 之外。
type openSubtitlesCredentials struct {
	Username string
	Password string
}

// NewOpenSubtitlesProvider 构造 OpenSubtitles 字幕源。
func NewOpenSubtitlesProvider(opts OpenSubtitlesOptions, httpOpts httpClientOptions) (Provider, error) {
	client, err := newHTTPClient(httpOpts)
	if err != nil {
		return nil, err
	}
	// 站点要求 UA 能标识调用方。
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = "litepan v1.0"
	}
	client.SetHeader("User-Agent", ua)
	client.SetHeader("Api-Key", strings.TrimSpace(opts.ApiKey))
	client.SetHeader("Content-Type", "application/json")
	client.SetHeader("Accept", "application/json")

	p := &openSubtitlesProvider{
		apiKey:    strings.TrimSpace(opts.ApiKey),
		userAgent: ua,
		client:    client,
	}
	// 密码不 TrimSpace：前后空格可能是密码的一部分。
	if strings.TrimSpace(opts.Username) != "" && opts.Password != "" {
		p.credentials = &openSubtitlesCredentials{
			Username: strings.TrimSpace(opts.Username),
			Password: opts.Password,
		}
	}
	return p, nil
}

func (p *openSubtitlesProvider) Name() string             { return "opensubtitles" }
func (p *openSubtitlesProvider) DisplayName() string      { return "OpenSubtitles" }
func (p *openSubtitlesProvider) RequiresCredential() bool { return true }

type openSubtitlesSearchResponse struct {
	TotalPages int `json:"total_pages"`
	Data       []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Attrs struct {
			SubtitleID     string  `json:"subtitle_id"`
			Language       string  `json:"language"`
			DownloadCount  int64   `json:"download_count"`
			Ratings        float64 `json:"ratings"`
			Release        string  `json:"release"`
			MovieHashMatch bool    `json:"moviehash_match"`
			Files          []struct {
				FileID   int64  `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
			FeatureDetails struct {
				Title      string `json:"title"`
				Year       int    `json:"year"`
				TmdbID     int64  `json:"tmdb_id"`
				ImdbID     string `json:"imdb_id"`
				SeasonNum  int    `json:"season_number"`
				EpisodeNum int    `json:"episode_number"`
			} `json:"feature_details"`
			Uploader struct {
				Name string `json:"name"`
			} `json:"uploader"`
		} `json:"attributes"`
	} `json:"data"`
}

func (p *openSubtitlesProvider) Search(ctx context.Context, req SearchRequest) ([]Candidate, error) {
	if p.apiKey == "" {
		return nil, ErrProviderDisabled
	}
	ctx, cancel := newRequestContext(ctx, 30*time.Second)
	defer cancel()

	params := map[string]string{
		"order_by":        "download_count",
		"order_direction": "desc",
	}
	// 主键优先：有 TMDB/IMDb 就用主键，比标题精确得多。
	switch {
	case req.TmdbId > 0:
		params["tmdb_id"] = strconv.FormatInt(req.TmdbId, 10)
	case strings.TrimSpace(req.ImdbId) != "":
		params["imdb_id"] = strings.TrimPrefix(strings.TrimSpace(req.ImdbId), "tt")
	default:
		query := firstNonEmpty(req.Title, req.OriginalTitle)
		if query == "" {
			return nil, errors.New("OpenSubtitles 检索需要标题或 TMDB/Imdb 主键")
		}
		params["query"] = query
	}

	if req.MediaType == "tvshow" {
		params["type"] = "episode"
		if req.Season > 0 {
			params["season_number"] = strconv.Itoa(req.Season)
		}
		if req.Episode > 0 {
			params["episode_number"] = strconv.Itoa(req.Episode)
		}
	} else {
		params["type"] = "movie"
	}
	if req.Year > 0 {
		params["year"] = strconv.Itoa(req.Year)
	}
	if len(req.Languages) > 0 {
		if langs := toOpenSubtitlesLanguages(req.Languages); len(langs) > 0 {
			params["languages"] = strings.Join(langs, ",")
		}
	}

	resp, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(params).
		Get(openSubtitlesAPIBase + "/subtitles")
	if err != nil {
		return nil, fmt.Errorf("OpenSubtitles 检索失败：%w", err)
	}
	if err := openSubtitlesStatusError(resp.StatusCode()); err != nil {
		return nil, err
	}

	var result openSubtitlesSearchResponse
	if err := decodeJSONBody(resp, &result); err != nil {
		return nil, fmt.Errorf("OpenSubtitles 检索失败：%w", err)
	}

	var out []Candidate
	for _, item := range result.Data {
		// 没有文件条目的结果无法下载，留着就是"点了就失败"的候选。
		if len(item.Attrs.Files) == 0 {
			continue
		}
		file := item.Attrs.Files[0]
		out = append(out, Candidate{
			Provider:      "opensubtitles",
			Slug:          strconv.FormatInt(file.FileID, 10),
			Title:         firstNonEmpty(item.Attrs.Release, file.FileName, item.Attrs.FeatureDetails.Title),
			Language:      NormalizeLanguage(item.Attrs.Language),
			Format:        DetectFormat(file.FileName),
			HashMatched:   item.Attrs.MovieHashMatch,
			ReleaseGroup:  extractReleaseGroup(item.Attrs.Release),
			Publisher:     item.Attrs.Uploader.Name,
			Rating:        item.Attrs.Ratings,
			DownloadCount: item.Attrs.DownloadCount,
			FileName:      file.FileName,
			PageURL:       "https://www.opensubtitles.com/subtitles/" + item.ID,
			Extra: map[string]string{
				"subtitle_id": item.Attrs.SubtitleID,
				"feature":     item.Attrs.FeatureDetails.Title,
			},
		})
	}
	return out, nil
}

// Download 先换取临时直链再立刻抓取。
// 两步之间不做任何等待或排队——直链时效很短。
func (p *openSubtitlesProvider) Download(ctx context.Context, candidate Candidate, destPath string) error {
	ctx, cancel := newRequestContext(ctx, 90*time.Second)
	defer cancel()

	link, err := p.requestDownloadLink(ctx, candidate.Slug)
	if err != nil {
		return fmt.Errorf("OpenSubtitles 下载字幕失败：%w", err)
	}
	resp, err := p.client.R().SetContext(ctx).Get(link)
	if err != nil {
		return fmt.Errorf("OpenSubtitles 下载字幕失败：%w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("OpenSubtitles 下载字幕失败：HTTP %d", resp.StatusCode())
	}
	return writeResponseToFile(resp, destPath)
}

// requestDownloadLink 用 file_id 换取临时直链；有账号时带 token 走更高配额。
func (p *openSubtitlesProvider) requestDownloadLink(ctx context.Context, fileID string) (string, error) {
	req := p.client.R().
		SetContext(ctx).
		SetBody(map[string]any{"file_id": parseFileID(fileID), "sub_format": ""})

	if token, err := p.ensureToken(ctx); err == nil && token != "" {
		req.SetAuthToken(token)
	}

	resp, err := req.Post(openSubtitlesAPIBase + "/download")
	if err != nil {
		return "", fmt.Errorf("OpenSubtitles 获取下载链接失败：%w", err)
	}
	switch resp.StatusCode() {
	case 200:
	case 401:
		return "", errors.New("OpenSubtitles 返回 401：API Key 或账号未授权")
	case 406:
		return "", errors.New("OpenSubtitles 返回 406：下载配额已用尽，请配置账号或稍后再试")
	case 429:
		return "", errors.New("OpenSubtitles 返回 429：请求频率超限")
	default:
		return "", fmt.Errorf("OpenSubtitles 获取下载链接失败：HTTP %d", resp.StatusCode())
	}

	var result struct {
		Link    string `json:"link"`
		Message string `json:"message"`
	}
	if err := decodeJSONBody(resp, &result); err != nil {
		return "", fmt.Errorf("OpenSubtitles 获取下载链接失败：%w", err)
	}
	if strings.TrimSpace(result.Link) == "" {
		return "", fmt.Errorf("OpenSubtitles 未返回下载链接：%s", firstNonEmpty(result.Message, "未知原因"))
	}
	return result.Link, nil
}

// parseFileID 把 slug 解析成 file_id；解析失败返回 0，由站点侧报参数错误。
func parseFileID(slug string) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(slug), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// ensureToken 按需登录换 token；未配置账号时返回空串表示匿名访问。
func (p *openSubtitlesProvider) ensureToken(ctx context.Context) (string, error) {
	if p.credentials == nil {
		return "", nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// token 有效期 24 小时，提前 5 分钟刷新。
	if p.token != "" && time.Now().Before(p.tokenExp) {
		return p.token, nil
	}

	resp, err := p.client.R().
		SetContext(ctx).
		SetBody(map[string]string{
			"username": p.credentials.Username,
			"password": p.credentials.Password,
		}).
		Post(openSubtitlesAPIBase + "/login")
	if err != nil {
		return "", fmt.Errorf("OpenSubtitles 登录失败：%w", err)
	}

	var result struct {
		Token string `json:"token"`
	}
	if resp.StatusCode() != 200 {
		return "", fmt.Errorf("OpenSubtitles 登录失败：HTTP %d", resp.StatusCode())
	}
	if err := decodeJSONBody(resp, &result); err != nil {
		return "", fmt.Errorf("OpenSubtitles 登录失败：%w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("OpenSubtitles 登录失败：HTTP %d", resp.StatusCode())
	}
	p.token = result.Token
	p.tokenExp = time.Now().Add(23*time.Hour + 55*time.Minute)
	return p.token, nil
}

func (p *openSubtitlesProvider) HealthCheck(ctx context.Context) error {
	ctx, cancel := newRequestContext(ctx, 15*time.Second)
	defer cancel()

	resp, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{"query": "test", "languages": "en"}).
		Get(openSubtitlesAPIBase + "/subtitles")
	if err != nil {
		return fmt.Errorf("OpenSubtitles 连通性检查失败：%w", err)
	}
	return openSubtitlesStatusError(resp.StatusCode())
}

// openSubtitlesStatusError 把状态码归一成可读错误。
func openSubtitlesStatusError(code int) error {
	switch code {
	case 200:
		return nil
	case 401:
		return errors.New("OpenSubtitles 返回 401：API Key 无效或已被封禁")
	case 403:
		return errors.New("OpenSubtitles 返回 403：User-Agent 不符合站点要求")
	case 406:
		return errors.New("OpenSubtitles 返回 406：无可返回结果或配额不足")
	case 429:
		return errors.New("OpenSubtitles 返回 429：请求频率超限")
	default:
		return fmt.Errorf("OpenSubtitles 请求失败：HTTP %d", code)
	}
}

// toOpenSubtitlesLanguages 把内部语言值翻译成站点取值（去重保序）。
// 站点区分简繁：zh-cn=简体，zh-tw=繁体。
func toOpenSubtitlesLanguages(langs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range langs {
		var mapped string
		switch NormalizeLanguage(raw) {
		case "zh-cn":
			mapped = "zh-cn"
		case "zh-tw":
			mapped = "zh-tw"
		case "zh":
			mapped = "zh-cn"
		case "en":
			mapped = "en"
		case "ja":
			mapped = "ja"
		case "ko":
			mapped = "ko"
		default:
			continue
		}
		if seen[mapped] {
			continue
		}
		seen[mapped] = true
		out = append(out, mapped)
	}
	return out
}

// extractReleaseGroup 从 release 名里取发布组。
// 组名通常是短标识，过长的多半是文件名里其它片段，一律放弃。
func extractReleaseGroup(release string) string {
	release = strings.TrimSpace(release)
	if release == "" {
		return ""
	}
	idx := strings.LastIndex(release, "-")
	if idx < 0 || idx == len(release)-1 {
		return ""
	}
	group := release[idx+1:]
	if len(group) > 24 || strings.ContainsAny(group, " /\\") {
		return ""
	}
	return group
}
