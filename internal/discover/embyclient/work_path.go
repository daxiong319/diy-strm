package embyclient

import (
	"context"
	"net/url"
	"strings"
)

// FindItemByTitle 按名称查条目（可再按年份过滤），返回第一个命中的。
//
// 为什么要按名称反查而不是只按 TMDB id：未识别兜底里往往还没有 TMDB id
// —— 文件名没解析出作品信息正是「未识别」的定义本身。等到有了 id 再查，
// 兜底已经用不上了。
//
// 命中判定刻意严格：名字必须完全相等（年份可选地相等）。
// 用「包含」匹配会把「庆余年」匹配到「庆余年2」，然后把文件落到错误年份的
// 目录里 —— 那是比不落兜底更糟的结果。
func (c *Client) FindItemByTitle(ctx context.Context, title string, year *int, isTV bool) (*BaseItemDtoV2, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	itemType := "Movie"
	if isTV {
		itemType = "Series"
	}
	params := url.Values{}
	params.Set("Recursive", "true")
	params.Set("IncludeItemTypes", itemType)
	params.Set("SearchTerm", title)
	params.Set("Fields", "Path,ProductionYear,ProviderIds")
	params.Set("Limit", "20")
	item, err := c.findFirstItem(params, "Emby 名称查询")
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, nil
	}
	if strings.TrimSpace(item.Name) != title {
		return nil, nil
	}
	if year != nil && *year > 0 && item.ProductionYear != *year {
		return nil, nil
	}
	return item, nil
}

// WorkPath 是给上层用的「这个作品在 Emby 里的本地路径」投影。
//
// 目录级条目（Series/Movie）本身就有 Path；只有以单文件入库的作品
// 才需要回退到某个 MediaSource 的路径，那种条目在 Emby 侧本来就不算
// 「作品目录」，所以这里返回空串而不是猜一个文件路径。
func WorkPath(item *BaseItemDtoV2) string {
	if item == nil || !item.IsFolder {
		return ""
	}
	return strings.TrimSpace(item.Path)
}
