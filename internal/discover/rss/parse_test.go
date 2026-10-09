package rss

// T16 · 解析器测试。
//
// 覆盖任务书点名的五类：RSS 2.0 / Atom / 命名空间 / HTML 清理 / magnet 提取，
// 外加编码判定与去重键兜底链 —— 后两者是验收 ②⑤ 的地基。

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ── RSS 2.0 ─────────────────────────────────────────────────────────────────

const rss20Sample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>示例动漫站</title>
    <link>https://example.test/</link>
    <description>示例频道</description>
    <item>
      <title>[Lilith-Raws] 某番剧 - 01 [1080p][Baha][WEB-DL]</title>
      <link>https://example.test/show/1</link>
      <guid isPermaLink="false">mikan-1001</guid>
      <pubDate>Mon, 06 Oct 2025 12:00:00 +0800</pubDate>
      <description>&lt;p&gt;第1话 &lt;b&gt;1080p&lt;/b&gt;&lt;/p&gt;</description>
      <enclosure url="magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&amp;dn=test" type="application/x-bittorrent" length="1048576000" />
    </item>
    <item>
      <title>[Lilith-Raws] 某番剧 - 02 [1080p]</title>
      <link>https://example.test/show/2</link>
      <guid isPermaLink="false">mikan-1002</guid>
      <pubDate>Mon, 13 Oct 2025 12:00:00 +0800</pubDate>
      <enclosure url="https://example.test/dl/2.torrent" type="application/x-bittorrent" length="1048576000" />
    </item>
  </channel>
</rss>`

func TestParseRSS20Basics(t *testing.T) {
	feed, err := Parse([]byte(rss20Sample), "")
	if err != nil {
		t.Fatalf("解析 RSS 2.0 失败: %v", err)
	}
	if feed.Title != "示例动漫站" {
		t.Errorf("feed 标题 = %q，期望「示例动漫站」", feed.Title)
	}
	if len(feed.Items) != 2 {
		t.Fatalf("条目数 = %d，期望 2", len(feed.Items))
	}
	// 顺序必须保持 feed 原始顺序：位点推进依赖它，解析层不排序。
	if feed.Items[0].Guid != "mikan-1001" || feed.Items[1].Guid != "mikan-1002" {
		t.Errorf("条目顺序/guid 不符: %+v", feed.Items)
	}
	first := feed.Items[0]
	if first.Kind != KindMagnet {
		t.Errorf("第一条 Kind = %q，期望 magnet（enclosure url 是 magnet:）", first.Kind)
	}
	if first.Length != 1048576000 {
		t.Errorf("enclosure length = %d，期望 1048576000", first.Length)
	}
	if !strings.HasPrefix(first.ResourceURL, "magnet:?xt=urn:btih:abcdef") {
		t.Errorf("资源地址 = %q，磁力被截断了（&amp; 应已反转义）", first.ResourceURL)
	}
	// 描述里的 HTML 要被清成纯文本，否则 include_regex 匹配「1080p」会被标签干扰。
	if strings.ContainsAny(first.Description, "<>") {
		t.Errorf("描述仍含尖括号: %q", first.Description)
	}
	if !strings.Contains(first.Description, "1080p") {
		t.Errorf("描述丢了正文: %q", first.Description)
	}
	want := time.Date(2025, 10, 6, 4, 0, 0, 0, time.UTC)
	if !first.Published.Equal(want) {
		t.Errorf("pubDate 解析 = %v，期望 %v", first.Published, want)
	}
	if feed.Items[1].Kind != KindTorrent {
		t.Errorf("第二条 Kind = %q，期望 torrent（.torrent 直链）", feed.Items[1].Kind)
	}
}

// ── Atom ────────────────────────────────────────────────────────────────────

const atomSample = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom 示例源</title>
  <link href="https://atom.test/" rel="alternate" />
  <subtitle>Atom 频道</subtitle>
  <entry>
    <title>Atom 条目一</title>
    <id>urn:uuid:1225c695-cfb8-4ebb-aaaa-000000000001</id>
    <link href="https://atom.test/e/1" rel="alternate" />
    <link href="magnet:?xt=urn:btih:1111111111111111111111111111111111111111&amp;dn=atom" rel="enclosure" type="application/x-bittorrent" />
    <published>2025-10-05T10:00:00Z</published>
    <updated>2025-10-05T10:00:00Z</updated>
    <content type="html">&lt;p&gt;正文一&lt;/p&gt;</content>
    <author><name>Atom 作者</name></author>
  </entry>
  <entry>
    <title>Atom 条目二</title>
    <id>urn:uuid:1225c695-cfb8-4ebb-aaaa-000000000002</id>
    <link href="https://atom.test/e/2" rel="alternate" />
    <published>2025-10-06T10:00:00Z</published>
    <summary>只有摘要，没有磁力</summary>
  </entry>
</feed>`

