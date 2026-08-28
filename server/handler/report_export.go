package handler

import (
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
)

const ReportExportMaxBodyBytes int64 = 50 * 1024 * 1024

var (
	exportScriptBlockRe   = regexp.MustCompile(`(?is)<\s*script\b[^>]*>.*?<\s*/\s*script\s*>`)
	exportDangerousTagRe  = regexp.MustCompile(`(?is)<\s*/?\s*(iframe|object|embed|link|meta|base)\b[^>]*>`)
	exportEventAttrRe     = regexp.MustCompile(`(?i)\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*)`)
	exportRemoteSrcHrefRe = regexp.MustCompile(`(?i)\s+(src|href)\s*=\s*(?:"[^"]*(?:https?|file|data|blob|javascript|vbscript)\s*:[^"]*"|'[^']*(?:https?|file|data|blob|javascript|vbscript)\s*:[^']*'|(?:https?|file|data|blob|javascript|vbscript)\s*:[^\s>]+)`)
	exportSafeDataImageRe = regexp.MustCompile(`(?i)\s+src\s*=\s*("data:image/(?:png|jpe?g);base64,[a-z0-9+/=]+")`)
)

func RegisterReportExportRoutes(r chi.Router) {
	r.Group(func(exports chi.Router) {
		exports.Use(MaxBodySizeMiddleware(ReportExportMaxBodyBytes))
		exports.Post("/api/report-exports/docx", ConvertReportDOCXHandler)
	})
}

func sanitizeExportHTML(html string) string {
	protectedImages := map[string]string{}
	html = exportSafeDataImageRe.ReplaceAllStringFunc(html, func(match string) string {
		key := "__ODA_SAFE_EXPORT_IMAGE_" + strconv.Itoa(len(protectedImages)) + "__"
		protectedImages[key] = match
		return " " + key
	})
	html = exportScriptBlockRe.ReplaceAllString(html, "")
	html = exportDangerousTagRe.ReplaceAllString(html, "")
	html = exportEventAttrRe.ReplaceAllString(html, "")
	html = exportRemoteSrcHrefRe.ReplaceAllString(html, "")
	for key, src := range protectedImages {
		html = strings.ReplaceAll(html, key, strings.TrimSpace(src))
	}
	return strings.TrimSpace(html)
}

func ConvertReportDOCXHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	workspaceID := identity.WorkspaceID
	userID := identity.UserID

	type request struct {
		RunID string `json:"runId"`
	}

	var req request

	r.Body = http.MaxBytesReader(w, r.Body, ReportExportMaxBodyBytes)
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "导出请求无效或请求体过大", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.RunID) == "" {
		http.Error(w, "缺少任务 ID", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.RunID) != req.RunID {
		http.Error(w, "任务 ID 必须保持原值", http.StatusBadRequest)
		return
	}

	run, err := runRepo.GetByID(r.Context(), req.RunID)
	if writeRepoLookupError(w, err, "任务不存在") {
		return
	}
	if run.WorkspaceID != workspaceID || run.UserID != userID {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	report, err := reportRepo.GetByRunID(r.Context(), req.RunID)
	if writeRepoLookupError(w, err, "报告尚未定稿") {
		return
	}
	if report == nil {
		http.Error(w, "报告存储返回了空记录", http.StatusInternalServerError)
		return
	}
	html, err := renderReportHTMLFromSnapshot(report)
	if err != nil {
		http.Error(w, "渲染报告快照失败", http.StatusInternalServerError)
		return
	}

	html = sanitizeExportHTML(html)
	if strings.TrimSpace(html) == "" {
		http.Error(w, "报告内容为空或无效", http.StatusBadRequest)
		return
	}

	body, filename, err := fileService.ConvertHTMLToDOCX(r.Context(), report.Title, html)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "转换报告文件失败", err)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeHeaderFilename(filename)+`"`)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		log.Printf("write report export run_id=%s: %v", req.RunID, err)
	}
}
