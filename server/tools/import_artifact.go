package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

// ImportArtifactTool imports a CSV artifact (normally produced by
// code_run_python) into the session analysis database through the strict
// snapshot pipeline. Structural interpretation of messy source files stays
// with the agent: it reads the original upload in the sandbox, cleans it in
// code, and only the resulting rectangular CSV enters the database here.
type ImportArtifactTool struct {
	SourceService *service.SourceService
	FileService   *service.FileService
	Ingester      *data.Ingester
	ReportState   *ReportState
	UploadLocker  UploadLocker
	SessionID     string
	WorkspaceID   string
	parentCtx     context.Context
}

func init() {
	RegisterGlobalTool(func(ctx ToolContext) Tool {
		if ctx.SourceService == nil || ctx.FileService == nil || ctx.Ingester == nil || ctx.SessionID == "" {
			return nil
		}
		return &ImportArtifactTool{
			SourceService: ctx.SourceService,
			FileService:   ctx.FileService,
			Ingester:      ctx.Ingester,
			ReportState:   ctx.ReportState,
			UploadLocker:  ctx.UploadLocker,
			SessionID:     ctx.SessionID,
			WorkspaceID:   ctx.WorkspaceID,
		}
	})
}

func (t *ImportArtifactTool) SetExecutionContext(ctx context.Context) { t.parentCtx = ctx }

