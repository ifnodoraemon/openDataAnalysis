package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderReportHTMLConvertsMarkdownHeadings(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis-1",
				Kind:    "markdown",
				Title:   "销售分析",
				Content: "## 销售总体表现\n\n- **收入** 增长 20%",
			},
		},
	})

	if !strings.Contains(html, "<h2>销售总体表现</h2>") {
		t.Fatalf("expected h2 markdown to render as heading, got: %s", html)
	}
	if !strings.Contains(html, "<li><strong>收入</strong> 增长 20%</li>") {
		t.Fatalf("expected list markdown to render as html, got: %s", html)
	}
	if strings.Contains(html, `<div class="cover">`) || strings.Contains(html, `<div class="toc">`) || strings.Contains(html, `<div class="footer">`) {
		t.Fatalf("expected no runtime-injected cover/toc/footer, got: %s", html)
	}
}

func TestRenderReportHTMLKeepsExplicitBlockTitleAndAuthoredHeading(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis-1",
				Kind:    "markdown",
				Title:   "销售分布与区域表现",
				Content: "## 2. 销售分布与区域表现\n\n正文",
			},
		},
	})

	if strings.Contains(html, "<h2>销售分布与区域表现</h2>") {
		t.Fatalf("expected markdown block not to render generated h2 heading, got: %s", html)
	}
	if !strings.Contains(html, `<div class="report-block-wrapper" data-block-id="analysis-1" data-block-kind="markdown" data-block-title="销售分布与区域表现">`) {
		t.Fatalf("expected block title metadata to remain on wrapper, got: %s", html)
	}
	if strings.Contains(html, `class="section" id="section-1" data-block-id`) {
		t.Fatalf("expected section not to duplicate data-block attributes, got: %s", html)
	}
}

func TestRenderReportHTMLShowsFinalTitleInBody(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("2025年上半年业务分析报告", "AI 数据分析师", &ReportState{
		Blocks: []ReportBlock{
			{ID: "summary", Kind: "markdown", Content: "## 执行摘要\n\n正文"},
		},
	})

	if !strings.Contains(html, `class="report-titlebar"`) {
		t.Fatalf("expected visible report title header, got: %s", html)
	}
	if !strings.Contains(html, "<h1>2025年上半年业务分析报告</h1>") {
		t.Fatalf("expected title text in report body, got: %s", html)
	}
	if !strings.Contains(html, `<div class="meta">AI 数据分析师</div>`) {
		t.Fatalf("expected author meta in report body, got: %s", html)
	}
	if strings.Contains(html, "Data Analysis Report") {
		t.Fatalf("did not expect renderer-generated title label, got: %s", html)
	}
}

func TestRenderReportHTMLBuildsDerivedTOC(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "summary", Kind: "markdown", Content: "## 执行摘要\n\n正文"},
			{ID: "sales", Kind: "markdown", Content: "## 一、销售分析\n\n正文"},
			{ID: "actions", Kind: "markdown", Content: "## 二、行动建议\n\n正文"},
		},
	})

	if !strings.Contains(html, `class="report-toc"`) {
		t.Fatalf("expected derived report toc, got: %s", html)
	}
	for _, expected := range []string{
		`<a href="#section-1">执行摘要</a>`,
		`<a href="#section-2">一、销售分析</a>`,
		`<a href="#section-3">二、行动建议</a>`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("expected toc item %s, got: %s", expected, html)
		}
	}
}

func TestRenderReportHTMLTOCUsesDocumentLevelSections(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("三表全维度深度对比分析报告（2025H1）", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "summary",
				Kind:    "markdown",
				Title:   "执行摘要",
				Content: "## 📊 三表全维度深度对比分析报告\n\n### 核心发现\n\n正文",
			},
			{ID: "channel", Kind: "markdown", Title: "一、营销渠道深度对比", Content: "## 一、营销渠道深度对比分析\n\n正文"},
			{ID: "channel-trend", Kind: "markdown", Title: "1.3 月度趋势与渠道波动", Content: "### 1.3 月度趋势与渠道波动\n\n正文"},
			{ID: "region", Kind: "markdown", Title: "二、区域销售深度对比", Content: "## 二、区域销售深度对比分析\n\n正文"},
		},
	})

	for _, expected := range []string{
		`<a href="#section-1">执行摘要</a>`,
		`<a href="#section-2">一、营销渠道深度对比</a>`,
		`<a href="#section-4">二、区域销售深度对比</a>`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("expected toc item %s, got: %s", expected, html)
		}
	}
	if !strings.Contains(html, `<a href="#section-3">1.3 月度趋势与渠道波动</a>`) {
		t.Fatalf("expected explicit block title to remain in toc without heading-level inference, got: %s", html)
	}
}

