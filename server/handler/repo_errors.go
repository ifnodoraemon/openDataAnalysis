package handler

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

func requireWorkspaceMembership(w http.ResponseWriter, ctx context.Context, workspaceID, userID string) bool {
	ok, err := workspaceRepo.IsMember(ctx, workspaceID, userID)
	if err != nil {
		log.Printf("workspace membership lookup failed workspace_id=%s user_id=%s err=%v", workspaceID, userID, err)
		http.Error(w, "验证工作区成员身份失败", http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.Error(w, "无权访问该工作区", http.StatusForbidden)
		return false
	}
	return true
}

func isRepoNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, repository.ErrNotFound)
}

func writeRepoLookupError(w http.ResponseWriter, err error, notFoundMessage string) bool {
	if err == nil {
		return false
	}
	if isRepoNotFound(err) {
		http.Error(w, notFoundMessage, http.StatusNotFound)
		return true
	}
	log.Printf("internal repo error: %v", err)
	http.Error(w, "服务器内部错误", http.StatusInternalServerError)
	return true
}
