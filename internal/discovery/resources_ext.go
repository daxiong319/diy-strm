package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/tgchannel"
)

// ---------------------------------------------------------------------------
// 资源闭环扩展：公开 TG 频道资源搜索 + 离线/转存/磁力动作
// （对齐参考实现 resources/search sources=hdhive|tg + resources/offline|transfer|torrent）
// ---------------------------------------------------------------------------

// SUPPORTED_TRANSFER_PROVIDERS 支持的转存目标网盘（对齐 diy-strm 转存链：123/光鸭/139）
var SUPPORTED_TRANSFER_PROVIDERS = []string{"123", "guangya", "pan139"}

// NormalizeTransferProvider 归一化目标网盘（错误文案对齐参考实现）
func NormalizeTransferProvider(provider string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "guangyapan", "guangya", "gy":
		return "guangya", nil
	case "123", "123pan":
		return "123", nil
	case "139", "pan139", "yidong":
		return "pan139", nil
	case "115":
		return "", fmt.Errorf("目标网盘仅支持 123、光鸭或 139（115 转存链未接入）")
	}
	if p == "" {
		return "", fmt.Errorf("未指定目标网盘")
	}
	return "", fmt.Errorf("目标网盘仅支持 123、光鸭或 139")
}

// TransferShareFn 分享链接转存执行器（由 controller 层注入，接通既有
// saveShareByLink 转存链：解析链接 → 对应网盘账号 → 保存到目录）
var TransferShareFn func(ctx context.Context, text, pwd, sourceType, targetDir string) (title string, total int, err error)

// OfflineLinkFn 磁力/ED2K 离线执行器（由 controller 层注入，接通 qBittorrent）
var OfflineLinkFn func(ctx context.Context, link, savePath string) error

// MediaTransferTargets 影视发现保存目录（settings key media_transfer_targets）
// {123:{folder_path,folder_name}, guangya:{...}, pan139:{...}}
func MediaTransferTargets() map[string]map[string]string {
	out := map[string]map[string]string{}
	raw, _ := mustSettings()[SettingMediaTransferTargets].(map[string]any)
	for provider, v := range raw {
		if m, ok := v.(map[string]any); ok {
			entry := map[string]string{}
			if s, ok := m["folder_path"].(string); ok {
				entry["folder_path"] = s
			}
			if s, ok := m["folder_name"].(string); ok {
				entry["folder_name"] = s
			}
			out[normalizeProviderKey(provider)] = entry
		}
	}
	return out
}

// normalizeProviderKey 网盘键归一
func normalizeProviderKey(p string) string {
	switch strings.ToLower(p) {
	case "guangyapan", "guangya", "gy":
		return "guangya"
	case "123", "123pan":
		return "123"
	case "139", "pan139":
		return "pan139"
	}
	return strings.ToLower(p)
}

// TransferTargetConfigured 保存目录是否已配置
func TransferTargetConfigured(provider string) bool {
	targets := MediaTransferTargets()
	entry, ok := targets[normalizeProviderKey(provider)]
	return ok && strings.TrimSpace(entry["folder_path"]) != ""
}

// TransferTargetDir 目标目录路径
func TransferTargetDir(provider string) string {
	targets := MediaTransferTargets()
	if entry, ok := targets[normalizeProviderKey(provider)]; ok {
		return strings.TrimSpace(entry["folder_path"])
	}
	return ""
}

// TransferShareLink 转存分享链接到目标网盘目录（未注入执行器时明确报错）
func TransferShareLink(ctx context.Context, text, pwd, provider string) (string, int, error) {
	provider, err := NormalizeTransferProvider(provider)
	if err != nil {
		return "", 0, err
	}
	if TransferShareFn == nil {
		return "", 0, fmt.Errorf("转存服务尚未就绪")
	}
	dir := TransferTargetDir(provider)
	if dir == "" {
		return "", 0, fmt.Errorf("请先在「影视发现 - 基础配置」中配置%s保存目录", resourceProviderName(provider))
	}
	return TransferShareFn(ctx, text, pwd, provider, dir)
}

// resourceProviderName 网盘中文名
func resourceProviderName(provider string) string {
	switch provider {
	case "123":
		return "123"
	case "guangya":
		return "光鸭"
	case "pan139":
		return "139"
	}
	return provider
}

