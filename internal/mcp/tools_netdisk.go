package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 网盘文件操作工具
//
// 这些能力全部位于 internal/api（目录列举、重命名、移动、新建、删除），
// 而 internal/api 为了注册 MCP 路由会 import 本包 —— 直接调用会形成循环依赖。
// 因此这里只声明函数类型，由 internal/api 在初始化时注入既有实现。
// ---------------------------------------------------------------------------

// NetdiskListFunc 列出目录内容。
type NetdiskListFunc func(ctx context.Context, accountID uint, parentID string, page, pageSize int, refresh bool) (any, error)

// NetdiskRenameFunc 重命名文件/目录。
type NetdiskRenameFunc func(ctx context.Context, accountID uint, fileID, newName string) error

// NetdiskMoveFunc 移动文件/目录到新父目录。
type NetdiskMoveFunc func(ctx context.Context, accountID uint, fileID, targetParentID string) error

// NetdiskMkdirFunc 新建目录。
type NetdiskMkdirFunc func(ctx context.Context, accountID uint, parentID, parentPath, name string) (any, error)

// NetdiskDeleteFunc 删除文件/目录。
type NetdiskDeleteFunc func(ctx context.Context, accountID uint, parentID, fileID, confirmName string) error

var (
	netdiskListFn   NetdiskListFunc
	netdiskRenameFn NetdiskRenameFunc
	netdiskMoveFn   NetdiskMoveFunc
	netdiskMkdirFn  NetdiskMkdirFunc
	netdiskDeleteFn NetdiskDeleteFunc
)

// RegisterNetdiskList 注册目录列举实现。
func RegisterNetdiskList(fn NetdiskListFunc) {
	hookMu.Lock()
	defer hookMu.Unlock()
	netdiskListFn = fn
}

// RegisterNetdiskRename 注册重命名实现。
func RegisterNetdiskRename(fn NetdiskRenameFunc) {
	hookMu.Lock()
	defer hookMu.Unlock()
	netdiskRenameFn = fn
}

// RegisterNetdiskMove 注册移动实现。
func RegisterNetdiskMove(fn NetdiskMoveFunc) {
	hookMu.Lock()
	defer hookMu.Unlock()
	netdiskMoveFn = fn
}

// RegisterNetdiskMkdir 注册新建目录实现。
func RegisterNetdiskMkdir(fn NetdiskMkdirFunc) {
	hookMu.Lock()
	defer hookMu.Unlock()
	netdiskMkdirFn = fn
}

// RegisterNetdiskDelete 注册删除实现。
func RegisterNetdiskDelete(fn NetdiskDeleteFunc) {
	hookMu.Lock()
	defer hookMu.Unlock()
	netdiskDeleteFn = fn
}

// netdiskAccountProp 是各网盘工具共用的账号参数说明。
const netdiskAccountProp = "网盘账号 ID；0 表示本地文件入口"

// netdiskListTool 列出网盘目录。
type netdiskListTool struct{}

func (netdiskListTool) Name() string { return "netdisk_list" }

func (netdiskListTool) Description() string {
	return "列出指定网盘账号下某个目录的文件与子目录。用于回答「这个目录里有什么」。"
}

func (netdiskListTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"account_id"}, map[string]any{
		"account_id": intProp(netdiskAccountProp),
		"parent_id":  strProp("父目录 ID；留空表示根目录"),
		"page":       intProp("页码，默认 1"),
		"page_size":  intProp("每页条数，默认 20，上限 100"),
		"refresh":    boolPropDefault("是否跳过缓存强制刷新，默认 false", false),
	})
}

func (netdiskListTool) ReadOnly() bool { return true }

func (netdiskListTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	hookMu.RLock()
	fn := netdiskListFn
	hookMu.RUnlock()
	if fn == nil {
		return nil, ErrRunnerNotRegistered
	}
	accountID, err := requireUint(args, "account_id", netdiskAccountProp)
	if err != nil {
		// 账号 0 是合法值（本地入口），所以这里只看字段是否存在。
		if _, ok := args["account_id"]; !ok {
			return nil, err
		}
		accountID = 0
	}
	page, pageSize := clampPage(argInt(args, "page"), argInt(args, "page_size"))
	return fn(ctx, accountID, argString(args, "parent_id"), page, pageSize, argBool(args, "refresh", false))
}

// netdiskRenameTool 重命名。
type netdiskRenameTool struct{}

func (netdiskRenameTool) Name() string { return "netdisk_rename" }

func (netdiskRenameTool) Description() string {
	return "重命名网盘上的文件或目录。会改变网盘内容，调用前必须先向用户确认。"
}

func (netdiskRenameTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"account_id", "file_id", "new_name"}, map[string]any{
		"account_id": intProp(netdiskAccountProp),
		"file_id":    strProp("要重命名的文件/目录 ID"),
		"new_name":   strProp("新名称（不含路径）"),
	})
}

func (netdiskRenameTool) ReadOnly() bool { return false }

