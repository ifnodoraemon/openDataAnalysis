package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

const runPreviewLimit = 3
const reportHTMLCSP = "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net https://cdnjs.cloudflare.com; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'self';"

func ListRunsHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if ok, _ := workspaceRepo.IsMember(r.Context(), identity.WorkspaceID, identity.UserID); !ok {
		http.Error(w, "user not authorized to access workspace", http.StatusForbidden)
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	session, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "session does not exist") {
		return
	}
	if session.UserID != identity.UserID || session.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized to access this session", http.StatusForbidden)
		return
	}
	if err := recoverStaleSessionRuns(r.Context(), sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	runs, err := runRepo.ListBySession(r.Context(), sessionID, 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"runs": serializeRuns(r.Context(), runs),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func GetRunHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if ok, _ := workspaceRepo.IsMember(r.Context(), identity.WorkspaceID, identity.UserID); !ok {
		http.Error(w, "user not authorized to access workspace", http.StatusForbidden)
		return
	}
	runID := chi.URLParam(r, "runID")
	run, err := runRepo.GetByID(r.Context(), runID)
	if writeRepoLookupError(w, err, "task does not exist") {
		return
	}
	if run.UserID != identity.UserID || run.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized to access this task", http.StatusForbidden)
		return
	}
	if err := recoverStaleSessionRuns(r.Context(), run.SessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	run, err = runRepo.GetByID(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"run": serializeRun(r.Context(), *run),
	}

	messages, err := messageRepo.ListByRunPath(r.Context(), runID)
	if err == nil {
		resp["messages"] = messages
	} else {
		resp["messages"] = []domain.RunMessage{}
	}
	attachRunRuntimeState(r.Context(), resp, *run)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func GetRunReportHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if ok, _ := workspaceRepo.IsMember(r.Context(), identity.WorkspaceID, identity.UserID); !ok {
		http.Error(w, "user not authorized to access workspace", http.StatusForbidden)
		return
	}
	runID := chi.URLParam(r, "runID")
	run, err := runRepo.GetByID(r.Context(), runID)
	if writeRepoLookupError(w, err, "task does not exist") {
		return
	}
	if run.UserID != identity.UserID || run.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized to access this task", http.StatusForbidden)
		return
	}
	if err := recoverStaleSessionRuns(r.Context(), run.SessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	run, err = runRepo.GetByID(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	report, reportErr := reportRepo.GetByRunID(r.Context(), runID)
	if reportErr == nil && report != nil {
		if html, ok := renderReportHTMLFromSnapshot(report); ok {
			setReportHTMLHeaders(w)
			w.Header().Set("Content-Disposition", `inline; filename="`+safeHeaderFilename(reportFilename(report.Title, runID))+`"`)
			_, _ = io.WriteString(w, html)
			return
		}
		reader, err := fileService.OpenStoredObject(r.Context(), identity.UserID, identity.WorkspaceID, report.HTMLStorageKey)
		if err == nil {
			defer reader.Close()
			setReportHTMLHeaders(w)
			w.Header().Set("Content-Disposition", `inline; filename="`+safeHeaderFilename(reportFilename(report.Title, runID))+`"`)
			_, _ = io.Copy(w, reader)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "task report file does not exist", http.StatusNotFound)
			return
		}
	}
	if reportErr != nil && reportErr != sql.ErrNoRows {
		http.Error(w, reportErr.Error(), http.StatusInternalServerError)
		return
	}
	if run.ReportFileID == nil || strings.TrimSpace(*run.ReportFileID) == "" {
		http.Error(w, "task has not generated a report yet", http.StatusNotFound)
		return
	}

	reader, file, err := fileService.OpenForDownload(r.Context(), identity.UserID, identity.WorkspaceID, *run.ReportFileID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "task report file does not exist", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", defaultContentType(file.ContentType))
	if strings.Contains(strings.ToLower(defaultContentType(file.ContentType)), "text/html") {
		w.Header().Set("Content-Security-Policy", reportHTMLCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.Header().Set("Content-Disposition", `inline; filename="`+safeHeaderFilename(file.DisplayName)+`"`)
	_, _ = io.Copy(w, reader)
}

func setReportHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", reportHTMLCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func renderReportHTMLFromSnapshot(report *domain.Report) (string, bool) {
	if report == nil || strings.TrimSpace(report.SnapshotJSON) == "" {
		return "", false
	}

	var snapshot domain.ReportSnapshot
	if err := json.Unmarshal([]byte(report.SnapshotJSON), &snapshot); err != nil {
		return "", false
	}

	html, ok := renderReportHTMLFromSnapshotData(&snapshot)
	if !ok {
		return "", false
	}
	return html, true
}

func renderReportHTMLFromSnapshotData(snapshot *domain.ReportSnapshot) (string, bool) {
	if snapshot == nil {
		return "", false
	}

	state := &tools.ReportState{
		FinalTitle:  strings.TrimSpace(snapshot.Title),
		FinalAuthor: strings.TrimSpace(snapshot.Author),
		Layout: tools.ReportLayout{
			CustomCSS: snapshot.Layout.CustomCSS,
			BodyClass: snapshot.Layout.BodyClass,
		},
		NeedsFinalize: snapshot.NeedsFinalize,
		Blocks:        make([]tools.ReportBlock, 0, len(snapshot.Blocks)),
		Charts:        make([]tools.ChartData, 0, len(snapshot.Charts)),
	}

	for _, block := range snapshot.Blocks {
		reportBlock := tools.ReportBlock{
			ID:      block.ID,
			Kind:    block.Kind,
			Title:   block.Title,
			Content: block.Content,
			ChartID: block.ChartID,
		}
		state.Blocks = append(state.Blocks, reportBlock)
	}

	for _, chart := range snapshot.Charts {
		state.Charts = append(state.Charts, tools.ChartData{
			ID:     chart.ID,
			Option: chart.Option,
			Width:  chart.Width,
			Height: chart.Height,
		})
	}

	return tools.RenderReportHTML(snapshot.Title, snapshot.Author, state), true
}

func serializeRuns(ctx context.Context, runs []domain.AnalysisRun) []map[string]interface{} {
	resp := make([]map[string]interface{}, 0, len(runs))
	for _, run := range runs {
		resp = append(resp, serializeRun(ctx, run))
	}
	return resp
}

func serializeRun(ctx context.Context, run domain.AnalysisRun) map[string]interface{} {
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
	if reportRepo != nil {
		if report, err := reportRepo.GetByRunID(ctx, run.ID); err == nil && report != nil {
			item["report"] = serializeReport(*report)
		}
	}
	if run.StartedAt != nil {
		item["startedAt"] = *run.StartedAt
	}
	if run.FinishedAt != nil {
		item["finishedAt"] = *run.FinishedAt
	}
	if preview := buildRunPreview(ctx, run.ID); len(preview) > 0 {
		item["previewMessages"] = preview
	}
	if childRuns, err := runRepo.ListByParent(ctx, run.ID); err == nil && len(childRuns) > 0 {
		item["childRuns"] = serializeRuns(ctx, childRuns)
	}
	return item
}

func buildRunPreview(ctx context.Context, runID string) []map[string]interface{} {
	if messageRepo == nil {
		return nil
	}
	messages, err := messageRepo.ListRecentByRun(ctx, runID, runPreviewLimit)
	if err != nil || len(messages) == 0 {
		return nil
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
	return items
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
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			if summary, ok := payload["ui_summary"].(string); ok && strings.TrimSpace(summary) != "" {
				return clipPreviewText(summary, 120)
			}
			if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
				return clipPreviewText(message, 120)
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
