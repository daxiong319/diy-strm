package config

import (
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir    string
	StrmDir    string
	DBPath     string
	ListenAddr string
	LogLevel   string
	// MediaDir 本地媒体根目录，供本地文件生成 CAS 清单使用。
	// 为空表示该功能未启用（不设置默认值，避免意外暴露任意本地路径）。
	MediaDir string
	// MediaDirs 额外的本地媒体根目录（逗号分隔的 LITEPAN_MEDIA_DIRS）。
	// 与 MediaDir 一起构成允许的根目录白名单；两者都为空即功能未启用。
	MediaDirs []string
}

func Default() Config {
	dataDir := "./data"
	return Config{
		DataDir:    dataDir,
		StrmDir:    "./strm",
		DBPath:     filepath.Join(dataDir, "litepan.db"),
		ListenAddr: ":5211",
		LogLevel:   "info",
		// MediaDir 默认空：本地生成 CAS 属于高权限能力，必须显式开启。
		MediaDir: "",
	}
}

// StrmDirForData 返回数据目录同级的 STRM 目录。
func StrmDirForData(dataDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "strm")
}

// MediaRoots 返回允许的本地媒体根目录白名单（MediaDir + MediaDirs，去空白项）。
// 返回空切片表示本地生成 CAS 功能未启用。
func (c Config) MediaRoots() []string {
	var roots []string
	if v := strings.TrimSpace(c.MediaDir); v != "" {
		roots = append(roots, v)
	}
	for _, d := range c.MediaDirs {
		if v := strings.TrimSpace(d); v != "" {
			roots = append(roots, v)
		}
	}
	return roots
}

// Load 在默认值基础上应用 LITEPAN_* 环境变量覆盖。
func Load() Config {
	c := Default()
	if v := strings.TrimSpace(os.Getenv("LITEPAN_DATA_DIR")); v != "" {
		c.DataDir = v
		c.DBPath = filepath.Join(v, "litepan.db")
		if strings.TrimSpace(os.Getenv("LITEPAN_STRM_DIR")) == "" {
			c.StrmDir = StrmDirForData(v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LITEPAN_STRM_DIR")); v != "" {
		c.StrmDir = v
	}
	if v := strings.TrimSpace(os.Getenv("LITEPAN_MEDIA_DIR")); v != "" {
		c.MediaDir = v
	}
	// LITEPAN_MEDIA_DIRS 逗号分隔，追加到白名单；空项跳过。
	if v := strings.TrimSpace(os.Getenv("LITEPAN_MEDIA_DIRS")); v != "" {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				c.MediaDirs = append(c.MediaDirs, p)
			}
		}
	}
	if v := os.Getenv("LITEPAN_DB_PATH"); v != "" {
		c.DBPath = v
	}
	if v := os.Getenv("LITEPAN_LISTEN"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("LITEPAN_LOG_LEVEL"); v != "" {
		c.LogLevel = strings.ToLower(v)
	}
	return c
}