func (netdiskRenameTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	hookMu.RLock()
	fn := netdiskRenameFn
	hookMu.RUnlock()
	if fn == nil {
		return nil, ErrRunnerNotRegistered
	}
	accountID := argUint(args, "account_id")
	fileID, err := requireString(args, "file_id", "文件 ID")
	if err != nil {
		return nil, err
	}
	newName, err := requireString(args, "new_name", "新名称")
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(newName, "/\\") {
		return nil, fmt.Errorf("新名称不能包含路径分隔符：%s", newName)
	}
	if err := fn(ctx, accountID, fileID, newName); err != nil {
		return nil, err
	}
	return map[string]any{"file_id": fileID, "new_name": newName, "message": "重命名完成"}, nil
}

// netdiskMoveTool 移动。
type netdiskMoveTool struct{}

func (netdiskMoveTool) Name() string { return "netdisk_move" }

func (netdiskMoveTool) Description() string {
	return "把网盘上的文件或目录移动到另一个目录。会改变网盘内容，调用前必须先向用户确认。"
}

func (netdiskMoveTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"account_id", "file_id", "target_parent_id"}, map[string]any{
		"account_id":       intProp(netdiskAccountProp),
		"file_id":          strProp("要移动的文件/目录 ID"),
		"target_parent_id": strProp("目标父目录 ID"),
	})
}

func (netdiskMoveTool) ReadOnly() bool { return false }

func (netdiskMoveTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	hookMu.RLock()
	fn := netdiskMoveFn
	hookMu.RUnlock()
	if fn == nil {
		return nil, ErrRunnerNotRegistered
	}
	accountID := argUint(args, "account_id")
	fileID, err := requireString(args, "file_id", "文件 ID")
	if err != nil {
		return nil, err
	}
	targetID, err := requireString(args, "target_parent_id", "目标父目录 ID")
	if err != nil {
		return nil, err
	}
	if err := fn(ctx, accountID, fileID, targetID); err != nil {
		return nil, err
	}
	return map[string]any{"file_id": fileID, "target_parent_id": targetID, "message": "移动完成"}, nil
}

// netdiskMkdirTool 新建目录。
type netdiskMkdirTool struct{}

func (netdiskMkdirTool) Name() string { return "netdisk_mkdir" }

func (netdiskMkdirTool) Description() string {
	return "在网盘指定目录下新建一个子目录。会改变网盘内容，调用前必须先向用户确认。"
}

func (netdiskMkdirTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"account_id", "name"}, map[string]any{
		"account_id":  intProp(netdiskAccountProp),
		"parent_id":   strProp("父目录 ID；留空表示根目录"),
		"parent_path": strProp("父目录展示路径，部分网盘（如 115）新建时需要"),
		"name":        strProp("要创建的目录名（不含路径）"),
	})
}

func (netdiskMkdirTool) ReadOnly() bool { return false }

func (netdiskMkdirTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	hookMu.RLock()
	fn := netdiskMkdirFn
	hookMu.RUnlock()
	if fn == nil {
		return nil, ErrRunnerNotRegistered
	}
	accountID := argUint(args, "account_id")
	name, err := requireString(args, "name", "目录名")
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("目录名不能包含路径分隔符：%s", name)
	}
	return fn(ctx, accountID, argString(args, "parent_id"), argString(args, "parent_path"), name)
}

// netdiskDeleteTool 删除（高风险写操作）。
type netdiskDeleteTool struct{}

func (netdiskDeleteTool) Name() string { return "netdisk_delete" }

func (netdiskDeleteTool) Description() string {
	return "删除网盘上的文件或目录。这是不可恢复的高风险操作：必须传 confirm=true 与 confirm_name（目标对象的确切名称）作为二次确认。调用前必须先向用户说明将删除什么并取得明确同意。"
}

func (netdiskDeleteTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"account_id", "parent_id", "file_id", "confirm", "confirm_name"}, map[string]any{
		"account_id":   intProp(netdiskAccountProp),
		"parent_id":    strProp("目标对象所在的父目录 ID（用于复核名称）"),
		"file_id":      strProp("要删除的文件/目录 ID"),
		"confirm":      boolProp("必须显式传 true 表示已确认删除"),
		"confirm_name": strProp("目标对象的确切名称，用于二次复核"),
	})
}

func (netdiskDeleteTool) ReadOnly() bool { return false }

func (netdiskDeleteTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	hookMu.RLock()
	fn := netdiskDeleteFn
	hookMu.RUnlock()
	if fn == nil {
		return nil, ErrRunnerNotRegistered
	}
	if !argBool(args, "confirm", false) {
		return nil, fmt.Errorf("删除操作需要确认：请传入 confirm=true 并同时提供 confirm_name（目标对象的确切名称）")
	}
	accountID := argUint(args, "account_id")
	fileID, err := requireString(args, "file_id", "文件 ID")
	if err != nil {
		return nil, err
	}
	confirmName, err := requireString(args, "confirm_name", "目标对象名称")
	if err != nil {
		return nil, err
	}
	if err := fn(ctx, accountID, argString(args, "parent_id"), fileID, confirmName); err != nil {
		return nil, err
	}
	return map[string]any{"file_id": fileID, "message": "删除完成"}, nil
}
