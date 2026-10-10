package app

import (
	"context"
	"encoding/json"

	"litepan/internal/logx"
	"litepan/internal/settings"
	"litepan/internal/wecom"
)

// wireWeComTrustedIP 装配企微可信 IP 自维护。
//
// 三条设计约束，都是任务书点名要的：
//
//  1. **关掉开关时是彻底的 no-op**：不发请求、不报错、不写日志。
//     行为与 T24 之前完全一致 —— 一个「默认关」的功能不该在关着的时候
//     还在后台做 DNS 查询和 HTTP 探测。
//
//  2. **以企微返回的错误为准，而不是主动探测出口 IP**。
//     主动探测只能看见你主动走的那条路：多网卡、多 NAT、容器、代理、双栈下
//     拿到的是主出口，而企微判 60020 用的可能是另一条 ——
//     于是「自动修复成功了」而接口仍然被拒，用户完全不知道该信哪个。
//     所以本服务只在真的收到 60020 时才动，并借那次报错确定「确实需要修」。
//
//  3. **合并而不是覆盖**：读回现有白名单，与新查到的出口 IP 取并集再写回。
//     覆盖会让用户手工加的条目全部消失，而用户不会知道 ——
//     下次他换网络时才发现通知又静默失败了。
//
// 关于凭证：corp_id / corp_secret 取自**已配置的通知渠道**（wecom_app），
// 因为那是用户已经填好的地方。再单开一份配置等于让他把 Secret 填两遍，
// 而两处不一致时最难查的是「明明配对了却不生效」。
//
// 凭证是**延迟读取**的（Credentials 回调每次现取）：渠道配置可以在运行期被改，
// 装配时抓一份快照的话，用户改完 Secret 之后自动修复会继续用旧的。
func wireWeComTrustedIP(st *storeBundle, logs *logx.Manager) *wecom.TrustedIPService {
	log := logs.For(logx.ModuleSystem)
	svc := wecom.NewTrustedIPService(nil, wecom.Config{
		Enabled: st.settings.Bool(settings.KeyMOWecomTrustedIPEnabled),
		Auto:    st.settings.Bool(settings.KeyMOWecomTrustedIPAuto),
	}, wecom.NewHTTPExitIPLookup(), botLogger{log})
	svc.SetCredentials(func() (corpID, secret string, ok bool) {
		return weComAppCredentials(st)
	})
	return svc
}

// weComAppCredentials 从已配置的通知渠道里取企业微信自建应用的凭证。
//
// 找不到就返回 ok=false：没配应用的用户不该看到自动修复相关的报错 ——
// 自动修复依赖那个应用发通知，应用都没有的时候它本来就无事可做。
func weComAppCredentials(st *storeBundle) (corpID, secret string, ok bool) {
	if st == nil || st.store == nil || st.store.NotifyChannels == nil {
		return "", "", false
	}
	channels, err := st.store.NotifyChannels.ListEnabled(context.Background())
	if err != nil {
		return "", "", false
	}
	for _, ch := range channels {
		if ch == nil || ch.Type != "wecom_app" {
			continue
		}
		cfg := map[string]string{}
		if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
			continue
		}
		if cfg["corp_id"] == "" || cfg["corp_secret"] == "" {
			continue
		}
		return cfg["corp_id"], cfg["corp_secret"], true
	}
	return "", "", false
}
