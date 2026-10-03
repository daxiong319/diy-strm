package subtitle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"resty.dev/v3"
)

// assrtAPIBase 是射手网开放接口地址。
//
// 站点限制：必须自备 API token（无 token 全部 401）；免费额度有频率限制，
// 429 时不做重试直接报错（把是否等待交给上层，避免整理流程长时间阻塞）；
// 下载接口返回的第三方网盘/直链有时效性，失败应重新检索而不是重试旧链接。
const assrtAPIBase = "https://api.assrt.net/v1"

type assrtProvider struct {
	apiKey string
	client *resty.Client
}

// NewAssrtProvider 构造射手网字幕源。
func NewAssrtProvider(apiKey string, opts httpClientOptions) (Provider, error) {
	client, err := newHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	return &assrtProvider{apiKey: strings.TrimSpace(apiKey), client: client}, nil
}

func (p *assrtProvider) Name() string             { return "assrt" }
func (p *assrtProvider) DisplayName() string      { return "射手网(assrt)" }
func (p *assrtProvider) RequiresCredential() bool { return true }

type assrtSearchResponse struct {
	Status int    `json:"status"`
	Err    string `json:"err"`
	Sub    struct {
		Subs []assrtSub `json:"subs"`
	} `json:"sub"`
}

type assrtSub struct {
	ID          int64  `json:"id"`
	NativeName  string `json:"native_name"`
	Title       string `json:"title"`
	Subtype     string `json:"subtype"`
	UploadTime  string `json:"upload_time"`
	ReleaseSite string `json:"release_site"`
	VoteScore   int    `json:"vote_score"`
	DownCount   int64  `json:"down_count"`
	Uploader    string `json:"uploader"`
	Lang        struct {
		LangAbbr  string `json:"langabbr"`
		LangShort string `json:"langshort"`
	} `json:"lang"`
}

type assrtDetailResponse struct {
	Status int    `json:"status"`
	Err    string `json:"err"`
	Sub    struct {
		Subs []assrtDetailSub `json:"subs"`
	} `json:"sub"`
}

type assrtDetailSub struct {
	ID        int64       `json:"id"`
	NativeNam string      `json:"native_name"`
	FileList  []assrtFile `json:"filelist"`
}

type assrtFile struct {
	FID   int64  `json:"fid"`
	FName string `json:"f"`
	URL   string `json:"url"`
}

// Search 按由紧到松的关键词逐个检索，首个有结果的关键词即停止放宽。
func (p *assrtProvider) Search(ctx context.Context, req SearchRequest) ([]Candidate, error) {
	if p.apiKey == "" {
		return nil, ErrProviderDisabled
	}
	ctx, cancel := newRequestContext(ctx, 30*time.Second)
	defer cancel()

	var (
		all     []Candidate
		lastErr error
	)
	for _, keyword := range buildTitleQueries(req) {
		subs, err := p.searchByKeyword(ctx, keyword)
		if err != nil {
			if isAssrtFatal(err) {
				return nil, err
			}
			lastErr = err
			continue
		}
		all = append(all, subs...)
		if len(all) > 0 {
			// 首个关键词已有结果就不再放宽，避免混入无关影片的字幕。
			break
		}
	}
	if len(all) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, nil
	}
	return dedupeCandidates(all), nil
}

// isAssrtFatal 判断错误是否属于"继续换关键词也没用"的类别。
func isAssrtFatal(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "401") || strings.Contains(msg, "429") || strings.Contains(msg, "额度")
}

// buildTitleQueries 生成由紧到松的检索关键词列表。
func buildTitleQueries(req SearchRequest) []string {
	base := strings.TrimSpace(req.Title)
	if base == "" {
		base = strings.TrimSpace(req.OriginalTitle)
	}
	alt := strings.TrimSpace(req.OriginalTitle)

	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	if req.MediaType == "tvshow" && req.Season > 0 && req.Episode > 0 {
		add(fmt.Sprintf("%s S%02dE%02d", base, req.Season, req.Episode))
		add(fmt.Sprintf("%s 第%d季第%d集", base, req.Season, req.Episode))
	}
	add(base)
	if alt != "" && alt != base {
		add(alt)
	}
	return out
}

