package medialibshare

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"strings"
)

// 分享链接里有两种东西，分工必须写死，别混：
//
//	code  —— 门牌号。明文、放在 URL 里、可以随便转发给别人。
//	         它本身不授予任何权限：没有令牌，光有 code 打不开任何数据。
//	token —— 钥匙。签发给**某一个访客**，只放在 X-Share-Token 请求头里。
//
// 把两者分开是这套模型能落地的唯一原因。合二为一（URL 里那个串就是凭证）
// 的话，任何拿到日志、截图、聊天记录转发链接的人都能直接看片，而且没法只废掉一个。
const (
	// codeLen 短码长度。32^10 ≈ 1.1e15 组合空间，短到能念、能手打、长到猜不中。
	// 参考实现 的正则 ^[0-9A-Za-z]{1,16}$ 允许到 16，这里取 10 是刻意的折中：
	// 手机上手输分享码比从聊天记录里点链接常见得多。
	codeLen = 10
	// tokenBytes 令牌熵。32 字节 = 256 位，令牌泄漏不可枚举。
	tokenBytes = 32
	// visitorIDLen 访客标识长度。
	visitorIDLen = 24
)

// codeAlphabet 是短码字符集：去掉了 0/O/1/l/I 这类手打和朗读容易混的字符。
// 分享码经常是「你把屏幕上那串念给我」这种传达方式，可读性比熵更值钱。
const codeAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// ErrRandom 表示系统随机源不可用。此时必须让调用失败，绝不能降级成可预测的短码
// 或固定令牌 —— 那等于给每个部署发一把相同的万能钥匙。
var ErrRandom = errors.New("分享凭证生成失败：系统随机源不可用")

// newCode 生成一个分享短码。
//
// 用拒绝采样而不是 `b % len(alphabet)`：后者在 256 不能被 32 整除时
// 会让前几个字符出现概率更高，组合空间按最差情况算要掉一大截。
// 分享码是这个功能的唯一对外标识，不值得为省几行代码留这个偏差。
func newCode() (string, error) {
	return randomString(codeLen, codeAlphabet)
}

// newToken 生成访客令牌（明文，只在签发响应里出现一次）。
func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", ErrRandom
	}
	// RawURLEncoding：URL 安全字符集，可以直接放进请求头而不用再转义。
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// NewVisitorID 生成访客标识，交给前端存进 localStorage。
//
// 它不是安全凭证 —— 谁都能随便编一个。它只是「同一个浏览器」的稳定标记，
// 用来实现验收第 4 条（同浏览器 24 小时算 1 次 visitor）。
// 为什么不按 IP：同一个学校/公司出口 IP 会把几百个人挤成一个人，
// 而家里 Wi-Fi 的动态 IP 又会把同一个人反复算成新人。两个方向的错都不可接受。
func NewVisitorID() (string, error) {
	return randomString(visitorIDLen, codeAlphabet)
}

func randomString(n int, alphabet string) (string, error) {
	limit := 256 - (256 % len(alphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", ErrRandom
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// HashToken 是令牌（以及短码）落库前的哈希。
//
// 为什么不直接存明文令牌：这一列是唯一一处「令牌长期存在的地方」。
// 存哈希之后，拿到数据库副本的人能做的事只是离线暴力猜令牌 —— 而 256 位熵下这件事不可行。
// 对照之下，短码也走哈希：它同样没理由以明文形式长期躺在库里。
//
// 用 SHA-256 而不是 bcrypt/argon2：这两类慢哈希是为了防**低熵密码**被离线枚举的，
// 而这里的输入本来就是满熵随机串，不需要为它付出每次校验几百毫秒的代价。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// EqualHash 是常数时间比较，避免哈希比对被计时侧信道区分。
//
// 标准库没有 crypto/subtle 的哈希版本，而这两个值（短码哈希、令牌哈希）
// 都是攻击者能反复提交、观察响应的输入。
func EqualHash(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtleCompare(ha[:], hb[:])
}

func subtleCompare(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// MaskIP 对访客 IP 做脱敏后落库。
//
// 这一列存在是因为「完全不留 IP」的统计没有意义（一个分享被谁看过，全靠 UA 猜），
// 但「留完整 IP」又确实更敏感 —— 这是**外人访问**的记录，比内网播放日志敏感一个级别。
// 折中：IPv4 保留前三段（网段）、IPv6 保留前 64 位，其余抹掉。
//
// IPv6 的 /64 是一个家庭/一个订阅者的完整量级，粒度与 IPv4 的 /24 对齐，
// 不至于让统计变成一堆不同的值。
func MaskIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		if raw == "" {
			return ""
		}
		// 不是 IP（例如代理串或畸形值）：不猜结构，直接整个丢掉而不是原样存。
		// 原样存等于把调用方塞进来的任意字符串变成一个字段。
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		// ⚠️ 必须补足到 4 字节再交给 net.IP.String()：直接传 v4[:3] 的话
		// net.IP 会按「16 字节 IPv6 但只给了 3 字节」去解析，
		// 输出成 "?c0a801.0" 这种带问号的畸形串 ——
		// 问号代表「这段不知道」，也就是说脱敏后的地址反而比原始的更接近可识别。
		// 掩码是把最后一字节清零（/24），不是「截掉一截再拼后缀」。
		var out [4]byte
		copy(out[:], v4)
		return net.IP(out[:]).String() + "/24"
	}
	// IPv6 同理：补足 16 字节并清零后 8 字节（/64），
	// net.IP.String() 自己会把它压成 "2001:db8::" 这种最简形式。
	var out [16]byte
	copy(out[:8], ip.To16()[:8])
	return net.IP(out[:]).String() + "/64"
}

// UserAgentSnippet 把 UA 压成一句能读的短描述，供管理端展示访客用的什么设备。
// 完整 UA 会带系统版本和机型细节，落库的是「大概什么设备」而不是一份指纹。
func UserAgentSnippet(ua string) string {
	s := strings.TrimSpace(ua)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "edg/"):
		return "Edge"
	case strings.Contains(lower, "chrome/") && !strings.Contains(lower, "chromium"):
		return "Chrome"
	case strings.Contains(lower, "firefox/"):
		return "Firefox"
	case strings.Contains(lower, "safari/") && strings.Contains(lower, "version/"):
		return "Safari"
	case strings.Contains(lower, "android"):
		return "Android 浏览器"
	case strings.Contains(lower, "iphone"), strings.Contains(lower, "ipad"):
		return "iOS 浏览器"
	}
	const max = 40
	if len(s) > max {
		s = s[:max]
	}
	return s
}
