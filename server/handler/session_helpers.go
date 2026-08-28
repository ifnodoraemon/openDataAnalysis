package handler

import (
	"context"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

func ensureSession(ctx context.Context, identity auth.Identity) (*domain.Session, error) {
	sess, _, err := sessionManager.GetOrCreate(ctx, "", identity.WorkspaceID, identity.UserID)
	if err != nil {
		return nil, err
	}
	record, err := sessionRepo.GetByID(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func serializeSession(session domain.Session) map[string]interface{} {
	item := map[string]interface{}{
		"id":         session.ID,
		"title":      session.Title,
		"lastSeenAt": session.LastSeenAt,
		"createdAt":  session.CreatedAt,
	}
	if session.LastRunID != nil {
		item["lastRunId"] = *session.LastRunID
	}
	return item
}
