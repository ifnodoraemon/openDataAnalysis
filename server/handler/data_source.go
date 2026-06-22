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

func serializeWorkspaceDataSource(ctx context.Context, ds domain.DataSource) map[string]interface{} {
	item := map[string]interface{}{
		"id":          ds.ID,
		"name":        ds.Name,
		"source_type": string(ds.SourceType),
		"status":      string(ds.Status),
		"created_at":  ds.CreatedAt,
		"updated_at":  ds.UpdatedAt,
	}
	if ds.SourceType != domain.SourceTypePostgresConnection {
		return item
	}

	conn, err := dbConnectionRepo.GetBySourceID(ctx, ds.ID)
	if err != nil || conn == nil {
		return item
	}
	allowlist, _ := service.ParseAllowlist(conn.AllowlistJSON)
	item["postgres"] = map[string]interface{}{
		"host":               conn.Host,
		"port":               conn.Port,
		"database_name":      conn.DatabaseName,
		"default_schema":     conn.DefaultSchema,
		"ssl_mode":           conn.SSLMode,
		"username":           conn.Username,
		"allowlist":          allowlist,
		"last_tested_at":     conn.LastTestedAt,
		"last_test_status":   conn.LastTestStatus,
		"last_error_message": conn.LastErrorMessage,
	}
	return item
}

type CreateDataSourceRequest struct {
	Name       string              `json:"name"`
	SourceType string              `json:"source_type"`
	Postgres   *PostgresConnection `json:"postgres,omitempty"`
}

type PostgresConnection struct {
	Host          string           `json:"host"`
	Port          int              `json:"port"`
	DatabaseName  string           `json:"database_name"`
	DefaultSchema string           `json:"default_schema"`
	SSLMode       string           `json:"ssl_mode"`
	Username      string           `json:"username"`
	Password      string           `json:"password"`
	Allowlist     []AllowlistEntry `json:"allowlist"`
}