func TestRenderReportHTMLTOCPrefersStructuredBlockTitles(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("全面深度对比分析报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "summary",
				Kind:    "markdown",
				Title:   "执行摘要",
				Content: "正文\n\n## 核心发现\n\n发现正文",
			},
			{
				ID:      "channel",
				Kind:    "markdown",
				Title:   "一、营销渠道深度对比",
				Content: "## 1.1 渠道ROI与营收总览\n\n正文\n\n## 1.2 营销漏斗效率对比\n\n正文",
			},
			{
				ID:      "region",
				Kind:    "markdown",
				Title:   "二、区域销售深度对比",
				Content: "## 2.1 区域营收与利润对比\n\n正文",
			},
		},
	})

	for _, expected := range []string{
		`<a href="#section-1">执行摘要</a>`,
		`<a href="#section-2">一、营销渠道深度对比</a>`,
		`<a href="#section-4">二、区域销售深度对比</a>`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("expected toc item %s, got: %s", expected, html)
		}
	}
	for _, unexpected := range []string{
		`核心发现</a>`,
		`1.1 渠道ROI与营收总览</a>`,
		`1.2 营销漏斗效率对比</a>`,
	} {
		if strings.Contains(html, unexpected) {
			t.Fatalf("expected structured block title to suppress subsection %s, got: %s", unexpected, html)
		}
	}
}

func TestRenderReportHTMLPreservesAuthoredLeadingReportTitle(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("全面数据深度分析报告 - 2025年上半年业务洞察", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:    "full-report",
				Kind:  "markdown",
				Title: "全面数据深度分析报告",
				Content: "# 全面数据深度分析报告\n\n" +
					"## 执行摘要\n\n摘要正文\n\n" +
					"## 一、销售趋势分析\n\n趋势正文",
			},
			{ID: "region", Kind: "markdown", Content: "## 二、区域业绩对比分析\n\n区域正文"},
		},
	})

	if !strings.Contains(html, `<a href="#section-1">全面数据深度分析报告</a>`) {
		t.Fatalf("expected authored block title to remain in toc, got: %s", html)
	}
	for _, expected := range []string{
		`<a href="#section-2">二、区域业绩对比分析</a>`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("expected toc item %s, got: %s", expected, html)
		}
	}
	if !strings.Contains(html, "<h1>全面数据深度分析报告</h1>") {
		t.Fatalf("expected model-authored report title to remain in body, got: %s", html)
	}
	if strings.Count(html, "<h1>全面数据深度分析报告 - 2025年上半年业务洞察</h1>") != 1 {
		t.Fatalf("expected one renderer-owned report title, got: %s", html)
	}
}

func TestRenderReportHTMLKeepsShortLeadingSectionTitle(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("Sales Report", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "sales", Kind: "markdown", Content: "# Sales\n\n正文"},
			{ID: "region", Kind: "markdown", Content: "## Region\n\n正文"},
		},
	})

	if !strings.Contains(html, `<a href="#section-1">Sales</a>`) {
		t.Fatalf("expected short section heading not to be treated as duplicate report title, got: %s", html)
	}
	if !strings.Contains(html, "<h1>Sales</h1>") && !strings.Contains(html, "<h2>Sales</h2>") {
		t.Fatalf("expected short leading section title to remain in body, got: %s", html)
	}
}

func TestRenderReportHTMLPrintCSSAllowsLongSectionsToFlow(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "long", Kind: "markdown", Content: "## 长章节\n\n正文"},
		},
	})

	if !strings.Contains(html, "@media print") {
		t.Fatalf("expected print css @media block to be present in HTML style, got: %s", html)
	}
}

