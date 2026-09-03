package tgchannel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ShareLink 帖内网盘分享链接
type ShareLink struct {
	URL  string
	Pwd  string
	Type string // 与 models.SourceType 一致：123 / guangyapan / pan139
}

// ChannelPost 频道帖子
type ChannelPost struct {
	PostID string
	Time   time.Time
	Text   string
	Links  []ShareLink
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
}

// 分享链接正则（与 telegram_bot 一致）
// channelHTTPClient 抓取频道的专用客户端：带整体超时，且不自动跟随重定向
// （3xx 原样返回给调用方判断 —— 失效频道会 302 回 t.me 首页，跟随后会把首页当空页解析）。
// 不能用 http.DefaultClient：调用方传的是订阅引擎的长生命周期 ctx（无单次超时），
// t.me 挂起会阻塞整个订阅轮询循环（5 分钟一轮的串行遍历）。
var channelHTTPClient = &http.Client{
	Timeout:       45 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

var (
	rePan123 = regexp.MustCompile(`(?:https?://)?(?:[a-z0-9\-]+\.)*(?:123pan\.com|123pan\.cn|123684\.com|123865\.com)/(?:s|123pan|share)/([A-Za-z0-9\-_]{6,})(?:\.html)?`)
	reGuangY = regexp.MustCompile(`(?:https?://)?(?:www\.)?guangyapan\.com/s/([A-Za-z0-9_\-]{6,})`)
	rePan139 = regexp.MustCompile(`(?:shareweb/#/)?w/i/([A-Za-z0-9_\-]{6,})`)
	rePwd    = regexp.MustCompile(`(?i)(?:提取码\s*[:：]?\s*|\bpwd\s*[:：=]?\s*|pwd=)([A-Za-z0-9]{4,8})`)
)

// ExtractShareLinks 从帖子文本提取全部网盘分享链接
func ExtractShareLinks(text string) []ShareLink {
	var out []ShareLink
	seen := map[string]bool{}

	add := func(match string, typ string) {
		if match == "" || seen[match] {
			return
		}
		seen[match] = true
		pwd := ""
		if pm := rePwd.FindStringSubmatch(text); pm != nil {
			pwd = pm[1]
		}
		out = append(out, ShareLink{URL: match, Pwd: pwd, Type: typ})
	}

	for _, m := range rePan123.FindAllStringSubmatch(text, -1) {
		add("https://www.123pan.com/s/"+m[1], "123")
	}
	for _, m := range reGuangY.FindAllStringSubmatch(text, -1) {
		add("https://www.guangyapan.com/s/" + m[1], "guangyapan")
	}
	for _, m := range rePan139.FindAllStringSubmatch(text, -1) {
		add("https://www.139.com/w/i/" + m[1], "pan139")
	}
	return out
}

// MatchKeywords 文本是否命中任一关键词（大小写不敏感）
func MatchKeywords(text string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	lower := strings.ToLower(text)
	for _, k := range keywords {
		if k != "" && strings.Contains(lower, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// normalizeChannelName 归一化频道名为 t.me/s 路径段（去 @ / URL 前缀 / 尾斜杠）
func normalizeChannelName(channel string) string {
	channel = strings.TrimPrefix(channel, "@")
	channel = strings.TrimPrefix(channel, "https://t.me/s/")
	channel = strings.TrimPrefix(channel, "https://t.me/")
	channel = strings.TrimPrefix(channel, "t.me/s/")
	channel = strings.TrimPrefix(channel, "t.me/")
	channel = strings.TrimRight(channel, "/")
	return channel
}

// ParseChannelPage 抓取并解析 t.me/s/{channel} 公开预览页（仅最新一页）
// 返回帖子列表（新到旧）。channel 传频道 @名（不带 @）。
func ParseChannelPage(ctx context.Context, channel string) ([]ChannelPost, error) {
	posts, _, err := ParseChannelPageRange(ctx, channel, "", 1)
	return posts, err
}

// ParseChannelPageRange 抓取 t.me/s/{channel} 公开预览页并按 ?before= 向后翻页回看历史。
// 复刻 tgto123 网页通道语义：
//   - stopID==""：只抓最新一页即返回（首启/预览走此路径，避免一上来就深翻历史）。
//   - stopID!=""：逐页新→旧扫描，遇到 PostID <= stopID（游标边界）即截断停止；
//     整页都新于 stopID 时用本页最旧 PostID 拼 ?before= 抓下一页，直到命中边界 /
//     空页 / 页数超 maxPages / ctx 取消。maxPages<=0 时按单页处理。
//
// 跨页按 PostID 去重、整体新→旧排序。返回帖子列表、实际请求页数与错误。
func ParseChannelPageRange(ctx context.Context, channel string, stopID string, maxPages int) ([]ChannelPost, int, error) {
	channel = normalizeChannelName(channel)
	if channel == "" {
		return nil, 0, fmt.Errorf("频道名为空")
	}
	stopID = strings.TrimSpace(stopID)
	if maxPages <= 0 {
		maxPages = 1
	}

	var all []ChannelPost
	seen := map[string]bool{}
	beforeID := ""
	pages := 0
	stop := false

	for page := 1; page <= maxPages && !stop; page++ {
		select {
		case <-ctx.Done():
			if len(all) == 0 {
				return nil, pages, ctx.Err()
			}
			return dedupChannelPosts(all, seen), pages, nil
		default:
		}

		pageURL := "https://t.me/s/" + channel
		if beforeID != "" {
			pageURL += "?before=" + beforeID
		}
		posts, err := fetchChannelPage(ctx, pageURL)
		if err != nil {
			if len(all) == 0 {
				return nil, pages, err
			}
			// 已翻到部分页后失败：返回已得结果，不再继续
			return dedupChannelPosts(all, seen), pages, nil
		}
		pages++
		if len(posts) == 0 {
			// 空页（无更多可用历史）
			break
		}

		// posts 新→旧；找到「<= 游标」的边界帖即截断并停止翻页
		if stopID != "" {
			cut := -1
			for i, p := range posts {
				if !postIDNewer(p.PostID, stopID) {
					cut = i
					break
				}
			}
			if cut >= 0 {
				posts = posts[:cut]
				stop = true
			}
		}
		for _, p := range posts {
			if p.PostID != "" && !seen[p.PostID] {
				seen[p.PostID] = true
				all = append(all, p)
			}
		}
		if stop {
			break
		}

		// 用本页最旧帖 ID 继续向前翻
		oldest := posts[len(posts)-1].PostID
		if oldest == "" {
			break
		}
		beforeID = oldest

		if page < maxPages {
			select {
			case <-ctx.Done():
				stop = true
			case <-time.After(500 * time.Millisecond):
			}
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return postIDNewer(all[i].PostID, all[j].PostID)
	})
	return all, pages, nil
}

// dedupChannelPosts 追加式去重辅助（ParseChannelPageRange 内部用，保持新→旧输入序）
func dedupChannelPosts(posts []ChannelPost, seen map[string]bool) []ChannelPost {
	out := make([]ChannelPost, 0, len(posts))
	for _, p := range posts {
		if p.PostID == "" || seen[p.PostID] {
			continue
		}
		seen[p.PostID] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return postIDNewer(out[i].PostID, out[j].PostID)
	})
	return out
}

// fetchChannelPage 抓取单个 t.me/s 页并解析（3 次重试 / 45s 超时 / 4MB 上限 / UA 轮换）
func fetchChannelPage(ctx context.Context, url string) ([]ChannelPost, error) {
	ua := userAgents[time.Now().Unix()%int64(len(userAgents))]

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		resp, err := channelHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("t.me 返回 %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound ||
			resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusTemporaryRedirect ||
			resp.StatusCode == http.StatusPermanentRedirect {
			loc := strings.ToLower(resp.Header.Get("Location"))
			resp.Body.Close()
			if strings.HasPrefix(loc, "https://t.me/") || strings.HasPrefix(loc, "http://t.me/") || strings.HasPrefix(loc, "//t.me/") {
				// 频道失效或无法公开访问（重定向回 t.me 首页）
				return nil, fmt.Errorf("频道已失效或无法公开访问（重定向至 t.me）")
			}
			lastErr = fmt.Errorf("t.me 重定向至 %s", loc)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("t.me 返回 %d（频道可能不存在或未公开）", resp.StatusCode)
		}
		posts, err := parseChannelHTML(string(body))
		if err != nil {
			return nil, err
		}
		return posts, nil
	}
	return nil, fmt.Errorf("抓取 %s 失败（3 次尝试）：%v", url, lastErr)
}

// parseChannelHTML 解析 t.me/s 预览页 HTML
func parseChannelHTML(page string) ([]ChannelPost, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("解析频道页面失败：%w", err)
	}

	var posts []ChannelPost
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "tgme_widget_message") {
					if post, ok := parseMessageDiv(n); ok && post.PostID != "" {
						posts = append(posts, post)
					}
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// 同一帖在嵌套 div 中会命中多次（wrapper/内容层），按 PostID 去重
	seen := map[string]bool{}
	unique := posts[:0]
	for _, p := range posts {
		if seen[p.PostID] {
			continue
		}
		seen[p.PostID] = true
		unique = append(unique, p)
	}

	// 页面帖子 DOM 顺序为旧到新；按帖子 ID 降序排序为新到旧（第一个是最新）。
	// 调用方（订阅引擎）依赖该顺序：从最新帖向后扫描，遇「小于等于游标」即停止。
	sort.Slice(unique, func(i, j int) bool {
		return postIDNewer(unique[i].PostID, unique[j].PostID)
	})
	return unique, nil
}

// postIDNewer 帖子 ID 数值比较（a 是否晚于 b）。ID 为纯数字字符串，按长度+字典序比较。
func postIDNewer(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// parseMessageDiv 解析单个帖子 div（tgme_widget_message）
func parseMessageDiv(n *html.Node) (ChannelPost, bool) {
	var post ChannelPost
	postID := ""
	text := ""
	dateStr := ""
	var hrefs []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "div":
			for _, a := range n.Attr {
				if a.Key == "data-post" && postID == "" {
					postID = a.Val
				}
			}
		case "time":
			for _, a := range n.Attr {
				if a.Key == "datetime" && dateStr == "" {
					dateStr = a.Val
				}
			}
		case "a":
			isDate := false
			href := ""
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "tgme_widget_message_date") {
					isDate = true
				}
				if a.Key == "href" {
					href = a.Val
				}
			}
			if isDate {
				// 帖子链接不参与文本提取，但仍需遍历其子节点获取 time@datetime
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && c.Data == "time" {
						for _, a := range c.Attr {
							if a.Key == "datetime" && dateStr == "" {
								dateStr = a.Val
							}
						}
					}
				}
				return
			}
			// 内联键盘按钮等元素：链接存于 href（如 123 盘分享按钮）
			if strings.HasPrefix(href, "http") {
				hrefs = append(hrefs, href)
			}
		}
		if n.Type == html.ElementNode && n.Data == "div" {
			isText := false
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "tgme_widget_message_text") {
					isText = true
				}
			}
			if isText {
				text = collectText(n)
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)

	if postID == "" && text == "" {
		return post, false
	}
	if id := postID; id != "" {
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		post.PostID = id
	}
	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		post.Time = t
	}
	post.Text = text
	linkText := text
	if len(hrefs) > 0 {
		linkText = strings.Join(hrefs, "\n") + "\n" + text
	}
	// 资源帖常把 123FSLink 暗号加密成 [ENCRYPTED_LINK_START]..[END] 嵌在按钮/正文，
	// 先按 tgto123 同款 AES 解密替换回明文，再提取网盘链接。
	linkText = DecryptEncryptedLinks(linkText)
	post.Links = ExtractShareLinks(linkText)
	return post, true
}

// collectText 收集节点内全部文本
func collectText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}
