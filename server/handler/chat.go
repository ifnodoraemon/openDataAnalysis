package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type chatRequest struct {
	Content     string                   `json:"content"`
	TurnContext *agent.TurnContext       `json:"turnContext,omitempty"`
	EditContext *agent.ReportEditContext `json:"editContext,omitempty"`
}

func ChatHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "缺少会话 ID", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(sessionID) != sessionID {
		http.Error(w, "会话 ID 必须保持原值", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	var req chatRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		writeHandlerError(w, http.StatusBadRequest, "请求体无效", err)
		return
	}

	shouldHydrate, err := shouldHydrateSessionFromPersistence(r.Context(), identity.WorkspaceID, identity.UserID, sessionID)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "检查会话状态失败", err)
		return
	}
	sess, _, err := sessionManager.GetOrCreate(r.Context(), sessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "获取会话失败", err)
		return
	}
	if shouldHydrate {
		if err := recoverStaleSessionRuns(r.Context(), sess.ID); err != nil {
			writeHandlerError(w, http.StatusInternalServerError, "恢复会话任务失败", err)
			return
		}
		if err := hydrateSessionFromPersistence(r.Context(), sess); err != nil {
			writeHandlerError(w, http.StatusInternalServerError, "恢复会话状态失败", err)
			return
		}
	}

	userMsg := agent.UserMessage{
		Content:     req.Content,
		TurnContext: req.TurnContext,
		EditContext: req.EditContext,
	}
	if err := userMsg.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	preparedUserMsg, extraRuntime, prepErr := resolvePreparedUserMessage(r.Context(), sess, userMsg)
	if prepErr != nil {
		writeHandlerError(w, http.StatusBadRequest, "准备用户请求失败", prepErr)
		return
	}

	requestCtx := detachedContext(r.Context())

	// Handle resuming run if waiting for user input
	if activeRunID := sess.ConsumeWaitingRun(); activeRunID != "" {
		if err := runBeforeUserRunHooks(r.Context(), sess, preparedUserMsg, prepareUserRunHook(handlerReportSnapshotLoader{})); err != nil {
			sess.ReturnRunToWaiting(activeRunID)
			writeHandlerError(w, http.StatusBadRequest, "应用请求上下文失败", err)
			return
		}
		if err := resumeWaitingRun(requestCtx, sess, identity, activeRunID, userMsg); err != nil {
			writeHandlerError(w, http.StatusBadRequest, "恢复任务失败", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "resumed",
			"run_id": activeRunID,
		})
		return
	}

	runID, ctx, err := sess.StartRun(requestCtx)
	if err != nil {
		writeHandlerError(w, http.StatusBadRequest, "启动任务失败", err)
		return
	}
	if err := runBeforeUserRunHooks(r.Context(), sess, preparedUserMsg, prepareUserRunHook(handlerReportSnapshotLoader{})); err != nil {
		sess.FinishRun(runID, "failed")
		writeHandlerError(w, http.StatusBadRequest, "应用请求上下文失败", err)
		return
	}

	now := time.Now()
	rawInput := preparedUserMsg.Content
	log.Printf("chat run started run_id=%s session_id=%s workspace_id=%s user_id=%s input_chars=%d", runID, sess.ID, sess.WorkspaceID, identity.UserID, len([]rune(rawInput)))

	if err := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
		return runRepo.Create(persistCtx, &domain.AnalysisRun{
			ID:           runID,
			SessionID:    sess.ID,
			WorkspaceID:  sess.WorkspaceID,
			UserID:       identity.UserID,
			RunKind:      domain.RunKindRoot,
			Status:       domain.RunStatusRunning,
			InputMessage: rawInput,
			StartedAt:    &now,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}); err != nil {
		sess.CancelRun(runID)
		writeHandlerError(w, http.StatusInternalServerError, "创建任务记录失败", err)
		return
	}

	if _, err := saveEventToDB(requestCtx, sess.WorkspaceID, sess.ID, runID, agent.RuntimeEvent{
		Type: "user",
		Data: userMsg,
	}); err != nil {
		sess.CancelRun(runID)
		errMsg := "保存用户消息失败"
		statusErr := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
			return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusFailed, &errMsg)
		})
		writeHandlerError(w, http.StatusInternalServerError, "保存用户消息失败", errors.Join(fmt.Errorf("%s: %w", errMsg, err), statusErr))
		return
	}

	if err := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
		return sessionRepo.UpdateLastRun(persistCtx, sess.ID, runID)
	}); err != nil {
		sess.CancelRun(runID)
		errMsg := "更新会话任务指针失败"
		statusErr := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
			return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusFailed, &errMsg)
		})
		writeHandlerError(w, http.StatusInternalServerError, "更新会话任务指针失败", errors.Join(fmt.Errorf("%s: %w", errMsg, err), statusErr))
		return
	}

	sendSessionEvent(sess.ID, runID, agent.RuntimeEvent{
		Type: agent.EventRunStarted,
		Data: agent.RunStartedData{RunID: runID},
	}, "")

	// Dispatch run execution in background goroutine
	go func() {
		runEmitter := newRuntimeEventDispatcher(requestCtx, sess, identity, runID)

		execCtx := agent.WithTraceMetadata(ctx, agent.TraceMetadata{
			WorkspaceID: sess.WorkspaceID,
			SessionID:   sess.ID,
			RunID:       runID,
		})
		execCtx = tools.WithExecutionMetadata(execCtx, tools.ExecutionMetadata{
			UserID:      identity.UserID,
			WorkspaceID: sess.WorkspaceID,
			SessionID:   sess.ID,
			RunID:       runID,
		})
		execCtx = agent.WithDelegateRunPersistence(execCtx, delegateRunPersistence{
			workspaceID: sess.WorkspaceID,
			sessionID:   sess.ID,
			userID:      identity.UserID,
			emit:        runEmitter.Emit,
		})

		ctxWithCancel, cancel := context.WithCancel(execCtx)
		sess.UpdateCancelFunc(runID, cancel)

		runtimeVarProvider := mergeRuntimeVarProvider(sess.RuntimeVars, extraRuntime)
		if err := dispatchRunExecution(runExecution{
			Context:     ctxWithCancel,
			Session:     sess,
			UserInput:   rawInput,
			RuntimeVars: runtimeVarProvider,
			Emit:        runEmitter.Emit,
			OnDone:      cancel,
		}); err != nil {
			cancel()
			failRunStart(requestCtx, sess, runID, err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"run_id": runID,
	})
}

