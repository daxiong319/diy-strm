package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"litepan/internal/discover/discovery"
)

// ---------------------------------------------------------------------------
// 影视发现与订阅工具
//
// 这些工具直接调用 internal/discover/discovery 的既有函数：
// 该包不依赖本包，因此不存在循环依赖，也不需要钩子。
// ---------------------------------------------------------------------------

// mediaSubscriptionListTool 列出影视订阅。
type mediaSubscriptionListTool struct{}

func (mediaSubscriptionListTool) Name() string { return "media_subscription_list" }

func (mediaSubscriptionListTool) Description() string {
	return "列出影视订阅（按更新时间排序）。可选只看启用中的订阅。用于回答「我追了哪些剧」「某部剧订阅了吗」。"
}

func (mediaSubscriptionListTool) InputSchema() json.RawMessage {
	return objectSchema(nil, map[string]any{
		"only_enabled": boolProp("只返回启用中的订阅，默认 false 返回全部"),
		"limit":        intProp("最多返回条数，默认 50，上限 200"),
	})
}

func (mediaSubscriptionListTool) ReadOnly() bool { return true }

func (mediaSubscriptionListTool) Handler(_ context.Context, args map[string]any) (any, error) {
	var enabled *bool
	if _, ok := args["only_enabled"]; ok {
		value := argBool(args, "only_enabled", false)
		enabled = &value
	}
	subs, err := discovery.ListSubscriptions(enabled)
	if err != nil {
		return nil, fmt.Errorf("查询订阅失败：%w", err)
	}
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if len(subs) > limit {
		subs = subs[:limit]
	}
	return map[string]any{
		"total": len(subs),
		"items": subs,
	}, nil
}

// mediaSubscriptionGetTool 查看单个订阅详情。
type mediaSubscriptionGetTool struct{}

func (mediaSubscriptionGetTool) Name() string { return "media_subscription_get" }

func (mediaSubscriptionGetTool) Description() string {
	return "查看单个影视订阅的详情（含最近执行记录），需要订阅 ID。"
}

func (mediaSubscriptionGetTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"id"}, map[string]any{
		"id":          intProp("订阅 ID"),
		"runs_limit":  intProp("最近执行记录条数，默认 5，上限 20"),
		"items_limit": intProp("最近条目条数，默认 10，上限 50"),
	})
}

func (mediaSubscriptionGetTool) ReadOnly() bool { return true }

func (mediaSubscriptionGetTool) Handler(_ context.Context, args map[string]any) (any, error) {
	id, err := requireUint(args, "id", "订阅 ID")
	if err != nil {
		return nil, err
	}
	sub, err := discovery.GetSubscription(id)
	if err != nil {
		return nil, fmt.Errorf("订阅 #%d 不存在或查询失败：%w", id, err)
	}
	runsLimit := argInt(args, "runs_limit")
	if runsLimit <= 0 {
		runsLimit = 5
	}
	if runsLimit > 20 {
		runsLimit = 20
	}
	itemsLimit := argInt(args, "items_limit")
	if itemsLimit <= 0 {
		itemsLimit = 10
	}
	if itemsLimit > 50 {
		itemsLimit = 50
	}
	// 附属数据查询失败时只记日志、仍然返回订阅本体：
	// 让 LLM 至少能看到订阅是否启用，而不是整个调用失败。
	out := map[string]any{"subscription": sub}
	if runs, runErr := discovery.ListSubscriptionRuns(id, runsLimit); runErr != nil {
		packageLog().Warn("查询订阅执行记录失败", "subscription", id, "error", runErr)
	} else {
		out["runs"] = runs
	}
	if items, itemErr := discovery.ListSubscriptionItems(id, "", itemsLimit); itemErr != nil {
		packageLog().Warn("查询订阅条目失败", "subscription", id, "error", itemErr)
	} else {
		out["items"] = items
	}
	return out, nil
}

// mediaSubscriptionRunTool 手动执行一次订阅（写操作）。
type mediaSubscriptionRunTool struct{}

func (mediaSubscriptionRunTool) Name() string { return "media_subscription_run" }

func (mediaSubscriptionRunTool) Description() string {
	return "立即手动执行一次指定的影视订阅（会按规则检索资源并可能触发转存，改变网盘内容）。调用前必须先向用户确认。"
}

func (mediaSubscriptionRunTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"id"}, map[string]any{
		"id": intProp("要执行的订阅 ID"),
	})
}

func (mediaSubscriptionRunTool) ReadOnly() bool { return false }

func (mediaSubscriptionRunTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	id, err := requireUint(args, "id", "订阅 ID")
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("执行已取消：%w", ctxErr)
	}
	run, err := discovery.ProcessSubscription(id, "mcp")
	if err != nil {
		return nil, fmt.Errorf("执行订阅 #%d 失败：%w", id, err)
	}
	return map[string]any{
		"subscription_id": id,
		"run":             run,
		"message":         fmt.Sprintf("订阅 #%d 已执行", id),
	}, nil
}

// mediaChannelListTool 列出频道源。
type mediaChannelListTool struct{}

func (mediaChannelListTool) Name() string { return "media_channel_list" }