func TestRenderReportHTMLKeepsUntitledChartBlockAtExplicitPosition(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "blk_overview", Kind: "markdown", Content: "## 一、数据概览\n\n总览"},
			{ID: "blk_sales_chart", Kind: "chart", ChartID: "chart_sales_trend", Content: "趋势说明"},
			{ID: "blk_sales_analysis", Kind: "markdown", Content: "## 二、销售分析\n\n销售正文"},
		},
		Charts: []ChartData{
			{ID: "chart_sales_trend", Option: json.RawMessage(`{"series":[{"type":"bar","data":[1]}]}`)},
		},
	})

	if !strings.Contains(html, `data-block-id="blk_sales_chart"`) {
		t.Fatalf("expected chart block to remain explicit, got: %s", html)
	}
	overviewIdx := strings.Index(html, `data-block-id="blk_overview"`)
	blockChartIdx := strings.Index(html, `data-block-id="blk_sales_chart"`)
	analysisIdx := strings.Index(html, `data-block-id="blk_sales_analysis"`)
	chartIdx := strings.Index(html, `data-chart-id="chart_sales_trend"`)
	if overviewIdx < 0 || blockChartIdx <= overviewIdx || analysisIdx <= blockChartIdx || chartIdx < blockChartIdx || chartIdx > analysisIdx {
		t.Fatalf("expected chart to render in its authored block order, got: %s", html)
	}
	if strings.Count(html, `data-chart-id="chart_sales_trend"`) != 1 {
		t.Fatalf("expected chart to render exactly once, got: %s", html)
	}
}

func TestRenderReportHTMLSplitsMarkdownBlockByTopLevelHeadings(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "blk_recommendations",
				Kind:    "markdown",
				Content: "## 六、建议\n\n建议正文\n\n## 七、结论\n\n结论正文",
			},
		},
	})

	if strings.Count(html, `data-block-id="blk_recommendations"`) != 1 {
		t.Fatalf("expected one wrapper block capturing the split sections, got: %s", html)
	}
	if !strings.Contains(html, `id="section-2"`) || !strings.Contains(html, "<h2>七、结论</h2>") {
		t.Fatalf("expected second split section to render as a plain section, got: %s", html)
	}
}

func TestRenderReportHTMLDoesNotAppendUnreferencedCharts(t *testing.T) {
	t.Parallel()

	option, err := json.Marshal(map[string]any{
		"xAxis": map[string]any{"type": "category", "data": []string{"1月"}},
		"yAxis": map[string]any{"type": "value"},
		"series": []map[string]any{
			{"type": "bar", "data": []int{100}},
		},
	})
	if err != nil {
		t.Fatalf("marshal option: %v", err)
	}

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis-1",
				Kind:    "markdown",
				Title:   "销售分析",
				Content: "这里只写了解读，没有引用图表。",
			},
		},
		Charts: []ChartData{
			{ID: "chart_sales", Option: option, Height: "360px"},
		},
	})

	if strings.Contains(html, "图表附录") {
		t.Fatalf("did not expect appendix for unreferenced charts")
	}
}

func TestRenderReportHTMLDoesNotInferKeywordSectionPresets(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "risks-1",
				Kind:    "markdown",
				Title:   "主要风险",
				Content: "需求口径仍可能调整。",
			},
			{
				ID:      "method-1",
				Kind:    "markdown",
				Title:   "分析方法",
				Content: "基于聚合 SQL 与图表交叉验证。",
			},
		},
	})

	if strings.Contains(html, `class="section risks"`) || strings.Contains(html, `class="section methodology"`) || strings.Contains(html, `class="summary-box"`) || strings.Contains(html, `class="conclusion-box"`) {
		t.Fatalf("expected no keyword-based section preset inference, got: %s", html)
	}
	if strings.Count(html, `class="section"`) < 2 {
		t.Fatalf("expected blocks to remain plain sections, got: %s", html)
	}
}

func TestRenderReportHTMLNormalizesPrefixedSectionTitles(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "sec-1", Kind: "markdown", Title: "一、数据概览", Content: "内容 1"},
			{ID: "sec-2", Kind: "markdown", Title: "02 各维度分布", Content: "内容 2"},
			{ID: "sec-3", Kind: "markdown", Title: "第3章 趋势变化", Content: "内容 3"},
		},
	})

	if !strings.Contains(html, `<h2>一、数据概览</h2>`) || !strings.Contains(html, `<h2>02 各维度分布</h2>`) || !strings.Contains(html, `<h2>第3章 趋势变化</h2>`) {
		t.Fatalf("expected markdown blocks to render generated h2 headings when content lacks headings, got: %s", html)
	}
}

