package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/session"
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

	runtimeSession.LockUpload()
	ingestResult, ingestErr := materializeAndProfile(r.Context(), runtimeSession, source, uploaded)
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
		resp["profile_mode"] = ingestResult.ProfileMode
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

type ingestResult struct {
	SnapshotID        string
	ProfileID         string
	TableName         string
	RowCount          int
	ColCount          int
	RowsImported      int
	RowsSkipped       int
	ImportDurationMs  int
	ProfileDurationMs int
	SnapshotSizeBytes int64
	ProfileMode       string
	ProfErr           error
}

func materializeAndProfile(ctx context.Context, sess *session.Session, source *domain.DataSource, file *domain.File) (*ingestResult, error) {
	tempPath, _, err := fileService.MaterializeToTemp(ctx, sess.ID, sess.WorkspaceID, file.ID)
	if err != nil {
		return nil, fmt.Errorf("file materialization failed: %w", err)
	}
	defer os.Remove(tempPath)

	importStart := time.Now()
	tableName, rowCount, colCount, err := sess.Ingester.ImportFileRawAs(tempPath, sourceScopedFileTableName(file.DisplayName, source.ID))
	importDuration := time.Since(importStart)
	if err != nil {
		return nil, fmt.Errorf("import failed: %w", err)
	}

	var profileMode domain.ProfileMode = domain.ProfileModeSampled
	if rowCount < 10000 {
		profileMode = domain.ProfileModeExact
	} else if rowCount < 100000 {
		profileMode = domain.ProfileModeMixed
	}

	var schema *data.SchemaInfo
	var schemaErr error
	if profileMode == domain.ProfileModeExact {
		schema, schemaErr = data.ExtractSchema(sess.Ingester.GetDB(), tableName)
	} else {
		schema, schemaErr = data.ExtractSchemaSampled(sess.Ingester.GetDB(), tableName)
	}
	if schemaErr != nil {
		return nil, fmt.Errorf("schema extraction failed: %w", schemaErr)
	}
	schemaSig := service.ComputeSchemaSignature(schema)

	profileStart := time.Now()

	snapshotSizeBytes := file.SizeBytes
	if dbPath := sess.Ingester.DBPath(); dbPath != "" {
		if fi, fiErr := os.Stat(dbPath); fiErr == nil {
			snapshotSizeBytes = fi.Size()
		}
	}

	facts := sourceService.BuildProfileFacts(schema, nil, nil, string(profileMode), snapshotSizeBytes)
	profileDuration := time.Since(profileStart)

	snapshot, err := sourceService.CreateSnapshot(
		ctx, sess.ID, source.ID,
		"file_upload", "", file.DisplayName,
		tableName, rowCount, colCount, schemaSig,
		rowCount, 0, int(importDuration.Milliseconds()), int(profileDuration.Milliseconds()), snapshotSizeBytes, profileMode,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	profile, profErr := sourceService.CreateSemanticProfile(
		ctx, sess.ID, sess.WorkspaceID, source.ID, snapshot.ID,
		tableName, schemaSig, facts,
	)
	if profErr != nil {
		log.Printf("upload: create semantic profile failed source_id=%s err=%v", source.ID, profErr)
		errMsg := profErr.Error()
		if updateErr := sourceService.RecordSnapshotError(ctx, snapshot.ID, errMsg); updateErr != nil {
			log.Printf("upload: failed to write profile error to snapshot snapshot_id=%s err=%v", snapshot.ID, updateErr)
		}
	}

	profileID := ""
	if profile != nil {
		profileID = profile.ID
	}

	return &ingestResult{
		SnapshotID:        snapshot.ID,
		ProfileID:         profileID,
		TableName:         tableName,
		RowCount:          rowCount,
		ColCount:          colCount,
		RowsImported:      rowCount,
		RowsSkipped:       0,
		ImportDurationMs:  int(importDuration.Milliseconds()),
		ProfileDurationMs: int(profileDuration.Milliseconds()),
		SnapshotSizeBytes: snapshotSizeBytes,
		ProfileMode:       string(profileMode),
		ProfErr:           profErr,
	}, nil
}

func sourceScopedFileTableName(displayName, sourceID string) string {
	ext := filepath.Ext(displayName)
	base := strings.TrimSuffix(filepath.Base(displayName), ext)
	if strings.TrimSpace(base) == "" {
		base = "table"
	}
	suffix := sourceTableSuffix(sourceID)
	if suffix == "" {
		return base
	}
	return base + "__" + suffix
}

func sourceTableSuffix(sourceID string) string {
	cleaned := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.TrimSpace(sourceID))
	if len(cleaned) > 8 {
		return cleaned[len(cleaned)-8:]
	}
	return cleaned
}
