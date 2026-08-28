package agent

import (
	"strings"
)

// clipText 仅用于日志和界面预览，不得写回模型事实历史。
func clipText(input string, max int) string {
	input = strings.TrimSpace(input)
	if input == "" || max <= 0 {
		return input
	}
	runes := []rune(input)
	if len(runes) <= max {
		return input
	}
	return string(runes[:max]) + "...(truncated)"
}