func TestParseAtomBasics(t *testing.T) {
	feed, err := Parse([]byte(atomSample), "")
	if err != nil {
		t.Fatalf("解析 Atom 失败: %v", err)
	}
	if feed.Title != "Atom 示例源" {
		t.Errorf("feed 标题 = %q", feed.Title)
	}
	if feed.Link != "https://atom.test/" {
		t.Errorf("feed 链接 = %q，期望取 rel=alternate 的 href", feed.Link)
	}
	if len(feed.Items) != 2 {
		t.Fatalf("条目数 = %d，期望 2", len(feed.Items))
	}
	e := feed.Items[0]
	// Atom 的 <link rel="alternate"> 是条目页面，不是资源 —— 两者不能混。
	if e.Link != "https://atom.test/e/1" {
		t.Errorf("条目 alternate 链接 = %q", e.Link)
	}
	if e.Kind != KindMagnet || !strings.HasPrefix(e.ResourceURL, "magnet:") {
		t.Errorf("rel=enclosure 的 href 没被收成资源: kind=%q url=%q", e.Kind, e.ResourceURL)
	}
	if e.Author != "Atom 作者" {
		t.Errorf("作者 = %q，期望取 <author><name>", e.Author)
	}
	if !e.Published.Equal(time.Date(2025, 10, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("published 解析 = %v", e.Published)
	}
	// 第二条没有资源：KindNone，不是空串。
	if feed.Items[1].Kind != KindNone {
		t.Errorf("无资源条目 Kind = %q，期望 none", feed.Items[1].Kind)
	}
}

// ── 命名空间 ────────────────────────────────────────────────────────────────

const nsSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"
     xmlns:dc="http://purl.org/dc/elements/1.1/"
     xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <channel>
    <title>带命名空间的源</title>
    <item>
      <title>NS 条目</title>
      <guid>rdf-guid-1</guid>
      <dc:creator>字幕组 A</dc:creator>
      <description>这是摘要，不含磁力</description>
      <content:encoded><![CDATA[<p>完整简介</p><a href="magnet:?xt=urn:btih:2222222222222222222222222222222222222222&amp;dn=ns">磁力</a>]]></content:encoded>
      <pubDate>Tue, 07 Oct 2025 08:30:00 +0800</pubDate>
    </item>
  </channel>
</rss>`

func TestParseNamespaceContentEncodedAndCreator(t *testing.T) {
	feed, err := Parse([]byte(nsSample), "")
	if err != nil {
		t.Fatalf("解析带命名空间的 feed 失败: %v", err)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("条目数 = %d，期望 1", len(feed.Items))
	}
	it := feed.Items[0]
	// dc:creator 必须收到（encoding/xml 退化成 local name 匹配）。
	if it.Author != "字幕组 A" {
		t.Errorf("作者 = %q，期望 dc:creator 的「字幕组 A」", it.Author)
	}
	// content:encoded 优先于 description —— 磁力藏在完整简介里，
	// 用 description（只有摘要）就抽不到。
	if it.Kind != KindMagnet {
		t.Fatalf("Kind = %q，期望 magnet —— content:encoded 没被优先取用", it.Kind)
	}
	if !strings.Contains(it.ResourceURL, "2222222222222222222222222222222222222222") {
		t.Errorf("磁力内容不对: %q", it.ResourceURL)
	}
	if !strings.Contains(it.Description, "完整简介") {
		t.Errorf("描述应取 content:encoded: %q", it.Description)
	}
}

// ── HTML 清理 ───────────────────────────────────────────────────────────────

func TestCleanText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"去标签保留内容", "<b>粗体</b>和<i>斜体</i>", "粗体和斜体"},
		// 单个 \n 而不是 \n\n：段落之间已经断行，标题只取首行时
		// 再补一个空行只会让标题多出一个前导换行。
		{"块级标签换行", "<p>第一段</p><p>第二段</p>", "第一段\n第二段"},
		{"br 转换行", "行一<br/>行二", "行一\n行二"},
		{"script 整体丢弃", `正文<script>var x="<b>不该出现</b>";</script>`, "正文"},
		{"style 整体丢弃", `正文<style>.a{color:red}</style>`, "正文"},
		{"实体反转义", "A&amp;B &lt;tag&gt; &#8212; &#x4e2d;", "A&B <tag> — 中"},
		{"空白归一", "  多  个空格\t换行\n\n\n\n尾  ", "多 个空格 换行\n\n尾"},
		{"未知具名实体保留", "&thetag;", "&thetag;"},
		{"非法码点删除", "a&#0;b", "ab"},
		{"裸尖括号删除", "5 < 6 > 4", "5 6 4"},
		// nbsp 归一后与普通空格一起被 spaceRunRe 压掉，所以是单空格。
		{"&nbsp; 变空格", "a&nbsp;&nbsp;b", "a b"},
		{"链接只留文字", `<a href="https://x.test/a">详情</a>`, "详情"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanText(c.in); got != c.want {
				t.Errorf("CleanText(%q)\n  得到 %q\n  期望 %q", c.in, got, c.want)
			}
		})
	}
}

// 实体反转义必须在剥标签**之后**：&lt;b&gt; 是要显示出来的字面量 "<b>"，
// 先反转义再剥标签会把它当标签删掉，用户就看不到 "<b>" 这三个字符了。
func TestUnescapeAfterTagStrip(t *testing.T) {
	got := CleanText("标题 &lt;b&gt;文字&lt;/b&gt;")
	if !strings.Contains(got, "<b>") {
		t.Errorf("结果 %q，期望保留字面量 <b>（实体反转义必须晚于剥标签）", got)
	}
}

// ── magnet / ed2k / thunder 提取 ────────────────────────────────────────────

func TestExtractResourceFromText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// 正文里的 magnet 常被包在 HTML 属性里，& 已转义成 &amp;。
			// 抽取时**必须反转义**，否则磁力参数名是字面量 "&amp;"，
			// 服务端解析出的 dn 是空。
			name: "正文里的 magnet 反转义 &amp;",
			in:   `简介：<a href="magnet:?xt=urn:btih:3333333333333333333333333333333333333333&amp;dn=a">下载</a>`,
			want: "magnet:?xt=urn:btih:3333333333333333333333333333333333333333&dn=a",
		},
		{
			name: "裸 magnet",
			in:   "链接 magnet:?xt=urn:btih:4444444444444444444444444444444444444444&dn=b 完",
			want: "magnet:?xt=urn:btih:4444444444444444444444444444444444444444&dn=b",
		},
		{
			name: "ed2k",
			in:   `ed2k://|file|某番剧.mp4|123456|44444444444444444444444444444444|/`,
			want: `ed2k://|file|某番剧.mp4|123456|44444444444444444444444444444444|/`,
		},
		{
			name: "thunder",
			in:   `thunder://abcdef`,
			want: `thunder://abcdef`,
		},
		{name: "没有链接", in: "纯文字简介", want: ""},
		{name: "空串", in: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractResourceFromText(c.in)
			if got != c.want {
				t.Errorf("extractResourceFromText\n  得到 %q\n  期望 %q", got, c.want)
			}
		})
	}
}

