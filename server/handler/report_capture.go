package handler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// triggerReportCapture schedules a background capture of the current report.
//
// It never blocks: the engine reads the newest ready view on its next request,
// and a missing view simply means this turn carries facts only. The coordinator
// owns dedupe and single-flight, so calling this on every report mutation is the
// intended usage rather than a bug.
func triggerReportCapture(sess *session.Session, reason string) {
	if sess == nil || sess.ReportState == nil {
		return
	}
	coordinator := sess.ReportViewCoordinator()
	if coordinator == nil {
		return
	}

	sess.ReportState.RLock()
	version := int(sess.ReportState.MutationVersion)
	// Capture the same bytes the user sees: the sanitized document, not the raw
	// render. Anything else would have the model review a page nobody can open.
	html := tools.RenderSafeReportHTML(sess.ReportState.FinalTitle, sess.ReportState.FinalAuthor, sess.ReportState)
	sess.ReportState.RUnlock()

	// Block hashes take their own read lock, so they are computed outside the
	// critical section above.
	hashes := sess.ReportState.BlockContentHashes()
	coordinator.Request(version, reason, html, hashes)
}

// finalizeCaptureTimeout bounds the one blocking capture in the whole pipeline.
// It is large enough for a long report and small enough that a dead browser
// cannot hold the delivery boundary hostage.
const finalizeCaptureTimeout = 8 * time.Second

// finalizeReportView takes a synchronous capture of the finalized report.
//
// Everything else is fire-and-forget, but at the delivery boundary there is no
// later trigger: if the model is about to write its final answer, this is the
// last chance for it to see the report as delivered. A failure is logged and
// ignored — a missing view must never fail a run that is otherwise finished.
func finalizeReportView(sess *session.Session) {
	if sess == nil || sess.ReportState == nil {
		return
	}
	coordinator := sess.ReportViewCoordinator()
	if coordinator == nil {
		return
	}

	sess.ReportState.RLock()
	version := int(sess.ReportState.MutationVersion)
	html := tools.RenderSafeReportHTML(sess.ReportState.FinalTitle, sess.ReportState.FinalAuthor, sess.ReportState)
	sess.ReportState.RUnlock()
	hashes := sess.ReportState.BlockContentHashes()

	ctx, cancel := context.WithTimeout(context.Background(), finalizeCaptureTimeout)
	defer cancel()
	if view := coordinator.CaptureNow(ctx, version, "finalize_precheck", html, hashes); view == nil {
		log.Printf("report view: finalize capture produced no view (session=%s version=%d)", sess.ID, version)
	}
}

// CaptureReportViewHandler forces the current report view into the model's next
// request. It is the manual "look at my screen" path: the user decides the model
// should see the report, instead of waiting for an edit to trigger a capture.
func CaptureReportViewHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		http.Error(w, "缺少会话 ID", http.StatusBadRequest)
		return
	}

	sess, _, err := sessionManager.GetOrCreate(r.Context(), sessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		writeHandlerError(w, http.StatusNotFound, "会话不存在或无权访问", err)
		return
	}
	coordinator := sess.ReportViewCoordinator()
	if coordinator == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":  "报告视图捕获未启用",
			"detail": "需要配置 RENDER_EXECUTOR_URL、RENDER_ASSET_BASE_URL 与 PROXY_TOKEN",
		})
		return
	}
	if sess.ReportState == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{"error": "会话还没有报告状态"})
		return
	}

	sess.ReportState.RLock()
	version := int(sess.ReportState.MutationVersion)
	html := tools.RenderSafeReportHTML(sess.ReportState.FinalTitle, sess.ReportState.FinalAuthor, sess.ReportState)
	sess.ReportState.RUnlock()
	hashes := sess.ReportState.BlockContentHashes()

	coordinator.RequestManual(version, html, hashes)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":         "accepted",
		"report_version": version,
		"ui_summary":     "已请求当前报告视图，模型下次发言时可见",
	})
}
