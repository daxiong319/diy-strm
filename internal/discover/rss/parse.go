package rss

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ─────────────────────────────────────────────────────────────────────────────
// 解析范围（自研，无第三方依赖）：
//
//	支持 RSS 2.0     <rss><channel><item>
//	支持 Atom 1.0    <feed><entry>，含 <link rel="alternate|enclosure">
//	支持 RSS 1.0/RDF <rdf:RDF><item>（无 <channel> 包裹）
//	支持命名空间     content:encoded（正文优先源）、dc:creator（作者）
//	支持 enclosure   url/type/length 三属性（RSS 用 url，Atom 用 href）
//	支持从正文抽 magnet: / ed2k: / thunder: 链接
//	支持时间         <pubDate>（RFC 822 各变体）与 Atom <published>/<updated>（RFC 3339）
//
//	不支持 JSON Feed、其它私有命名空间的正文覆盖、paged feed 分页翻页、
//	**不支持 GBK / GB2312 / Big5 等非 UTF-8 字符集**（见 decode.go 与 doc.go）。
// ─────────────────────────────────────────────────────────────────────────────

// rssChannel 是 <channel>。RSS 2.0 的 <item> 与 RDF 的 <item> 结构一致，共用。
type rssChannel struct {
	Title       string       `xml:"title"`
	Link        string       `xml:"link"`
	Description string       `xml:"description"`
	Items       []rssItemXML `xml:"item"`
}

// rssItemXML 一个 <item>。
//
// Encoded 收 content:encoded、Creator 收 dc:creator：encoding/xml 对
// 带命名空间前缀的元素名会退化成 `local` 部分匹配，所以这两条 `xml:"encoded"`
// / `xml:"creator"` 约束靠的正是"只匹配 local name"这一行为 ——
// 同名的裸 <encoded> / <creator> 也会被收进来，这在 feed 生态里无害。
type rssItemXML struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	GUID        string      `xml:"guid"`
	Description string      `xml:"description"`
	PubDate     string      `xml:"pubDate"`
	Enclosures  []Enclosure `xml:"enclosure"`
	Author      string      `xml:"author"`
	Encoded     string      `xml:"encoded"` // content:encoded
	Creator     string      `xml:"creator"` // dc:creator
}

// Enclosure 是 RSS <enclosure> 与 Atom <link rel="enclosure"> 的并集。
type Enclosure struct {
	URL    string `xml:"url,attr"`  // RSS 形式
	Href   string `xml:"href,attr"` // Atom 形式
	Type   string `xml:"type,attr"`
	Length int64  `xml:"length,attr"`
}

// ResourceURL 取 enclosure 的实际地址（RSS 用 url 属性，Atom 用 href）。
func (e Enclosure) ResourceURL() string {
	if u := strings.TrimSpace(e.URL); u != "" {
		return u
	}
	return strings.TrimSpace(e.Href)
}

// linkXML 是 Atom 的 <link>。Rel 为空按 alternate 处理（Atom 规范）。
type linkXML struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type atomEntry struct {
	ID         string    `xml:"id"`
	Title      string    `xml:"title"`
	Links      []linkXML `xml:"link"`
	Summary    string    `xml:"summary"`
	Content    string    `xml:"content"`
	Published  string    `xml:"published"`
	Updated    string    `xml:"updated"`
	AuthorName string    `xml:"author>name"`
}

// Feed 是一份解析结果。
type Feed struct {
	// Title feed 标题。
	Title string
	// Link feed 站点地址（Atom 取 feed 级 alternate link）。
	Link string
	// Description feed 描述。
	Description string
	// Items 条目列表。**保持 feed 原始顺序**（多数 feed 新条目在前），
	// 抓取层按这个顺序推进位点，所以解析层不排序。
	Items []Item
}