func TestRenderReportHTMLSupportsLayoutOverrides(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Layout: ReportLayout{
			CustomCSS: ".hero{color:red;}",
			BodyClass: "magazine",
		},
		Blocks: []ReportBlock{
			{
				ID:      "body-1",
				Kind:    "markdown",
				Title:   "正文",
				Content: "内容",
			},
		},
	})

	if strings.Contains(html, `<div class="cover">`) || strings.Contains(html, `<div class="toc">`) || strings.Contains(html, `<div class="footer">`) {
		t.Fatalf("expected no default cover/toc/footer")
	}
	if !strings.Contains(html, `body class="magazine"`) {
		t.Fatalf("expected body class override")
	}
	if !strings.Contains(html, ".hero{color:red;}") {
		t.Fatalf("expected custom css to be injected")
	}
}

func TestRenderReportHTMLUsesBlocksAsPrimaryModel(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "intro", Kind: "markdown", Title: "导言", Content: "## 导言\n\n这是导言。"},
			{ID: "raw", Kind: "html", Title: "自定义块", Content: `<h3>自定义块</h3><div class="custom">RAW</div>`},
			{ID: "trend", Kind: "chart", Title: "趋势图", ChartID: "chart_sales", Content: "图表解读"},
		},
		Charts: []ChartData{
			{ID: "chart_sales", Option: json.RawMessage(`{"xAxis":{"type":"category","data":["1月"]},"yAxis":{"type":"value"},"series":[{"type":"bar","data":[100]}]}`)},
		},
	})

	if !strings.Contains(html, "导言") || !strings.Contains(html, `class="custom"`) {
		t.Fatalf("expected markdown/html blocks to render")
	}
	if !strings.Contains(html, `data-chart-id="chart_sales"`) || !strings.Contains(html, "图表解读") {
		t.Fatalf("expected chart block to render chart and commentary")
	}
	if strings.Contains(html, `data-block-id="trend" data-block-kind="chart" data-chart-id="chart_sales"`) {
		t.Fatalf("expected chart block wrapper not to carry data-chart-id, got: %s", html)
	}
	if !strings.Contains(html, `data-chart-option="`) || !strings.Contains(html, `id="oda-chart-runtime" src="/oda-chart-runtime.js"`) {
		t.Fatalf("expected chart data attributes and external runtime, got: %s", html)
	}
}

func TestRenderReportHTMLSanitizesUnsafeHTML(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML(`<img src=x onerror=alert(1)>`, "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "raw", Kind: "html", Title: "原始块", Content: `<div class="custom" onclick="alert(1)"><script>alert(1)</script><a href="javascript:alert(1)">bad</a><a href="https://example.com">good</a></div>`},
			{ID: "md", Kind: "markdown", Title: "正文", Content: `<script>alert(1)</script> **ok**`},
		},
	})

	if strings.Contains(html, `onclick="alert(1)"`) || strings.Contains(html, `href="javascript:alert(1)"`) {
		t.Fatalf("expected unsafe html/js attributes to be removed, got: %s", html)
	}
	if !strings.Contains(html, `<a href="https://example.com" rel="noopener noreferrer" target="_blank">good</a>`) {
		t.Fatalf("expected safe link to remain, got: %s", html)
	}
	if !strings.Contains(html, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("expected title to render, got %s", html)
	}
}

func TestRenderReportHTMLSanitizesUnsafeMarkdownBlocks(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:   "md-unsafe",
				Kind: "markdown",
				Content: "## 恶意内容\n\n" +
					"<script>alert(1)</script>\n\n" +
					"<img src=x onerror=alert(1)>\n\n" +
					"<a href=\"javascript:alert(1)\">evil link</a>\n\n" +
					"<iframe src=\"https://evil.example\"></iframe>\n\n" +
					"安全正文",
			},
		},
	})

	for _, forbidden := range []string{
		"<script>alert(1)",
		"onerror=alert(1)",
		"javascript:alert(1)",
		"<iframe",
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("expected markdown block to sanitize %q, got: %s", forbidden, html)
		}
	}
	if !strings.Contains(html, "<h2>恶意内容</h2>") || !strings.Contains(html, "安全正文") {
		t.Fatalf("expected benign markdown content to survive sanitization, got: %s", html)
	}
	if strings.Contains(html, `<a href="javascript:`) {
		t.Fatalf("expected javascript href to be dropped from markdown link, got: %s", html)
	}
	if !strings.Contains(html, "<a>evil link</a>") {
		t.Fatalf("expected inert anchor text to remain without href, got: %s", html)
	}
}

