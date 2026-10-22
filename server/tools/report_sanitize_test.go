package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sanitizeCase struct {
	Name           string   `json:"name"`
	Input          string   `json:"input"`
	MustContain    []string `json:"must_contain"`
	MustNotContain []string `json:"must_not_contain"`
}

type sanitizeCorpus struct {
	Cases []sanitizeCase `json:"cases"`
}

func loadSanitizeCorpus(t *testing.T) []sanitizeCase {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "report-sanitize-cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared sanitizer corpus: %v", err)
	}
	var corpus sanitizeCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode shared sanitizer corpus: %v", err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("shared sanitizer corpus is empty")
	}
	return corpus.Cases
}

// TestSanitizeReportHTMLSharedCorpus keeps this implementation aligned with
// client/src/utils/sanitize.js: both assert the same properties from
// testdata/report-sanitize-cases.json.
func TestSanitizeReportHTMLSharedCorpus(t *testing.T) {
	for _, tc := range loadSanitizeCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			got := SanitizeReportHTML(tc.Input)
			for _, want := range tc.MustContain {
				if !strings.Contains(got, want) {
					t.Errorf("output must contain %q, got:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.MustNotContain {
				if strings.Contains(got, unwanted) {
					t.Errorf("output must not contain %q, got:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestSanitizeReportHTMLKeepsTrustedReportScaffolding(t *testing.T) {
	source := `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>报告</title>
<link rel="stylesheet" href="/assets/katex/katex.min.css">
<style>.report-block-wrapper { margin: 0 }</style>
<script id="oda-echarts-loader" src="/assets/echarts.min.js"></script>
</head>
<body class="report-body">
<div class="report-block-wrapper" data-block-id="blk_1" data-block-kind="markdown">
  <div class="chart-box" data-chart-id="cht_1" data-chart-option="{&quot;series&quot;:[]}" style="height:400px"></div>
</div>
<script id="oda-chart-runtime" src="/oda-chart-runtime.js"></script>
</body>
</html>`
	got := SanitizeReportHTML(source)
	for _, want := range []string{
		`id="oda-echarts-loader"`,
		`src="/assets/echarts.min.js"`,
		`id="oda-chart-runtime"`,
		`src="/oda-chart-runtime.js"`,
		`href="/assets/katex/katex.min.css"`,
		`data-block-id="blk_1"`,
		`data-chart-option`,
		`class="report-body"`,
		`.report-block-wrapper`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trusted scaffolding %q was stripped:\n%s", want, got)
		}
	}
}

// TestSanitizeReportHTMLIsIdempotent guards the double-sanitize path: the server
// sanitizes before sending, and the client sanitizes again on arrival.
func TestSanitizeReportHTMLIsIdempotent(t *testing.T) {
	source := `<!DOCTYPE html><html><head><style>@import url('https://fonts.example/x.css'); .a{color:red}</style>
<script id="oda-echarts-loader" src="/assets/echarts.min.js"></script></head>
<body class="b"><div class="chart-box" data-chart-id="c" data-chart-option="{}" style="height:400px">x</div>
<a href="/x" onclick="bad()">l</a><script>alert(1)</script></body></html>`
	once := SanitizeReportHTML(source)
	twice := SanitizeReportHTML(once)
	if once != twice {
		t.Fatalf("sanitizer is not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

func TestSanitizeReportHTMLDropsUnparseableInput(t *testing.T) {
	// Not a hard parser failure in practice, but the contract matters: the
	// sanitizer must never hand back content it could not reason about.
	if got := SanitizeReportHTML(""); got != "" {
		t.Fatalf("expected empty output for empty input, got %q", got)
	}
}

func TestRenderReportHTMLIsSanitizable(t *testing.T) {
	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "blk_1", Kind: "markdown", Title: "结论", Content: "## 结论\n\n文本 **加粗**。"},
			{ID: "blk_2", Kind: "html", Title: "片段", Content: `<div onclick="x()">不安全</div><script>alert(1)</script>`},
		},
		Charts: []ChartData{{ID: "cht_1", Option: json.RawMessage(`{"series":[{"type":"bar","data":[1,2]}]}`), Width: "100%", Height: "400px"}},
	}
	state.FinalTitle = "测试报告"
	html := SanitizeReportHTML(RenderReportHTML("测试报告", "", state))
	if html == "" {
		t.Fatal("sanitizing a rendered report produced empty output")
	}
	for _, unwanted := range []string{"alert(1)", "onclick=", "x()"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("rendered report leaked %q after sanitizing", unwanted)
		}
	}
	if !strings.Contains(html, "<h2") {
		t.Error("markdown heading was lost while sanitizing")
	}
}
