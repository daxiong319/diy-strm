// Package proxyrules —— 多规则反代运行时管理（对齐 tgto123 emby_proxy/config/save_one 语义）：
// 每条启用的 Emby302ProxyRule 拉起一个独立端口监听（StartStandalone 多实例化），
// 飞牛影视/飞牛音乐走同一反代引擎（302/STRM/播放记录/弹幕钩子同链路）。
package proxyrules

import (
	"strconv"
	"sync"

	"diy-strm/emby302/web"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

var (
	runtimeMu     sync.Mutex
	runtimeByRule = map[uint]*web.StandaloneServer{} // ruleID -> 运行实例
)

// ReloadProxyRules 全量重建多规则反代监听：
// 停掉已删除/禁用的，启动/保留启用的；Host/Key 变化时重启对应端口。
// 由启动流程与规则保存回调调用。
func ReloadProxyRules() {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	rules, err := models.ListEmby302ProxyRules()
	if err != nil {
		helpers.AppLogger.Errorf("读取反代规则失败：%v", err)
		return
	}
	want := map[uint]models.Emby302ProxyRule{}
	for _, rule := range rules {
		if rule.Enabled && rule.ListenPort > 0 {
			want[rule.ID] = rule
		}
	}

	// 停掉不再需要的
	for id, srv := range runtimeByRule {
		if _, keep := want[id]; !keep {
			srv.Stop()
			delete(runtimeByRule, id)
		}
	}

	// 启动/重启需要的
	for id, rule := range want {
		cur, running := runtimeByRule[id]
		if running && cur != nil && cur.Port() == strconv.Itoa(rule.ListenPort) {
			continue // 端口未变，保持运行
		}
		if running {
			cur.Stop()
			delete(runtimeByRule, id)
		}
		srv, err := web.StartStandalone(strconv.Itoa(rule.ListenPort))
		if err != nil {
			helpers.AppLogger.Errorf("[反代规则 %d] 启动失败：%v", rule.ID, err)
			continue
		}
		helpers.AppLogger.Infof("[反代规则 %d] %s 已启动：0.0.0.0:%d（%s）", rule.ID, rule.Name, rule.ListenPort, rule.ProxyType)
		runtimeByRule[id] = srv
	}
}

// 规则监听不修改引擎全局配置：源地址统一由主 Emby 配置（startEmby302）写入，
// 规则校验层保证 rule.Host == GlobalEmbyConfig.EmbyUrl（见 models.Emby302ProxyRule.Validate），
// 因此多端口共用同一源，无跨规则串源问题。

// StopAllProxyRules 停止全部规则监听（退出时）
func StopAllProxyRules() {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	for id, srv := range runtimeByRule {
		srv.Stop()
		delete(runtimeByRule, id)
	}
}
