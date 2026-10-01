// Package drivers 通过空导入聚合所有驱动，触发各驱动的 init() 注册。
package drivers

import (
	"litepan/internal/driver"

	// drivers/115 被 .gitignore 排除、不入库，空导入会让 clone 后编译失败。
	_ "litepan/drivers/115_Open"
	pan123open "litepan/drivers/123_Open"
	cloud139 "litepan/drivers/139Cloud"
	_ "litepan/drivers/189Cloud"
	_ "litepan/drivers/Baidu_Open"
	"litepan/drivers/Guangya"
	_ "litepan/drivers/LocalFs"
	_ "litepan/drivers/OneDrive"
	_ "litepan/drivers/OpenList"
	_ "litepan/drivers/Quark"
	_ "litepan/drivers/WebDAV"
)

// 分享转存能力断言：编译期确保已实现 driver.ShareLinkSaver 的驱动不会因签名漂移而失效。
var (
	_ driver.ShareLinkSaver = (*pan123open.Driver)(nil)
	_ driver.ShareLinkSaver = (*guangya.Driver)(nil)
	_ driver.ShareLinkSaver = (*cloud139.Driver)(nil)
)

// 分享标题探测能力断言：目前只有 123 实现了只读探测（光鸭/139 的分享信息接口
// 尚未确认可只读拉取顶级目录名），故不为其断言。缺失时 watcher 按「无法确定即放行」降级。
var _ driver.ShareDirProbe = (*pan123open.Driver)(nil)
