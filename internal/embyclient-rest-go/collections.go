// Package embyclient-rest-go —— 虚拟库/合集所需的最小扩展：
// 按 TMDB ID 找条目、创建合集、向合集添加条目。
package embyclientrestgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// FindItemByTmdb 按 TMDB Provider ID 查条目（AnyProviderIdEquals=tmdb.xxx）。
// isTV=true 按剧集 IncludeItemTypes=Series，否则电影 Movie。
func (c *Client) FindItemByTmdb(ctx context.Context, tmdbID int64, isTV bool) (string, error) {
	itemType := "Movie"
	if isTV {
		itemType = "Series"
	}
	params := url.Values{}
	params.Set("Recursive", "true")
	params.Set("IncludeItemTypes", itemType)
	params.Set("Limit", "1")
	params.Set("AnyProviderIdEquals", fmt.Sprintf("tmdb.%d", tmdbID))
	params.Set("Fields", "ProviderIds")
	params.Set("api_key", c.apiKey)
	base, err := url.Parse(strings.TrimRight(c.embyURL, "/") + "/emby/Items")
	if err != nil {
		return "", err
	}
	base.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Emby 条目查询 HTTP %d", resp.StatusCode)
	}
	var out struct {
		Items []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 {
		return "", nil
	}
	return out.Items[0].Id, nil
}

// CreateCollection 创建 Emby 合集（BoxSet），返回合集 Item ID。
func (c *Client) CreateCollection(ctx context.Context, name string) (string, error) {
	params := url.Values{}
	params.Set("Name", name)
	params.Set("Ids", "")
	params.Set("ParentId", "")
	params.Set("IsFolder", "true")
	params.Set("api_key", c.apiKey)
	body, err := c.postJSON(ctx, "/emby/Collections?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Id string `json:"Id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Id == "" {
		return "", fmt.Errorf("创建合集响应解析失败：%s", truncateStr(string(body), 160))
	}
	return out.Id, nil
}

// AddToCollection 把条目批量加入合集。
func (c *Client) AddToCollection(ctx context.Context, collectionID string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	params := url.Values{}
	params.Set("Ids", strings.Join(itemIDs, ","))
	params.Set("api_key", c.apiKey)
	_, err := c.postJSON(ctx, "/emby/Collections/"+url.PathEscape(collectionID)+"/Items?"+params.Encode(), nil)
	return err
}

// postJSON POST 请求（2xx 即成功，返回 body）
func (c *Client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.embyURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Emby HTTP %d：%s", resp.StatusCode, truncateStr(string(data), 160))
	}
	return data, nil
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
