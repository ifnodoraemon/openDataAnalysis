package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type chatRequest struct {
	Content     string                  `json:"content"`
	TurnContext *agent.TurnContext       `json:"turnContext,omitempty"`
	EditContext *agent.ReportEditContext `json:"editContext,omitempty"`
}

func ChatHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "sessionID required", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		http.Error(w, "content cannot be empty", http.StatusBadRequest)
		return
	}

	sess, _, err := sessionManager.GetOrCreate(r.Context(), sessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		http.Error(w, "failed to get session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	userMsg := agent.UserMessage{
		Content:     req.Content,
		TurnContext: req.TurnContext,
		EditContext: req.EditContext,
	}

	preparedUserMsg, extraRuntime, prepErr := resolvePreparedUserMessage(r.Context(), sess, userMsg)
	if prepErr != nil {
		preparedUserMsg = userMsg
		extraRuntime = nil
	}

	requestCtx := detachedContext(r.Context())
	if err := runBeforeUserRunHooks(r.Context(), sess, preparedUserMsg, prepareUserRunHook(handlerReportSnapshotLoader{})); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Handle resuming run if waiting for user input
	if activeRunID := sess.ConsumeWaitingRun(); activeRunID != "" {
		go func() {
			_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
				return runRepo.UpdateStatus(persistCtx, activeRunID, domain.RunStatusRunning, nil)
			})

			runEmitter := newRuntimeEventDispatcher(requestCtx, nil, nil, sess, identity, activeRunID)

			resumeCtx := agent.WithTraceMetadata(requestCtx, agent.TraceMetadata{
				WorkspaceID: sess.WorkspaceID,
				SessionID:   sess.ID,
				RunID:       activeRunID,
			})
			resumeCtx = tools.WithExecutionMetadata(resumeCtx, tools.ExecutionMetadata{
				WorkspaceID: sess.WorkspaceID,
				SessionID:   sess.ID,
				RunID:       activeRunID,
			})
			resumeCtx = agent.WithDelegateRunPersistence(resumeCtx, delegateRunPersistence{
				workspaceID: sess.WorkspaceID,
				sessionID:   sess.ID,
				userID:      identity.UserID,
				emit:        runEmitter.Emit,
			})

			ctxWithCancel, cancel := context.WithCancel(resumeCtx)
			sess.UpdateCancelFunc(activeRunID, cancel)

			if err := dispatchRunExecution(runExecution{
				Context:     ctxWithCancel,
				Session:     sess,
				RuntimeVars: sess.RuntimeVars,
				Emit:        runEmitter.Emit,
				OnDone:      cancel,
			}); err != nil {
				cancel()
				failRunStart(requestCtx, nil, nil, sess, activeRunID, err)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "resumed",
			"run_id": activeRunID,
		})
		return
	}

	runID, ctx, err := sess.StartRun(requestCtx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now()
	rawInput := preparedUserMsg.Content
	log.Printf("chat run started run_id=%s session_id=%s workspace_id=%s user_id=%s input_chars=%d", runID, sess.ID, sess.WorkspaceID, identity.UserID, len([]rune(rawInput)))

	_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
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
	})

	saveEventToDB(requestCtx, sess.WorkspaceID, sess.ID, runID, agent.WSEvent{
		Type: "user",
		Data: userMsg,
	})

	_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
		return sessionRepo.UpdateLastRun(persistCtx, sess.ID, runID)
	})

	if record, err := sessionRepo.GetByID(requestCtx, sess.ID); err == nil && (record.Title == "" || record.Title == "Untitled Analysis") {
		_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
			return sessionRepo.UpdateTitle(persistCtx, sess.ID, deriveSessionTitle(rawInput))
		})
	}

	// Dispatch run execution in background goroutine
	go func() {
		runEmitter := newRuntimeEventDispatcher(requestCtx, nil, nil, sess, identity, runID)

		execCtx := agent.WithTraceMetadata(ctx, agent.TraceMetadata{
			WorkspaceID: sess.WorkspaceID,
			SessionID:   sess.ID,
			RunID:       runID,
		})
		execCtx = tools.WithExecutionMetadata(execCtx, tools.ExecutionMetadata{
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
			failRunStart(requestCtx, nil, nil, sess, runID, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "started",
		"run_id": runID,
	})
}

func CancelRunHandler(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if strings.TrimSpace(runID) == "" {
		http.Error(w, "runID required", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	run, err := getRunWithPersistence(r.Context(), runID)
	if err != nil || run.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "run not found or unauthorized", http.StatusNotFound)
		return
	}

	sess, _, err := sessionManager.GetOrCreate(r.Context(), run.SessionID, identity.WorkspaceID, identity.UserID)
	if err == nil {
		sess.CancelRun(runID)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
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
		http.Error(w, "runID required", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	run, err := getRunWithPersistence(r.Context(), runID)
	if err != nil || run.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "run not found or unauthorized", http.StatusNotFound)
		return
	}

	sess, _, err := sessionManager.GetOrCreate(r.Context(), run.SessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	_ = sess

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"run_id": runID,
	})
}
