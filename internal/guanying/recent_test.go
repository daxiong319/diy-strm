package guanying

import (
	"strings"
	"testing"
)

// TestJsLiteralToJSON 首页 inlist 的 JS 字面量→JSON 归一化（裸键、单引号串、混合引号）。
func TestJsLiteralToJSON(t *testing.T) {
	in := `[{"g":["","","全26集"],"t":["瘴气营地的青春性事与死亡","生逢其时","歪心狼对阵ACME"],"a":[[2026,0,6,16,3],[2026,1,3,20,6],[2026,0,6,8,14]],"r":[1.3,10.4,3429],"z":[12,12,5],"b":[18,32,1],"w":[174,210,96],"d":[7.6,6.6,7.9],"im":[7.2,5.9,7.8],"i":["vAPPx","GMjj7","4OJPZ"],"q":[["4K"],["4K"],[]],"ty":"mv","h2":1,"cg":1,"ht":"最近更新的电影"},{"t":["侠探杰克 第四季"],"i":["rx55"],"ty":"tv","ht":"最近更新的剧集","st":'有"引号"的文本',"empty":''}]`
	out := jsLiteralToJSON(in)
	// 结果必须是合法 JSON
	if !strings.Contains(out, `"g"`) || !strings.Contains(out, `"ht"`) {
		t.Fatalf("裸键未加引号：%s", out[:120])
	}
	if !strings.Contains(out, `"最近更新的电影"`) {
		t.Fatalf("字符串内容损坏：%s", out[:200])
	}
	if strings.Contains(out, `'`) {
		t.Fatalf("单引号未清除：%s", out[:300])
	}
	// 单引号转双引号后 st 字段内容保留
	if !strings.Contains(out, `"st":"有\"引号\"的文本"`) && !strings.Contains(out, "有") {
		t.Fatalf("含引号字符串处理异常：%s", out)
	}
}