func (mediaChannelListTool) Description() string {
	return "列出已配置的发现频道/站点源（TG 频道、站点等）及其启用状态。"
}

func (mediaChannelListTool) InputSchema() json.RawMessage {
	return objectSchema(nil, map[string]any{
		"source_type": strProp("按来源类型过滤（如 tg），留空返回全部"),
	})
}

func (mediaChannelListTool) ReadOnly() bool { return true }

func (mediaChannelListTool) Handler(_ context.Context, args map[string]any) (any, error) {
	channels, err := discovery.ListChannels(argString(args, "source_type"))
	if err != nil {
		return nil, fmt.Errorf("查询频道失败：%w", err)
	}
	return map[string]any{"total": len(channels), "items": channels}, nil
}

// mediaChannelRunTool 执行一次频道订阅（写操作，走钩子）。
type mediaChannelRunTool struct{}

func (mediaChannelRunTool) Name() string { return "media_channel_subscription_run" }

func (mediaChannelRunTool) Description() string {
	return "执行一次指定的频道订阅任务（会拉取频道内容并可能转存到网盘）。调用前必须先向用户确认。"
}

func (mediaChannelRunTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"id"}, map[string]any{
		"id": intProp("要执行的订阅 ID"),
	})
}

func (mediaChannelRunTool) ReadOnly() bool { return false }

func (mediaChannelRunTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	id, err := requireUint(args, "id", "订阅 ID")
	if err != nil {
		return nil, err
	}
	summary, ok, runErr := runChannelSubscription(ctx, id)
	if runErr != nil {
		return nil, runErr
	}
	return map[string]any{
		"subscription_id": id,
		"summary":         summary,
		"changed":         ok,
	}, nil
}

// mediaEmbyMissingStatusTool 查询 Emby 缺失状态。
type mediaEmbyMissingStatusTool struct{}

func (mediaEmbyMissingStatusTool) Name() string { return "media_emby_missing_status" }

func (mediaEmbyMissingStatusTool) Description() string {
	return "查询 Emby 媒体库的缺失剧集/电影统计状态（需要已配置 Emby）。"
}

func (mediaEmbyMissingStatusTool) InputSchema() json.RawMessage {
	return objectSchema(nil, nil)
}

func (mediaEmbyMissingStatusTool) ReadOnly() bool { return true }

func (mediaEmbyMissingStatusTool) Handler(_ context.Context, _ map[string]any) (any, error) {
	status, err := discovery.EmbyMissingStatus()
	if err != nil {
		return nil, fmt.Errorf("查询 Emby 缺失状态失败：%w", err)
	}
	return status, nil
}

// mediaEmbyMissingResultsTool 查询某次缺失扫描的结果。
type mediaEmbyMissingResultsTool struct{}

func (mediaEmbyMissingResultsTool) Name() string { return "media_emby_missing_results" }

func (mediaEmbyMissingResultsTool) Description() string {
	return "查询 Emby 缺失扫描的结果列表。不传 scan_id 时先返回最近的扫描批次列表。"
}

func (mediaEmbyMissingResultsTool) InputSchema() json.RawMessage {
	return objectSchema(nil, map[string]any{
		"scan_id": intProp("扫描 ID；不传则返回最近的扫描批次列表"),
		"limit":   intProp("最多返回条数，默认 20，上限 200"),
	})
}

func (mediaEmbyMissingResultsTool) ReadOnly() bool { return true }

func (mediaEmbyMissingResultsTool) Handler(_ context.Context, args map[string]any) (any, error) {
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	scanID := argUint(args, "scan_id")
	if scanID == 0 {
		// 没给 scan_id 时给批次列表，LLM 才能追问「是哪一次」。
		return map[string]any{"scans": discovery.EmbyMissingScansList(limit)}, nil
	}
	results, err := discovery.EmbyMissingResultsList(scanID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询缺失扫描 #%d 的结果失败：%w", scanID, err)
	}
	return map[string]any{
		"scan_id": scanID,
		"total":   len(results),
		"items":   results,
	}, nil
}

// mediaEmbyMissingScanTool 启动缺失扫描（写操作）。
type mediaEmbyMissingScanTool struct{}

func (mediaEmbyMissingScanTool) Name() string { return "media_emby_missing_scan_start" }

func (mediaEmbyMissingScanTool) Description() string {
	return "启动一次 Emby 媒体库缺失扫描（会遍历媒体库，耗时可能较长）。调用前必须先向用户确认。"
}

func (mediaEmbyMissingScanTool) InputSchema() json.RawMessage {
	return objectSchema(nil, map[string]any{
		"library_ids": strProp("要扫描的媒体库 ID 列表，逗号分隔；留空表示全部媒体库"),
	})
}

func (mediaEmbyMissingScanTool) ReadOnly() bool { return false }

func (mediaEmbyMissingScanTool) Handler(_ context.Context, args map[string]any) (any, error) {
	scan, err := discovery.StartEmbyMissingScan(argStringSlice(args, "library_ids"))
	if err != nil {
		return nil, fmt.Errorf("启动缺失扫描失败：%w", err)
	}
	return map[string]any{"scan": scan, "message": "缺失扫描已启动"}, nil
}