// Parse 解析一份 feed 响应体。
//
// charsetName 是**已经提取好的字符集名**（如 "utf-8" / "gbk"），不是整条
// Content-Type 头 —— 要从头里取值请先调 CharsetFromContentType。
// 可以为空（此时只看 XML 声明）。
//
// ⚠️ 参数刻意叫 charsetName 而不是 contentType：两者形态差别很大
// （"utf-8" vs "text/xml; charset=utf-8"），而把整条头传进来的后果**不会报错**，
// 只会静默走到「未知字符集」分支、把一个完全正常的 UTF-8 feed 判成不支持。
// 这个坑我自己踩过一次（测试传的是 Content-Type、生产传的是 charset）。
//
// 非 UTF-8 时返回 ErrUnsupportedEncoding —— **不静默降级**：用 UTF-8 解 GBK
// 会得到一串替换字符，解析"成功"但内容全是乱码，后面 include_regex 匹配不上，
// 用户完全不知道发生了什么，只能反复重贴 URL。
func Parse(data []byte, charsetName string) (*Feed, error) {
	if len(data) == 0 {
		return nil, ErrNoFeed
	}

	// 字符集优先级：HTTP 头显式声明 > XML 声明 > 假定 UTF-8。
	cs := strings.TrimSpace(charsetName)
	if cs == "" {
		cs = xmlDeclaredCharset(data)
	}
	if err := CheckCharsetSupported(cs); err != nil {
		return nil, err
	}
	// 声明 UTF-8（或没声明）但实际不是合法 UTF-8 的，同样按不支持处理 ——
	// 这种情况绝大多数是站点用 GBK 输出却没在头部声明。
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w（响应体不是合法 UTF-8，常见于站点实际用 GBK 输出却未声明）", ErrUnsupportedEncoding)
	}
	data = stripBOM(data)

	// 三种结构逐个试。前两种命中判定看"有没有条目或标题"，
	// 因为 encoding/xml 对根元素名之外的容错比想象中宽。
	var r rssDoc
	if err := xml.Unmarshal(data, &r); err == nil && (len(r.Channel.Items) > 0 || strings.TrimSpace(r.Channel.Title) != "") {
		return buildFeed(r.Channel), nil
	}
	var a atomDoc
	if err := xml.Unmarshal(data, &a); err == nil && len(a.Entries) > 0 {
		return buildAtomFeed(a), nil
	}
	var rdf rdfDoc
	if err := xml.Unmarshal(data, &rdf); err == nil && len(rdf.Items) > 0 {
		ch := rdf.Channel
		ch.Items = append(ch.Items, rdf.Items...)
		return buildFeed(ch), nil
	}
	return nil, ErrNoFeed
}

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type atomDoc struct {
	XMLName  xml.Name    `xml:"feed"`
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle"`
	Links    []linkXML   `xml:"link"`
	Entries  []atomEntry `xml:"entry"`
}

// rdfDoc 是 RSS 1.0 / RDF：<item> 直接挂在 <rdf:RDF> 下，没有 <channel> 包裹。
type rdfDoc struct {
	XMLName xml.Name     `xml:"RDF"`
	Channel rssChannel   `xml:"channel"`
	Items   []rssItemXML `xml:"item"`
}

func buildFeed(ch rssChannel) *Feed {
	f := &Feed{
		Title:       CleanText(ch.Title),
		Link:        strings.TrimSpace(ch.Link),
		Description: CleanText(ch.Description),
	}
	for _, x := range ch.Items {
		f.Items = append(f.Items, convertRSSItem(x))
	}
	return f
}

func buildAtomFeed(a atomDoc) *Feed {
	f := &Feed{Title: CleanText(a.Title), Description: CleanText(a.Subtitle)}
	for _, l := range a.Links {
		if rel := strings.ToLower(strings.TrimSpace(l.Rel)); rel == "" || rel == "alternate" {
			f.Link = strings.TrimSpace(l.Href)
			break
		}
	}
	for _, e := range a.Entries {
		f.Items = append(f.Items, convertAtomEntry(e))
	}
	return f
}

