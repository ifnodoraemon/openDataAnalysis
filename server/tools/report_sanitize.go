package tools

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// SanitizeReportHTML applies the report-document allowlist to a rendered report.
//
// The client already sanitizes report HTML before it reaches an iframe. This
// server-side pass exists because the same document also leaves through paths a
// browser never sanitizes: the persisted artifact users download, the standalone
// report endpoint, and (later) the headless capture input. Without it those
// three carry model-authored markup untouched, and the preview would not match
// what the user actually receives.
//
// Policies are two, not one, and the split is deliberate:
//
//   - input side: sanitizeHTMLFragment, strict, applied to model-authored block
//     content before it enters the document;
//   - output side: this function, an allowlist that also admits the renderer's
//     own chart/math assets, applied to the finished document.
//
// The rules mirror client/src/utils/sanitize.js; both sides assert against
// testdata/report-sanitize-cases.json so they cannot drift silently.
func SanitizeReportHTML(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		// Unparseable input is not renderable; emitting nothing beats emitting
		// the original.
		return ""
	}
	sanitizeReportTree(doc)
	return renderSanitizedDocument(doc)
}

// RenderSafeReportHTML renders a report and sanitizes it in one step.
//
// Anything that leaves the server must use this instead of RenderReportHTML:
// preview events, the persisted artifact users download, and the standalone
// report endpoint. RenderReportHTML stays raw because callers that re-parse the
// document for structure (and its tests) want the unsanitized tree.
func RenderSafeReportHTML(title, author string, state *ReportState) string {
	return SanitizeReportHTML(RenderReportHTML(title, author, state))
}

var (
	reportAllowedTags = tagSet(
		"html", "head", "body", "meta", "link", "title", "style", "script",
		"div", "span", "p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption",
		"ul", "ol", "li", "dl", "dt", "dd", "a", "strong", "em", "b", "i",
		"u", "s", "mark", "small", "sub", "sup", "blockquote", "pre", "code",
		"img", "figure", "figcaption", "section", "article", "aside", "header",
		"footer", "main", "nav", "details", "summary",
	)
	reportRemoveTags = tagSet(
		"iframe", "object", "embed", "base", "form", "input", "button",
		"select", "textarea", "noscript",
	)
	// Report scripts load exclusively from same-origin static assets. Each
	// (id, path) pair is an exact match, so a CDN URL or a path trick such as
	// /assets/echarts.min.js/../../evil.js is rejected.
	reportScriptSources = map[string]string{
		"oda-echarts-loader":   "/assets/echarts.min.js",
		"oda-chart-runtime":    "/oda-chart-runtime.js",
		"oda-math-loader":      "/assets/katex/katex.min.js",
		"oda-math-auto-render": "/assets/katex/contrib/auto-render.min.js",
		"oda-math-runtime":     "/oda-math-runtime.js",
	}
	keptAttributeNames = map[string]struct{}{
		"target": {}, "rel": {}, "charset": {}, "name": {},
	}
	// Attributes a report document may carry beyond the ones handled explicitly.
	reportAttributeNames = map[string]struct{}{
		"id": {}, "title": {}, "colspan": {}, "rowspan": {}, "width": {},
		"height": {}, "alt": {}, "role": {}, "lang": {}, "dir": {},
	}
	inlineStyleDangerous = regexp.MustCompile(`(?i)(?:expression|javascript|vbscript|behavior|@import)\s*\(`)
	inlineStyleURL       = regexp.MustCompile(`(?i)url\s*\(`)
	styleImportRule      = regexp.MustCompile(`(?i)@import[^;]*(?:;|$)`)
)

func tagSet(tags ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		set[tag] = struct{}{}
	}
	return set
}

// sanitizeReportTree walks depth-first, then unwraps and removes in reverse order
// so a mutation never invalidates a node reference still pending.
func sanitizeReportTree(doc *html.Node) {
	var toUnwrap, toRemove []*html.Node
	for node := doc.FirstChild; node != nil; node = node.NextSibling {
		walkReportNode(node, &toUnwrap, &toRemove)
	}
	for i := len(toUnwrap) - 1; i >= 0; i-- {
		unwrapNode(toUnwrap[i])
	}
	for i := len(toRemove) - 1; i >= 0; i-- {
		removeNode(toRemove[i])
	}
	// Attribute cleaning can strip a script's id, so the allowlist pass runs
	// after it, matching the client's order.
	for _, node := range collectNodes(doc, "script") {
		if !isAllowedReportScript(node) {
			removeNode(node)
		}
	}
	for _, node := range collectNodes(doc, "style") {
		// Order matters for the fixpoint: the @import statement is removed whole
		// first (the client does the same), then any url() load, then the shared
		// script-vector stripper.
		css := styleImportRule.ReplaceAllString(textContent(node), "")
		css = inlineStyleURL.ReplaceAllString(css, "")
		setTextContent(node, sanitizeCSS(css))
	}
	if body := findFirstNode(doc, "body"); body != nil {
		className := sanitizeBodyClass(attributeValue(body, "class"))
		if className == "" {
			removeAttribute(body, "class")
		} else {
			setAttribute(body, "class", className)
		}
	}
}

func walkReportNode(node *html.Node, toUnwrap, toRemove *[]*html.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		walkReportNode(child, toUnwrap, toRemove)
		child = next
	}
	if node.Type != html.ElementNode {
		return
	}
	if _, remove := reportRemoveTags[node.Data]; remove {
		*toRemove = append(*toRemove, node)
		return
	}
	if _, allowed := reportAllowedTags[node.Data]; !allowed {
		*toUnwrap = append(*toUnwrap, node)
		return
	}
	cleanReportAttributes(node)
}

