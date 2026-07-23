package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

var allowedExtensions = map[string]bool{
	".csv":  true,
	".xlsx": true,
	".xls":  true,
}

var allowedMimeTypes = map[string]bool{
	"text/csv":                          true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"application/vnd.ms-excel":          true,
	"application/octet-stream":          true,
}

var magicBytes = map[string][]byte{
	".xlsx": {0x50, 0x4B, 0x03, 0x04},
	".xls":  {0xD0, 0xCF, 0x11, 0xE0},
}

func validateUploadedFile(filename, contentType string, head []byte) error {
	cleanName := filepath.Clean(filename)
	if strings.Contains(cleanName, "..") || filepath.IsAbs(cleanName) {
		return fmt.Errorf("invalid filename: path traversal detected")
	}
	if strings.ContainsAny(cleanName, `<>:"|?*`) {
		return fmt.Errorf("invalid filename: contains forbidden characters")
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedExtensions[ext] {
		return fmt.Errorf("file type %s is not allowed. Allowed: .csv, .xlsx, .xls", ext)
	}

	if contentType != "" && !allowedMimeTypes[contentType] {
		return fmt.Errorf("MIME type %s is not allowed", contentType)
	}

	if expected, ok := magicBytes[ext]; ok && len(head) >= len(expected) {
		for i, b := range expected {
			if head[i] != b {
				return fmt.Errorf("file content does not match extension %s", ext)
			}
		}
	}

	return nil
}

func UploadHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	sess, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "session does not exist") {
		return
	}
	if sess.UserID != identity.UserID || sess.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized to access this session", http.StatusForbidden)
		return
	}

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "failed to parse upload request", http.StatusBadRequest)
		return
	}

	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	head := make([]byte, 8)
	if _, err := file.Read(head); err != nil && err.Error() != "EOF" {
		http.Error(w, "failed to read file header", http.StatusBadRequest)
		return
	}
	if _, err := file.Seek(0, 0); err != nil {
		http.Error(w, "failed to seek file", http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"file_id":  uploaded.ID,
		"filename": uploaded.DisplayName,
		"purpose":  uploaded.Purpose,
		"size":     uploaded.SizeBytes,
		"message":  fmt.Sprintf("file %s uploaded successfully (%.2f MB)", uploaded.DisplayName, float64(uploaded.SizeBytes)/(1024*1024)),
	}

	source, dsErr := sourceService.EnsureFileSource(r.Context(), sess.WorkspaceID, uploaded.ID, uploaded.DisplayName, identity.UserID)
	if dsErr != nil {
		log.Printf("upload: ensure source failed file_id=%s err=%v", uploaded.ID, dsErr)
		resp["ingest_status"] = "failed"
		resp["message"] = fmt.Sprintf("file uploaded but data source creation failed: %v", dsErr)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	resp["source_id"] = source.ID

	runtimeSession, _, wsErr := sessionManager.GetOrCreate(r.Context(), sessionID, sess.WorkspaceID, identity.UserID)
	if wsErr != nil {
		log.Printf("upload: get session failed session_id=%s err=%v", sessionID, wsErr)
		resp["ingest_status"] = "failed"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	connector, connectorErr := sourceConnectors.Get(source.SourceType)
	if connectorErr != nil {
		log.Printf("upload: connector lookup failed source_id=%s err=%v", source.ID, connectorErr)
		resp["ingest_status"] = "failed"
		resp["message"] = fmt.Sprintf("file uploaded but import failed: %v", connectorErr)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	runtimeSession.LockUpload()
	ingestResult, ingestErr := connector.Import(r.Context(), service.SourceImportRequest{
		SourceID:    source.ID,
		WorkspaceID: sess.WorkspaceID,
		SessionID:   sess.ID,
		Ingester:    runtimeSession.Ingester,
	})
	runtimeSession.UnlockUpload()
	if ingestErr != nil {
		log.Printf("upload: materialize failed file_id=%s err=%v", uploaded.ID, ingestErr)
		resp["ingest_status"] = "failed"
		resp["message"] = fmt.Sprintf("file uploaded but import failed: %v", ingestErr)
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
		resp["data_size_tier"] = ingestResult.DataSizeTier
		resp["import_row_limit"] = ingestResult.ImportRowLimit
		resp["import_truncated"] = ingestResult.ImportTruncated
		resp["large_dataset"] = ingestResult.RowCount >= 1000000
		if ingestResult.ProfErr != nil {
			resp["ingest_status"] = "partial"
			resp["message"] = fmt.Sprintf("file imported but semantic profiling failed: %v", ingestResult.ProfErr)
		} else {
			resp["ingest_status"] = "success"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func sourceScopedFileTableName(displayName, sourceID string) string {
	return service.SourceScopedFileTableName(displayName, sourceID)
}

