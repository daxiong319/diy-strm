package rss

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ─────────────────────────────────────────────────────────────────────────────
// HTML 片段清理。feed 的 description / content:encoded 绝大多数是 HTML 片段，
// 而下游的身份校验与 include_regex 匹配跑在**纯文本**上：
// 标题里一个 <br> 会让「1080p」匹配不上，去标签后才有意义。
//
// 仓内唯一相近的实现是 internal/discover/tgchannel/tgchannel_parse.go 的
// collectText（golang.org/x/net/html AST 遍历），但它是私有函数，
// 且既不做实体反转义也不归一空白 —— 对 RSS 来说两样都必需
// （&amp; / &#8212; 在标题里极常见）。所以这里自研。
//
// 刻意不用 x/net/html 的 AST：喂进来的常常是**片段**（没有 html/body 包裹、
// 标签不闭合很常见），AST 解析器对残缺片段容错差，而正则剥标签在
// 「我们只想拿到人读得见的文字」这个目标上够用且不会整体失败。
// ─────────────────────────────────────────────────────────────────────────────

var (
	// script / style 的内容整体丢弃：里面的尖括号和文字都不是给人看的，
	// 而且正则剥标签会留下 JS 代码当正文。
	//
	// ⚠️ 写成两条而不是一条带反向引用的 `<(script|style)\b[^>]*>.*?</\1\s*>`：
	// Go 用 RE2，**不支持反向引用**，那条正则会在 MustCompile 处 panic，
	// 而 panic 发生在 init 阶段 —— 包一旦被 import 整个进程就挂，
	// 比编译错误难查得多（症状是「测试跑到一半整个 go test 崩了」，
	// 栈顶却在别人的包初始化里）。两条各写一遍是 RE2 下的唯一写法。
	scriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	styleRe  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	// 块级标签换成换行，否则整篇正文会挤成一行。
	blockTagRe = regexp.MustCompile(`(?i)<br\s*/?>|</p\s*>|</div\s*>|</li\s*>|</tr\s*>|</h[1-6]\s*>|</blockquote\s*>`)
	// 其余标签剥掉但保留内容：<a href>x</a> → x，<b>x</b> → x。
	//
	// ⚠️ 不能写成 `(?s)<[^>]*>`：那是"从 < 到第一个 >"全吃，标题里
	// 「5 < 6 > 4」「A<B」这种**真文本里的尖括号**会被当标签整段删掉 ——
	// 症状是标题里凭空少一截字，而且删得干干净净、看不出发生过什么。
	// HTML 分词规则说标签名必须以 ASCII 字母开头，闭合/自闭合/声明/PI
	// 分别以 / ! ? 开头，所以只吃这五种开头的才安全。
	tagRe = regexp.MustCompile(`(?is)<[a-zA-Z/!?][^>]*>`)
	// 裸尖括号残留（剥不干净的）：删掉，避免正则元字符混进标题。
	// ⚠️ 必须在**实体反转义之前**跑：顺序反过来的话，&lt;b&gt; 反转义成 <b>
	// 就会被这里当裸尖括号删掉，用户永远看不到字面量 "<b>" 这几个字符。
	looseAngleRe = regexp.MustCompile(`[<>]`)
	// 连续空白压成一个空格（保留换行）。
	// 用 `+` 而不是 `{2,}`：落单的 tab / nbsp 也要归一 —— 否则同一个标题在
	// 两个 feed 里一个用 tab 一个用空格，include_regex 与去重键都会分叉。
	spaceRunRe   = regexp.MustCompile(`[ \t\x{00a0}]+`)
	newlineRunRe = regexp.MustCompile(`\n{3,}`)
)