// SubmitOfflineLink 提交磁力链接离线（走 qBittorrent；savePath 取设置）
func SubmitOfflineLink(ctx context.Context, link, provider string) error {
	if OfflineLinkFn == nil {
		return fmt.Errorf("离线服务尚未就绪（请在 MoviePilot 设置中配置 qBittorrent）")
	}
	savePath := ""
	if p, err := NormalizeTransferProvider(provider); err == nil {
		savePath = TransferTargetDir(p)
	}
	return OfflineLinkFn(ctx, strings.TrimSpace(link), savePath)
}

// ---------------------------------------------------------------------------
// 公开 TG 频道资源搜索
// ---------------------------------------------------------------------------

// TGResourceChannels 资源检索频道配置（settings key tg_resource_channels）
// {123:[@channel...], guangya:[...], pan139:[...]}
func TGResourceChannels() map[string][]string {
	out := map[string][]string{}
	raw, _ := mustSettings()[SettingTGResourceChannels].(map[string]any)
	for provider, v := range raw {
		list, ok := v.([]any)
		if !ok {
			continue
		}
		channels := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				s = tgchannelNormalizeChannel(s)
				if s != "" {
					channels = append(channels, s)
				}
			}
		}
		if len(channels) > 0 {
			out[normalizeProviderKey(provider)] = channels
		}
	}
	return out
}

// tgchannelNormalizeChannel 归一化频道（去 @ / URL）
func tgchannelNormalizeChannel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimPrefix(s, "https://t.me/s/")
	s = strings.TrimPrefix(s, "https://t.me/")
	s = strings.TrimPrefix(s, "t.me/s/")
	s = strings.TrimPrefix(s, "t.me/")
	s = strings.TrimSuffix(s, "/")
	return s
}

// TGResource 频道资源匹配结果
type TGResource struct {
	Provider   string             `json:"provider"` // 网盘组（123/guangya/pan139）
	Channel    string             `json:"channel"`
	PostID     string             `json:"post_id"`
	MessageURL string             `json:"message_url"`
	PostText   string             `json:"post_text"`
	PostTime   string             `json:"post_time"`
	Link       tgchannel.ShareLink `json:"-"`
}

// TGSearchError 单频道错误（对齐参考实现 errors 结构）
type TGSearchError struct {
	Channel string `json:"channel"`
	Error   string `json:"error"`
}

var (
	tgSearchMu    sync.Mutex
	tgSearchLimit = make(chan struct{}, 3) // 并发频道数上限（对齐参考实现全局限速语义）
)

// SearchTGChannelResources 公开频道资源搜索：
// 每频道并发（限 3）、逐关键词串行抓取 t.me/s?q=，按关键词匹配后提取分享链接。
func SearchTGChannelResources(ctx context.Context, title string, aliases []string, year string) ([]TGResource, []TGSearchError) {
	channelsByProvider := TGResourceChannels()
	if len(channelsByProvider) == 0 {
		return nil, []TGSearchError{{Channel: "", Error: "请先在基础配置中配置至少一个公开 TG 资源检索频道"}}
	}
	terms := tgSearchTerms(title, aliases)
	if len(terms) == 0 {
		return nil, []TGSearchError{{Channel: "", Error: "搜索关键词过短"}}
	}
	// 会话缓存（5 分钟）
	cacheKey := tgSearchCacheKey(channelsByProvider, terms, year)
	if raw := externalCacheGet(cacheKey); raw != "" {
		var cached struct {
			Items  []TGResource   `json:"items"`
			Errors []TGSearchError `json:"errors"`
		}
		if jsonUnmarshal([]byte(raw), &cached) == nil {
			return cached.Items, cached.Errors
		}
	}

	type providerChannels struct {
		provider string
		channels []string
	}
	jobs := make([]providerChannels, 0, len(channelsByProvider))
	for provider, channels := range channelsByProvider {
		jobs = append(jobs, providerChannels{provider: provider, channels: channels})
	}
	// 稳定顺序
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].provider < jobs[j].provider })

	var (
		mu      sync.Mutex
		items   []TGResource
		errs    []TGSearchError
		seenKey = map[string]bool{}
		wg      sync.WaitGroup
	)
	for _, job := range jobs {
		for _, channel := range job.channels {
			wg.Add(1)
			go func(provider, channel string) {
				defer wg.Done()
				tgSearchLimit <- struct{}{}
				defer func() { <-tgSearchLimit }()
				channelItems, channelErrs := searchOneTGChannel(ctx, provider, channel, terms, year)
				mu.Lock()
				defer mu.Unlock()
				for _, item := range channelItems {
					if seenKey[item.Link.URL] {
						continue
					}
					seenKey[item.Link.URL] = true
					items = append(items, item)
				}
				errs = append(errs, channelErrs...)
			}(job.provider, channel)
		}
	}
	wg.Wait()

	externalCacheSet(cacheKey, map[string]any{"items": items, "errors": errs}, 5*time.Minute)
	return items, errs
}

