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

// ParseChannelPage 抓取并解析 t.me/s/{channel} 公开预览页
// 返回帖子列表（新到旧）。channel 传频道 @名（不带 @）。
func ParseChannelPage(ctx context.Context, channel string) ([]ChannelPost, error) {
	channel = strings.TrimPrefix(channel, "@")
	channel = strings.TrimPrefix(channel, "https://t.me/s/")
	channel = strings.TrimPrefix(channel, "https://t.me/")
	channel = strings.TrimPrefix(channel, "t.me/s/")
	channel = strings.TrimPrefix(channel, "t.me/")
	channel = strings.TrimRight(channel, "/")
	if channel == "" {
		return nil, fmt.Errorf("频道名为空")
	}

	url := "https://t.me/s/" + channel
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

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("t.me 返回 %d", resp.StatusCode)
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
