package api

import (
	"context"
	"fmt"
	"strings"

	"litepan/internal/discover/discovery"
	"litepan/internal/domain"
	"litepan/internal/mcp"
)

// 把只存在于 internal/api（或其依赖方向上游）的能力，反向注册给 internal/mcp。
//
// 为什么需要这一层：internal/mcp 的工具需要「网盘文件操作」与「立即执行一次频道订阅」，
// 这两件事的实现分别位于 internal/api 的 file.Service 封装与 discover/discovery 包。
// 而 internal/api 为了注册 MCP 路由必须 import internal/mcp —— 若 mcp 直接调用 api 就成环。
// 因此 mcp 只声明函数类型，由这里在初始化时注入。
//
// 注册是幂等的：McpServer() 外层有 sync.Once，重复调用 registerMcpHooks 只会覆盖为
// 同一批函数指针，不会累积或 panic。

// registerMcpHooks 注册全部钩子实现。
func (h *Handler) registerMcpHooks() {
	mcp.RegisterChannelSubscriptionRunner(h.runChannelSubscriptionForTool)

	mcp.RegisterNetdiskList(h.netdiskListForTool)
	mcp.RegisterNetdiskRename(h.netdiskRenameForTool)
	mcp.RegisterNetdiskMove(h.netdiskMoveForTool)
	mcp.RegisterNetdiskMkdir(h.netdiskMkdirForTool)
	mcp.RegisterNetdiskDelete(h.netdiskDeleteForTool)
}

// runChannelSubscriptionForTool 立即执行一次频道订阅（供 LLM 工具调用）。
//
// 前置校验刻意严格：LLM 可能凭上下文猜一个 ID，若对「不存在」或「已暂停」的订阅
// 静默继续，它会拿到一个看似正常的汇总文案并据此向用户复述错误结论。
// 因此这里把三种情况都变成明确错误，让模型能如实回报。
func (h *Handler) runChannelSubscriptionForTool(ctx context.Context, subscriptionID uint) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("执行已取消：%w", err)
	}
	sub, err := discovery.GetSubscription(subscriptionID)
	if err != nil || sub == nil {
		return "", false, fmt.Errorf("订阅 #%d 不存在", subscriptionID)
	}
	if !sub.Enabled {
		return "", false, fmt.Errorf("订阅 #%d（%s）已暂停，请先恢复后再运行", subscriptionID, sub.Title)
	}
	summary, ok := discovery.RunSubscriptionOnce(*sub)
	return summary, ok, nil
}

// netdiskListForTool 列出目录内容。
//
// refresh 传 false：LLM 触发的列举走目录缓存，避免每次问答都把整个网盘扫一遍。
// 用户要最新结果时会在界面上手动刷新。
func (h *Handler) netdiskListForTool(ctx context.Context, accountID uint, parentID string, page, pageSize int, _ bool) (any, error) {
	if h.files == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "文件服务未就绪")
	}
	if accountID == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "account_id 不能为空")
	}
	items, err := h.files.List(ctx, int64(accountID), parentID, false)
	if err != nil {
		return nil, err
	}
	total := len(items)
	// 分页在内存里做：file.Service.List 一次返回整目录（目录内条目数通常有限），
	// 而工具调用只需要一页，返回全量会把上下文塞满。
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageItems := make([]fileDTO, 0, end-start)
	for _, it := range items[start:end] {
		pageItems = append(pageItems, fileToDTO(it))
	}
	return map[string]any{
		"parent_id": parentID,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
		"items":     pageItems,
	}, nil
}

// netdiskRenameForTool 重命名。
func (h *Handler) netdiskRenameForTool(ctx context.Context, accountID uint, fileID, newName string) error {
	if h.files == nil {
		return domain.Errorf(domain.CodeNotImplement, "文件服务未就绪")
	}
	if accountID == 0 {
		return domain.Errorf(domain.CodeValidation, "account_id 不能为空")
	}
	if strings.TrimSpace(fileID) == "" {
		return domain.Errorf(domain.CodeValidation, "file_id 不能为空")
	}
	if strings.TrimSpace(newName) == "" {
		return domain.Errorf(domain.CodeValidation, "new_name 不能为空")
	}
	// parentID 传空：RenameFile 内部会按 fileID 解析所在目录。
	return h.files.RenameFile(ctx, int64(accountID), fileID, newName, "")
}

// netdiskMoveForTool 移动。
func (h *Handler) netdiskMoveForTool(ctx context.Context, accountID uint, fileID, targetParentID string) error {
	if h.files == nil {
		return domain.Errorf(domain.CodeNotImplement, "文件服务未就绪")
	}
	if accountID == 0 {
		return domain.Errorf(domain.CodeValidation, "account_id 不能为空")
	}
	if strings.TrimSpace(fileID) == "" {
		return domain.Errorf(domain.CodeValidation, "file_id 不能为空")
	}
	if strings.TrimSpace(targetParentID) == "" {
		return domain.Errorf(domain.CodeValidation, "target_parent_id 不能为空")
	}
	// sourceParentID 传空：MoveFiles 的该参数仅用于源目录缓存失效，传空不影响移动本身。
	return h.files.MoveFiles(ctx, int64(accountID), []string{fileID}, targetParentID, "")
}

// netdiskMkdirForTool 新建目录。
func (h *Handler) netdiskMkdirForTool(ctx context.Context, accountID uint, parentID, _ string, name string) (any, error) {
	if h.files == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "文件服务未就绪")
	}
	if accountID == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "account_id 不能为空")
	}
	if strings.TrimSpace(name) == "" {
		return nil, domain.Errorf(domain.CodeValidation, "name 不能为空")
	}
	item, err := h.files.CreateFolder(ctx, int64(accountID), parentID, name)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"folder_id":   item.ID,
		"folder_name": item.Name,
		"parent_id":   parentID,
	}, nil
}

// netdiskDeleteForTool 删除（进回收站）。
//
// confirmName 必须与实际文件名一致：删除不可逆性最高，要求 LLM 复述一次名字，
// 能挡住「ID 猜错但名字没核对」的误删。空值直接拒绝，不设「跳过校验」的捷径。
func (h *Handler) netdiskDeleteForTool(ctx context.Context, accountID uint, parentID, fileID, confirmName string) error {
	if h.files == nil {
		return domain.Errorf(domain.CodeNotImplement, "文件服务未就绪")
	}
	if accountID == 0 {
		return domain.Errorf(domain.CodeValidation, "account_id 不能为空")
	}
	if strings.TrimSpace(fileID) == "" {
		return domain.Errorf(domain.CodeValidation, "file_id 不能为空")
	}
	confirmName = strings.TrimSpace(confirmName)
	if confirmName == "" {
		return domain.Errorf(domain.CodeValidation,
			"删除前必须提供 confirm_name（要删除项目的准确名称）以确认目标无误")
	}
	item, err := h.files.Info(ctx, int64(accountID), fileID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(item.Name) != confirmName {
		return domain.Errorf(domain.CodeValidation,
			"confirm_name 与实际名称不一致（实际为 %q），已拒绝删除以免误删", item.Name)
	}
	return h.files.DeleteFiles(ctx, int64(accountID), []string{fileID}, parentID)
}
