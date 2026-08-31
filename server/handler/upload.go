package handler

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

var allowedExtensions = map[string]bool{
	".csv":  true,
	".xlsx": true,
}

var allowedMimeTypes = map[string]bool{
	"text/csv": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"application/octet-stream": true,
}

var magicBytes = map[string][]byte{
	".xlsx": {0x50, 0x4B, 0x03, 0x04},
}

func validateUploadedFile(filename, contentType string, head []byte) error {
	cleanName := filepath.Clean(filename)
	if strings.Contains(cleanName, "..") || filepath.IsAbs(cleanName) {
		return fmt.Errorf("文件名无效：检测到路径穿越")
	}
	if strings.ContainsAny(cleanName, `<>:"|?*`) {
		return fmt.Errorf("文件名无效：包含禁止字符")
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedExtensions[ext] {
		return fmt.Errorf("不允许使用 %s 文件类型，仅支持 .csv 和 .xlsx", ext)
	}

	if contentType != "" && !allowedMimeTypes[contentType] {
		return fmt.Errorf("不允许使用 MIME 类型 %s", contentType)
	}

	if expected, ok := magicBytes[ext]; ok && len(head) >= len(expected) {
		for i, b := range expected {
			if head[i] != b {
				return fmt.Errorf("文件内容与扩展名 %s 不匹配", ext)
			}
		}
	}

	return nil
}

func UploadHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "缺少 session_id", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(sessionID) != sessionID {
		http.Error(w, "session_id 必须保持原值", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	sess, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if sess.UserID != identity.UserID || sess.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此会话", http.StatusForbidden)
		return
	}

	// maxMemory is the in-RAM threshold for multipart parsing; anything above
	// spills to temp files. The 100MB total body cap is enforced separately by
	// MaxBodySizeMiddleware, so a small threshold avoids OOM under concurrent
	// uploads without reducing the allowed upload size.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "解析上传请求失败", http.StatusBadRequest)
		return
	}

	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "获取上传文件失败", http.StatusBadRequest)
		return
	}
	defer file.Close()

	head := make([]byte, 8)
	if _, err := file.Read(head); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "读取文件头失败", http.StatusBadRequest)
		return
	}
	if _, err := file.Seek(0, 0); err != nil {
		http.Error(w, "重置文件读取位置失败", http.StatusInternalServerError)
		return
	}

	if err := validateUploadedFile(
		fileHeader.Filename,
		fileHeader.Header.Get("Content-Type"),
		head,
	); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	uploaded, err := fileService.Upload(r.Context(), service.UploadFileInput{
		UserID:      identity.UserID,
		WorkspaceID: sess.WorkspaceID,
		SessionID:   sess.ID,
		FileName:    fileHeader.Filename,
		ContentType: fileHeader.Header.Get("Content-Type"),
		Size:        fileHeader.Size,
		Body:        file,
	})
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "上传文件失败", err)
		return
	}

	resp := map[string]interface{}{
		"file_id":  uploaded.ID,
		"filename": uploaded.DisplayName,
		"purpose":  uploaded.Purpose,
		"size":     uploaded.SizeBytes,
		"message":  fmt.Sprintf("文件 %s 上传成功（%.2f MB）", uploaded.DisplayName, float64(uploaded.SizeBytes)/(1024*1024)),
	}

	source, dsErr := sourceService.EnsureFileSource(r.Context(), sess.WorkspaceID, uploaded.ID, uploaded.DisplayName, identity.UserID)
	if dsErr != nil {
		log.Printf("upload: ensure source failed file_id=%s err=%v", uploaded.ID, dsErr)
		resp["ingest_status"] = "failed"
		resp["message"] = "文件已上传，但创建数据源失败"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["source_id"] = source.ID

	runtimeSession, _, wsErr := sessionManager.GetOrCreate(r.Context(), sessionID, sess.WorkspaceID, identity.UserID)
	if wsErr != nil {
		log.Printf("upload: get session failed session_id=%s err=%v", sessionID, wsErr)
		resp["ingest_status"] = "failed"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	connector, connectorErr := sourceConnectors.Get(source.SourceType)
	if connectorErr != nil {
		log.Printf("upload: connector lookup failed source_id=%s err=%v", source.ID, connectorErr)
		resp["ingest_status"] = "failed"
		resp["message"] = "文件已上传，但导入失败"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	runtimeSession.LockUpload()
	importer, importerOK := connector.(service.ImportingConnector)
	var ingestResult *service.SnapshotImportResult
	var ingestErr error
	if !importerOK {
		ingestErr = fmt.Errorf("source type %s does not support importing", source.SourceType)
	} else {
		ingestResult, ingestErr = importer.Import(r.Context(), service.SourceImportRequest{
			SourceID:    source.ID,
			WorkspaceID: sess.WorkspaceID,
			SessionID:   sess.ID,
			Ingester:    runtimeSession.Ingester,
		})
	}
	runtimeSession.UnlockUpload()
	if ingestErr != nil {
		log.Printf("upload: materialize failed file_id=%s err=%v", uploaded.ID, ingestErr)
		resp["ingest_status"] = "failed"
		resp["message"] = "文件已上传，但导入失败"
	} else {
		resp["snapshot_id"] = ingestResult.SnapshotID
		resp["semantic_profile_id"] = ingestResult.ProfileID
		resp["analysis_table_name"] = ingestResult.TableName
		resp["row_count"] = ingestResult.RowCount
		resp["column_count"] = ingestResult.ColCount
		resp["rows_imported"] = ingestResult.RowsImported
		resp["rows_skipped"] = ingestResult.RowsSkipped
		resp["import_duration_ms"] = ingestResult.ImportDurationMs
		resp["profile_duration_ms"] = ingestResult.ProfileDurationMs
		resp["snapshot_size_bytes"] = ingestResult.SnapshotSizeBytes
		resp["profile_mode"] = string(ingestResult.ProfileMode)
		resp["import_row_limit"] = ingestResult.ImportRowLimit
		resp["import_truncated"] = ingestResult.ImportTruncated
		resp["ingest_status"] = "success"
		if len(ingestResult.CleanupErrors) > 0 {
			resp["cleanup_errors"] = ingestResult.CleanupErrors
		}
		auditPayload, err := serviceAuditPayload(map[string]interface{}{
			"source_id":           source.ID,
			"source_type":         string(source.SourceType),
			"analysis_table_name": ingestResult.TableName,
			"row_count":           ingestResult.RowCount,
			"column_count":        ingestResult.ColCount,
			"rows_imported":       ingestResult.RowsImported,
			"rows_skipped":        ingestResult.RowsSkipped,
			"profile_id":          ingestResult.ProfileID,
			"cleanup_errors":      ingestResult.CleanupErrors,
		})
		if err != nil {
			http.Error(w, "序列化导入审计事实失败", http.StatusInternalServerError)
			return
		}
		if auditErr := sourceService.RecordAuditEvent(r.Context(), domain.AuditEvent{
			WorkspaceID:  sess.WorkspaceID,
			SessionID:    sess.ID,
			ActorUserID:  identity.UserID,
			EventType:    "data_source_imported",
			ResourceType: "source_snapshot",
			ResourceID:   ingestResult.SnapshotID,
			PayloadJSON:  auditPayload,
			CreatedAt:    time.Now(),
		}); auditErr != nil {
			resp["audit_errors"] = []string{auditErr.Error()}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
