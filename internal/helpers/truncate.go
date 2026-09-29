package helpers

// TruncateID 截断 ID 用于日志
func TruncateID(s string) string {
	if len(s) <= 24 {
		return s
	}
	return s[:24] + "..."
}
