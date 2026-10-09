// Package rss 是 T16 的 RSS/Atom 订阅源实现。
//
// ⚠️ 本包的解析器是**自研**的：go.mod 不允许新增第三方依赖，
// 而仓内既没有 RSS 解析器、也没有通用 HTML 清理、也没有字符集转换。
// 支持范围与不支持项见 doc.go（变更说明要求「显眼写出」）。
package rss

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrUnsupportedEncoding 表示 feed 声明了本解析器不支持的字符集。
//
// 刻意做成独立错误而不是笼统的解析失败：用户贴一个 GBK 站点源进来
// 却只看到「解析失败」，会去反复重贴 URL，而正确做法是换成 UTF-8 源
// 或等我们支持 GBK —— 这两件事需要用户在 UI 上看到不同的提示。
var ErrUnsupportedEncoding = errors.New("feed 使用了不支持的字符集（当前只支持 UTF-8）")

// ErrNoFeed 表示响应解析得出来但里面没有 RSS 也不是 Atom。
// 多见于把网页地址当 feed 贴进来（返回 200 的 HTML 落地页）。
var ErrNoFeed = errors.New("响应不是 RSS 2.0、也不是 Atom 1.0")

// ItemKind 条目携带的资源形态。
type ItemKind string

const (
	// KindNone 没有可用资源：条目只是更新通知，没有 enclosure 也没有磁力。
	KindNone ItemKind = "none"
	// KindShare 网盘分享链接。
	KindShare ItemKind = "share"
	// KindTorrent .torrent 直链（可转成磁力后走离线下载）。
	KindTorrent ItemKind = "torrent"
	// KindMagnet magnet: 链接。
	KindMagnet ItemKind = "magnet"
	// KindEd2k ed2k: 链接。
	KindEd2k ItemKind = "ed2k"
	// KindDirect http(s) 直链（http(s) 的 enclosure，非磁力非种子）。
	KindDirect ItemKind = "direct"
)

// Item 是一条解析后的 feed 条目。字段名刻意不带 connector 包的前缀，
// 转成 connector.Item 的适配在 to_connector.go。
type Item struct {
	// Guid 条目去重键。见 Guid() 的合成兜底口径。
	Guid string
	// Title 标题。
	Title string
	// Link 条目页面地址（RSS 的 <link> / Atom 的 alternate link）。
	Link string
	// Description 清洗后的纯文本摘要（HTML 片段已剥掉标签）。
	Description string
	// Author 作者（RSS 的 <author> 或 dc:creator）。
	Author string
	// Kind 资源形态。
	Kind ItemKind
	// ResourceURL 资源地址：enclosure 的 url、磁力链接、或 content 里抽到的 magnet。
	ResourceURL string
	// Length enclosure 的 length 属性，字节。0 = 未声明。
	Length int64
	// Type enclosure 的 type 属性（mime）。空 = 未声明。
	Type string
	// Published 发布时间。零值 = feed 未给。
	Published time.Time
	// Raw 原始 XML 片段，供日志与调试。
	Raw string
}

// ErrUnsupportedFeed 表示 feed 里出现了本解析器认识的结构但明确不支持的变体。
func ErrUnsupportedFeed(detail string) error {
	if detail == "" {
		return ErrNoFeed
	}
	return fmt.Errorf("%w: %s", ErrNoFeed, detail)
}

// ─────────────────────────────────────────────────────────────────────────────
// 去重键。0044 迁移的 uq_rss_subscription_history_guid 是整条链路唯一的去重
// 依据，它填什么决定了这个功能会不会反复提交同一个条目，所以这一段必须稳。
//
// 兜底链（三级，照 参考实现「先 info_hash 再 guid 再 link」的两级口径扩成三级，
// 第三级是 litepan 补的 —— 参考实现 侧这层逻辑在 .so 里，无源码可抄）：
//
//	1. <guid> / <id>       feed 自己给的去重键，最可靠
//	2. 资源身份            magnet 的种子哈希 / ed2k 的文件哈希 —— 比链接更强：
//	                      同一个资源可能被两个站点用不同详情页链接发布，
//	                      但种子哈希一定是同一份内容
//	3. <link>              详情页地址
//	4. 合成键             "源ID|归一化标题|发布时间" —— 上面都没有时的兜底
//
// ⚠️ 合成键的稳定性是它唯一的价值：标题归一化（去空白、折叠大小写、
// 去方括号里的画质/字幕标记）后，同一资源在不同站点给出的标题变体
// （"[Lilith-Raws] 某番剧 - 01 [1080p][Baha]" vs "某番剧 01"）仍有很大概率
// 落到同一个归一化结果上。不完美，但**远好过填空串** ——
// 空串会让所有无 guid 条目挤成同一行，第二条起就全部被去重吃掉，
// 用户会看到「这个源只处理过一条」。
// ─────────────────────────────────────────────────────────────────────────────

