package mediarequest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"litepan/pkg/security"
)

// PortalCookieName 求片站自己的会话 cookie 名。
//
// ⚠️ **不能复用 adminauth 的 admin_session。**
// cookie 不区分端口：两个端口在同一主机上会共用同一个 cookie 存储。
// 如果求片站把家人的会话写进 admin_session，那么家人在求片站登录之后，
// 在管理台上名义上也「已登录」了 —— 管理台还会再过一遍 RBAC 所以不越权，
// 但排查问题时会看到两个端口显示互相矛盾的「当前身份」，而且求片站登出会
// 把管理台的会话一起登出（家人在手机上退出，管理台也得重新登录）。
const PortalCookieName = "litepan_request"

// portalSessionTTL 求片站会话有效期。
//
// 比管理台的短：求片站是「打开、搜、点、关掉」的场景，长期挂着没意义；
// 而家人共用一台设备时，短会话意味着「上一个人登出后下一个人不会自动继承身份」。
const portalSessionTTL = 7 * 24 * time.Hour

// SessionIdentity cookie 里带的东西。
//
// 只有身份，没有权限：每次请求都用它重新从库里算 Principal
// （见 api 层的 portalRequirePermission）。这样「管理员在后台把人禁用了」
// 会在下一次请求就生效，而不是等到 cookie 自然过期。
type SessionIdentity struct {
	Username string `json:"u"`
	// UserID 0 = 超管（虚拟主体，rbac_users 里没有行）。
	UserID   int64 `json:"id"`
	IsSuper  bool  `json:"s"`
	IssuedAt int64 `json:"t"`
}

// SessionSigner 签发与校验求片站会话。
type SessionSigner struct {
	ser *security.TimedSerializer
	ttl time.Duration
}

// NewSessionSigner 用同一个 core secret 构造。
//
// 刻意与 adminauth 用同一个 secret：两个服务本来就在同一进程里，
// 用不同 secret 只会让「这个 cookie 是哪个服务签的」变成一道排查题。
// 但 cookie 名不同，所以两者不会互相覆盖（见 PortalCookieName 的说明）。
func NewSessionSigner(secret []byte) *SessionSigner {
	return &SessionSigner{ser: security.NewTimedSerializer(secret), ttl: portalSessionTTL}
}

// Issue 签发会话，写入 cookie。
func (s *SessionSigner) Issue(w http.ResponseWriter, r *http.Request, id SessionIdentity) error {
	if s == nil || s.ser == nil {
		return errors.New("求片会话未初始化")
	}
	id.IssuedAt = time.Now().Unix()
	payload, err := json.Marshal(id)
	if err != nil {
		return err
	}
	token, err := s.ser.Dumps(string(payload))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     PortalCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   security.SecureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.ttl.Seconds()),
	})
	return nil
}

// Clear 清掉会话 cookie。
func (s *SessionSigner) Clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     PortalCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   security.SecureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Read 读会话。cookie 无效/过期/被篡改一律返回 ok=false（不返回错误）。
func (s *SessionSigner) Read(r *http.Request) (SessionIdentity, bool) {
	var out SessionIdentity
	if s == nil || s.ser == nil {
		return out, false
	}
	c, err := r.Cookie(PortalCookieName)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return out, false
	}
	raw, err := s.ser.Loads(c.Value, int(s.ttl.Seconds()))
	if err != nil || raw == "" {
		return out, false
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, false
	}
	if strings.TrimSpace(out.Username) == "" {
		return out, false
	}
	return out, true
}

// PortalPort 求片站端口。
//
// 默认 7812，与 参考实现 一致 —— 照抄 参考实现 部署文档的人（Docker 映射 7812）
// 迁到 litepan 时不用改任何东西。
// 与 litepan 管理台默认端口 5211 不冲突。
const DefaultPortalPort = 7812

// MinPortalPort / MaxPortalPort 端口取值范围。
const (
	MinPortalPort = 1024
	MaxPortalPort = 65535
)

// ClampPortalPort 把配置值夹进合法范围。
//
// 非法值返回 0 —— 调用方据此**放弃起这个口**而不是用 80 端口去起。
// 「配置写错了」的正确反应是明确报错让人去改，
// 而不是猜一个端口起来：猜错了的后果是「求片站悄悄开在了某个意外的地方」。
func ClampPortalPort(port int) int {
	if port < MinPortalPort || port > MaxPortalPort {
		return 0
	}
	return port
}
