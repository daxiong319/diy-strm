// Package gcid 光鸭云盘 GCID 秒传导出/导入（对齐 tgto123 新版 guangya_gcid_transfer + gcid_cid_cache 语义）。
// 导出：递归扫描光鸭目录 → 对每个文件取 GCID（列表接口直带；缺失的按 DownloadURL 流式计算）→
// 生成秒传 JSON → 发送到 TG 机器人（用户保存/分享）。
// 导入：粘贴秒传 JSON → 逐条 GetResCenterToken（GCID 秒传）→ 失败项跳过并汇报。
package gcid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/discovery"
	"diy-strm/internal/guangyapan"
	"diy-strm/internal/helpers"
)

// GcidItem 秒传 JSON 单条记录
type GcidItem struct {
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	GCID     string `json:"gcid"`
	// 可选：原始目录相对路径（导出时保留，导入时可重建目录）
	RelPath string `json:"rel_path,omitempty"`
}

// GcidExportDoc 秒传 JSON 文档
type GcidExportDoc struct {
	Version    int        `json:"version"`
	Provider   string     `json:"provider"`
	SourceDir  string     `json:"source_dir,omitempty"`
	ExportedAt int64      `json:"exported_at"`
	Items      []GcidItem `json:"items"`
}

// gcidFileListResp 文件列表响应（File 之外的原始字段按需提取 GCID）
type gcidFileListResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Total int `json:"total"`
		List  []struct {
			FileID   string `json:"fileId"`
			ParentID string `json:"parentId"`
			FileName string `json:"fileName"`
			FileSize int64  `json:"fileSize"`
			ResType  int    `json:"resType"`
			GCID     string `json:"gcid"`
		} `json:"list"`
	} `json:"data"`
}

// ScanDirForExport 递归扫描目录生成秒传条目（限并发、限总量）。
// GCID 来源优先级：列表接口直带字段 → 下载直链流式计算（CalculateGCID）。
func ScanDirForExport(ctx context.Context, client *guangyapan.Client, dirID, dirPath string, maxFiles int) ([]GcidItem, error) {
	if maxFiles <= 0 {
		maxFiles = 2000
	}
	items := make([]GcidItem, 0, 128)
	var mu sync.Mutex
	var walkErr error

	var walk func(ctx context.Context, parentID, relPath string) error
	walk = func(ctx context.Context, parentID, relPath string) error {
		if ctx.Err() != nil || walkErr != nil {
			return walkErr
		}
		mu.Lock()
		if len(items) >= maxFiles {
			mu.Unlock()
			return nil
		}
		mu.Unlock()
		page := 1
		for {
			resp, err := listFilesRaw(ctx, client, parentID, page)
			if err != nil {
				return err
			}
			for _, f := range resp.Data.List {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				mu.Lock()
				full := len(items) >= maxFiles
				mu.Unlock()
				if full {
					return nil
				}
				childRel := relPath
				if childRel != "" {
					childRel += "/"
				}
				if f.ResType == 2 {
					if err := walk(ctx, f.FileID, childRel+f.FileName); err != nil {
						return err
					}
					continue
				}
				gcid := strings.ToUpper(strings.TrimSpace(f.GCID))
				if gcid == "" && f.FileSize > 0 {
					if computed, cErr := computeGcidViaDownload(ctx, client, f.FileID, f.FileSize); cErr == nil {
						gcid = computed
					} else {
						helpers.AppLogger.Warnf("[GCID 导出] %s 计算失败，跳过：%v", f.FileName, cErr)
						continue
					}
				}
				if gcid == "" {
					continue
				}
				mu.Lock()
				items = append(items, GcidItem{
					FileName: f.FileName,
					FileSize: f.FileSize,
					GCID:     gcid,
					RelPath:  childRel + f.FileName,
				})
				mu.Unlock()
			}
			if len(resp.Data.List) < guangyapan.PageSize || len(items) >= maxFiles {
				break
			}
			page++
		}
		return nil
	}

	if err := walk(ctx, dirID, ""); err != nil {
		return items, err
	}
	return items, walkErr
}

