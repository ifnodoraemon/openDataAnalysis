package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

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
