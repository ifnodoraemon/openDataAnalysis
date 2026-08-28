package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

const runPreviewLimit = 3
const reportHTMLCSP = "default-src 'self'; script-src 'self' " + tools.ReportKaTeXCDNBaseURL + " style-src 'self' 'unsafe-inline' " + tools.ReportKaTeXCDNBaseURL + " https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com " + tools.ReportKaTeXCDNBaseURL + "fonts/; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'self';"

func ListRunsHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "缺少 session_id", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(sessionID) != sessionID {
		http.Error(w, "session_id 必须保持原值", http.StatusBadRequest)
		return
	}

	session, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if session.UserID != identity.UserID || session.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此会话", http.StatusForbidden)
		return
	}
	if err := recoverStaleSessionRuns(r.Context(), sessionID); err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "恢复任务状态失败", err)
		return
	}

	runs, err := runRepo.ListBySession(r.Context(), sessionID, 20)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "获取任务历史失败", err)
		return
	}
	serializedRuns, err := serializeRuns(r.Context(), runs)
	if err != nil {
		http.Error(w, "序列化任务历史失败", http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"runs": serializedRuns,
	}
	writeJSON(w, http.StatusOK, resp)
}

func GetRunHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	runID := chi.URLParam(r, "runID")
	run, err := runRepo.GetByID(r.Context(), runID)
	if writeRepoLookupError(w, err, "任务不存在") {
		return
	}
	if run.UserID != identity.UserID || run.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此任务", http.StatusForbidden)
		return
	}
	if err := recoverStaleSessionRuns(r.Context(), run.SessionID); err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "恢复任务状态失败", err)
		return
	}
	run, err = runRepo.GetByID(r.Context(), runID)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "重新加载任务失败", err)
		return
	}

	serializedRun, err := serializeRun(r.Context(), *run)
	if err != nil {
		http.Error(w, "序列化任务失败", http.StatusInternalServerError)
		return
	}
	resp := map[string]interface{}{
		"run": serializedRun,
	}

	messages, err := messageRepo.ListByRunPath(r.Context(), runID)
	if err != nil {
		http.Error(w, "获取任务消息失败", http.StatusInternalServerError)
		return
	}
	resp["messages"] = messages
	if err := attachRunRuntimeState(r.Context(), resp, *run); err != nil {
		http.Error(w, "获取任务运行时状态失败", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func GetRunReportHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	runID := chi.URLParam(r, "runID")
	run, err := runRepo.GetByID(r.Context(), runID)
	if writeRepoLookupError(w, err, "任务不存在") {
		return
	}
	if run.UserID != identity.UserID || run.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此任务", http.StatusForbidden)
		return
	}
	if err := recoverStaleSessionRuns(r.Context(), run.SessionID); err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "恢复任务状态失败", err)
		return
	}
	run, err = runRepo.GetByID(r.Context(), runID)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "重新加载任务失败", err)
		return
	}

	report, reportErr := reportRepo.GetByRunID(r.Context(), runID)
	if errors.Is(reportErr, repository.ErrNotFound) {
		http.Error(w, "任务尚未生成已定稿报告", http.StatusNotFound)
		return
	}
	if reportErr != nil {
		writeHandlerError(w, http.StatusInternalServerError, "获取报告失败", reportErr)
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
	setReportHTMLHeaders(w)
	w.Header().Set("Content-Disposition", `inline; filename="`+safeHeaderFilename(reportFilename(report.Title, runID))+`"`)
	if _, err := io.WriteString(w, html); err != nil {
		return
	}
}

func setReportHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", reportHTMLCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func renderReportHTMLFromSnapshot(report *domain.Report) (string, error) {
	if report == nil || strings.TrimSpace(report.SnapshotJSON) == "" {
		return "", fmt.Errorf("report snapshot is required")
	}

	var snapshot domain.ReportSnapshot
	if err := jsoncontract.Decode([]byte(report.SnapshotJSON), &snapshot); err != nil {
		return "", fmt.Errorf("invalid report snapshot: %w", err)
	}

	return renderReportHTMLFromSnapshotData(&snapshot)
}

func renderReportHTMLFromSnapshotData(snapshot *domain.ReportSnapshot) (string, error) {
	if snapshot == nil {
		return "", fmt.Errorf("report snapshot is required")
	}
	restored := &session.Session{ReportState: &tools.ReportState{}}
	if err := restored.LoadReportSnapshot(snapshot); err != nil {
		return "", fmt.Errorf("invalid report snapshot state: %w", err)
	}
	return tools.RenderReportHTML(snapshot.Title, snapshot.Author, restored.ReportState), nil
}

