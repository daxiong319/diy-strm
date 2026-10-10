package inboundbot

import "strings"

// ExtractTransferLink 从一段文本里提取分享链接或磁力。
//
// 只在明确要求时才在**每个平台各判一次**：Bot 是外部入口，
// 见到 URL 就去调网盘 API 等于给任何能发言的人一个无鉴权的下载器。
func ExtractTransferLink(body string) (string, bool) {
	for _, field := range strings.Fields(body) {
		field = strings.Trim(field, "，。！!？?、,.;；:：\"'()（）[]【】<>")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "magnet:") {
			return field, true
		}
		if !strings.HasPrefix(field, "http://") && !strings.HasPrefix(field, "https://") {
			continue
		}
		if looksLikeShareLink(field) {
			return field, true
		}
	}
	return "", false
}

// knownShareHosts 是 litepan 已支持转存的网盘域名片段。
//
// 白名单而不是「任意 http 链接都转存」：理由同上。
var knownShareHosts = []string{
	"115.com", "115cdn.com", "123pan.com", "123684.com", "123865.com",
	"139.com", "yun.139.com", "189.cn", "cloud.189.cn",
	"baidu.com", "pan.baidu.com", "quark.cn", "pan.quark.cn",
	"aliyundrive.com", "alipan.com", "xunlei.com",
}

// looksLikeShareLink 判断一个 URL 像不像网盘分享链接。
func looksLikeShareLink(raw string) bool {
	lower := strings.ToLower(raw)
	// 只看 host 部分：路径里的 query（提取码）不该影响域名判断。
	if i := strings.IndexByte(lower, '?'); i >= 0 {
		host := lower[:i]
		for _, h := range knownShareHosts {
			if strings.Contains(host, h) {
				return true
			}
		}
	}
	for _, h := range knownShareHosts {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}