// TestParseHomepageInlist 从完整首页 HTML 提取三个板块。
func TestParseHomepageInlist(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>Loading...</title></head><body><script> _obj.header={};_obj.inlist=[{"g":["","","","","","","","","","","",""],"t":["瘴气营地的青春性事与死亡","年会不能停！2","戴高乐之战：淬炼时代","托尼","逃出绝命街","汪汪队立大功大电影3：勇闯恐龙岛","一夜限定","海洋奇缘：启航","给阿嬷的情书","特立独行","求救信号","歪心狼对阵ACME"],"a":[[2026,0,6,16,3],[2026,1,6,34,3],[2026,62,3,20,38],[2026,0,3,20,6],[2026,0,4,5,8],[2026,56,6,5,4],[2026,0,3,6,7],[2026,0,6,5,8],[2026,1,3,6,26],[2026,1,3,6,4],[2026,0,5,8,6],[2026,0,6,8,14]],"r":[1.3,10.4,3429,1.1,6.9,6850,1.4,2.9,96.7,7.8,3.9,3.4],"z":[12,12,5,9,13,7,6,14,14,13,12,6],"b":[18,32,1,20,67,23,31,58,6,24,68,18],"w":[174,210,96,156,229,190,191,238,249,236,222,196],"d":[7.6,6.6,7.9,7,5.7,7.2,6.3,6.7,9.3,6.7,7.1,7.5],"im":[7.2,5.9,7.8,7.5,6.4,6.1,6.1,5.8,8.2,6.3,6.8,7.5],"i":["vAPPx","GMjj7","4OJPZ","J4A29","4OkeZ","nErrg","zeXVP","Ow8B8","eGe5d","eGeYw","7pDxA","zeXxR"],"q":[["4K"],["4K"],[],["4K"],["4K"],["4K"],["4K","BD"],[],["4K"],["4K","BD"],["4K","BD"]],"ty":"mv","h2":1,"cg":1,"ht":"最近更新的电影"},{"g":["全6集","全8集"],"t":["灾","侠女内莉"],"i":["xGeg","2QRM"],"ty":"tv","h2":1,"cg":1,"ht":"最近更新的剧集"}];_obj.footer={t:'2.8'};</script></body></html>`
	blocks := parseHomepageInlist([]byte(html))
	if len(blocks) != 2 {
		t.Fatalf("应解析出 2 个板块（实际 %d）", len(blocks))
	}
	mv := recentItemsFromBlock("mv", blocks[0])
	if len(mv) != 12 {
		t.Fatalf("电影板块应 12 条（实际 %d）", len(mv))
	}
	if mv[0].Title != "瘴气营地的青春性事与死亡" || mv[0].ID != "vAPPx" {
		t.Fatalf("首条解析错误：%+v", mv[0])
	}
	if mv[0].Year != 2026 || mv[0].Douban != 7.6 || mv[0].OnlineN != 12 {
		t.Fatalf("数值字段解析错误：%+v", mv[0])
	}
	if len(mv[0].Quality) != 1 || mv[0].Quality[0] != "4K" {
		t.Fatalf("画质标签解析错误：%+v", mv[0].Quality)
	}
	tv := recentItemsFromBlock("tv", blocks[1])
	if len(tv) != 2 || tv[0].Status != "全6集" {
		t.Fatalf("剧集板块解析错误：%+v", tv)
	}
	// 海报与详情页 URL 构造（海报带 _Aimg 尺寸后缀 384）
	if !strings.Contains(mv[0].Poster, "/img/mv/vAPPx/384.webp") {
		t.Fatalf("海报 URL 错误：%s", mv[0].Poster)
	}
}

// TestRecentItemsFromBlockPaging /res/change 翻页响应解析（JSON 直接解，无 inlist 包装）。
func TestRecentItemsFromBlockPaging(t *testing.T) {
	block := map[string]any{
		"g":  []any{"", "", ""},
		"t":  []any{"我看见两朵一样的云", "抓特务", "耳语者"},
		"a":  []any{[]any{2026.0, 1.0, 7.0}, []any{2026.0, 1.0, 3.0}, []any{2026.0, 0.0, 3.0}},
		"i":  []any{"kAt7", "dZy7", "WeVX"},
		"d":  []any{7.0, 6.0, 0.0},
		"z":  []any{10.0, 12.0, 13.0},
		"b":  []any{1.0, 12.0, 6.0},
		"w":  []any{20.0, 102.0, 152.0},
		"q":  []any{[]any{"4K"}, []any{}, []any{"4K"}},
		"ty": "mv",
	}
	items := recentItemsFromBlock("mv", block)
	if len(items) != 3 {
		t.Fatalf("翻页应解析 3 条（实际 %d）", len(items))
	}
	if items[1].Title != "抓特务" || items[1].MagnetN != 12 || items[1].PanN != 102 {
		t.Fatalf("翻页字段错位：%+v", items[1])
	}
}

// TestParsePlayLines downurl 响应 playlist 在线播放线路解析。
func TestParsePlayLines(t *testing.T) {
	raw := []any{
		map[string]any{"i": "BmmZpq", "t": "西瓜线路(26)", "list": []any{[]any{[]any{"第", "集"}, []any{1.0, 26.0}}}},
		map[string]any{"i": "zKKvab", "t": "暴风线路(26)", "list": []any{[]any{[]any{"第", "集"}, []any{1.0, 25.0}}, []any{[]any{"第", "集完结"}, 26.0}}},
		map[string]any{"i": "", "t": "缺ID应跳过", "list": []any{}},
	}
	lines := parsePlayLines(raw)
	if len(lines) != 2 {
		t.Fatalf("应解析 2 条线路（实际 %d）", len(lines))
	}
	if playLineEpisodes(lines[0]["list"]) != 26 {
		t.Fatalf("西瓜线路集数错误：%d", playLineEpisodes(lines[0]["list"]))
	}
	if playLineEpisodes(lines[1]["list"]) != 26 {
		t.Fatalf("暴风线路集数错误（25+1完结）：%d", playLineEpisodes(lines[1]["list"]))
	}
	if playLineFirstEpisode(lines[0]["list"]) != 1 {
		t.Fatalf("首集号错误：%d", playLineFirstEpisode(lines[0]["list"]))
	}
}

// TestJsLiteralToJSONEdge 边界：字符串内转义、单引号内嵌双引号、true/false 裸值。
func TestJsLiteralToJSONEdge(t *testing.T) {
	in := `{"msg":'浏览器验证已过期','ok':true,'none':null,'esc':"含\"转义\""}`
	out := jsLiteralToJSON(in)
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"none":null`) {
		t.Fatalf("裸布尔/null 不应加引号：%s", out)
	}
	if !strings.Contains(out, `"esc":"含\"转义\""`) {
		t.Fatalf("转义字符串损坏：%s", out)
	}
}