func cleanReportAttributes(node *html.Node) {
	for _, attr := range append([]html.Attribute(nil), node.Attr...) {
		name := strings.ToLower(strings.TrimSpace(attr.Key))
		value := attr.Val
		switch {
		case strings.HasPrefix(name, "on"):
			removeAttribute(node, attr.Key)
		case name == "id" || name == "content":
			// Reports depend on ids (blocks, charts, anchors). Scripts are the
			// exception: only the five loader ids are real.
			allowed := name == "id" && (node.Data != "script" || isAllowedReportScriptID(value))
			if !allowed {
				removeAttribute(node, attr.Key)
			}
		case name == "href" || name == "src":
			safe := ""
			if node.Data == "link" && name == "href" {
				if isSameOriginPath(value) {
					safe = value
				}
			} else if resolved, ok := sanitizeURL(strings.TrimSpace(value)); ok {
				safe = resolved
			}
			if safe == "" {
				removeAttribute(node, attr.Key)
				continue
			}
			setAttribute(node, attr.Key, safe)
			if name == "href" && node.Data == "a" {
				setAttribute(node, "rel", "noopener noreferrer")
				setAttribute(node, "target", "_blank")
			}
		case name == "style":
			// Reports own chart styling, so inline style survives but without
			// any load or script vector.
			if safe := sanitizeInlineStyle(value); safe != "" {
				setAttribute(node, attr.Key, safe)
			} else {
				removeAttribute(node, attr.Key)
			}
		case name == "class":
			// Block ids, chart boxes and layout classes all ride on class in
			// report mode; the token allowlist is the guard.
			if safe := sanitizeBodyClass(value); safe != "" {
				setAttribute(node, attr.Key, safe)
			} else {
				removeAttribute(node, attr.Key)
			}
		case strings.HasPrefix(name, "data-"):
			// data-chart-option carries chart facts; dropping it blanks charts.
		case isReportAttributeName(name):
			// Presentation attributes stay.
		default:
			if _, kept := keptAttributeNames[name]; !kept {
				removeAttribute(node, attr.Key)
			}
		}
	}
}

func isReportAttributeName(name string) bool {
	if strings.HasPrefix(name, "aria-") {
		return true
	}
	_, ok := reportAttributeNames[name]
	return ok
}

func isAllowedReportScriptID(id string) bool {
	_, ok := reportScriptSources[id]
	return ok
}

func isAllowedReportScript(node *html.Node) bool {
	src := strings.TrimSpace(attributeValue(node, "src"))
	id := attributeValue(node, "id")
	if src == "" || id == "" {
		return false
	}
	expected, ok := reportScriptSources[id]
	if !ok {
		return false
	}
	// There is no request origin at render time, so only relative same-origin
	// paths are accepted; an absolute URL is never allowlisted.
	return src == expected
}

// isSameOriginPath reports whether a URL is a document-relative path. Absolute
// URLs cannot be proven same-origin without a request origin, so they fail.
func isSameOriginPath(value string) bool {
	raw := strings.TrimSpace(value)
	if raw == "" || strings.Contains(raw, "\\") {
		return false
	}
	return strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//")
}

func sanitizeInlineStyle(value string) string {
	if strings.ContainsRune(value, '<') {
		return ""
	}
	if inlineStyleDangerous.MatchString(value) {
		return ""
	}
	// Inline styles must not trigger a load; <img> goes through the attribute
	// sanitizer instead.
	if inlineStyleURL.MatchString(value) {
		return ""
	}
	return value
}

func renderSanitizedDocument(doc *html.Node) string {
	root := findFirstNode(doc, "html")
	if root == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, root); err != nil {
		return ""
	}
	return "<!DOCTYPE html>\n" + buf.String()
}

func collectNodes(node *html.Node, tag string) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == tag {
				found = append(found, child)
			}
			walk(child)
		}
	}
	walk(node)
	return found
}

func findFirstNode(node *html.Node, tag string) *html.Node {
	if node.Type == html.ElementNode && node.Data == tag {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirstNode(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func textContent(node *html.Node) string {
	var buf bytes.Buffer
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.TextNode {
				buf.WriteString(child.Data)
			}
			walk(child)
		}
	}
	walk(node)
	return buf.String()
}

func setTextContent(node *html.Node, text string) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		node.RemoveChild(child)
		child = next
	}
	node.AppendChild(&html.Node{Type: html.TextNode, Data: text})
}

func attributeValue(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func setAttribute(node *html.Node, name, value string) {
	for i := range node.Attr {
		if strings.EqualFold(node.Attr[i].Key, name) {
			node.Attr[i].Val = value
			return
		}
	}
	node.Attr = append(node.Attr, html.Attribute{Key: name, Val: value})
}

func removeAttribute(node *html.Node, name string) {
	kept := node.Attr[:0]
	for _, attr := range node.Attr {
		if !strings.EqualFold(attr.Key, name) {
			kept = append(kept, attr)
		}
	}
	node.Attr = kept
}

func unwrapNode(node *html.Node) {
	parent := node.Parent
	if parent == nil {
		return
	}
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		node.RemoveChild(child)
		parent.InsertBefore(child, node)
		child = next
	}
	parent.RemoveChild(node)
}

func removeNode(node *html.Node) {
	if node.Parent != nil {
		node.Parent.RemoveChild(node)
	}
}
