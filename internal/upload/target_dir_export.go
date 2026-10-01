package upload

import "context"

// EnsureTargetDir 解析（必要时创建）账号下的相对目录，返回目录 ID。
// relDir 为相对 rootID 的路径（如 "CAS/动画"），为空时直接返回 rootID。
// 供 CAS 自动转存等外部模块复用上传目标目录逻辑。
func (m *Manager) EnsureTargetDir(ctx context.Context, accountID int64, rootID, relDir string) (string, error) {
	return ensureUploadTargetDir(ctx, m.files, m.targetDirCache, accountID, rootID, relDir)
}
