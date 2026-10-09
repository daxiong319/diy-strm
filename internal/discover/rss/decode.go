package rss

import (
	"fmt"
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// 字符集判定。
//
// 为什么不做 GBK 解码：go.mod 禁止新增依赖，而 golang.org/x/text 虽已在
// 依赖树里（indirect），其 encoding/html/charset 子包在本机 module cache 中
// 并不存在；为了一次抓取把整个 GBK 表搬进来不划算，而绝大多数 RSS 源
// （PT 站、动漫站）都是 UTF-8。所以本期明确只支持 UTF-8，
// 非 UTF-8 时返回可识别的 ErrUnsupportedEncoding 让 UI 明确提示
// （验收 ⑤：不静默失败）。解析出的一堆 U+FFFD 替换字符比直接报错坏得多 ——
// 用户看到的会是一串看不懂的乱码，却完全不知道换个源就好了。
// ─────────────────────────────────────────────────────────────────────────────

// utf8CharsetAliases 是 Content-Type / XML 声明里表示 UTF-8 的各种写法。
var utf8CharsetAliases = map[string]bool{
	"utf-8": true, "utf8": true, "utf_8": true, "unicode-1-1-utf-8": true,
	"x-unicode-2-0-utf-8": true, "us-ascii": true, "ascii": true,
	"iso-8859-1": true, // latin1 是 UTF-8 的严格子集，按 UTF-8 解一定成功
	"latin1":     true, "l1": true, "ansi_x3.4-1968": true,
}

// knownUnsupportedCharsets 是**明确不支持**且常见的字符集，
// 单独列出来的意义是给一个更具体的错误文案（用户能照着去换源）。
var knownUnsupportedCharsets = map[string]string{
	"gb2312":       "GB2312",
	"gbk":          "GBK",
	"gb18030":      "GB18030",
	"big5":         "Big5",
	"big5-hkscs":   "Big5-HKSCS",
	"euc-jp":       "EUC-JP",
	"shift_jis":    "Shift_JIS",
	"sjis":         "Shift_JIS",
	"windows-1250": "Windows-1250",
	"windows-1251": "Windows-1251",
	"windows-1252": "Windows-1252",
	"windows-1253": "Windows-1253",
	"windows-1254": "Windows-1254",
	"koi8-r":       "KOI8-R",
	"koi8-u":       "KOI8-U",
	"iso-8859-2":   "ISO-8859-2",
	"iso-8859-5":   "ISO-8859-5",
	"iso-8859-7":   "ISO-8859-7",
}

// xmlCharsetRe 抓 <?xml version="1.0" encoding="gbk"?> 里的 encoding。
var xmlCharsetRe = regexp.MustCompile(`(?i)<\?xml[^>]*encoding\s*=\s*["']([^"']+)["']`)

// xmlDeclaredCharset 从 XML 声明里取编码名，没有则返回空串。
//
// 只在**前若干字节**里找：有些站点会在 XML 声明之前放 BOM 或空行，
// 但不会有几百 KB 的前言，所以前 256 字节足够。
func xmlDeclaredCharset(data []byte) string {
	head := data
	if len(head) > 256 {
		head = head[:256]
	}
	m := xmlCharsetRe.FindSubmatch(head)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// NormalizeCharsetName 把字符集名规整成可比较的形式：
// 去空白、去引号、转小写。
func NormalizeCharsetName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	s = strings.ToLower(s)
	// 去掉可能的 "; q=" 尾巴之外的内容，Content-Type 里 charset 后偶尔带参数。
	if i := strings.IndexAny(s, ";"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// CharsetFromContentType 从 Content-Type 头里取 charset 名。
//
// 注意 header 里 charset 后可能带 `; q=0.9` 之类参数，
// 也有写成 `text/xml; charset="gbk"` 的 —— 都要能取到。
func CharsetFromContentType(contentType string) string {
	if contentType == "" {
		return ""
	}
	lower := strings.ToLower(contentType)
	i := strings.Index(lower, "charset")
	if i < 0 {
		return ""
	}
	rest := contentType[i+len("charset"):]
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return ""
	}
	if rest[0] == '=' {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	// 取到分号或逗号为止。
	if j := strings.IndexAny(rest, ";,"); j >= 0 {
		rest = rest[:j]
	}
	return NormalizeCharsetName(rest)
}

// CheckCharsetSupported 判断字符集是否受支持。
//
// 返回 nil 表示可以用。空串（未声明）按 UTF-8 处理 —— 因为 Parse 紧接着
// 会用 utf8.Valid 做实际校验，没声明又不是合法 UTF-8 的那时才会报错，
// 这比在这里猜一个默认字符集更可靠。
func CheckCharsetSupported(name string) error {
	n := NormalizeCharsetName(name)
	if n == "" || utf8CharsetAliases[n] {
		return nil
	}
	if label, ok := knownUnsupportedCharsets[n]; ok {
		return fmt.Errorf("%w：%s", ErrUnsupportedEncoding, label)
	}
	// 未知字符集名：也按不支持处理。静默按 UTF-8 解的结果是满屏乱码，
	// 而用户能做的只有换源或等支持 —— 明确说出来更有用。
	return fmt.Errorf("%w：%s（可识别的 UTF-8 写法：utf-8 / utf8）", ErrUnsupportedEncoding, name)
}

// SupportedCharsetsForUI 返回可以展示给用户的字符集支持说明。
// UI 用它拼「支持范围」提示，避免前端硬编码一份会漂移的文案。
func SupportedCharsetsForUI() []string {
	return []string{"UTF-8"}
}