// CleanText 把 HTML 片段转成适合显示与匹配的纯文本。
//
// 处理顺序是有讲究的，**每一步的次序都有原因**：
//  1. 先丢 script/style（它们的代码里有 < 和 &，会污染后面的实体反转义）；
//  2. 块级标签换行（保留段落结构，否则整篇挤成一行、显示全乱）；
//  3. 剥剩余标签；
//  4. **先删裸尖括号，再反转义实体** —— 反过来会吃掉字面量：
//     "&lt;b&gt;" 反转义成 "<b>" 后才去删裸尖括号，那三个字符就没了；
//     而用户看到的应该是 "<b>" 这个字面量（feed 里想显示尖括号时就写实体）。
//     先删裸尖括号则只清掉「剥标签没清干净的漏网之鱼」，而实体造出来的
//     尖括号是**合法的显示内容**，必须留下。
//  5. 归一空白、TrimSpace。
func CleanText(s string) string {
	if s == "" {
		return ""
	}
	s = scriptRe.ReplaceAllString(s, "")
	s = styleRe.ReplaceAllString(s, "")
	s = blockTagRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = looseAngleRe.ReplaceAllString(s, "")
	s = UnescapeEntities(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = spaceRunRe.ReplaceAllString(s, " ")
	s = newlineRunRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// namedEntities 是 HTML 里真正常见的具名实体。
//
// 刻意只列这批：完整的 HTML5 实体表有 2000+ 条，自研一个全表既不现实
// 也不需要 —— RSS 标题与简介里出现的实体就这几十个，剩下的
// （&nbsp; 之外的生僻符号）由数字实体路径覆盖。
var namedEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'",
	"nbsp": " ", "ensp": " ", "emsp": " ", "thinsp": " ",
	"hellip": "…", "mdash": "—", "ndash": "–",
	"lsquo": "‘", "rsquo": "’", "ldquo": "“", "rdquo": "”",
	"laquo": "«", "raquo": "»", "middot": "·", "bull": "•",
	"copy": "©", "reg": "®", "trade": "™", "deg": "°",
	"plusmn": "±", "times": "×", "divide": "÷",
	"frac12": "½", "frac14": "¼", "frac34": "¾",
	"sup2": "²", "sup3": "³", "micro": "µ", "para": "¶",
	"sect": "§", "dagger": "†", "permil": "‰",
	"prime": "′", "Prime": "″",
	"euro": "€", "pound": "£", "yen": "¥", "cent": "¢",
	"larr": "←", "uarr": "↑", "rarr": "→", "darr": "↓", "harr": "↔",
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ",
	"Alpha": "Α", "Beta": "Β", "Gamma": "Γ", "Delta": "Δ",
}

// entityRe 匹配 &#123; / &#x1F4A9; / &amp; 三种写法。
var entityRe = regexp.MustCompile(`&(#x[0-9a-fA-F]+|#[0-9]+|[a-zA-Z][a-zA-Z0-9]{1,31});`)

// UnescapeEntities 反转义 HTML 实体。
//
// 数字实体按 Unicode 码点解；码点非法（0、代理区、超出范围）时**删除该实体**
// 而不是吐 U+FFFD —— 一个删掉的字符不影响用户读标题，一个替换字符会让
// 「文件名对不上」这种排查变得莫名其妙。
func UnescapeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return entityRe.ReplaceAllStringFunc(s, func(m string) string {
		body := m[1 : len(m)-1]
		switch {
		case strings.HasPrefix(body, "#x"), strings.HasPrefix(body, "#X"):
			cp, err := strconv.ParseInt(body[2:], 16, 32)
			if err != nil {
				return ""
			}
			return runeOrEmpty(int(cp))
		case strings.HasPrefix(body, "#"):
			cp, err := strconv.ParseInt(body[1:], 10, 32)
			if err != nil {
				return ""
			}
			return runeOrEmpty(int(cp))
		default:
			if v, ok := namedEntities[body]; ok {
				return v
			}
			// 未知具名实体：原样保留。有些 feed 的标题里就带着
			// 自定义实体（如 &thetag;），删掉比留着更糟。
			return m
		}
	})
}

func runeOrEmpty(cp int) string {
	r := rune(cp)
	if cp <= 0 || cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF) {
		return ""
	}
	if !unicode.IsPrint(r) && r != '\n' && r != '\t' {
		return ""
	}
	return string(r)
}
