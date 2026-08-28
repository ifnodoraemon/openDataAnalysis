package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

func sessionExists(ctx context.Context, sessionID string) (bool, error) {
	if sessionRepo == nil {
		return false, fmt.Errorf("session repository is not configured")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return false, fmt.Errorf("session ID must be a non-empty exact value")
	}
	_, err := sessionRepo.GetByID(ctx, sessionID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func shouldHydrateSessionFromPersistence(ctx context.Context, workspaceID, userID, sessionID string) (bool, error) {
	if sessionManager == nil {
		return false, fmt.Errorf("session manager is not configured")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return false, fmt.Errorf("session ID must be a non-empty exact value")
	}
	if _, ok, err := sessionManager.Peek(sessionID, workspaceID, userID); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	return sessionExists(ctx, sessionID)
}

func hydrateSessionFromPersistence(ctx context.Context, sess *session.Session) error {
	if sess == nil {
		return fmt.Errorf("session is not initialized")
	}
	entries, subgoals, reportSnapshot, _, editState, err := loadSessionRuntimeStateFromPersistence(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("failed to read persisted runtime state: %w", err)
	}
	if err := sess.LoadRuntimeStateEntries(entries, subgoals); err != nil {
		return fmt.Errorf("failed to restore runtime state: %w", err)
	}
	if reportSnapshot != nil {
		if err := sess.LoadReportSnapshot(reportSnapshot); err != nil {
			return fmt.Errorf("failed to restore report snapshot: %w", err)
		}
	}
	if editState != nil && editState.Active && editState.EditContext != nil {
		sess.ConfigureEditState(editState.EditContext)
	} else {
		sess.ClearEditState()
	}
	return nil
}

func recoverStaleSessionRuns(ctx context.Context, sessionID string) error {
	if runRepo == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if sessionManager != nil && sessionManager.IsSessionLive(sessionID) {
		return nil
	}

	roots, err := runRepo.ListBySession(ctx, sessionID, -1)
	if err != nil {
		return err
	}
	for _, run := range roots {
		if err := recoverStaleRunTree(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func recoverStaleRunTree(ctx context.Context, run domain.AnalysisRun) error {
	if run.Status == domain.RunStatusRunning || run.Status == domain.RunStatusWaitingUserInput {
		errMsg := "服务重启后无法恢复该任务，已自动标记为失败"
		if run.ErrorMessage == nil || strings.TrimSpace(*run.ErrorMessage) != errMsg || run.Status != domain.RunStatusFailed {
			if err := runRepo.UpdateStatus(ctx, run.ID, domain.RunStatusFailed, &errMsg); err != nil {
				return err
			}
		}
	}

	children, err := runRepo.ListByParent(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := recoverStaleRunTree(ctx, child); err != nil {
			return err
		}
	}
	return nil
}
