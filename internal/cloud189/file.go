package cloud189

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 文件操作（对齐 Cloud189Service）
// ---------------------------------------------------------------------------

// ListFiles 个人网盘文件列表
func (c *Client) ListFiles(ctx context.Context, folderID string) ([]FileInfo, error) {
	q := url.Values{
		"folderId":   {folderID},
		"mediaType":  {"0"},
		"orderBy":    {"lastOpTime"},
		"descending": {"true"},
		"pageNum":    {"1"},
		"pageSize":   {"1000"},
	}
	body, err := c.signedGet(ctx, WebURL, "/api/open/file/listFiles.action", q, nil)
	if err != nil {
		return nil, err
	}
	return parseFileList(body)
}

func parseFileList(body []byte) ([]FileInfo, error) {
	var out struct {
		ResCode    int    `json:"res_code"`
		ResMsg     string `json:"res_msg"`
		FileListAO struct {
			FileList []struct {
				ID             json.Number `json:"id"`
				Name           string      `json:"name"`
				Size           json.Number `json:"size"`
				IsFolder       bool        `json:"isFolder"`
				MD5            string      `json:"md5"`
				SliceMD5       string      `json:"sliceMd5"`
				ParentFolderID json.Number `json:"parentFolderId"`
				CreateDate     string      `json:"createDate"`
				LastOpTime     string      `json:"lastOpTime"`
			} `json:"fileList"`
		} `json:"fileListAO"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析文件列表失败: %w", err)
	}
	if out.ResCode != 0 {
		return nil, fmt.Errorf("天翼云盘接口错误: %s (code=%d)", out.ResMsg, out.ResCode)
	}
	items := make([]FileInfo, 0, len(out.FileListAO.FileList))
	for _, f := range out.FileListAO.FileList {
		size, _ := f.Size.Int64()
		items = append(items, FileInfo{
			ID:         f.ID.String(),
			Name:       f.Name,
			Size:       size,
			IsDir:      f.IsFolder,
			MD5:        f.MD5,
			SliceMD5:   f.SliceMD5,
			ParentID:   f.ParentFolderID.String(),
			CreateDate: parseCloudTime(f.CreateDate),
			LastOpTime: parseCloudTime(f.LastOpTime),
		})
	}
	return items, nil
}

func parseCloudTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// CreateFolder 新建目录
func (c *Client) CreateFolder(ctx context.Context, parentFolderID, folderName string) (string, error) {
	form := url.Values{
		"parentFolderId": {parentFolderID},
		"folderName":     {folderName},
	}
	body, err := c.signedPost(ctx, WebURL, "/api/open/file/createFolder.action", form, nil)
	if err != nil {
		return "", err
	}
	var out struct {
		ResCode int         `json:"res_code"`
		ResMsg  string      `json:"res_msg"`
		ID      json.Number `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ResCode != 0 {
		return "", fmt.Errorf("创建目录失败: %s", out.ResMsg)
	}
	return out.ID.String(), nil
}

// EnsureFolderPath 逐级确保目录存在，返回末级目录 ID
func (c *Client) EnsureFolderPath(ctx context.Context, rootID, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return rootID, nil
	}
	current := rootID
	for _, seg := range strings.Split(path, "/") {
		files, err := c.ListFiles(ctx, current)
		if err != nil {
			return "", err
		}
		var found string
		for _, f := range files {
			if f.IsDir && f.Name == seg {
				found = f.ID
				break
			}
		}
		if found == "" {
			found, err = c.CreateFolder(ctx, current, seg)
			if err != nil {
				return "", err
			}
		}
		current = found
	}
	return current, nil
}

// RenameFile 重命名
func (c *Client) RenameFile(ctx context.Context, fileID, destName string) error {
	form := url.Values{
		"fileId":       {fileID},
		"destFileName": {destName},
	}
	body, err := c.signedPost(ctx, WebURL, "/api/open/file/renameFile.action", form, nil)
	if err != nil {
		return err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	if out.ResCode != 0 {
		return fmt.Errorf("重命名失败: %s", out.ResMsg)
	}
	return nil
}

// DeleteFile 删除文件（个人网盘）
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	form := url.Values{"fileId": {fileID}}
	body, err := c.signedPost(ctx, WebURL, "/api/open/file/deleteFile.action", form, nil)
	if err != nil {
		return err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	if out.ResCode != 0 {
		return fmt.Errorf("删除文件失败: %s", out.ResMsg)
	}
	return nil
}

// GetDownloadLink 获取网盘直链（302）
func (c *Client) GetDownloadLink(ctx context.Context, fileID string, shareID string) (string, error) {
	typ := "2"
	if shareID != "" {
		typ = "4"
	}
	q := url.Values{
		"fileId":  {fileID},
		"shareId": {shareID},
		"type":    {typ},
		"dt":      {"1"},
	}
	body, err := c.signedGet(ctx, WebURL, "/api/portal/getNewVlcVideoPlayUrl.action", q, nil)
	if err != nil {
		return "", err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
		Normal  struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			URL     string `json:"url"`
		} `json:"normal"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ResCode != 0 {
		return "", fmt.Errorf("获取直链失败: %s", out.ResMsg)
	}
	if out.Normal.Code != 1 {
		return "", fmt.Errorf("获取直链失败: %s", out.Normal.Message)
	}
	// 跟随 302 获取最终直链
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, out.Normal.URL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36 Edg/119.0.0.0")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("直链 302 无 Location")
	}
	return loc, nil
}

// GetFamilyInfo 获取家庭云信息（第一个 userRole==1 的家庭）
func (c *Client) GetFamilyInfo(ctx context.Context) (*FamilyInfo, error) {
	body, err := c.signedGet(ctx, APIURL, "/open/family/manage/getFamilyList.action", nil, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		FamilyInfoResp []struct {
			FamilyID     int64  `json:"familyId"`
			RootFolderID string `json:"rootFolderId"`
			UserRole     int    `json:"userRole"`
		} `json:"familyInfoResp"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	for _, f := range out.FamilyInfoResp {
		if f.UserRole == 1 {
			return &FamilyInfo{FamilyID: f.FamilyID, RootFolderID: f.RootFolderID, UserRole: f.UserRole}, nil
		}
	}
	return nil, fmt.Errorf("无可用家庭云")
}