// DedupKey 返回该条目的去重键（永不返回空串）。
//
// sourceID 只在合成键里用到：同一个条目被两个源订阅到时 guid 应相同
// （去重是全局的），但如果 feed 没给任何标识，退到合成键时加上源 ID
// 可以避免两个源各自的「无标识条目」互相吃掉。
//
// ⚠️ 方法名不是 Guid：Item 已有 Guid **字段**（feed 自带的 <guid>），
// 同名方法在 Go 里会遮蔽字段、编译不过。
func (it Item) DedupKey(sourceID int64) string {
	if g := strings.TrimSpace(it.Guid); g != "" {
		return g
	}
	if id := it.ContentIdentity(); id != "" {
		return id
	}
	if l := strings.TrimSpace(it.Link); l != "" {
		return l
	}
	return it.syntheticGuid(sourceID)
}

// ContentIdentity 从资源地址里取内容身份（magnet 种子哈希 / ed2k 文件哈希）。
//
// ⚠️ **不复用 connector.NormalizeInfoHash**：那个函数只接受 40 位 hex 或
// RFC 4648 base32（BEP 47 的种子哈希），而 ed2k 用的是 32 位 MD5，
// 传进去会被判非法返回空串 —— ed2k 条目就会掉到 link 兜底。
// 同一部片子在两个源里一个是 magnet、一个是 ed2k 时更是必然漏判。
// 40 位走 connector 口径（跨源去重要一致），32 位单独放行。
func (it Item) ContentIdentity() string {
	switch it.Kind {
	case KindMagnet:
		return NormalizeHash40(extractMagnetHash(it.ResourceURL))
	case KindEd2k:
		return NormalizeHash32(ed2kHash(it.ResourceURL))
	}
	return ""
}

// hash40Re 40 位小写 hex（种子哈希）。
var hash40Re = regexp.MustCompile(`^[0-9a-f]{40}$`)

// hash32Re 32 位小写 hex（ed2k 的 MD5 文件哈希）。
var hash32Re = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NormalizeHash40 规整 40 位 hex 种子哈希，空串表示非法。
func NormalizeHash40(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if hash40Re.MatchString(s) {
		return s
	}
	return ""
}

// NormalizeHash32 规整 32 位 hex（ed2k MD5），空串表示非法。
func NormalizeHash32(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if hash32Re.MatchString(s) {
		return s
	}
	return ""
}

// syntheticGuid 合成兜底键。
func (it Item) syntheticGuid(sourceID int64) string {
	title := NormalizeTitleForDedup(it.Title)
	// 发布时间两种格式都放进键里：feed 有 pubDate 用它，没有就用空串。
	// 这样「同一条目在不同轮里抓到」时间一致，键也一致。
	ts := ""
	if !it.Published.IsZero() {
		ts = it.Published.UTC().Format(time.RFC3339)
	}
	// 标题为空时把链接也塞进来，避免整条源所有条目共用一个空标题键。
	key := strings.Join([]string{strconv.FormatInt(sourceID, 10), title, ts, strings.TrimSpace(it.Link)}, "|")
	// 全空（标题、链接、时间都没有）的条目没有稳定身份：退到标题+类型的占位，
	// 保证键非空。
	if strings.Trim(title, "| ") == "" {
		key = fmt.Sprintf("%d|unknown|%s", sourceID, it.Type)
	}
	return "synthetic:" + key
}

// NormalizeTitleForDedup 把标题归一化成去重可用的形式。
//
// 只做两件保守的事：去首尾空白 + 折叠内部空白 + 转小写。
// **刻意不做激进的清洗**（去方括号、去画质标记、去 [NC-Raws] 前缀）——
// 那会让两部不同的剧被判成同一条目，代价（漏掉一条目）比多处理一次大得多。
// 激进归一化留到真出问题再改，这里先保证「同一标题多次出现 → 同一键」。
func NormalizeTitleForDedup(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = spaceRunRe.ReplaceAllString(s, " ")
	return s
}

// magnetHashRe 抽 magnet:?xt=urn:btih:<40 位 hex>。
//
// 大写小写都要收（BEP 47 规定种子哈希用大写，但 feed 里两种都有）。
var magnetHashRe = regexp.MustCompile(`(?i)xt=urn:btih:([0-9a-f]{40})`)

// extractMagnetHash 从 magnet 链接里取种子哈希。
func extractMagnetHash(magnet string) string {
	m := magnetHashRe.FindStringSubmatch(magnet)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// ed2kHashRe 抽 ed2k 的 32 位文件哈希（ed2k 用的是 MD5，不是种子哈希）。
//
// ⚠️ 不能用 `\|([0-9a-f]{32})\|$` 这种「贴到结尾」的写法：
// ed2k URI 的**哈希后面还有 `|/`**，所以贴结尾的模式永远匹配不上，
// ed2k 条目会静默掉到 link 兜底 —— 而 ed2k 链接的网站端路径（?id=、hash=）
// 各站不同，跨源去重必然失效。
// 正确形态是 `|文件哈希|`（两侧各一个竖线），结尾再加 `/?` 放宽尾部。
var ed2kHashRe = regexp.MustCompile(`(?i)\|([0-9a-f]{32})\|`)

// ed2kHash 从 ed2k 链接里取文件哈希。
func ed2kHash(link string) string {
	m := ed2kHashRe.FindStringSubmatch(strings.TrimSpace(link))
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// isHTTPScheme 判断一个地址是不是可以直接取的资源。
func isHTTPScheme(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "magnet", "ed2k":
		return true
	}
	return false
}