func (p *assrtProvider) searchByKeyword(ctx context.Context, keyword string) ([]Candidate, error) {
	resp, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"token":    p.apiKey,
			"q":        keyword,
			"cnt":      "20",
			"pos":      "0",
			"no_muxer": "1",
		}).
		Get(assrtAPIBase + "/sub/search")
	if err != nil {
		return nil, fmt.Errorf("射手网检索失败：%w", err)
	}
	switch resp.StatusCode() {
	case 401:
		return nil, errors.New("射手网返回 401：API Token 无效或已过期")
	case 429:
		return nil, errors.New("射手网返回 429：请求过于频繁，已触发额度限制")
	}
	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("射手网检索失败：HTTP %d", resp.StatusCode())
	}

	var result assrtSearchResponse
	if err := decodeJSONBody(resp, &result); err != nil {
		return nil, fmt.Errorf("射手网检索失败：%w", err)
	}
	if result.Status != 0 {
		msg := strings.TrimSpace(result.Err)
		if msg == "" {
			msg = "未知错误"
		}
		return nil, fmt.Errorf("射手网检索失败：%s", msg)
	}

	out := make([]Candidate, 0, len(result.Sub.Subs))
	for _, sub := range result.Sub.Subs {
		out = append(out, Candidate{
			Provider:      "assrt",
			Slug:          strconv.FormatInt(sub.ID, 10),
			Title:         firstNonEmpty(sub.NativeName, sub.Title),
			Language:      NormalizeLanguage(firstNonEmpty(sub.Lang.LangAbbr, sub.Lang.LangShort)),
			Format:        DetectFormat(sub.Subtype),
			ReleaseGroup:  sub.ReleaseSite,
			Publisher:     sub.Uploader,
			Rating:        float64(sub.VoteScore),
			DownloadCount: sub.DownCount,
			FileName:      sub.NativeName,
			Extra: map[string]string{
				"upload_time": sub.UploadTime,
				"keyword":     keyword,
			},
		})
	}
	return out, nil
}

// Download 先查详情拿到真实文件地址，再下载并落盘。
func (p *assrtProvider) Download(ctx context.Context, candidate Candidate, destPath string) error {
	ctx, cancel := newRequestContext(ctx, 60*time.Second)
	defer cancel()

	resp, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"token": p.apiKey,
			"id":    candidate.Slug,
		}).
		Get(assrtAPIBase + "/sub/detail")
	if err != nil {
		return fmt.Errorf("射手网获取字幕详情失败：%w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("射手网获取字幕详情失败：HTTP %d", resp.StatusCode())
	}

	var detail assrtDetailResponse
	if err := decodeJSONBody(resp, &detail); err != nil {
		return fmt.Errorf("射手网获取字幕详情失败：%w", err)
	}
	if detail.Status != 0 {
		return fmt.Errorf("射手网获取字幕详情失败：%s", strings.TrimSpace(detail.Err))
	}
	if len(detail.Sub.Subs) == 0 || len(detail.Sub.Subs[0].FileList) == 0 {
		return errors.New("射手网字幕详情中没有可下载文件")
	}

	file := pickAssrtFile(detail.Sub.Subs[0].FileList, candidate.Format)
	if strings.TrimSpace(file.URL) == "" {
		return errors.New("射手网字幕文件缺少下载地址")
	}
	return p.downloadURL(ctx, file.URL, destPath)
}

// pickAssrtFile 挑一个合适的文件：
// 指定格式时优先同格式；否则优先能做时间轴校正的文本格式，sup 放最后。
func pickAssrtFile(files []assrtFile, want SubtitleFormat) assrtFile {
	if want != "" {
		for _, f := range files {
			if DetectFormat(firstNonEmpty(f.FName, f.URL)) == want {
				return f
			}
		}
	}
	for _, f := range files {
		switch DetectFormat(firstNonEmpty(f.FName, f.URL)) {
		case FormatASS, FormatSRT, FormatSSA, FormatVTT, FormatSUB:
			return f
		}
	}
	return files[0]
}

func (p *assrtProvider) downloadURL(ctx context.Context, rawURL, destPath string) error {
	if _, err := url.Parse(rawURL); err != nil {
		return fmt.Errorf("射手网下载地址非法：%w", err)
	}
	resp, err := p.client.R().SetContext(ctx).Get(rawURL)
	if err != nil {
		return fmt.Errorf("射手网下载字幕失败：%w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("射手网下载字幕失败：HTTP %d", resp.StatusCode())
	}
	return writeResponseToFile(resp, destPath)
}

// HealthCheck 用最小查询验证 token 是否可用。
func (p *assrtProvider) HealthCheck(ctx context.Context) error {
	if p.apiKey == "" {
		return ErrProviderDisabled
	}
	ctx, cancel := newRequestContext(ctx, 15*time.Second)
	defer cancel()

	resp, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{"token": p.apiKey, "q": "test", "cnt": "1"}).
		Get(assrtAPIBase + "/sub/search")
	if err != nil {
		return fmt.Errorf("射手网连通性检查失败：%w", err)
	}
	switch resp.StatusCode() {
	case 200:
		var result assrtSearchResponse
		if err := decodeJSONBody(resp, &result); err != nil {
			return fmt.Errorf("射手网连通性检查失败：%w", err)
		}
		if result.Status != 0 {
			return fmt.Errorf("射手网连通性检查失败：%s", firstNonEmpty(result.Err, "未知错误"))
		}
		return nil
	case 401:
		return errors.New("射手网连通性检查失败：API Token 无效")
	case 429:
		return errors.New("射手网连通性检查失败：请求频率超限")
	default:
		return fmt.Errorf("射手网连通性检查失败：HTTP %d", resp.StatusCode())
	}
}

// dedupeCandidates 按 provider+slug 去重，保留先出现者。
// 多关键词检索会重复带回同一条字幕。
func dedupeCandidates(in []Candidate) []Candidate {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		key := c.Provider + "\x00" + c.Slug
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// firstNonEmpty 返回首个 TrimSpace 后非空的字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