// listFilesRaw 列目录（保留 GCID 原始字段）
func listFilesRaw(ctx context.Context, client *guangyapan.Client, parentID string, page int) (*gcidFileListResp, error) {
	body := map[string]interface{}{
		"parentId":  parentID,
		"page":      page,
		"pageSize":  guangyapan.PageSize,
		"orderBy":   3,
		"sortType":  1,
		"fileTypes": []int{},
	}
	var out gcidFileListResp
	if err := client.Request(ctx, guangyapan.APIFileList, body, &out); err != nil {
		return nil, err
	}
	if out.Code != 0 && out.Code != 200 {
		return nil, fmt.Errorf("光鸭云盘列表失败：code=%d msg=%s", out.Code, out.Msg)
	}
	return &out, nil
}

// computeGcidViaDownload 走下载直链流式计算 GCID
func computeGcidViaDownload(ctx context.Context, client *guangyapan.Client, fileID string, size int64) (string, error) {
	urlText, err := client.GetDownloadURL(ctx, fileID)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlText, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载直链 HTTP %d", resp.StatusCode)
	}
	return guangyapan.CalculateGCIDFromReader(resp.Body, size)
}

// ImportFromDoc 从秒传 JSON 导入到光鸭目标目录（逐条秒传，失败跳过）。
// 返回成功/失败计数与失败明细（最多 20 条）。
func ImportFromDoc(ctx context.Context, client *guangyapan.Client, doc *GcidExportDoc, targetDirID string) (okCount, failCount int, failed []string) {
	for _, item := range doc.Items {
		if ctx.Err() != nil {
			break
		}
		if item.GCID == "" || item.FileSize <= 0 || item.FileName == "" {
			failCount++
			if len(failed) < 20 {
				failed = append(failed, item.FileName+"（缺少 GCID 或大小）")
			}
			continue
		}
		if err := flashImportOne(ctx, client, targetDirID, item); err != nil {
			failCount++
			if len(failed) < 20 {
				failed = append(failed, item.FileName+"："+err.Error())
			}
			continue
		}
		okCount++
	}
	return okCount, failCount, failed
}

// flashImportOne 单文件 GCID 秒传：申请凭证（带 GCID 语义的 gcid 参数由 check_can_flash_upload 完成）→
// 命中后等待入库；未命中返回错误（不回退普通上传——导入场景没有本地文件）。
func flashImportOne(ctx context.Context, client *guangyapan.Client, parentID string, item GcidItem) error {
	token, rapid, err := client.GetResCenterToken(ctx, parentID, item.FileName, item.FileSize, "")
	if err != nil {
		return err
	}
	taskID := token.TaskID
	if !rapid {
		canFlash, newTaskID, ferr := client.CheckCanFlashUpload(ctx, taskID, item.GCID)
		if ferr != nil {
			return ferr
		}
		if !canFlash {
			return errors.New("秒传未命中（源文件可能已被删除或哈希不符）")
		}
		if newTaskID != "" {
			taskID = newTaskID
		}
	}
	// 等待云端入库完成
	_, err = client.WaitFileUploadedExported(ctx, taskID)
	return err
}

// SaveExportHistory 记录最近一次导出（诊断用，写 discovery_settings）
func SaveExportHistory(sourceDir string, count int, fileName string) {
	payload := map[string]any{
		"source_dir":  sourceDir,
		"count":       count,
		"file_name":   fileName,
		"exported_at": time.Now().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(payload)
	setting := discovery.DiscoverySetting{Key: "gcid_last_export", Value: string(raw), UpdatedAt: time.Now()}
	if err := db.Db.Save(&setting).Error; err != nil {
		helpers.AppLogger.Debugf("GCID 导出历史写入失败：%v", err)
	}
}
