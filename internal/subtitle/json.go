package subtitle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"resty.dev/v3"
)

// decodeJSONBody 把 resty 响应体解析成 v。
//
// 不用 resty 的 SetResult 是历史原因：老版各 provider 都先看状态码再决定解析，
// 这样错误分支不必伪装成"解析失败"。这里统一收口，顺带给出可读的错误文案。
func decodeJSONBody(resp *resty.Response, v any) error {
	if resp == nil || resp.RawResponse == nil || resp.RawResponse.Body == nil {
		return errors.New("响应体为空")
	}
	body, err := io.ReadAll(io.LimitReader(resp.RawResponse.Body, maxSubtitleFileSize))
	if err != nil {
		return fmt.Errorf("读取响应失败：%w", err)
	}
	if len(body) == 0 {
		return errors.New("响应体为空")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("解析响应 JSON 失败：%w", err)
	}
	return nil
}
