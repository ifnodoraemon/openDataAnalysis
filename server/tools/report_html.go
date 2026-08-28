package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	htmlstd "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	htmlnode "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var (
	htmlHeadingRegexp = regexp.MustCompile(`(?im)^\s*<h[1-6][^>]*>(.*?)</h[1-6]>`)
	htmlTagsRegexp    = regexp.MustCompile(`<[^>]*>`)
	mdHeadingRegexp   = regexp.MustCompile(`(?m)^\s*(?:#{1,6})\s+(.+?)(?:\r?\n|$)`)
)

func ResolveReportTitleFromState(state *ReportState) string {
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state.FinalTitle)
}

// RenderReportHTML 生成完整的研报 HTML（含 ECharts 图表支持）
func RenderReportHTML(title, author string, state *ReportState) string {
	if state == nil {
		state = &ReportState{}
	}
	if title == "" && state != nil {
		title = ResolveReportTitleFromState(state)
	}
	if author == "" && state != nil {
		author = state.FinalAuthor
	}
	title = strings.TrimSpace(title)
	author = strings.TrimSpace(author)
	units := buildRenderUnits(state.Blocks, title)
	safeTitle := escapeHTMLText(title)
	titleHeaderHTML := renderReportTitleHeader(title, author)
	tocHTML := renderReportTOC(state.Blocks, title)

	var bodyHTML strings.Builder
	chapterNum := 0

	var lastBlockID string
	var wrapperOpen bool

	for _, unit := range units {
		block := unit.Block
		blockKind := block.Kind
		if _, ok := reportBlockRenderers[blockKind]; !ok {
			continue
		}
		if block.ID != lastBlockID {
			if wrapperOpen {
				bodyHTML.WriteString("</div>\n")
			}
			wrapperTitle := blockDisplayTitle(block)
			bodyHTML.WriteString(fmt.Sprintf(`<div class="report-block-wrapper" data-block-id="%s" data-block-kind="%s" data-block-title="%s">`+"\n",
				escapeHTMLAttr(block.ID),
				escapeHTMLAttr(blockKind),
				escapeHTMLAttr(wrapperTitle)))
			wrapperOpen = true
			lastBlockID = block.ID
		}

		chapterNum++
		bodyHTML.WriteString(renderReportBlockHTML(block, chapterNum, state.Charts))
	}

	if wrapperOpen {
		bodyHTML.WriteString("</div>\n")
	}

	chartScripts := buildChartScripts(state.Charts)
	customCSS := sanitizeCSS(state.Layout.CustomCSS)
	bodyClass := sanitizeBodyClass(state.Layout.BodyClass)

	customCSSBlock := ""
	if customCSS != "" {
		customCSSBlock = "\n" + customCSS + "\n"
	}

	echartsURL := "/assets/echarts.min.js"
	if config.Cfg != nil && config.Cfg.ReportEchartsUrl != "" {
		echartsURL = config.Cfg.ReportEchartsUrl
	}
	echartsScriptNode := fmt.Sprintf(`<script id="oda-echarts-loader" src="%s"></script>`, escapeHTMLAttr(echartsURL))

	katexCSS := `<link rel="stylesheet" href="` + ReportKaTeXCDNBaseURL + `katex.min.css">`
	katexScripts := `<script id="oda-math-loader" defer src="` + ReportKaTeXCDNBaseURL + `katex.min.js"></script>
<script id="oda-math-auto-render" defer src="` + ReportKaTeXCDNBaseURL + `contrib/auto-render.min.js"></script>
<script id="oda-math-runtime" defer src="/oda-math-runtime.js"></script>`

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
%s
%s
<style>
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&family=Noto+Sans+SC:wght@400;500;700;900&display=swap');

:root {
  --primary: #2563eb;
  --primary-light: #3b82f6;
  --primary-soft: #eff6ff;
  --accent: #10b981;
  --accent-light: #d1fae5;
  --text: #334155;
  --text-light: #64748b;
  --text-muted: #94a3b8;
  --bg: #ffffff;
  --bg-alt: #f8fafc;
  --bg-warm: #fcfcfd;
  --border: #e2e8f0;
  --border-light: #f1f5f9;
  --shadow-sm: 0 2px 4px rgba(0,0,0,0.02), 0 1px 2px rgba(0,0,0,0.04);
  --shadow-md: 0 4px 6px -1px rgba(0,0,0,0.05), 0 2px 4px -1px rgba(0,0,0,0.03);
  --shadow-lg: 0 10px 15px -3px rgba(0,0,0,0.05), 0 4px 6px -2px rgba(0,0,0,0.03);
  --shadow-hover: 0 20px 25px -5px rgba(0,0,0,0.08), 0 10px 10px -5px rgba(0,0,0,0.04);
  --radius: 16px;
  --radius-sm: 10px;
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: "Inter", "Noto Sans SC", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  color: var(--text);
  line-height: 1.7;
  background: var(--bg-alt);
  -webkit-font-smoothing: antialiased;
  padding: 1rem;
}

.report-titlebar {
  max-width: 820px;
  margin: 1.5rem auto 1rem;
  padding: 3rem;
  background: var(--bg);
  border-top: 6px solid var(--primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  position: relative;
  overflow: hidden;
}
.report-titlebar::before {
  content: '';
  position: absolute;
  top: 0; left: 0; right: 0; bottom: 0;
  background: linear-gradient(135deg, rgba(37,99,235,0.03) 0%%, rgba(255,255,255,0) 100%%);
  pointer-events: none;
}
.report-titlebar h1 {
  color: #0f172a;
  font-size: 2.2rem;
  line-height: 1.3;
  font-weight: 800;
  margin: 0;
  letter-spacing: -0.02em;
}
.report-titlebar .meta {
  margin-top: 1rem;
  color: var(--text-light);
  font-size: 0.95rem;
  font-weight: 500;
  display: flex;
  align-items: center;
  gap: 8px;
}
.report-titlebar .meta::before {
  content: '✍️';
  font-size: 1.1em;
}
.report-toc {
  max-width: 820px;
  margin: 1rem auto;
  padding: 2rem 3rem;
  background: var(--bg);
  border-radius: var(--radius);
  box-shadow: var(--shadow-md);
  border-left: 4px solid var(--accent);
}
.report-toc h2 {
  color: #0f172a;
  font-size: 1.2rem;
  font-weight: 800;
  margin-bottom: 1rem;
  display: flex;
  align-items: center;
  gap: 8px;
}
.report-toc h2::before {
  content: '📑';
}
.report-toc ol {
  list-style: none;
  counter-reset: toc-counter;
  margin: 0;
}
.report-toc li {
  position: relative;
  color: var(--text-light);
  padding: 0.5rem 0 0.5rem 2rem;
  border-bottom: 1px dashed var(--border);
  transition: all 0.2s ease;
}
.report-toc li::before {
  counter-increment: toc-counter;
  content: counter(toc-counter) ".";
  position: absolute;
  left: 0;
  color: var(--primary);
  font-weight: 700;
  font-size: 0.9rem;
}
.report-toc li:last-child {
  border-bottom: none;
}
.report-toc a {
  color: var(--text);
  text-decoration: none;
  font-weight: 500;
  transition: color 0.2s;
}
.report-toc a:hover {
  color: var(--primary);
}

/* === Sections === */
.section {
  max-width: 820px;
  margin: 1.5rem auto;
  padding: 3rem;
  background: var(--bg);
  border-radius: var(--radius);
  box-shadow: var(--shadow-sm);
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  border: 1px solid var(--border-light);
}
.section:hover { 
  box-shadow: var(--shadow-hover); 
  transform: translateY(-2px);
}
.section h2 {
  color: #0f172a;
  font-size: 1.6rem;
  font-weight: 800;
  margin-bottom: 1.5rem;
  padding-bottom: 0.75rem;
  border-bottom: 2px solid var(--border-light);
  position: relative;
  letter-spacing: -0.01em;
}
.section h2::after {
  content: '';
  position: absolute;
  bottom: -2px;
  left: 0;
  width: 80px;
  height: 3px;
  background: var(--primary);
  border-radius: 2px;
}
.content p {
  margin-bottom: 1.25rem;
  color: var(--text);
  font-size: 1.05rem;
}
.content h4 {
  color: #0f172a;
  font-size: 1.15rem;
  font-weight: 700;
  margin: 2rem 0 1rem;
  padding-left: 1rem;
  border-left: 4px solid var(--primary);
  border-radius: 2px;
}
.content h3 {
  color: #0f172a;
  font-size: 1.3rem;
  font-weight: 800;
  margin: 2rem 0 1rem;
}
.content h5 {
  color: var(--primary);
  font-size: 1.05rem;
  font-weight: 600;
  margin: 1.5rem 0 0.75rem;
}
.content ul, .content ol {
  margin: 1rem 0 1.5rem;
  padding-left: 1.5rem;
}
.content li {
  margin-bottom: 0.5rem;
  font-size: 1.05rem;
  color: var(--text);
  padding-left: 0.25rem;
}
.content li::marker {
  color: var(--primary);
  font-weight: 600;
}
.content blockquote {
  border-left: 4px solid var(--primary-light);
  background: var(--primary-soft);
  padding: 1rem 1.5rem;
  margin: 1.5rem 0;
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  color: #1e293b;
  font-style: italic;
}
.content code {
  background: var(--border-light);
  padding: 0.2rem 0.4rem;
  border-radius: 4px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.9em;
  color: #ef4444;
}

/* === Charts === */
.chart-box {
  width: 100%%;
  height: 450px;
  margin: 2rem 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg);
  box-shadow: var(--shadow-sm);
  transition: all 0.3s ease;
  overflow: hidden;
}
.chart-box:hover { 
  box-shadow: var(--shadow-lg); 
  border-color: var(--primary-light);
}

/* === Tables === */
table {
  width: 100%%;
  border-collapse: separate;
  border-spacing: 0;
  margin: 1.5rem 0;
  font-size: 0.95rem;
  border-radius: var(--radius-sm);
  overflow: hidden;
  box-shadow: var(--shadow-sm);
  border: 1px solid var(--border);
}
th {
  background: var(--bg-alt);
  color: #0f172a;
  padding: 1rem 1.25rem;
  text-align: left;
  font-weight: 700;
  border-bottom: 2px solid var(--border);
}
td {
  padding: 1rem 1.25rem;
  border-bottom: 1px solid var(--border-light);
  color: var(--text);
  background: var(--bg);
}
tr:last-child td {
  border-bottom: none;
}
tr:hover td { 
  background: var(--primary-soft); 
}
strong { 
  color: #0f172a; 
  font-weight: 700; 
}

/* === Print === */
@media print {
  @page { size: A4; margin: 15mm 14mm; }
  body { background: white; padding: 0; }
  .report-titlebar,
  .report-toc,
  .section {
    max-width: none;
    box-shadow: none !important;
    border: none !important;
    border-radius: 0;
    background: white;
    padding: 0 !important;
    transform: none !important;
  }
  .report-titlebar {
    margin: 0 0 20pt;
    border-top: none;
    border-bottom: 2px solid var(--primary);
  }
  .report-toc {
    margin: 0 0 20pt;
    border-left: none;
  }
  .section {
    margin: 0 0 20pt;
  }
}
/* === Responsive === */
@media (max-width: 860px) {
  .report-titlebar, .report-toc, .section { 
    padding: 1.5rem; 
    border-radius: 12px;
  }
  body { padding: 0.5rem; }
}
%s
</style>
%s
</head>
<body class="%s">
%s
%s
%s
%s
</body>
</html>`, safeTitle, katexCSS, katexScripts, customCSSBlock, echartsScriptNode, bodyClass, titleHeaderHTML, tocHTML, bodyHTML.String(), chartScripts)
}

func renderReportTitleHeader(title, author string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	meta := ""
	if strings.TrimSpace(author) != "" {
		meta = fmt.Sprintf(`<div class="meta">%s</div>`, escapeHTMLText(strings.TrimSpace(author)))
	}
	return fmt.Sprintf(`<header class="report-titlebar" data-report-title="true">
  <h1>%s</h1>
  %s
</header>`, escapeHTMLText(title), meta)
}

type reportTOCItem struct {
	Anchor string
	Title  string
}

func renderReportTOC(blocks []ReportBlock, reportTitle string) string {
	items := buildReportTOCItems(blocks, reportTitle)
	if len(items) < 1 {
		return ""
	}

	var html strings.Builder
	html.WriteString(`<nav class="report-toc" aria-label="报告目录">` + "\n")
	html.WriteString("<h2>目录</h2>\n<ol>\n")
	for _, item := range items {
		html.WriteString(fmt.Sprintf(`<li><a href="#%s">%s</a></li>`+"\n", escapeHTMLAttr(item.Anchor), escapeHTMLText(item.Title)))
	}
	html.WriteString("</ol>\n</nav>")
	return html.String()
}

func buildReportTOCItems(blocks []ReportBlock, reportTitle string) []reportTOCItem {
	baseUnits := buildBaseRenderUnits(blocks, reportTitle)
	items := make([]reportTOCItem, 0, len(baseUnits))
	sectionNum := 0
	for _, unit := range baseUnits {
		block := unit.Block
		splitUnits := splitRenderUnitSections(unit)
		if len(splitUnits) == 0 {
			continue
		}
		if title := structuredBlockTOCTitle(block, reportTitle); title != "" {
			firstAnchor := ""
			for range splitUnits {
				sectionNum++
				if firstAnchor == "" {
					firstAnchor = fmt.Sprintf("section-%d", sectionNum)
				}
			}
			items = append(items, reportTOCItem{
				Anchor: firstAnchor,
				Title:  title,
			})
			continue
		}
		for _, splitUnit := range splitUnits {
			sectionNum++
			title := documentTOCTitle(splitUnit.Block, reportTitle)
			if title == "" {
				continue
			}
			items = append(items, reportTOCItem{
				Anchor: fmt.Sprintf("section-%d", sectionNum),
				Title:  title,
			})
		}
	}
	return items
}

func structuredBlockTOCTitle(block ReportBlock, _ string) string {
	return strings.TrimSpace(block.Title)
}

func documentTOCTitle(block ReportBlock, _ string) string {
	if block.Kind == "markdown" {
		if heading, _, ok := firstMarkdownHeading(block.Content); ok {
			return heading
		}
	}
	if block.Kind == "html" {
		if title := extractContentHeadingTitle(block.Content); title != "" {
			return title
		}
	}
	return strings.TrimSpace(block.Title)
}

func firstMarkdownHeading(content string) (string, int, bool) {
	for _, line := range strings.Split(content, "\n") {
		level, ok := markdownHeadingLevel(line)
		if !ok {
			continue
		}
		title := strings.TrimSpace(strings.TrimSpace(line)[level:])
		return strings.TrimSpace(title), level, true
	}
	return "", 0, false
}

func buildChartScripts(charts []ChartData) string {
	if len(charts) == 0 {
		return ""
	}
	return `<script id="oda-chart-runtime" src="/oda-chart-runtime.js"></script>`
}

func blockDisplayTitle(block ReportBlock) string {
	if title := strings.TrimSpace(block.Title); title != "" {
		return title
	}
	return extractContentHeadingTitle(block.Content)
}

func extractContentHeadingTitle(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	htmlLoc := htmlHeadingRegexp.FindStringIndex(content)
	mdLoc := mdHeadingRegexp.FindStringIndex(content)

	var firstMatch []string
	if htmlLoc != nil && (mdLoc == nil || htmlLoc[0] < mdLoc[0]) {
		firstMatch = htmlHeadingRegexp.FindStringSubmatch(content)
	} else if mdLoc != nil {
		firstMatch = mdHeadingRegexp.FindStringSubmatch(content)
	}

	if len(firstMatch) > 1 {
		return strings.TrimSpace(htmlTagsRegexp.ReplaceAllString(firstMatch[1], ""))
	}
	return ""
}

type reportBlockRenderer func(ReportBlock, int, []ChartData) string

var reportBlockRenderers = map[string]reportBlockRenderer{
	"markdown": renderMarkdownBlockHTMLStandalone,
	"html":     renderHTMLBlockStandalone,
	"chart":    renderChartBlockHTML,
}

type reportRenderUnit struct {
	Block ReportBlock
}

func renderReportBlockHTML(block ReportBlock, chapterNum int, charts []ChartData) string {
	kind := block.Kind
	switch kind {
	case "markdown":
		return renderMarkdownBlockHTML(block, chapterNum, charts)
	case "html":
		return renderHTMLBlock(block, chapterNum, charts)
	default:
		renderer, ok := reportBlockRenderers[kind]
		if !ok {
			return ""
		}
		return renderer(block, chapterNum, charts)
	}
}

func renderMarkdownBlockHTMLStandalone(block ReportBlock, chapterNum int, charts []ChartData) string {
	return renderMarkdownBlockHTML(block, chapterNum, charts)
}

func renderMarkdownBlockHTML(block ReportBlock, chapterNum int, charts []ChartData) string {
	displayTitle := blockDisplayTitle(block)

	headingHTML := ""
	if displayTitle != "" && extractContentHeadingTitle(block.Content) == "" {
		headingHTML = fmt.Sprintf("<h2>%s</h2>\n", escapeHTMLText(displayTitle))
	}

	contentHTML := headingHTML + processContent(block.Content, charts)

	return fmt.Sprintf(`
			<div class="section" id="section-%d">
				<div class="content">%s</div>
			</div>`, chapterNum, contentHTML)
}

func renderHTMLBlock(block ReportBlock, chapterNum int, charts []ChartData) string {
	displayTitle := blockDisplayTitle(block)

	headingHTML := ""
	if displayTitle != "" && extractContentHeadingTitle(block.Content) == "" {
		headingHTML = fmt.Sprintf("<h2>%s</h2>\n", escapeHTMLText(displayTitle))
	}

	contentHTML := headingHTML + sanitizeHTMLFragment(block.Content)
	return fmt.Sprintf(`
		<div class="section html-block" id="section-%d">
			<div class="content">%s</div>
		</div>`, chapterNum, contentHTML)
}

func renderHTMLBlockStandalone(block ReportBlock, chapterNum int, charts []ChartData) string {
	return renderHTMLBlock(block, chapterNum, charts)
}

func renderChartBlockHTML(block ReportBlock, chapterNum int, charts []ChartData) string {
	title := blockDisplayTitle(block)
	content := fmt.Sprintf("{{chart:%s}}", block.ChartID)
	if strings.TrimSpace(block.Content) != "" {
		content += "\n\n" + block.Content
	}
	var headingHTML string
	if title != "" {
		headingHTML = fmt.Sprintf("<h2>%s</h2>", escapeHTMLText(title))
	}
	return fmt.Sprintf(`
		<div class="section chart-block" id="section-%d">
			%s
			<div class="content">%s</div>
		</div>`, chapterNum, headingHTML, processContent(content, charts))
}

func buildRenderUnits(blocks []ReportBlock, _ string) []reportRenderUnit {
	baseUnits := buildBaseRenderUnits(blocks, "")
	units := make([]reportRenderUnit, 0, len(baseUnits))
	for _, unit := range baseUnits {
		units = append(units, splitRenderUnitSections(unit)...)
	}
	return units
}

func buildBaseRenderUnits(blocks []ReportBlock, _ string) []reportRenderUnit {
	if len(blocks) == 0 {
		return nil
	}
	baseUnits := make([]reportRenderUnit, 0, len(blocks))
	for _, block := range blocks {
		baseUnits = append(baseUnits, reportRenderUnit{Block: block})
	}
	return baseUnits
}

func splitRenderUnitSections(unit reportRenderUnit) []reportRenderUnit {
	if unit.Block.Kind != "markdown" {
		return []reportRenderUnit{unit}
	}

	fragments := splitMarkdownIntoTopLevelSections(unit.Block.Content)
	if len(fragments) <= 1 {
		return []reportRenderUnit{unit}
	}

	units := make([]reportRenderUnit, 0, len(fragments))
	for _, fragment := range fragments {
		block := unit.Block
		block.Content = fragment
		units = append(units, reportRenderUnit{Block: block})
	}
	return units
}

func splitMarkdownIntoTopLevelSections(content string) []string {
	lines := strings.Split(content, "\n")
	minLevel := 0
	headingCount := 0
	for _, line := range lines {
		level, ok := markdownHeadingLevel(line)
		if !ok || level > 2 {
			continue
		}
		if minLevel == 0 || level < minLevel {
			minLevel = level
			headingCount = 1
			continue
		}
		if level == minLevel {
			headingCount++
		}
	}
	if minLevel == 0 || headingCount <= 1 {
		return []string{content}
	}

	parts := make([]string, 0, headingCount)
	current := make([]string, 0, len(lines))
	for _, line := range lines {
		level, ok := markdownHeadingLevel(line)
		if ok && level == minLevel && len(current) > 0 {
			parts = append(parts, strings.TrimSpace(strings.Join(current, "\n")))
			current = current[:0]
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		parts = append(parts, strings.TrimSpace(strings.Join(current, "\n")))
	}
	if len(parts) <= 1 {
		return []string{content}
	}
	return parts
}

func markdownHeadingLevel(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '#' {
		return 0, false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level >= len(trimmed) || trimmed[level] != ' ' {
		return 0, false
	}
	return level, true
}

// processContent 处理内容：Markdown 转 HTML + 替换图表占位符
func processContent(content string, charts []ChartData) string {
	html := sanitizeHTMLFragment(markdownToHTML(content))

	chartRefCounts := make(map[string]int)
	html = chartReferenceRegexp.ReplaceAllStringFunc(html, func(match string) string {
		chartID := chartReferenceRegexp.FindStringSubmatch(match)[1]
		if chartID != strings.TrimSpace(chartID) {
			return match
		}
		// 查找对应图表
		for _, ch := range charts {
			if ch.ID == chartID {
				chartRefCounts[chartID]++
				containerID := fmt.Sprintf("%s-ref-%d", chartID, chartRefCounts[chartID])
				optionAttr := escapeHTMLAttr(safeJSONForInlineScript(ch.Option))
				style := ""
				if ch.Width != "" || ch.Height != "" {
					var declarations []string
					if ch.Width != "" {
						declarations = append(declarations, "width:"+ch.Width)
					}
					if ch.Height != "" {
						declarations = append(declarations, "height:"+ch.Height)
					}
					style = ` style="` + escapeHTMLAttr(strings.Join(declarations, ";")+";") + `"`
				}
				return fmt.Sprintf(`<div id="%s" data-chart-id="%s" data-chart-option="%s" class="chart-box"%s></div>`, escapeHTMLAttr(containerID), escapeHTMLAttr(ch.ID), optionAttr, style)
			}
		}
		return ""
	})

	return html
}

// markdownToHTML 使用 goldmark 进行标准的 Markdown 到 HTML 的转换
func markdownToHTML(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}

	// Pre-process: Extract math blocks to protect them from markdown formatting (like _, *)
	replacements := make(map[string]string)
	var out strings.Builder
	out.Grow(len(md))

	i := 0
	idCounter := 0
	for i < len(md) {
		if strings.HasPrefix(md[i:], "$$") {
			start := i
			i += 2
			found := false
			for i < len(md) {
				if strings.HasPrefix(md[i:], "$$") {
					i += 2
					found = true
					break
				}
				i++
			}
			if found {
				idCounter++
				id := fmt.Sprintf("MATH_PLACEHOLDER_%d_END", idCounter)
				replacements[id] = md[start:i]
				out.WriteString(id)
				continue
			}
			i = start + 1
			out.WriteByte(md[start])
			continue
		}

		if strings.HasPrefix(md[i:], `\(`) {
			start := i
			i += 2
			found := false
			for i < len(md) {
				if strings.HasPrefix(md[i:], `\)`) {
					i += 2
					found = true
					break
				}
				i++
			}
			if found {
				idCounter++
				id := fmt.Sprintf("MATH_PLACEHOLDER_%d_END", idCounter)
				replacements[id] = md[start:i]
				out.WriteString(id)
				continue
			}
			i = start + 1
			out.WriteByte(md[start])
			continue
		}

		if strings.HasPrefix(md[i:], `\[`) {
			start := i
			i += 2
			found := false
			for i < len(md) {
				if strings.HasPrefix(md[i:], `\]`) {
					i += 2
					found = true
					break
				}
				i++
			}
			if found {
				idCounter++
				id := fmt.Sprintf("MATH_PLACEHOLDER_%d_END", idCounter)
				replacements[id] = md[start:i]
				out.WriteString(id)
				continue
			}
			i = start + 1
			out.WriteByte(md[start])
			continue
		}

		out.WriteByte(md[i])
		i++
	}

	newMd := out.String()

	gm := goldmark.New(
		goldmark.WithExtensions(extension.GFM), // Removed mathjax, rely on manual extraction
		goldmark.WithRendererOptions(
			gmhtml.WithUnsafe(), // 输出 HTML 由 sanitizeHTMLFragment 白名单净化后再插入报告
		),
	)

	var buf bytes.Buffer
	if err := gm.Convert([]byte(newMd), &buf); err != nil {
		return fixCJKBoldHTML(fmt.Sprintf("<p>%s</p>", escapeHTMLText(md)))
	}

	htmlStr := buf.String()
	for k, v := range replacements {
		htmlStr = strings.ReplaceAll(htmlStr, k, htmlstd.EscapeString(v))
	}

	return fixCJKBoldHTML(htmlStr)
}

func fixCJKBoldHTML(htmlStr string) string {
	doc, err := htmlnode.ParseFragment(strings.NewReader(htmlStr), &htmlnode.Node{
		Type:     htmlnode.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil {
		return htmlStr
	}

	re := regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)

	var walk func(*htmlnode.Node)
	walk = func(n *htmlnode.Node) {
		if n.Type == htmlnode.ElementNode && (n.Data == "code" || n.Data == "pre") {
			return
		}
		if n.Type == htmlnode.TextNode && strings.Contains(n.Data, "**") {
			n.Data = re.ReplaceAllString(n.Data, "%%ODA_STRONG_START%%$1%%ODA_STRONG_END%%")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	for _, node := range doc {
		walk(node)
	}

	var out bytes.Buffer
	for _, node := range doc {
		htmlnode.Render(&out, node)
	}

	res := out.String()
	res = strings.ReplaceAll(res, "%%ODA_STRONG_START%%", "<strong>")
	res = strings.ReplaceAll(res, "%%ODA_STRONG_END%%", "</strong>")
	return res
}

func escapeHTMLText(value string) string {
	return htmlstd.EscapeString(value)
}

func escapeHTMLAttr(value string) string {
	return htmlstd.EscapeString(value)
}

func sanitizeBodyClass(value string) string {
	fields := strings.Fields(value)
	safe := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		valid := true
		for _, r := range field {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				continue
			}
			valid = false
			break
		}
		if valid {
			safe = append(safe, field)
		}
	}
	return strings.Join(safe, " ")
}

func sanitizeCSS(value string) string {
	css := strings.TrimSpace(value)
	if css == "" {
		return ""
	}
	replacements := []string{
		"</style", "",
		"</Style", "",
		"</STYLE", "",
		"@import", "",
		"@Import", "",
		"@IMPORT", "",
		"expression(", "",
		"Expression(", "",
		"EXPRESSION(", "",
		"javascript:", "",
		"Javascript:", "",
		"JAVASCRIPT:", "",
		"behavior:", "",
		"Behavior:", "",
		"BEHAVIOR:", "",
		"vbscript:", "",
		"Vbscript:", "",
		"VBSCRIPT:", "",
	}
	replacer := strings.NewReplacer(replacements...)
	css = replacer.Replace(css)
	css = cssStripRe.ReplaceAllString(css, "")
	return css
}

var cssStripRe = regexp.MustCompile(`(?i)(@[\s]*import|expression[\s]*\(|behavior[\s]*:)`)

func safeJSONForInlineScript(raw json.RawMessage) string {
	option := strings.TrimSpace(string(raw))
	if option == "" || option == "null" {
		return "{}"
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "{}"
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return "{}"
	}
	return escapeInlineScript(string(normalized))
}

func escapeInlineScript(value string) string {
	replacer := strings.NewReplacer(
		"</script", "<\\/script",
		"<!--", "<\\!--",
		"-->", "--\\>",
		"\u2028", "\\u2028",
		"\u2029", "\\u2029",
	)
	return replacer.Replace(value)
}

const (
	ReportKaTeXVersion    = "0.16.9"
	ReportKaTeXCDNBaseURL = "https://cdn.jsdelivr.net/npm/katex@" + ReportKaTeXVersion + "/dist/"
)

var allowedHTMLBlockTags = map[string]struct{}{
	"a": {}, "b": {}, "blockquote": {}, "br": {}, "code": {}, "div": {}, "em": {}, "h1": {}, "h2": {}, "h3": {}, "h4": {}, "h5": {}, "h6": {},
	"hr": {}, "i": {}, "li": {}, "ol": {}, "p": {}, "pre": {}, "span": {}, "strong": {}, "table": {}, "tbody": {}, "td": {}, "th": {}, "thead": {}, "tr": {}, "ul": {},
}

func sanitizeHTMLFragment(fragment string) string {
	doc, err := htmlnode.Parse(strings.NewReader("<!DOCTYPE html><html><body><div id=\"__oda_root__\">" + fragment + "</div></body></html>"))
	if err != nil {
		return fmt.Sprintf("<p>%s</p>", escapeHTMLText(fragment))
	}
	root := findHTMLNodeByID(doc, "__oda_root__")
	if root == nil {
		return fmt.Sprintf("<p>%s</p>", escapeHTMLText(fragment))
	}
	container := &htmlnode.Node{Type: htmlnode.ElementNode, Data: "div"}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		sanitizeHTMLNode(container, child)
	}
	var out bytes.Buffer
	for child := container.FirstChild; child != nil; child = child.NextSibling {
		if err := htmlnode.Render(&out, child); err != nil {
			return fmt.Sprintf("<p>%s</p>", escapeHTMLText(fragment))
		}
	}
	return out.String()
}

func sanitizeHTMLNode(parent, node *htmlnode.Node) {
	switch node.Type {
	case htmlnode.TextNode:
		parent.AppendChild(&htmlnode.Node{Type: htmlnode.TextNode, Data: node.Data})
	case htmlnode.ElementNode:
		tag := strings.ToLower(strings.TrimSpace(node.Data))
		switch tag {
		case "script", "style", "iframe", "object", "embed", "link", "meta", "base":
			return
		}
		if _, ok := allowedHTMLBlockTags[tag]; !ok {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				sanitizeHTMLNode(parent, child)
			}
			return
		}
		safeNode := &htmlnode.Node{Type: htmlnode.ElementNode, Data: tag}
		safeNode.Attr = sanitizeHTMLAttrs(tag, node.Attr)
		parent.AppendChild(safeNode)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			sanitizeHTMLNode(safeNode, child)
		}
	default:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			sanitizeHTMLNode(parent, child)
		}
	}
}

func sanitizeHTMLAttrs(tag string, attrs []htmlnode.Attribute) []htmlnode.Attribute {
	safe := make([]htmlnode.Attribute, 0, len(attrs))
	for _, attr := range attrs {
		key := strings.ToLower(strings.TrimSpace(attr.Key))
		value := strings.TrimSpace(attr.Val)
		if key == "" || strings.HasPrefix(key, "on") {
			continue
		}
		switch key {
		case "class":
			className := sanitizeBodyClass(strings.ReplaceAll(value, ":", " "))
			className = strings.ReplaceAll(className, " ", "-")
			if className == "" {
				continue
			}
			safe = append(safe, htmlnode.Attribute{Key: key, Val: className})
		case "href":
			href, ok := sanitizeURL(value)
			if !ok || tag != "a" {
				continue
			}
			safe = append(safe, htmlnode.Attribute{Key: key, Val: href})
			safe = append(safe, htmlnode.Attribute{Key: "rel", Val: "noopener noreferrer"})
			safe = append(safe, htmlnode.Attribute{Key: "target", Val: "_blank"})
		case "title":
			safe = append(safe, htmlnode.Attribute{Key: key, Val: value})
		case "colspan", "rowspan":
			if _, err := strconv.Atoi(value); err == nil {
				safe = append(safe, htmlnode.Attribute{Key: key, Val: value})
			}
		}
	}
	return safe
}

func sanitizeURL(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "#") || strings.HasPrefix(value, "/") {
		return value, true
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto":
		return value, true
	default:
		return "", false
	}
}

func findHTMLNodeByID(node *htmlnode.Node, id string) *htmlnode.Node {
	if node == nil {
		return nil
	}
	if node.Type == htmlnode.ElementNode {
		for _, attr := range node.Attr {
			if attr.Key == "id" && attr.Val == id {
				return node
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findHTMLNodeByID(child, id); found != nil {
			return found
		}
	}
	return nil
}
