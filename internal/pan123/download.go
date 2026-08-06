package pan123

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"resty.dev/v3"
)

// GetDownloadInfo 获取文件下载信息
func (c *Client) GetDownloadInfo(ctx context.Context, file File) (*DownloadInfoResp, error) {
	data := map[string]interface{}{
		"driveId":   0,
		"etag":      file.Etag,
		"fileId":    file.FileId,
		"fileName":  file.FileName,
		"s3keyFlag": file.S3KeyFlag,
		"size":      file.Size,
		"type":      file.Type,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	respBody, err := c.Request(ctx, c.api("/file/download_info"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body)
	})
	if err != nil {
		return nil, err
	}
	var downloadInfo DownloadInfoResp
	if err := json.Unmarshal(respBody, &downloadInfo); err != nil {
		return nil, fmt.Errorf("解析下载信息失败：%w", err)
	}
	return &downloadInfo, nil
}

// ResolveDownloadURL 解析最终可用的下载直链
// 123 云盘的 DownloadUrl 可能：
//  1. 直接可下载
//  2. 带 params 参数（base64 编码的真实 URL）
//  3. 访问后 302 重定向或返回 JSON（data.redirect_url）
func (c *Client) ResolveDownloadURL(ctx context.Context, rawUrl string) (string, error) {
	if rawUrl == "" {
		return "", fmt.Errorf("下载链接为空")
	}
	u, err := url.Parse(rawUrl)
	if err != nil {
		return "", fmt.Errorf("解析下载链接失败：%w", err)
	}
	// 处理 params 参数（base64 编码的真实 URL）
	if params := u.Query().Get("params"); params != "" {
		decoded, err := base64.StdEncoding.DecodeString(params)
		if err != nil {
			return "", fmt.Errorf("解码下载链接 params 失败：%w", err)
		}
		realUrl, err := url.Parse(string(decoded))
		if err != nil {
			return "", fmt.Errorf("解析解码后的下载链接失败：%w", err)
		}
		u = realUrl
	}

	// 使用标准库 http.Client，禁止自动重定向，手动解析 302
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("构造下载请求失败：%w", err)
	}
	req.Header.Set(HeaderReferer, webReferer)
	res, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("访问下载链接失败：%w", err)
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusFound || res.StatusCode == http.StatusMovedPermanently || res.StatusCode == http.StatusTemporaryRedirect:
		// 302 重定向，取 location
		location := res.Header.Get("Location")
		if location == "" {
			return "", fmt.Errorf("重定向响应缺少 Location")
		}
		return location, nil
	case res.StatusCode < http.StatusMultipleChoices:
		// 200 响应，可能是 JSON（data.redirect_url）或直接可下载
		resBody, _ := io.ReadAll(res.Body)
		var redirectResp struct {
			Data struct {
				RedirectURL string `json:"redirect_url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resBody, &redirectResp); err == nil && redirectResp.Data.RedirectURL != "" {
			return redirectResp.Data.RedirectURL, nil
		}
		return u.String(), nil
	default:
		return "", fmt.Errorf("下载链接响应异常，状态码：%d", res.StatusCode)
	}
}