// searchOneTGChannel 单频道逐关键词搜索
func searchOneTGChannel(ctx context.Context, provider, channel string, terms []string, year string) ([]TGResource, []TGSearchError) {
	var items []TGResource
	var searchErr error
	seenPost := map[string]bool{}
	for _, term := range terms {
		posts, _, err := tgchannel.ParseChannelSearch(ctx, channel, term, 1)
		if err != nil {
			searchErr = err
			continue
		}
		for _, post := range posts {
			if seenPost[post.PostID] {
				continue
			}
			seenPost[post.PostID] = true
			// 年份过滤（帖子含其它年份且不含目标年份时跳过）
			if year != "" && strings.Contains(post.Text, year) == false && containsOtherYear(post.Text, year) {
				continue
			}
			links := tgchannel.ExtractShareLinks(post.Text)
			for _, link := range links {
				items = append(items, TGResource{
					Provider:   provider,
					Channel:    channel,
					PostID:     post.PostID,
					MessageURL: fmt.Sprintf("https://t.me/%s/%s", channel, post.PostID),
					PostText:   strings.TrimSpace(post.Text),
					PostTime:   postTimeText(post.Time),
					Link:       link,
				})
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	var errs []TGSearchError
	if searchErr != nil && len(items) == 0 {
		errs = append(errs, TGSearchError{Channel: "@" + channel, Error: "公开频道查询失败：" + searchErr.Error()})
	}
	return items, errs
}

// containsOtherYear 帖子是否包含与目标年份不同的年份标记（用于弱过滤）
func containsOtherYear(text, year string) bool {
	re := regexp.MustCompile(`(19|20)\d{2}`)
	for _, m := range re.FindAllString(text, -1) {
		if m != year {
			return true
		}
	}
	return false
}

// tgSearchTerms 搜索词（主标题 + 别名，≥2 字符，最多 3 个）
func tgSearchTerms(title string, aliases []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, kw := range append([]string{title}, aliases...) {
		kw = strings.TrimSpace(kw)
		if len([]rune(kw)) < 2 || seen[kw] {
			continue
		}
		seen[kw] = true
		out = append(out, kw)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// tgSearchCacheKey 搜索缓存键
func tgSearchCacheKey(channels map[string][]string, terms []string, year string) string {
	parts := []string{}
	for _, provider := range []string{"123", "guangya", "pan139"} {
		for _, ch := range channels[provider] {
			parts = append(parts, provider+":"+ch)
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|") + "||" + strings.Join(terms, "|") + "||" + year))
	return "media:tg-public-resource-search:v1:" + hex.EncodeToString(sum[:8])
}

// postTimeText 帖子时间展示
func postTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

// ---------------------------------------------------------------------------
// 种子文件 → 磁力（.torrent 上传离线用；最小 bencode 解析，仅 v1 infohash）
// ---------------------------------------------------------------------------

var torrentNameRe = regexp.MustCompile(`4:name(\d+):`)

// TorrentBytesToMagnet 从 .torrent 文件内容提取磁力链接（BT v1/混合；v2-only 报错）
func TorrentBytesToMagnet(content []byte) (string, error) {
	const maxTorrentBytes = 4 << 20
	if len(content) > maxTorrentBytes {
		return "", fmt.Errorf("种子文件过大（上限 4MB）")
	}
	info, _, err := bencodeFindInfoDict(content)
	if err != nil {
		return "", fmt.Errorf("种子解析失败：%v", err)
	}
	if len(info) == 0 {
		return "", fmt.Errorf("种子缺少 info 字典")
	}
	// v2-only 检测：info 内含 file tree 且无 pieces
	if bytesContainsKey(info, "file tree") && !bytesContainsKey(info, "pieces") {
		return "", fmt.Errorf("暂不支持 BitTorrent v2 种子")
	}
	// v1 infohash = SHA1(info dict 原始字节)
	hash := sha1Sum(info)
	name := torrentNameFromInfo(info)
	magnet := "magnet:?xt=urn:btih:" + hex.EncodeToString(hash)
	if name != "" {
		magnet += "&dn=" + urlEscape(name)
	}
	return magnet, nil
}

// bencodeFindInfoDict 在 torrent 内容中定位 4:info 字典的原始字节段
func bencodeFindInfoDict(content []byte) ([]byte, int, error) {
	idx := bytesIndex(content, []byte("4:info"))
	if idx < 0 {
		return nil, 0, fmt.Errorf("未找到 info 字典")
	}
	start := idx + len("4:info")
	end, err := bencodeElementEnd(content, start)
	if err != nil {
		return nil, 0, err
	}
	return content[start:end], end - start, nil
}

// bencodeElementEnd 计算从 pos 开始的 bencode 元素结束位置（不含）
func bencodeElementEnd(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, fmt.Errorf("bencode 截断")
	}
	switch data[pos] {
	case 'i': // integer i...e
		end := bytesIndexRange(data, pos, []byte("e"))
		if end < 0 {
			return 0, fmt.Errorf("bencode 整数未闭合")
		}
		return end + 1, nil
	case 'l', 'd': // list / dict
		pos++
		for pos < len(data) && data[pos] != 'e' {
			var err error
			pos, err = bencodeElementEnd(data, pos)
			if err != nil {
				return 0, err
			}
		}
		if pos >= len(data) {
			return 0, fmt.Errorf("bencode 容器未闭合")
		}
		return pos + 1, nil
	default: // string <len>:<bytes>
		colon := bytesIndexRange(data, pos, []byte(":"))
		if colon < 0 {
			return 0, fmt.Errorf("bencode 字符串格式错误")
		}
		n, err := strconv.Atoi(string(data[pos:colon]))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bencode 长度解析失败")
		}
		return colon + 1 + n, nil
	}
}

// torrentNameFromInfo 从 info dict 提取 name
func torrentNameFromInfo(info []byte) string {
	m := torrentNameRe.FindSubmatchIndex(info)
	if m == nil {
		return ""
	}
	n, _ := strconv.Atoi(string(info[m[2]:m[3]]))
	start := m[1] // 匹配结束（冒号之后）即字符串起点
	if start+n > len(info) {
		return ""
	}
	return string(info[start : start+n])
}

func bytesIndex(data, sub []byte) int { return bytesIndexRange(data, 0, sub) }

func bytesIndexRange(data []byte, from int, sub []byte) int {
	if from < 0 || from > len(data) {
		return -1
	}
	idx := strings.Index(string(data[from:]), string(sub))
	if idx < 0 {
		return -1
	}
	return from + idx
}

func bytesContainsKey(data []byte, key string) bool {
	return bytesIndex(data, []byte(strconv.Itoa(len(key))+":"+key)) >= 0
}

// urlEscape 极简 URL 编码（磁力 dn 用）
func urlEscape(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.IndexByte("-_.~", c) >= 0 {
			sb.WriteByte(c)
		} else {
			const hexDigits = "0123456789ABCDEF"
			sb.WriteByte('%')
			sb.WriteByte(hexDigits[c>>4])
			sb.WriteByte(hexDigits[c&0xF])
		}
	}
	return sb.String()
}

// sha1Sum SHA1 摘要
func sha1Sum(data []byte) []byte {
	// crypto/sha1 引入在调用处保持包整洁
	h := newSHA1()
	h.Write(data)
	return h.Sum(nil)
}

// 资源搜索缓存 TTL（对齐参考实现 5 分钟会话缓存语义）
const resourceSearchCacheTTL = 5 * time.Minute
