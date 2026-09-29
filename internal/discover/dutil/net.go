package dutil

import (
	"fmt"
	"net/http"
	"time"
)

// PostUrl 对齐老 helpers.PostUrl：对目标 URL 发一个禁用重定向的 POST（用于 Emby 库刷新等触发类调用）。
func PostUrl(targetUrl string) error {
	req, err := http.NewRequest("POST", targetUrl, nil)
	if err != nil {
		return fmt.Errorf("创建 %s 的 HTTP POST 请求失败：%v", targetUrl, err)
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送 %s 的 HTTP POST 请求失败：%v", targetUrl, err)
	}
	defer resp.Body.Close()
	return nil
}