// convertRSSItem 把一个 <item> 转成 Item。
func convertRSSItem(x rssItemXML) Item {
	// 正文优先 content:encoded：RSS 里 description 常常只是摘要，
	// content:encoded 才是完整简介，而磁力链接通常藏在完整简介里。
	body := x.Encoded
	if strings.TrimSpace(body) == "" {
		body = x.Description
	}
	it := Item{
		// 显式给 KindNone：没有资源地址的条目（纯更新通知）零值是 ""，
		// 而 "" 混在 Kind 枚举里会让下游的 switch 落进 default 分支，
		// 报错信息变成「未知类型」而不是「这条没有可下载资源」。
		Kind:        KindNone,
		Guid:        strings.TrimSpace(x.GUID),
		Title:       CleanText(x.Title),
		Link:        strings.TrimSpace(x.Link),
		Description: CleanText(body),
		// dc:creator 比 <author> 更常用（<author> 在 RSS 2.0 里是邮箱）。
		Author:    firstNonEmpty(x.Creator, x.Author),
		Published: parseFeedTime(x.PubDate),
	}
	if url, mime, length := pickEnclosure(x.Enclosures); url != "" {
		it.ResourceURL, it.Type, it.Length = url, mime, length
	}
	if it.ResourceURL == "" {
		it.ResourceURL = extractResourceFromText(body)
	}
	if it.ResourceURL != "" {
		it.Kind = ClassifyResource(it.ResourceURL, it.Type)
	}
	return it
}

// convertAtomEntry 把一个 <entry> 转成 Item。
func convertAtomEntry(x atomEntry) Item {
	// Atom 的 content 可能是 out-of-line（带 src 属性），那时正文为空，
	// 只保留 summary —— 这里不追 src（那等于替用户发起第二次抓取）。
	body := x.Content
	if strings.TrimSpace(body) == "" {
		body = x.Summary
	}
	it := Item{
		Kind:        KindNone,
		Guid:        strings.TrimSpace(x.ID),
		Title:       CleanText(x.Title),
		Description: CleanText(body),
		Author:      CleanText(x.AuthorName),
		Published:   parseFeedTime(firstNonEmpty(x.Published, x.Updated)),
	}
	for _, l := range x.Links {
		href := strings.TrimSpace(l.Href)
		if href == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(l.Rel)) {
		case "enclosure":
			if it.ResourceURL == "" {
				it.ResourceURL, it.Type = href, l.Type
			}
		default: // alternate 与"没有 rel"（Atom 规范里 rel 默认 alternate）
			if it.Link == "" {
				it.Link = href
			}
		}
	}
	if it.ResourceURL == "" {
		it.ResourceURL = extractResourceFromText(body)
	}
	if it.ResourceURL != "" {
		it.Kind = ClassifyResource(it.ResourceURL, it.Type)
	}
	return it
}

// pickEnclosure 从多个 enclosure 里挑最像"可下载资源"的那个。
//
// 有些 feed 会塞图片预览或音频的 enclosure，那不是我们要的；优先级打分
// 让磁力 > 种子 > ed2k > thunder > http 直链 > 其它，图片/音视频压到最低。
func pickEnclosure(list []Enclosure) (url, mime string, length int64) {
	best := 0
	for _, e := range list {
		u := e.ResourceURL()
		if u == "" {
			continue
		}
		if score := enclosureScore(u, e.Type); score > best {
			best, url, mime, length = score, u, e.Type, e.Length
		}
	}
	return url, mime, length
}