// enclosure 里塞封面图的 feed 很常见，图片必须被压到最后 ——
// 否则每个条目都会拿封面当资源下载。
func TestPickEnclosurePrefersMagnetOverImage(t *testing.T) {
	list := []Enclosure{
		{URL: "https://cdn.test/cover.jpg", Type: "image/jpeg", Length: 20000},
		{URL: "magnet:?xt=urn:btih:5555555555555555555555555555555555555555", Type: "application/x-bittorrent", Length: 900},
	}
	url, mime, length := pickEnclosure(list)
	if !strings.HasPrefix(url, "magnet:") {
		t.Fatalf("选中了 %q —— 封面图不该压过磁力", url)
	}
	if mime != "application/x-bittorrent" || length != 900 {
		t.Errorf("mime/length = %q/%d", mime, length)
	}
	// 全是图片时也要挑一个（哪怕分最低），不能返回空 —— 返回空等于条目丢失。
	onlyImage := []Enclosure{{URL: "https://cdn.test/cover.jpg", Type: "image/jpeg", Length: 20000}}
	u, _, _ := pickEnclosure(onlyImage)
	if u != "https://cdn.test/cover.jpg" {
		t.Errorf("只有图片时挑了 %q，期望仍返回它", u)
	}
}

// ── 编码 ────────────────────────────────────────────────────────────────────

// 验收 ⑤：非 UTF-8 必须给明确的不支持提示，不静默失败，也不吐乱码。
func TestParseRejectsGBKWithClearError(t *testing.T) {
	gbk := "<?xml version=\"1.0\" encoding=\"GBK\"?><rss><channel><title>\xd6\xd0\xce\xc4</title></channel></rss>"
	_, err := Parse([]byte(gbk), "")
	if err == nil {
		t.Fatal("GBK feed 解析成功了 —— 必须报不支持")
	}
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("错误 = %v，期望 ErrUnsupportedEncoding", err)
	}
	if !strings.Contains(err.Error(), "GBK") {
		t.Errorf("错误文案 %q 里没有点明 GBK，用户不知道该换什么源", err)
	}
}