func (t *ImportArtifactTool) Name() string { return "data_import_artifact" }
func (t *ImportArtifactTool) Capability() ToolCapability {
	return ToolCapability{Mode: "action", RuntimeEnabled: true, Delegable: false}
}
func (t *ImportArtifactTool) Description() string {
	return "Import a CSV file (an artifact id from code_run_python, or any workspace CSV file id) into the session analysis database as a queryable table, through the same snapshot/profile/binding pipeline as uploads. The CSV must be structurally clean: first row is the exact header, every data row has at most that many cells, no title rows. The importer is deterministic — a dirty CSV fails with the offending row and cell count, so clean it in code first. The optional name labels the import in session source facts. Returns the analysis table name and shape facts."
}
func (t *ImportArtifactTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"artifact_id":{"type":"string","description":"Artifact id returned by code_run_python (or a workspace CSV file id)."},"name":{"type":"string","description":"Optional exact label for this import, shown in session source facts."}},"required":["artifact_id"]}`)
}

func (t *ImportArtifactTool) Execute(args json.RawMessage) (string, error) {
	var params struct {
		ArtifactID string `json:"artifact_id"`
		Name       string `json:"name"`
	}
	if err := decodeToolArgs(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse parameters: %w", err)
	}
	if params.ArtifactID == "" || params.ArtifactID != strings.TrimSpace(params.ArtifactID) {
		return toolFailure("data_import_artifact", "invalid_artifact_id", "artifact_id must be a non-empty exact value", nil), nil
	}
	if params.Name != strings.TrimSpace(params.Name) {
		return toolFailure("data_import_artifact", "invalid_name", "name must be an exact value without leading or trailing whitespace", nil), nil
	}
	execCtx := t.parentCtx
	if execCtx == nil {
		return toolFailure("data_import_artifact", "missing_execution_context", "tool execution context is not initialized", nil), nil
	}
	meta := ExecutionMetadataFromContext(execCtx)
	if meta.UserID == "" || meta.WorkspaceID == "" || meta.SessionID == "" {
		return toolFailure("data_import_artifact", "missing_execution_identity", "authenticated user, workspace, and session identities are required", nil), nil
	}
	if t.UploadLocker != nil {
		t.UploadLocker.LockUpload()
		defer t.UploadLocker.UnlockUpload()
	}

	reader, file, err := t.FileService.OpenForDownload(execCtx, meta.UserID, meta.WorkspaceID, params.ArtifactID)
	if err != nil {
		return toolFailure("data_import_artifact", "artifact_not_found", "artifact could not be opened: "+err.Error(), map[string]interface{}{"artifact_id": params.ArtifactID}), nil
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, 50*1024*1024+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return toolFailure("data_import_artifact", "artifact_read_failed", "artifact could not be read", nil), nil
	}
	if len(content) > 50*1024*1024 {
		return toolFailure("data_import_artifact", "artifact_too_large", "artifact exceeds the 50 MiB limit", nil), nil
	}
	if !strings.EqualFold(filepath.Ext(file.DisplayName), ".csv") {
		return toolFailure("data_import_artifact", "artifact_not_csv", fmt.Sprintf("artifact %q is not a CSV file; write cleaned data with pandas to_csv (or plain CSV) and import that output", file.DisplayName), map[string]interface{}{"artifact_id": params.ArtifactID, "filename": file.DisplayName}), nil
	}

	objectName := params.Name
	if objectName == "" {
		objectName = strings.TrimSuffix(file.DisplayName, filepath.Ext(file.DisplayName))
	}

	ds, err := t.SourceService.EnsureFileSource(execCtx, meta.WorkspaceID, file.ID, file.DisplayName, meta.UserID)
	if err != nil {
		return toolFailure("data_import_artifact", "source_ensure_failed", "data source could not be created: "+err.Error(), nil), nil
	}
	preSnapshot, err := t.SourceService.BeginSnapshotImport(execCtx, meta.SessionID, ds.ID, string(domain.SourceTypeFileUpload), "", objectName)
	if err != nil {
		return toolFailure("data_import_artifact", "snapshot_begin_failed", "import could not be started: "+err.Error(), nil), nil
	}

	tmp, err := os.CreateTemp("", "oda-agent-import-*.csv")
	if err != nil {
		t.failSnapshot(execCtx, preSnapshot.ID, "temporary file could not be created")
		return toolFailure("data_import_artifact", "temp_file_failed", "temporary file could not be created", nil), nil
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		t.failSnapshot(execCtx, preSnapshot.ID, "temporary file could not be written")
		return toolFailure("data_import_artifact", "temp_file_failed", "temporary file could not be written", nil), nil
	}
	tmp.Close()
	defer os.Remove(tmpPath)

	importStart := time.Now()
	tableName, rowCount, colCount, importErr := t.Ingester.ImportFileRawAs(tmpPath, preSnapshot.AnalysisTableName, "")
	importDuration := time.Since(importStart)
	if importErr != nil {
		t.failSnapshot(execCtx, preSnapshot.ID, importErr.Error())
		return toolFailure("data_import_artifact", "csv_import_failed", "clean CSV import failed: "+importErr.Error(), map[string]interface{}{
			"snapshot_id": preSnapshot.ID, "expected_shape": "first row header, rectangular data, no title rows",
		}), nil
	}

	result, err := t.SourceService.FinalizeSnapshotImport(execCtx, service.SnapshotImportCompletion{
		SnapshotID:        preSnapshot.ID,
		SessionID:         meta.SessionID,
		SourceID:          ds.ID,
		UpstreamKind:      string(domain.SourceTypeFileUpload),
		UpstreamSchema:    "",
		UpstreamObject:    objectName,
		AnalysisTableName: tableName,
		RowCount:          rowCount,
		ColCount:          colCount,
		RowsImported:      rowCount,
		RowsSkipped:       0,
		ImportDuration:    importDuration,
		SnapshotSizeBytes: file.SizeBytes,
		Ingester:          t.Ingester,
	})
	if err != nil {
		return toolFailure("data_import_artifact", "snapshot_finalize_failed", "import could not be completed: "+err.Error(), nil), nil
	}

	payload := map[string]interface{}{
		"analysis_table_name": result.TableName,
		"row_count":           result.RowCount,
		"column_count":        result.ColCount,
		"snapshot_id":         result.SnapshotID,
		"source_id":           ds.ID,
		"artifact_id":         params.ArtifactID,
		"name":                objectName,
		"ui_summary":          fmt.Sprintf("已导入清洗数据「%s」：%d 行 × %d 列。", objectName, result.RowCount, result.ColCount),
	}
	if len(result.CleanupErrors) > 0 {
		payload["cleanup_warnings"] = result.CleanupErrors
	}
	return toolSuccess("data_import_artifact", payload), nil
}

func (t *ImportArtifactTool) failSnapshot(ctx context.Context, snapshotID, message string) {
	if t.SourceService == nil {
		return
	}
	_ = t.SourceService.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusFailed, &message)
}