func enclosureScore(url, mimeType string) int {
	u := strings.ToLower(strings.TrimSpace(url))
	t := strings.ToLower(mimeType)
	score := 0
	switch {
	case strings.HasPrefix(u, "magnet:") || strings.Contains(t, "magnet"):
		score = 100
	case strings.HasSuffix(u, ".torrent") || strings.Contains(t, "torrent") || strings.Contains(t, "x-bittorrent"):
		score = 90
	case strings.HasPrefix(u, "ed2k://"):
		score = 80
	case strings.HasPrefix(u, "thunder://") || strings.HasPrefix(u, "acdown://"):
		score = 70
	case strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"):
		// http 直链。含 .torrent 的在上面的分支已经接住了，
		// 这里剩下的都是详情页或真直链，给中等分。
		score = 60
	}
	// 明确是图片的压到最低（番剧 RSS 很爱塞封面图当 enclosure）。
	if strings.HasPrefix(t, "image/") {
		return 1
	}
	if strings.HasPrefix(t, "audio/") || strings.HasPrefix(t, "text/html") {
		return 5
	}
	return score
}

var (
	magnetRe  = regexp.MustCompile(`(?i)magnet:\?[^\s"'<>&]?[^\s"'<>]*`)
	ed2kRe    = regexp.MustCompile(`(?i)ed2k://\|file\|[^\s"'<>]+`)
	thunderRe = regexp.MustCompile(`(?i)thunder://[^\s"'<>]+`)
)

// extractResourceFromText 从正文里抽出 magnet: / ed2k: / thunder: 链接。
//
// 很多番剧站的 RSS 里 enclosure 指向详情页（http），真正的磁力在
// content:encoded 的 HTML 片段里 —— 这是 RSS 场景最常见的取资源路径。
func extractResourceFromText(text string) string {
	if text == "" {
		return ""
	}
	for _, re := range []*regexp.Regexp{magnetRe, ed2kRe, thunderRe} {
		if m := re.FindString(text); m != "" {
			// ⚠️ 抽到的是**未反转义**的原始串：正文里的磁力几乎总被包在
			// HTML 属性中（<a href="magnet:...?xt=..&amp;dn=xx">），
			// 正则吃进去的 &amp; 必须在这里解开，否则下游拿到的磁力
			// 参数名是字面量 "&amp;" 而不是 "&"，字典序全错、
			// 服务端解析出的 dn 是空 —— 磁力能下但名字丢了。
			//
			// 用 UnescapeEntities 而不是自己只认 &amp;：正文里还可能混进
			// &#38; &#x26; 这两种数字实体写法，而且它们出现在**参数中间**
			// （不是结尾），按结尾裁剪的写法根本抓不到。
			return strings.TrimSpace(UnescapeEntities(m))
		}
	}
	return ""
}

// ClassifyResource 从地址 + mime 判定 Kind。
func ClassifyResource(url, mimeType string) ItemKind {
	u := strings.ToLower(strings.TrimSpace(url))
	t := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(u, "magnet:") || strings.Contains(t, "magnet"):
		return KindMagnet
	case strings.Contains(u, ".torrent") || strings.Contains(t, "torrent") || strings.Contains(t, "x-bittorrent"):
		return KindTorrent
	case strings.HasPrefix(u, "ed2k://"):
		return KindEd2k
	case strings.HasPrefix(u, "thunder://") || strings.HasPrefix(u, "acdown://"):
		return KindTorrent
	case strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"):
		return KindDirect
	}
	return KindNone
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// parseFeedTime 解析 feed 时间。RSS 用 RFC 822 各变体，Atom 用 RFC 3339。
// 解析不出来返回零值（未知），**不猜一个当前时间填进去** ——
// 位点推进依赖发布时间，猜错会把整条链路推到未来。
func parseFeedTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	for _, layout := range rssTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// rssTimeLayouts 覆盖真实 feed 里能见到的 pubDate 写法。
// 排在前面的先试：带数字时区（+0800）比缩写时区（MST）可靠得多，
// 因为 MST 依赖解析器自带的 location 表，个别写法会落到 UTC 或直接失败。
var rssTimeLayouts = []string{
	time.RFC1123Z,
	time.RFC822Z,
	time.RFC1123,
	time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST",
	"Mon, 2 Jan 2006 15:04:05",
	"Mon, 2 Jan 2006 15:04",
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
