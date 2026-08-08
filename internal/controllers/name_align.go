package controllers

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"diy-strm/internal/models"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// ---- 文件名解析 ----

// nameAlignParsed 文件名解析出的剧集信息。
type nameAlignParsed struct {
	Title   string
	Season  int
	Episode int
}

var (
	// nameAlignSxxExx 匹配 S01E01 / s01e01 / S1E01 形式。
	nameAlignSxxExx = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})\s*[ex](\d{1,3})(?:$|[^a-z0-9])`)
	// nameAlignNxN 匹配 1x01 形式。
	nameAlignNxN = regexp.MustCompile(`(?:^|[^a-z0-9])(\d{1,2})[xX](\d{1,3})(?:$|[^a-z0-9])`)
	// nameAlignEp 匹配 EP01 / ep.01 形式。
	nameAlignEp = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ep\s*\.?\s*(\d{1,3})(?:$|[^a-z0-9])`)
	// nameAlignChinese 匹配 第01集 / 第1话 形式（无前导边界，避免吞掉标题末位汉字）。
	nameAlignChinese = regexp.MustCompile(`第\s*(\d{1,3})\s*[集話话]`)
)

// parseNameAlignEpisode 从文件名解析剧集信息，解析失败返回 false。
func parseNameAlignEpisode(fileName string) (nameAlignParsed, bool) {
	ext := path.Ext(fileName)
	stem := strings.TrimSuffix(fileName, ext)

	if m := nameAlignSxxExx.FindStringSubmatchIndex(stem); m != nil {
		return nameAlignParsed{
			Title:   cleanMediaTitle(stem[:m[0]]),
			Season:  nameAlignAtoiRange(stem, m[2], m[3]),
			Episode: nameAlignAtoiRange(stem, m[4], m[5]),
		}, true
	}
	if m := nameAlignNxN.FindStringSubmatchIndex(stem); m != nil {
		return nameAlignParsed{
			Title:   cleanMediaTitle(stem[:m[0]]),
			Season:  nameAlignAtoiRange(stem, m[2], m[3]),
			Episode: nameAlignAtoiRange(stem, m[4], m[5]),
		}, true
	}
	if m := nameAlignEp.FindStringSubmatchIndex(stem); m != nil {
		return nameAlignParsed{
			Title:   cleanMediaTitle(stem[:m[0]]),
			Episode: nameAlignAtoiRange(stem, m[2], m[3]),
		}, true
	}
	if m := nameAlignChinese.FindStringSubmatchIndex(stem); m != nil {
		// 防止把年份等数字结尾误当标题一部分（如 "2024第3集"），要求 "第" 前不是数字。
		if m[0] > 0 && stem[m[0]-1] >= '0' && stem[m[0]-1] <= '9' {
			return nameAlignParsed{}, false
		}
		return nameAlignParsed{
			Title:   cleanMediaTitle(stem[:m[0]]),
			Episode: nameAlignAtoiRange(stem, m[2], m[3]),
		}, true
	}
	return nameAlignParsed{}, false
}

// cleanMediaTitle 清洗剧名：去除季集匹配前的尾部杂讯，并将常见分隔符归一为空格。
func cleanMediaTitle(raw string) string {
	raw = strings.TrimRight(raw, " \t._-[]()·:,;，。；'\"!！?？&")
	repl := strings.NewReplacer(".", " ", "_", " ", "-", " ", "·", " ", "(", " ", ")", " ", "[", " ", "]", " ", "【", " ", "】", " ")
	fields := strings.Fields(repl.Replace(raw))
	return strings.Join(fields, " ")
}

func nameAlignAtoiRange(s string, start, end int) int {
	v, err := strconv.Atoi(s[start:end])
	if err != nil {
		return 0
	}
	return v
}

// buildNameAlignNewName 按媒体类型与标题生成规范化文件名，无法生成时返回 false。
func buildNameAlignNewName(parsed nameAlignParsed, mediaTitle string, mediaType string, year int, ext string) (string, bool) {
	title := strings.TrimSpace(mediaTitle)
	if title == "" {
		title = parsed.Title
	}
	if title == "" {
		return "", false
	}
	switch mediaType {
	case "movie":
		if year > 0 {
			return fmt.Sprintf("%s (%d)%s", title, year, ext), true
		}
		return title + ext, true
	default:
		season := parsed.Season
		if season == 0 {
			season = 1
		}
		if parsed.Episode == 0 {
			return "", false
		}
		return fmt.Sprintf("%s S%02dE%02d%s", title, season, parsed.Episode, ext), true
	}
}

// ---- 控制器 ----

type nameAlignPreviewItem struct {
	FileID  string `json:"file_id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
	Changed bool   `json:"changed"`
	Reason  string `json:"reason,omitempty"`
}

// NameAlignPreview 命名对齐预览：解析选中文件名并生成规范化建议名。
func NameAlignPreview(c *gin.Context) {
	var req requests.NameAlignPreviewRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	mediaTitle := strings.TrimSpace(req.MediaTitle)
	result := make([]nameAlignPreviewItem, 0, len(req.Items))
	for _, item := range req.Items {
		parsed, ok := parseNameAlignEpisode(item.Name)
		if !ok {
			result = append(result, nameAlignPreviewItem{FileID: item.FileID, OldName: item.Name, Reason: "无法从文件名解析季集信息"})
			continue
		}
		newName, ok := buildNameAlignNewName(parsed, mediaTitle, req.MediaType, req.Year, path.Ext(item.Name))
		if !ok {
			result = append(result, nameAlignPreviewItem{FileID: item.FileID, OldName: item.Name, Reason: "无法生成新名称，请填写媒体标题"})
			continue
		}
		result = append(result, nameAlignPreviewItem{
			FileID:  item.FileID,
			OldName: item.Name,
			NewName: newName,
			Changed: newName != item.Name,
		})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "预览成功", Data: result})
}

type nameAlignApplySuccess struct {
	FileID  string `json:"file_id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

type nameAlignApplyFailed struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type nameAlignApplyResult struct {
	Success []nameAlignApplySuccess `json:"success"`
	Failed  []nameAlignApplyFailed  `json:"failed"`
}

// NameAlignApply 命名对齐应用：按预览结果批量重命名，逐条执行并汇总结果。
func NameAlignApply(c *gin.Context) {
	var req requests.NameAlignApplyRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	result := nameAlignApplyResult{
		Success: make([]nameAlignApplySuccess, 0, len(req.Items)),
		Failed:  make([]nameAlignApplyFailed, 0),
	}
	for _, item := range req.Items {
		if item.NewName == item.Name {
			result.Success = append(result.Success, nameAlignApplySuccess{FileID: item.FileID, OldName: item.Name, NewName: item.Name})
			continue
		}
		if err := renameNetdiskFile(account, item.FileID, item.NewName); err != nil {
			result.Failed = append(result.Failed, nameAlignApplyFailed{FileID: item.FileID, Name: item.Name, Reason: err.Error()})
			continue
		}
		result.Success = append(result.Success, nameAlignApplySuccess{FileID: item.FileID, OldName: item.Name, NewName: item.NewName})
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.ParentID)
	message := fmt.Sprintf("重命名完成：成功 %d 个，失败 %d 个", len(result.Success), len(result.Failed))
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: result})
}