type AllowlistEntry struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
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

	if req.SourceType != "postgres_connection" {
		http.Error(w, "only postgres_connection type is supported", http.StatusBadRequest)
		return
	}
	if req.Postgres == nil {
		http.Error(w, "missing postgres connection config", http.StatusBadRequest)
		return
	}

	if len(config.Cfg.AuthSecret) < 32 {
		http.Error(w, "AUTH_SECRET too short, cannot create SQL data source", http.StatusForbidden)
		return
	}

	conn := &domain.DatabaseConnection{
		Driver:        "postgres",
		Host:          req.Postgres.Host,
		Port:          req.Postgres.Port,
		DatabaseName:  req.Postgres.DatabaseName,
		DefaultSchema: req.Postgres.DefaultSchema,
		SSLMode:       req.Postgres.SSLMode,
		Username:      req.Postgres.Username,
	}

	allowlistJSON, _ := json.Marshal(req.Postgres.Allowlist)
	conn.AllowlistJSON = string(allowlistJSON)

	ciphertext, encErr := service.EncryptPassword(req.Postgres.Password, config.Cfg.AuthSecret)
	if encErr != nil {
		http.Error(w, fmt.Sprintf("credential encryption failed: %v", encErr), http.StatusInternalServerError)
		return
	}
	conn.SecretCiphertext = ciphertext

	ds, err := sourceService.CreatePostgresSource(r.Context(), identity.WorkspaceID, req.Name, identity.UserID, conn)
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
	Name     *string             `json:"name"`
	Postgres *PostgresConnection `json:"postgres,omitempty"`
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
	if ds.SourceType != domain.SourceTypePostgresConnection {
		http.Error(w, "only postgres_connection sources can be updated here", http.StatusBadRequest)
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

	if req.Postgres != nil {
		conn, err := dbConnectionRepo.GetBySourceID(r.Context(), sourceID)
		if err != nil || conn == nil {
			http.Error(w, "connection config does not exist", http.StatusNotFound)
			return
		}
		if strings.TrimSpace(req.Postgres.Host) == "" || strings.TrimSpace(req.Postgres.DatabaseName) == "" || strings.TrimSpace(req.Postgres.Username) == "" {
			http.Error(w, "host, database_name and username are required", http.StatusBadRequest)
			return
		}
		if req.Postgres.Port <= 0 {
			http.Error(w, "port must be greater than 0", http.StatusBadRequest)
			return
		}

		conn.Driver = "postgres"
		conn.Host = strings.TrimSpace(req.Postgres.Host)
		conn.Port = req.Postgres.Port
		conn.DatabaseName = strings.TrimSpace(req.Postgres.DatabaseName)
		conn.DefaultSchema = strings.TrimSpace(req.Postgres.DefaultSchema)
		conn.SSLMode = strings.TrimSpace(req.Postgres.SSLMode)
		if conn.SSLMode == "" {
			conn.SSLMode = "disable"
		}
		conn.Username = strings.TrimSpace(req.Postgres.Username)
		allowlistJSON, _ := json.Marshal(req.Postgres.Allowlist)
		conn.AllowlistJSON = string(allowlistJSON)
		if strings.TrimSpace(req.Postgres.Password) != "" {
			if len(config.Cfg.AuthSecret) < 32 {
				http.Error(w, "AUTH_SECRET too short, cannot update SQL data source secret", http.StatusForbidden)
				return
			}
			ciphertext, encErr := service.EncryptPassword(req.Postgres.Password, config.Cfg.AuthSecret)
			if encErr != nil {
				http.Error(w, fmt.Sprintf("credential encryption failed: %v", encErr), http.StatusInternalServerError)
				return
			}
			conn.SecretCiphertext = ciphertext
		}
		conn.LastTestedAt = nil
		conn.LastTestStatus = ""
		conn.LastErrorMessage = nil
		if err := dbConnectionRepo.Update(r.Context(), conn); err != nil {
			http.Error(w, fmt.Sprintf("failed to update connection config: %v", err), http.StatusInternalServerError)
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
	if ds.SourceType != domain.SourceTypePostgresConnection {
		http.Error(w, "only workspace SQL sources can be deleted here", http.StatusBadRequest)
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

	result := sourceService.TestPostgresConnection(r.Context(), sourceID, config.Cfg.AuthSecret)

	conn, _ := dbConnectionRepo.GetBySourceID(r.Context(), sourceID)
	if conn != nil {
		now := time.Now()
		conn.LastTestedAt = &now
		success, _ := result["success"].(bool)
		if success {
			conn.LastTestStatus = "success"
			conn.LastErrorMessage = nil
		} else {
			conn.LastTestStatus = "failed"
			if msg, ok := result["message"].(string); ok {
				conn.LastErrorMessage = &msg
			}
		}
		if err := dbConnectionRepo.Update(r.Context(), conn); err != nil {
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

	conn, err := dbConnectionRepo.GetBySourceID(r.Context(), sourceID)
	if err != nil {
		http.Error(w, "connection config does not exist", http.StatusNotFound)
		return
	}

	allowlist, _ := service.ParseAllowlist(conn.AllowlistJSON)
	result := make([]map[string]interface{}, len(allowlist))
	for i, e := range allowlist {
		result[i] = map[string]interface{}{
			"schema": e.Schema,
			"name":   e.Name,
			"kind":   e.Kind,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"objects": result,
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

	if req.SessionID == "" || req.ObjectName == "" {
		http.Error(w, "missing session_id or object_name", http.StatusBadRequest)
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

	sess.LockUpload()
	result, err := sourceService.ImportPostgresSnapshot(
		r.Context(), sourceID, req.SessionID, req.SchemaName, req.ObjectName,
		sess.Ingester, config.Cfg.AuthSecret,
	)
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
		"import_duration_ms":  result.ImportDurationMs,
		"profile_duration_ms": result.ProfileDurationMs,
		"snapshot_size_bytes": result.SnapshotSizeBytes,
		"profile_mode":        string(result.ProfileMode),
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
