package handler

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
)

// DownloadFileHandler streams a durable source, report, or analysis artifact
// after resolving access through the authenticated workspace identity.
func DownloadFileHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || strings.TrimSpace(identity.UserID) == "" || strings.TrimSpace(identity.WorkspaceID) == "" {
		http.Error(w, "需要登录", http.StatusUnauthorized)
		return
	}
	fileID := chi.URLParam(r, "fileID")
	if strings.TrimSpace(fileID) == "" {
		http.Error(w, "缺少文件 ID", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(fileID) != fileID {
		http.Error(w, "文件 ID 必须保持原值", http.StatusBadRequest)
		return
	}
	if fileService == nil {
		http.Error(w, "文件服务不可用", http.StatusServiceUnavailable)
		return
	}
	reader, file, err := fileService.OpenForDownload(r.Context(), identity.UserID, identity.WorkspaceID, fileID)
	if err != nil {
		http.Error(w, "文件不存在或无权访问", http.StatusNotFound)
		return
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Printf("download file close file_id=%s: %v", fileID, err)
		}
	}()

	w.Header().Set("Content-Type", defaultContentType(file.ContentType))
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeHeaderFilename(file.DisplayName)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("download file stream file_id=%s: %v", fileID, err)
	}
}
