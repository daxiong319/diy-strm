package controllers

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// 需要过滤的隐藏/系统目录
var localPathExcludedDirs = map[string]bool{
	"$recycle.bin": true,
	"system volume information": true,
	"$winreagent": true,
}

// ListLocalDirectories 列出服务器本地的子目录
// @Summary 列出本地目录
// @Description 供本地路径选择器使用，仅返回目录列表
// @Tags 本地路径
// @Success 200 {object} APIResponse[any]
// @Router /path/local [get]
// @Security JwtAuth
func ListLocalDirectories(c *gin.Context) {
	base := c.DefaultQuery("path", "/")
	if strings.TrimSpace(base) == "" {
		base = "/"
	}
	base = filepath.Clean(base)
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "目录不存在或不可访问：" + base, Data: nil})
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取目录失败：" + err.Error(), Data: nil})
		return
	}
	dirs := make([]map[string]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if localPathExcludedDirs[strings.ToLower(name)] {
			continue
		}
		dirs = append(dirs, map[string]string{
			"name": name,
			"path": filepath.ToSlash(filepath.Join(base, name)),
		})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"] < dirs[j]["name"] })
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取目录列表成功", Data: map[string]any{
		"current_path": filepath.ToSlash(base),
		"dirs":         dirs,
	}})
}