func TestRenderReportHTMLPreservesBenignMarkdownThroughSanitization(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:   "md-benign",
				Kind: "markdown",
				Content: "## 标题\n\n" +
					"[示例链接](https://example.com/docs)\n\n" +
					"`inline code`\n\n" +
					"```go\nfmt.Println(\"ok\")\n```\n\n" +
					"| 列A | 列B |\n|-----|-----|\n| 1   | 2   |\n\n" +
					"**加粗文本**\n\n- 列表项\n",
			},
		},
	})

	for _, expected := range []string{
		"<h2>标题</h2>",
		`<a href="https://example.com/docs" rel="noopener noreferrer" target="_blank">示例链接</a>`,
		"inline code",
		"fmt.Println(&#34;ok&#34;)",
		"<table>",
		"<th>列A</th>",
		"<td>1</td>",
		"<strong>加粗文本</strong>",
		"<li>列表项</li>",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("expected benign markdown to keep %q, got: %s", expected, html)
		}
	}
}

func TestRenderReportHTMLSanitizesChartBlockContent(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "blk_chart", Kind: "chart", ChartID: "chart_sales", Content: "<script>alert(1)</script>图表解读"},
		},
		Charts: []ChartData{
			{ID: "chart_sales", Option: json.RawMessage(`{"series":[{"type":"bar","data":[1]}]}`)},
		},
	})

	if strings.Contains(html, "<script>alert(1)") || strings.Contains(html, "alert(1)") {
		t.Fatalf("expected chart block content to be sanitized, got: %s", html)
	}
	if !strings.Contains(html, `data-chart-id="chart_sales"`) || !strings.Contains(html, "图表解读") {
		t.Fatalf("expected chart container and benign commentary to survive, got: %s", html)
	}
}

func TestRenderReportHTMLExtractsBlockTitle(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试提取", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "b1", Kind: "markdown", Title: "", Content: "一些前缀文字\n## 纯 Markdown 标题 \n正文"},
			{ID: "b2", Kind: "html", Title: "", Content: `<h3 id="custom">嵌套 <span style="color:red">HTML</span> 标题</h3>内容`},
		},
	})
	if !strings.Contains(html, `data-block-id="b1" data-block-kind="markdown" data-block-title="纯 Markdown 标题"`) {
		t.Fatalf("expected markdown wrapper title to be extracted from content heading, got: %s", html)
	}
	if !strings.Contains(html, `data-block-id="b2" data-block-kind="html" data-block-title="嵌套 HTML 标题"`) {
		t.Fatalf("expected html wrapper title to be extracted and tags stripped, got: %s", html)
	}
	if !strings.Contains(html, `<h2>纯 Markdown 标题</h2>`) || !strings.Contains(html, `<h3>嵌套 <span>HTML</span> 标题</h3>`) {
		t.Fatalf("expected original section headings to be preserved after sanitization, got: %s", html)
	}
	if strings.Contains(html, `id="custom"`) || strings.Contains(html, `style="color:red"`) {
		t.Fatalf("expected unsafe html attributes to be stripped from extracted heading, got: %s", html)
	}
}

func TestRenderReportHTMLAllowsRepeatedChartReferences(t *testing.T) {
	t.Parallel()

	html := RenderReportHTML("测试报告", "AI", &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "markdown", Title: "分析", Content: "{{chart:chart_sales}}\n\n{{chart:chart_sales}}"},
		},
		Charts: []ChartData{
			{ID: "chart_sales", Option: json.RawMessage(`{"series":[{"type":"bar","data":[1]}]}`)},
		},
	})

	if strings.Count(html, `data-chart-id="chart_sales"`) != 2 {
		t.Fatalf("expected repeated chart references to render 2 containers, got html: %s", html)
	}
	if !strings.Contains(html, `id="chart_sales-ref-1"`) || !strings.Contains(html, `id="chart_sales-ref-2"`) {
		t.Fatalf("expected unique container ids for repeated chart references, got html: %s", html)
	}
}

func TestRenderReportHTMLUsesBlockTitleWhenContentLacksHeading(t *testing.T) {
	t.Parallel()

	// Markdown block content may intentionally omit a heading; block.Title still
	// needs to appear in both the TOC and body.
	html := RenderReportHTML("回归测试", "AI", &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis-block-1",
				Kind:    "markdown",
				Title:   "分析模块",
				Content: "这是一段没有内置 heading 的正文。",
			},
		},
	})

	if !strings.Contains(html, "<h2>分析模块</h2>") {
		t.Fatalf("expected renderer to use block.Title when content lacks a heading, got: %s", html)
	}
}
