package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
)

func ListSessionsHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	sessions, err := sessionRepo.ListByUserWorkspace(r.Context(), identity.UserID, identity.WorkspaceID, 20)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "获取会话列表失败", err)
		return
	}

	respSessions := make([]map[string]interface{}, 0, len(sessions))
	for _, session := range sessions {
		respSessions = append(respSessions, serializeSession(session))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": respSessions,
	})
}

func CreateSessionHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	session, err := ensureSession(r.Context(), identity)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "创建会话失败", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session":      serializeSession(*session),
		"files":        []map[string]interface{}{},
		"runs":         []map[string]interface{}{},
		"runtimeState": serializeRuntimeState(map[string]agent.MemoryEntry{}, []agent.Subgoal{}, ""),
	})
}

func GetSessionHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	session, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if session.UserID != identity.UserID || session.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此会话", http.StatusForbidden)
		return
	}

	if err := recoverStaleSessionRuns(r.Context(), session.ID); err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "恢复任务状态失败", err)
		return
	}
	runs, err := runRepo.ListBySession(r.Context(), session.ID, 20)
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
		"session": serializeSession(*session),
		"runs":    serializedRuns,
	}
	if err := attachRuntimeState(r.Context(), resp, identity.WorkspaceID, identity.UserID, session.ID); err != nil {
		http.Error(w, "获取运行时状态失败", http.StatusInternalServerError)
		return
	}

	sources, srcErr := sourceService.GetSessionSources(r.Context(), session.ID)
	if srcErr != nil {
		http.Error(w, "获取会话数据源失败", http.StatusInternalServerError)
		return
	}
	resp["sessionSources"] = sources
	writeJSON(w, http.StatusOK, resp)
}

func UpdateSessionHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	session, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if session.UserID != identity.UserID || session.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权修改此会话", http.StatusForbidden)
		return
	}

	var req struct {
		Title string `json:"title"`
	}
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求体无效", http.StatusBadRequest)
		return
	}

	if err := sessionRepo.UpdateTitle(r.Context(), sessionID, req.Title); err != nil {
		http.Error(w, "更新标题失败", http.StatusInternalServerError)
		return
	}

	session.Title = req.Title
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": serializeSession(*session),
	})
}

func DeleteSessionHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if !requireWorkspaceMembership(w, r.Context(), identity.WorkspaceID, identity.UserID) {
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	session, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if session.UserID != identity.UserID || session.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权删除此会话", http.StatusForbidden)
		return
	}

	if err := deleteSessionResources(r.Context(), *session); err != nil {
		http.Error(w, "删除会话失败", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
