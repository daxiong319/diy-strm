package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"litepan/internal/domain"
)

// tsLayout 与 SQLite CURRENT_TIMESTAMP 文本格式一致（UTC）。
const tsLayout = "2006-01-02 15:04:05"

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// parseTS 把可空的时间文本解析为 time.Time，NULL/空值返回零值。
func parseTS(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	raw := strings.TrimSpace(ns.String)
	if i := strings.Index(raw, " m="); i > 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	for _, layout := range []string{
		tsLayout,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// tsValue 把 time.Time 转为可写入的值，零值写 NULL。
func tsValue(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(tsLayout)
}

// wrapDB 把底层数据库错误归一为结构化 AppError，已是领域错误时原样返回，避免 NOT_FOUND 降级。
func wrapDB(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := domain.AsAppError(err); ok {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errf(domain.CodeNotFound)
	}
	return domain.Wrap(domain.CodeInternal, err)
}

// nowUnix 返回当前 Unix 时间戳（秒），与 emby_refresh_tasks 的时间列一致。
func nowUnix() int64 {
	return time.Now().Unix()
}

// mergeItemIDsJSON 合并两组以 JSON 数组文本保存的条目 ID，去重并排序。
// 任一输入非法时按空集合处理，保证合并结果始终是可解析的 JSON 数组。
func mergeItemIDsJSON(a, b string) string {
	merged := append(decodeJSONStringArray(a), decodeJSONStringArray(b)...)
	seen := make(map[string]struct{}, len(merged))
	out := make([]string, 0, len(merged))
	for _, id := range merged {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return "[]"
	}
	sort.Strings(out)
	buf, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(buf)
}

// decodeJSONStringArray 解析以 JSON 文本保存的字符串数组，失败时返回 nil。
func decodeJSONStringArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}