func CancelRunHandler(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if strings.TrimSpace(runID) == "" {
		http.Error(w, "缺少任务 ID", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(runID) != runID {
		http.Error(w, "任务 ID 必须保持原值", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	run, err := getRunWithPersistence(r.Context(), runID)
	if writeRepoLookupError(w, err, "任务不存在") {
		return
	}
	if run.WorkspaceID != identity.WorkspaceID || run.UserID != identity.UserID {
		http.Error(w, "无权访问该任务", http.StatusForbidden)
		return
	}

	sess, _, err := sessionManager.GetOrCreate(r.Context(), run.SessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	if !sess.CancelRun(runID) {
		http.Error(w, "任务当前未运行", http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "cancelled",
		"run_id": runID,
	})
}

type userInputResponseRequest struct {
	Response string `json:"response"`
}

func SubmitUserInputHandler(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if strings.TrimSpace(runID) == "" {
		http.Error(w, "缺少任务 ID", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(runID) != runID {
		http.Error(w, "任务 ID 必须保持原值", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	run, err := getRunWithPersistence(r.Context(), runID)
	if writeRepoLookupError(w, err, "任务不存在") {
		return
	}
	if run.WorkspaceID != identity.WorkspaceID || run.UserID != identity.UserID {
		http.Error(w, "无权访问该任务", http.StatusForbidden)
		return
	}

	sess, _, err := sessionManager.GetOrCreate(r.Context(), run.SessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}

	var req userInputResponseRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		writeHandlerError(w, http.StatusBadRequest, "请求体无效", err)
		return
	}
	userMsg := agent.UserMessage{Content: req.Response}
	if err := userMsg.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if waitingRunID, ok := sess.GetWaitingRunID(); !ok || waitingRunID != runID {
		http.Error(w, "任务当前未等待用户输入", http.StatusConflict)
		return
	}
	requestCtx := detachedContext(r.Context())
	if sess.ConsumeWaitingRunExact(runID) == "" {
		http.Error(w, "任务输入已被消费", http.StatusConflict)
		return
	}
	if err := runBeforeUserRunHooks(r.Context(), sess, userMsg, prepareUserRunHook(handlerReportSnapshotLoader{})); err != nil {
		sess.ReturnRunToWaiting(runID)
		writeHandlerError(w, http.StatusBadRequest, "应用请求上下文失败", err)
		return
	}
	if err := resumeWaitingRun(requestCtx, sess, identity, runID, userMsg); err != nil {
		writeHandlerError(w, http.StatusBadRequest, "恢复任务失败", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "resumed",
		"run_id": runID,
	})
}

func resumeWaitingRun(requestCtx context.Context, sess *session.Session, identity auth.Identity, runID string, userMsg agent.UserMessage) error {
	if sess == nil || runID == "" {
		return fmt.Errorf("waiting run is not available")
	}
	if _, err := saveEventToDB(requestCtx, sess.WorkspaceID, sess.ID, runID, agent.RuntimeEvent{Type: "user", Data: userMsg}); err != nil {
		sess.ReturnRunToWaiting(runID)
		return fmt.Errorf("failed to persist user response: %w", err)
	}
	if err := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
		return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusRunning, nil)
	}); err != nil {
		sess.ReturnRunToWaiting(runID)
		return fmt.Errorf("failed to persist resumed run state: %w", err)
	}
	if err := sess.Engine.ProvideAskUserResult(userMsg.Content, identity.UserID); err != nil {
		sess.ReturnRunToWaiting(runID)
		rollbackErr := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
			return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusWaitingUserInput, nil)
		})
		return errors.Join(err, rollbackErr)
	}
	go func() {
		runEmitter := newRuntimeEventDispatcher(requestCtx, sess, identity, runID)
		resumeCtx := agent.WithTraceMetadata(requestCtx, agent.TraceMetadata{WorkspaceID: sess.WorkspaceID, SessionID: sess.ID, RunID: runID})
		resumeCtx = tools.WithExecutionMetadata(resumeCtx, tools.ExecutionMetadata{UserID: identity.UserID, WorkspaceID: sess.WorkspaceID, SessionID: sess.ID, RunID: runID})
		resumeCtx = agent.WithDelegateRunPersistence(resumeCtx, delegateRunPersistence{workspaceID: sess.WorkspaceID, sessionID: sess.ID, userID: identity.UserID, emit: runEmitter.Emit})
		ctxWithCancel, cancel := context.WithCancel(resumeCtx)
		sess.UpdateCancelFunc(runID, cancel)
		if err := dispatchRunExecution(runExecution{Context: ctxWithCancel, Session: sess, RuntimeVars: sess.RuntimeVars, Emit: runEmitter.Emit, OnDone: cancel}); err != nil {
			cancel()
			failRunStart(requestCtx, sess, runID, err)
		}
	}()
	return nil
}
