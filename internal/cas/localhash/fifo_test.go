package localhash

import (
	"os/exec"
	"runtime"
)

// mkfifo 创建命名管道用于测试"拒绝非普通文件"分支。
// 用 syscall.Mkfifo 需要平台构建标签，这里用 mkfifo 命令保持跨平台简单；
// 命令不可用时返回错误，调用方按 Skip 处理。
func mkfifo(path string) error {
	if runtime.GOOS == "windows" {
		return exec.ErrNotFound
	}
	return exec.Command("mkfifo", path).Run()
}
