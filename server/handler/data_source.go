package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

func SessionSourcesHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "session does not exist") {
		return
	}
	if sess.UserID != identity.UserID || sess.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized to access this session", http.StatusForbidden)
		return
	}

	sources, err := sourceService.GetSessionSources(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to get data sources", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"sources": sources,
	})
}

func DeleteSessionSourceHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	sourceID := chi.URLParam(r, "sourceID")
	sourceObjectKey := strings.TrimSpace(r.URL.Query().Get("source_object_key"))
	if sourceObjectKey == "" {
		http.Error(w, "source_object_key is required", http.StatusBadRequest)
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

	source, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "data source does not exist") {
		return
	}
	if source.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized to access this data source", http.StatusForbidden)
		return
	}

	tableNames, err := sourceService.RemoveSessionSource(r.Context(), sessionID, sourceID, sourceObjectKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to remove source: %v", err), http.StatusInternalServerError)
		return
	}
	if len(tableNames) > 0 {
		if runtimeSession, _, runtimeErr := sessionManager.GetOrCreate(r.Context(), sessionID, sess.WorkspaceID, identity.UserID); runtimeErr == nil {
			for _, tableName := range tableNames {
				if strings.TrimSpace(tableName) == "" {
					continue
				}
				if dropErr := runtimeSession.Ingester.DropTable(tableName); dropErr != nil {
					log.Printf("DeleteSessionSourceHandler: drop runtime table failed table=%s err=%v", tableName, dropErr)
				}
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func SemanticProfileDetailHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	profileID := chi.URLParam(r, "profileID")

	profile, confirmations, err := sourceService.GetProfileDetail(r.Context(), profileID)
	if err != nil {
		http.Error(w, "failed to get profile", http.StatusNotFound)
		return
	}

	ds, err := dataSourceRepo.GetByID(r.Context(), profile.SourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized", http.StatusForbidden)
		return
	}

	confJSON, _ := json.Marshal(confirmations)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"profile_id":          profile.ID,
		"session_id":          profile.SessionID,
		"source_id":           profile.SourceID,
		"snapshot_id":         profile.SnapshotID,
		"analysis_table_name": profile.AnalysisTableName,
		"schema_signature":    profile.SchemaSignature,
		"profile_status":      string(profile.ProfileStatus),
		"profile_json":        json.RawMessage(profile.ProfileJSON),
		"confirmations":       json.RawMessage(string(confJSON)),
		"created_at":          profile.CreatedAt,
		"updated_at":          profile.UpdatedAt,
	})
}

type ConfirmProfileRequest struct {
	SessionID string                 `json:"session_id"`
	Scope     string                 `json:"scope"`
	Overrides map[string]interface{} `json:"overrides"`
}

func ConfirmProfileHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	profileID := chi.URLParam(r, "profileID")

	profile, _, err := sourceService.GetProfileDetail(r.Context(), profileID)
	if err != nil {
		http.Error(w, "failed to get profile", http.StatusNotFound)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), profile.SourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "not authorized", http.StatusForbidden)
		return
	}

	var req ConfirmProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Scope != "session" && req.Scope != "workspace" {
		http.Error(w, "scope must be 'session' or 'workspace'", http.StatusBadRequest)
		return
	}
	if req.Scope == "session" && req.SessionID == "" {
		http.Error(w, "session_id is required when scope is 'session'", http.StatusBadRequest)
		return
	}
	if req.SessionID != "" {
		sess, sessErr := sessionRepo.GetByID(r.Context(), req.SessionID)
		if sessErr != nil || sess.WorkspaceID != identity.WorkspaceID {
			http.Error(w, "session not found or not authorized", http.StatusForbidden)
			return
		}
	}

	overridesJSON, _ := json.Marshal(req.Overrides)

	updated, err := sourceService.ConfirmProfile(
		r.Context(), profileID,
		identity.WorkspaceID, req.SessionID,
		identity.UserID, req.Scope, string(overridesJSON),
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("confirmation failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"profile_id":     updated.ID,
		"profile_status": string(updated.ProfileStatus),
	})
}

func ListDataSourcesHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	sources, err := dataSourceRepo.ListByWorkspace(r.Context(), identity.WorkspaceID)
	if err != nil {
		http.Error(w, "failed to get data source list", http.StatusInternalServerError)
		return
	}

	var result []map[string]interface{}
	for _, ds := range sources {
		item := serializeWorkspaceDataSource(r.Context(), ds)
		result = append(result, item)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data_sources": result,
	})
}

func ListDataSourceTypesHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if sourceConnectors == nil {
		http.Error(w, "source connector registry is not initialized", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"source_types": sourceConnectors.Specs(),
	})
}

func serializeWorkspaceDataSource(ctx context.Context, ds domain.DataSource) map[string]interface{} {
	item := map[string]interface{}{
		"id":          ds.ID,
		"name":        ds.Name,
		"source_type": string(ds.SourceType),
		"status":      string(ds.Status),
		"created_at":  ds.CreatedAt,
		"updated_at":  ds.UpdatedAt,
	}
	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		return item
	}
	publicConfig, err := connector.PublicConfig(ctx, ds.ID)
	if err != nil {
		return item
	}
	item["config"] = publicConfig
	return item
}

type CreateDataSourceRequest struct {
	Name       string          `json:"name"`
	SourceType string          `json:"source_type"`
	Config     json.RawMessage `json:"config"`
	Credential json.RawMessage `json:"credential"`
}

func CreateDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	var req CreateDataSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name cannot be empty", http.StatusBadRequest)
		return
	}
	sourceType := domain.SourceType(strings.TrimSpace(req.SourceType))
	if sourceType == domain.SourceTypeFileUpload {
		http.Error(w, "file_upload sources are created by file upload", http.StatusBadRequest)
		return
	}
	connector, err := sourceConnectors.Get(sourceType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sourceConfig, validationErr := connector.NormalizeConfig(r.Context(), service.SourceConfigRequest{
		RawConfig:         req.Config,
		RawCredential:     req.Credential,
		RequireCredential: true,
		AuthSecret:        config.Cfg.AuthSecret,
	})
	if validationErr != nil {
		http.Error(w, validationErr.Error(), http.StatusBadRequest)
		return
	}

	ds, err := sourceService.CreateConfiguredSource(r.Context(), identity.WorkspaceID, name, identity.UserID, sourceType, sourceConfig)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create data source: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":          ds.ID,
		"name":        ds.Name,
		"source_type": string(ds.SourceType),
		"status":      string(ds.Status),
	})
}

type UpdateDataSourceRequest struct {
	Name       *string         `json:"name"`
	Config     json.RawMessage `json:"config"`
	Credential json.RawMessage `json:"credential"`
}

func UpdateDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "data source does not exist", http.StatusNotFound)
		return
	}
	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req UpdateDataSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			http.Error(w, "name cannot be empty", http.StatusBadRequest)
			return
		}
		ds.Name = name
	}

	configProvided := len(req.Config) > 0 && strings.TrimSpace(string(req.Config)) != "" && strings.TrimSpace(string(req.Config)) != "null"
	credentialProvided := len(req.Credential) > 0 && strings.TrimSpace(string(req.Credential)) != "" && strings.TrimSpace(string(req.Credential)) != "null"
	if configProvided || credentialProvided {
		existing, err := sourceConfigRepo.GetBySourceID(r.Context(), sourceID)
		if err != nil {
			http.Error(w, "source config does not exist", http.StatusNotFound)
			return
		}
		normalizedConfig, validationErr := connector.NormalizeConfig(r.Context(), service.SourceConfigRequest{
			SourceID:          sourceID,
			RawConfig:         req.Config,
			RawCredential:     req.Credential,
			Existing:          existing,
			RequireCredential: false,
			AuthSecret:        config.Cfg.AuthSecret,
		})
		if validationErr != nil {
			http.Error(w, validationErr.Error(), http.StatusBadRequest)
			return
		}
		normalizedConfig.SourceID = sourceID
		normalizedConfig.ConnectorType = ds.SourceType
		normalizedConfig.CreatedAt = existing.CreatedAt
		normalizedConfig.LastTestedAt = nil
		normalizedConfig.LastTestStatus = ""
		normalizedConfig.LastErrorMessage = nil
		normalizedConfig.UpdatedAt = time.Now()
		if err := sourceConfigRepo.Update(r.Context(), normalizedConfig); err != nil {
			http.Error(w, fmt.Sprintf("failed to update source config: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if err := dataSourceRepo.Update(r.Context(), ds); err != nil {
		http.Error(w, fmt.Sprintf("failed to update data source: %v", err), http.StatusInternalServerError)
		return
	}

	updated, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil {
		http.Error(w, "failed to reload data source", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(serializeWorkspaceDataSource(r.Context(), *updated))
}

func DeleteDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "data source does not exist", http.StatusNotFound)
		return
	}
	if ds.SourceType == domain.SourceTypeFileUpload {
		http.Error(w, "file upload sources are removed through session source deletion", http.StatusBadRequest)
		return
	}

	tables, err := sourceService.DeleteWorkspaceSource(r.Context(), sourceID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to delete data source: %v", err), http.StatusInternalServerError)
		return
	}
	for _, table := range tables {
		if table.SessionID == "" || table.TableName == "" {
			continue
		}
		if runtimeSession, _, runtimeErr := sessionManager.GetOrCreate(r.Context(), table.SessionID, identity.WorkspaceID, identity.UserID); runtimeErr == nil {
			if dropErr := runtimeSession.Ingester.DropTable(table.TableName); dropErr != nil {
				log.Printf("DeleteDataSourceHandler: drop runtime table failed table=%s err=%v", table.TableName, dropErr)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func TestDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "data source does not exist", http.StatusNotFound)
		return
	}

	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := connector.Test(r.Context(), service.SourceTestRequest{
		SourceID:   sourceID,
		AuthSecret: config.Cfg.AuthSecret,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("connection test failed: %v", err), http.StatusInternalServerError)
		return
	}

	if ds.SourceType != domain.SourceTypeFileUpload {
		now := time.Now()
		success, _ := result["success"].(bool)
		status := "failed"
		var errMsg *string
		if success {
			status = "success"
		} else {
			if msg, ok := result["message"].(string); ok {
				errMsg = &msg
			}
		}
		if err := sourceConfigRepo.UpdateTestResult(r.Context(), sourceID, &now, status, errMsg); err != nil {
			log.Printf("TestDataSourceHandler: failed to persist test result source_id=%s err=%v", sourceID, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func CatalogDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "data source does not exist", http.StatusNotFound)
		return
	}

	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	objects, err := connector.Catalog(r.Context(), sourceID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get catalog: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"objects": objects,
	})
}

type ImportRequest struct {
	SessionID  string `json:"session_id"`
	SchemaName string `json:"schema_name"`
	ObjectName string `json:"object_name"`
}

func ImportDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil || ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "data source does not exist", http.StatusNotFound)
		return
	}

	var req ImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}
	if ds.SourceType != domain.SourceTypeFileUpload && strings.TrimSpace(req.ObjectName) == "" {
		http.Error(w, "missing object_name", http.StatusBadRequest)
		return
	}

	sess, _, sessErr := sessionManager.GetOrCreate(r.Context(), req.SessionID, identity.WorkspaceID, identity.UserID)
	if sessErr != nil {
		http.Error(w, "failed to get session", http.StatusInternalServerError)
		return
	}
	if sess.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "session does not belong to this workspace", http.StatusForbidden)
		return
	}

	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess.LockUpload()
	result, err := connector.Import(r.Context(), service.SourceImportRequest{
		SourceID:       sourceID,
		WorkspaceID:    identity.WorkspaceID,
		SessionID:      req.SessionID,
		Object:         service.SourceObjectRef{Schema: req.SchemaName, Name: req.ObjectName},
		Ingester:       sess.Ingester,
		AuthSecret:     config.Cfg.AuthSecret,
		ImportRowLimit: config.Cfg.SQLImportRowLimit,
	})
	sess.UnlockUpload()
	if err != nil {
		http.Error(w, fmt.Sprintf("import failed: %v", err), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"source_id":           sourceID,
		"snapshot_id":         result.SnapshotID,
		"semantic_profile_id": result.ProfileID,
		"analysis_table_name": result.TableName,
		"row_count":           result.RowCount,
		"column_count":        result.ColCount,
		"rows_imported":       result.RowsImported,
		"rows_skipped":        result.RowsSkipped,
		"import_row_limit":    result.ImportRowLimit,
		"import_truncated":    result.ImportTruncated,
		"import_duration_ms":  result.ImportDurationMs,
		"profile_duration_ms": result.ProfileDurationMs,
		"snapshot_size_bytes": result.SnapshotSizeBytes,
		"profile_mode":        string(result.ProfileMode),
		"data_size_tier":      result.DataSizeTier,
		"large_dataset":       result.RowCount >= 1000000,
	}
	if result.ProfErr != nil {
		resp["ingest_status"] = "partial"
		resp["message"] = fmt.Sprintf("import succeeded but semantic profiling failed: %v", result.ProfErr)
	} else {
		resp["ingest_status"] = "success"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