// 有些站点实际用 GBK 输出却**没在头部声明** —— 那时 charset 名是空，
// 但 utf8.Valid 会失败，仍然必须报「不支持」而不是吐一屏替换字符。
func TestParseRejectsUndeclaredNonUTF8(t *testing.T) {
	bad := []byte("<?xml version=\"1.0\"?><rss><channel><title>\xd6\xd0\xce\xc4</title></channel></rss>")
	_, err := Parse(bad, "")
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("错误 = %v，期望 ErrUnsupportedEncoding（未声明但非 UTF-8）", err)
	}
	if !strings.Contains(err.Error(), "未声明") {
		t.Errorf("错误文案 %q 应点明「未声明」这一情形", err)
	}
}

func TestParseAcceptsUTF8DeclaredInHTTPHeaderOnly(t *testing.T) {
	// XML 声明里没有 encoding，只有 HTTP 头声明 utf-8。
	body := []byte(`<rss version="2.0"><channel><title>无声明源</title><item><guid>g1</guid><title>t</title></item></channel></rss>`)
	// ⚠️ 这里传的是**提取后的字符集名**，不是整条 Content-Type ——
	// Parse 的第二个参数是 charsetName，传整条头会静默走进「未知字符集」分支，
	// 把正常的 UTF-8 feed 判成不支持（这正是这个用例最初失败的原因）。
	if _, err := Parse(body, CharsetFromContentType("text/xml; charset=utf-8")); err != nil {
		t.Fatalf("HTTP 头声明 UTF-8 却失败: %v", err)
	}
	if _, err := Parse(body, ""); err != nil {
		t.Fatalf("完全没有声明（合法 UTF-8）却失败: %v", err)
	}
}