func serializeRuns(ctx context.Context, runs []domain.AnalysisRun) ([]map[string]interface{}, error) {
	resp := make([]map[string]interface{}, 0, len(runs))
	for _, run := range runs {
		item, err := serializeRun(ctx, run)
		if err != nil {
			return nil, err
		}
		resp = append(resp, item)
	}
	return resp, nil
}

func serializeRun(ctx context.Context, run domain.AnalysisRun) (map[string]interface{}, error) {
	item := map[string]interface{}{
		"id":           run.ID,
		"sessionId":    run.SessionID,
		"workspaceId":  run.WorkspaceID,
		"runKind":      run.RunKind,
		"delegateRole": run.DelegateRole,
		"status":       run.Status,
		"inputMessage": run.InputMessage,
		"summary":      run.Summary,
		"createdAt":    run.CreatedAt,
		"updatedAt":    run.UpdatedAt,
	}
	if run.ParentRunID != nil {
		item["parentRunId"] = *run.ParentRunID
	}
	if run.GoalID != nil {
		item["goalId"] = *run.GoalID
	}
	if run.ErrorMessage != nil {
		item["errorMessage"] = *run.ErrorMessage
	}
	if run.ReportFileID != nil {
		item["reportFileId"] = *run.ReportFileID
	}
	if reportRepo == nil || runRepo == nil || messageRepo == nil {
		return nil, fmt.Errorf("task serialization repositories are not configured")
	}
	report, err := reportRepo.GetByRunID(ctx, run.ID)
	if err == nil && report != nil {
		item["report"] = serializeReport(*report)
	} else if err != nil && !isRepoNotFound(err) {
		return nil, fmt.Errorf("failed to read report for task %s: %w", run.ID, err)
	}
	if run.StartedAt != nil {
		item["startedAt"] = *run.StartedAt
	}
	if run.FinishedAt != nil {
		item["finishedAt"] = *run.FinishedAt
	}
	preview, err := buildRunPreview(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	if len(preview) > 0 {
		item["previewMessages"] = preview
	}
	childRuns, err := runRepo.ListByParent(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to list child tasks for %s: %w", run.ID, err)
	}
	if len(childRuns) > 0 {
		serializedChildren, err := serializeRuns(ctx, childRuns)
		if err != nil {
			return nil, err
		}
		item["childRuns"] = serializedChildren
	}
	return item, nil
}

func buildRunPreview(ctx context.Context, runID string) ([]map[string]interface{}, error) {
	if messageRepo == nil {
		return nil, fmt.Errorf("message repository is not configured")
	}
	messages, err := messageRepo.ListRecentByRun(ctx, runID, runPreviewLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to read task preview messages: %w", err)
	}
	if len(messages) == 0 {
		return nil, nil
	}
	items := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		summary := summarizeRunMessage(msg)
		if summary == "" {
			continue
		}
		items = append(items, map[string]interface{}{
			"type":    msg.Type,
			"name":    msg.Name,
			"summary": summary,
		})
	}
	return items, nil
}

func summarizeRunMessage(msg domain.RunMessage) string {
	content := strings.TrimSpace(msg.Content)
	switch msg.Type {
	case "assistant_status":
		return clipPreviewText(content, 120)
	case "tool_call":
		return msg.Name
	case "tool_result":
		var payload map[string]interface{}
		if err := jsoncontract.Decode([]byte(content), &payload); err == nil {
			if summary, ok := payload["ui_summary"].(string); ok && strings.TrimSpace(summary) != "" {
				return clipPreviewText(summary, 120)
			}
		}
		if msg.Name != "" {
			return clipPreviewText(msg.Name+": "+content, 120)
		}
		return clipPreviewText(content, 120)
	case "run_completed":
		return clipPreviewText(content, 120)
	case "error":
		return clipPreviewText(content, 120)
	default:
		return clipPreviewText(content, 120)
	}
}

func clipPreviewText(input string, max int) string {
	input = strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
	if max <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= max {
		return input
	}
	return string(runes[:max]) + "..."
}

func serializeReport(report domain.Report) map[string]interface{} {
	return map[string]interface{}{
		"id":        report.ID,
		"runId":     report.RunID,
		"title":     report.Title,
		"author":    report.Author,
		"createdAt": report.CreatedAt,
	}
}

func reportFilename(title, runID string) string {
	name := strings.TrimSpace(title)
	if name == "" {
		name = "report-" + runID
	}
	if !strings.HasSuffix(strings.ToLower(name), ".html") {
		name += ".html"
	}
	return name
}

func defaultContentType(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return "application/octet-stream"
	}
	return contentType
}

func safeHeaderFilename(name string) string {
	replacer := strings.NewReplacer("\n", "", "\r", "", `"`, "")
	safe := strings.TrimSpace(replacer.Replace(name))
	if safe == "" {
		return "report.html"
	}
	return safe
}
