package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	htmlScriptElementRe = regexp.MustCompile(`(?is)<\s*script\b[^>]*>.*?<\s*/\s*script\s*>`)
	htmlScriptTagRe     = regexp.MustCompile(`(?is)<\s*/?\s*script\b[^>]*>`)

	htmlEventAttrRe = regexp.MustCompile(`(?i)\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*)`)

	htmlEventAttrNoSpaceRe = regexp.MustCompile(`(?i)/on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*)`)

	htmlDangerousHrefRe = regexp.MustCompile(`(?i)((?:href|src)\s*=\s*["'])\s*(?:javascript|vbscript|data|blob)[\s]*:[^"']*`)
)

func sanitizeReportHTML(html string) string {
	html = htmlScriptElementRe.ReplaceAllString(html, "")
	html = htmlScriptTagRe.ReplaceAllString(html, "")

	html = htmlEventAttrNoSpaceRe.ReplaceAllString(html, "")

	html = htmlEventAttrRe.ReplaceAllString(html, "")

	html = htmlDangerousHrefRe.ReplaceAllStringFunc(html, func(match string) string {
		idx := strings.IndexAny(match, `"'`)
		if idx < 0 {
			return match
		}
		return match[:idx+1]
	})

	return html
}

func applyReportHTMLGuardrail(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return result
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return result
	}
	html, ok := payload["html"].(string)
	if !ok || strings.TrimSpace(html) == "" {
		return result
	}
	payload["html"] = sanitizeReportHTML(html)
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return string(sanitized)
}