func TestCharsetFromContentType(t *testing.T) {
	cases := map[string]string{
		"text/xml; charset=utf-8":            "utf-8",
		"text/xml;charset=\"GBK\"":           "gbk",
		"application/rss+xml; CHARSET=Utf-8": "utf-8",
		"text/xml; charset=gbk; q=0.9":       "gbk",
		"text/html":                          "",
		"":                                   "",
	}
	for in, want := range cases {
		if got := CharsetFromContentType(in); got != want {
			t.Errorf("CharsetFromContentType(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 把 HTML 落地页当 feed 贴进来是最常见的用户失误，要给明确错误而不是空结果。
func TestParseRejectsHTMLPage(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>登录</title></head><body>请登录</body></html>`
	_, err := Parse([]byte(html), "")
	if !errors.Is(err, ErrNoFeed) {
		t.Fatalf("错误 = %v，期望 ErrNoFeed（这是个 HTML 落地页）", err)
	}
}

func TestParseEmptyBody(t *testing.T) {
	if _, err := Parse(nil, ""); !errors.Is(err, ErrNoFeed) {
		t.Errorf("空响应体错误 = %v，期望 ErrNoFeed", err)
	}
}

// ── 去重键 ──────────────────────────────────────────────────────────────────

func TestDedupKeyFallbackChain(t *testing.T) {
	cases := []struct {
		name   string
		item   Item
		want   string
		reason string
	}{
		{
			name: "feed 自带 guid 优先",
			item: Item{Guid: "feed-guid-1", Link: "https://x.test/1",
				Kind: KindMagnet, ResourceURL: "magnet:?xt=urn:btih:6666666666666666666666666666666666666666"},
			want:   "feed-guid-1",
			reason: "feed 自己给的标识最可靠",
		},
		{
			name:   "缺 guid 用 magnet 种子哈希",
			item:   Item{Link: "https://x.test/2", Kind: KindMagnet, ResourceURL: "magnet:?xt=urn:btih:7777777777777777777777777777777777777777&dn=a"},
			want:   "7777777777777777777777777777777777777777",
			reason: "同一资源换个站点发布时链接会变、种子哈希不会",
		},
		{
			name:   "缺 guid 无资源用 link",
			item:   Item{Link: "https://x.test/3", Kind: KindNone},
			want:   "https://x.test/3",
			reason: "详情页地址至少是条目标识",
		},
		{
			name:   "全缺走合成键",
			item:   Item{Title: "某番剧 第01话", Kind: KindNone, Published: time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)},
			want:   "synthetic:1|某番剧 第01话|2025-10-01T00:00:00Z|",
			reason: "什么都没有时也不能返回空串",
		},
		{
			name:   "ed2k 用 32 位 MD5",
			item:   Item{Link: "https://x.test/4", Kind: KindEd2k, ResourceURL: "ed2k://|file|a.mp4|1|88888888888888888888888888888888|/"},
			want:   "88888888888888888888888888888888",
			reason: "ed2k 是 MD5 不是种子哈希，不能用 40 位校验",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.item.DedupKey(1)
			if got != c.want {
				t.Errorf("DedupKey = %q\n  期望 %q（%s）", got, c.want, c.reason)
			}
			if strings.TrimSpace(got) == "" {
				t.Fatal("去重键为空串 —— 所有无标识条目会挤成同一行")
			}
		})
	}
}

// 合成键必须稳定：同一条目在两轮抓取里算出的键要一致，否则会重复入库。
func TestDedupKeySyntheticIsStable(t *testing.T) {
	a := Item{Title: "  某番剧   第01话  ", Kind: KindNone}
	b := Item{Title: "某番剧 第01话", Kind: KindNone}
	if a.DedupKey(7) != b.DedupKey(7) {
		t.Errorf("空白差异导致合成键不同: %q vs %q", a.DedupKey(7), b.DedupKey(7))
	}
	// 不同源要能区分开，否则两个源的「无标识条目」互相吃掉。
	if a.DedupKey(7) == a.DedupKey(8) {
		t.Errorf("不同源算出了同一个合成键: %q", a.DedupKey(7))
	}
}

// ── 时间解析 ────────────────────────────────────────────────────────────────

func TestParseFeedTimeVariants(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"Mon, 06 Oct 2025 12:00:00 +0800", time.Date(2025, 10, 6, 4, 0, 0, 0, time.UTC)},
		{"Mon, 06 Oct 2025 12:00:00 GMT", time.Date(2025, 10, 6, 12, 0, 0, 0, time.UTC)},
		{"2025-10-06T04:00:00Z", time.Date(2025, 10, 6, 4, 0, 0, 0, time.UTC)},
		{"2025-10-06 12:00:00", time.Date(2025, 10, 6, 12, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got := parseFeedTime(c.in)
		if !got.Equal(c.want) {
			t.Errorf("parseFeedTime(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
	// 解析不出来必须返回零值，不能猜一个 now 填进去 ——
	// 位点推进依赖发布时间，猜错会把整条链路推到未来。
	if got := parseFeedTime("昨天"); !got.IsZero() {
		t.Errorf("非法时间解析成了 %v，期望零值", got)
	}
	if got := parseFeedTime(""); !got.IsZero() {
		t.Errorf("空时间解析成了 %v，期望零值", got)
	}
}

// ── RSS 1.0 / RDF ───────────────────────────────────────────────────────────

func TestParseRDFItemsOutsideChannel(t *testing.T) {
	rdf := `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
  <channel rdf:about="https://rdf.test/"><title>RDF 源</title><link>https://rdf.test/</link></channel>
  <item rdf:about="https://rdf.test/1"><title>RDF 条目</title><link>https://rdf.test/1</link></item>
</rdf:RDF>`
	feed, err := Parse([]byte(rdf), "")
	if err != nil {
		t.Fatalf("解析 RDF 失败: %v", err)
	}
	if len(feed.Items) != 1 || feed.Items[0].Title != "RDF 条目" {
		t.Errorf("RDF 条目没收到: %+v", feed.Items)
	}
	if feed.Title != "RDF 源" {
		t.Errorf("RDF channel 标题 = %q", feed.Title)
	}
}

// BOM 必须剥掉，否则 encoding/xml 第一行就报「非法字符」。
func TestParseStripsBOM(t *testing.T) {
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(rss20Sample)...)
	if _, err := Parse(body, ""); err != nil {
		t.Fatalf("带 BOM 的 feed 解析失败: %v", err)
	}
}

// ── HTML 落地页 / URL 校验 ───────────────────────────────────────────────────

func TestValidateFeedURL(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"https://mikanani.me/RSS/Bangumi?bangumiId=1", false},
		{"http://example.test/feed.xml", false},
		{"", true},
		{"   ", true},
		{"ftp://example.test/f", true},
		{"mikanani.me/RSS/Bangumi", true}, // 没有 scheme
		{"javascript:alert(1)", true},
	}
	for _, c := range cases {
		err := ValidateFeedURL(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateFeedURL(%q) 错误 = %v，期望出错=%v", c.in, err, c.wantErr)
		}
	}
}
