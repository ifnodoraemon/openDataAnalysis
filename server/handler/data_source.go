package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

func SessionSourcesHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if sess.UserID != identity.UserID || sess.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此会话", http.StatusForbidden)
		return
	}

	sources, err := sourceService.GetSessionSources(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "获取数据源失败", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sources": sources,
	})
}

func DeleteSessionSourceHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	sourceID := chi.URLParam(r, "sourceID")
	sourceObjectKey := r.URL.Query().Get("source_object_key")
	if strings.TrimSpace(sourceObjectKey) == "" {
		http.Error(w, "缺少 source_object_key", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(sourceObjectKey) != sourceObjectKey {
		http.Error(w, "source_object_key 必须保持原值", http.StatusBadRequest)
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

	source, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if source.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权访问此数据源", http.StatusForbidden)
		return
	}

	dropRuntimeTable := func(table service.SourceRuntimeTable) error {
		runtimeSession, _, err := sessionManager.GetOrCreate(r.Context(), table.SessionID, sess.WorkspaceID, identity.UserID)
		if err != nil {
			return err
		}
		runtimeSession.LockUpload()
		defer runtimeSession.UnlockUpload()
		return runtimeSession.Ingester.DropTable(table.TableName)
	}
	err = sourceService.RemoveSessionSource(r.Context(), sessionID, sourceID, sourceObjectKey, dropRuntimeTable)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "移除数据源失败", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func SemanticProfileDetailHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	profileID := chi.URLParam(r, "profileID")

	profile, confirmations, err := sourceService.GetProfileDetail(r.Context(), profileID)
	if err != nil {
		http.Error(w, "获取画像失败", http.StatusNotFound)
		return
	}

	ds, err := dataSourceRepo.GetByID(r.Context(), profile.SourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权执行此操作", http.StatusForbidden)
		return
	}

	assets, assetErr := sourceService.GetSemanticAssets(r.Context(), identity.WorkspaceID, profile.SchemaSignature)
	if assetErr != nil {
		http.Error(w, "获取可复用数据源资产失败", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"profile_id":          profile.ID,
		"session_id":          profile.SessionID,
		"source_id":           profile.SourceID,
		"snapshot_id":         profile.SnapshotID,
		"analysis_table_name": profile.AnalysisTableName,
		"schema_signature":    profile.SchemaSignature,
		"profile_status":      string(profile.ProfileStatus),
		"profile_json":        json.RawMessage(profile.ProfileJSON),
		"confirmations":       confirmations,
		"semantic_assets":     assets,
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
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	profileID := chi.URLParam(r, "profileID")

	profile, _, err := sourceService.GetProfileDetail(r.Context(), profileID)
	if err != nil {
		http.Error(w, "获取画像失败", http.StatusNotFound)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), profile.SourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "无权执行此操作", http.StatusForbidden)
		return
	}

	var req ConfirmProfileRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求体无效", http.StatusBadRequest)
		return
	}
	if req.Scope != "session" && req.Scope != "workspace" {
		http.Error(w, "scope 必须是 'session' 或 'workspace'", http.StatusBadRequest)
		return
	}
	if req.Scope == "session" && req.SessionID == "" {
		http.Error(w, "scope 为 'session' 时必须提供 session_id", http.StatusBadRequest)
		return
	}
	if req.SessionID != "" {
		sess, sessErr := sessionRepo.GetByID(r.Context(), req.SessionID)
		if writeRepoLookupError(w, sessErr, "会话不存在") {
			return
		}
		if sess.UserID != identity.UserID || sess.WorkspaceID != identity.WorkspaceID {
			http.Error(w, "无权访问此会话", http.StatusForbidden)
			return
		}
		if profile.SessionID != req.SessionID {
			http.Error(w, "画像不属于指定会话", http.StatusBadRequest)
			return
		}
	}

	overridesJSON, err := json.Marshal(req.Overrides)
	if err != nil {
		http.Error(w, "序列化确认补丁失败", http.StatusInternalServerError)
		return
	}

	updated, auditErrors, err := sourceService.ConfirmProfile(
		r.Context(), profileID,
		identity.WorkspaceID, req.SessionID,
		identity.UserID, req.Scope, string(overridesJSON), "",
		domain.ConfirmationProvenanceAuthenticatedRequest,
	)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "确认失败", err)
		return
	}

	response := map[string]interface{}{
		"profile_id":     updated.ID,
		"profile_status": string(updated.ProfileStatus),
	}
	if len(auditErrors) > 0 {
		response["audit_errors"] = auditErrors
	}
	writeJSON(w, http.StatusOK, response)
}

func ListDataSourcesHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	sources, err := dataSourceRepo.ListByWorkspace(r.Context(), identity.WorkspaceID)
	if err != nil {
		http.Error(w, "获取数据源列表失败", http.StatusInternalServerError)
		return
	}

	var result []map[string]interface{}
	for _, ds := range sources {
		item, err := serializeWorkspaceDataSource(r.Context(), ds)
		if err != nil {
			http.Error(w, "序列化数据源失败", http.StatusInternalServerError)
			return
		}
		result = append(result, item)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data_sources": result,
	})
}

func ListDataSourceTypesHandler(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	if sourceConnectors == nil {
		http.Error(w, "数据源连接器注册表尚未初始化", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"source_types": sourceConnectors.Specs(),
	})
}

func serializeWorkspaceDataSource(ctx context.Context, ds domain.DataSource) (map[string]interface{}, error) {
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
		return nil, fmt.Errorf("source %s connector: %w", ds.ID, err)
	}
	publicConfig, err := connector.PublicConfig(ctx, ds.ID)
	if err != nil {
		return nil, fmt.Errorf("source %s public config: %w", ds.ID, err)
	}
	item["config"] = publicConfig
	return item, nil
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
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	var req CreateDataSourceRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求体无效", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "名称不能为空", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) != req.Name || strings.TrimSpace(req.SourceType) != req.SourceType {
		http.Error(w, "name 和 source_type 必须保持原值", http.StatusBadRequest)
		return
	}
	name := req.Name
	sourceType := domain.SourceType(req.SourceType)
	if sourceType == domain.SourceTypeFileUpload {
		http.Error(w, "file_upload 数据源只能通过上传文件创建", http.StatusBadRequest)
		return
	}
	connector, err := sourceConnectors.Get(sourceType)
	if err != nil {
		writeHandlerError(w, http.StatusBadRequest, "不支持的数据源类型", err)
		return
	}
	sourceConfig, validationErr := connector.NormalizeConfig(r.Context(), service.SourceConfigRequest{
		RawConfig:          req.Config,
		ConfigProvided:     len(req.Config) > 0,
		RawCredential:      req.Credential,
		CredentialProvided: len(req.Credential) > 0,
		RequireCredential:  true,
		AuthSecret:         config.Cfg.AuthSecret,
	})
	if validationErr != nil {
		writeHandlerError(w, http.StatusBadRequest, "数据源配置无效", validationErr)
		return
	}

	ds, err := sourceService.CreateConfiguredSource(r.Context(), identity.WorkspaceID, name, identity.UserID, sourceType, sourceConfig)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "创建数据源失败", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
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
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "数据源不存在", http.StatusNotFound)
		return
	}
	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		writeHandlerError(w, http.StatusBadRequest, "不支持的数据源类型", err)
		return
	}

	var req UpdateDataSourceRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求体无效", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			http.Error(w, "名称不能为空", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(*req.Name) != *req.Name {
			http.Error(w, "name 必须保持原值", http.StatusBadRequest)
			return
		}
		ds.Name = *req.Name
	}

	configProvided := len(req.Config) > 0
	credentialProvided := len(req.Credential) > 0
	if (configProvided && string(req.Config) == "null") || (credentialProvided && string(req.Credential) == "null") {
		http.Error(w, "不更新 config 或 credential 时必须省略字段，不能传 null", http.StatusBadRequest)
		return
	}
	if configProvided || credentialProvided {
		existing, err := sourceConfigRepo.GetBySourceID(r.Context(), sourceID)
		if err != nil {
			http.Error(w, "数据源配置不存在", http.StatusNotFound)
			return
		}
		normalizedConfig, validationErr := connector.NormalizeConfig(r.Context(), service.SourceConfigRequest{
			SourceID:           sourceID,
			RawConfig:          req.Config,
			ConfigProvided:     configProvided,
			RawCredential:      req.Credential,
			CredentialProvided: credentialProvided,
			Existing:           existing,
			RequireCredential:  false,
			AuthSecret:         config.Cfg.AuthSecret,
		})
		if validationErr != nil {
			writeHandlerError(w, http.StatusBadRequest, "数据源配置无效", validationErr)
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
			writeHandlerError(w, http.StatusInternalServerError, "更新数据源配置失败", err)
			return
		}
	}

	if err := dataSourceRepo.Update(r.Context(), ds); err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "更新数据源失败", err)
		return
	}

	updated, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if err != nil {
		http.Error(w, "重新加载数据源失败", http.StatusInternalServerError)
		return
	}

	serialized, err := serializeWorkspaceDataSource(r.Context(), *updated)
	if err != nil {
		http.Error(w, "序列化数据源失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, serialized)
}

func DeleteDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}

	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "数据源不存在", http.StatusNotFound)
		return
	}
	if ds.SourceType == domain.SourceTypeFileUpload {
		http.Error(w, "上传文件创建的数据源必须通过会话数据源删除", http.StatusBadRequest)
		return
	}

	dropRuntimeTable := func(table service.SourceRuntimeTable) error {
		sessionRecord, err := sessionRepo.GetByID(r.Context(), table.SessionID)
		if err != nil {
			return err
		}
		runtimeSession, _, err := sessionManager.GetOrCreate(r.Context(), table.SessionID, identity.WorkspaceID, sessionRecord.UserID)
		if err != nil {
			return err
		}
		runtimeSession.LockUpload()
		defer runtimeSession.UnlockUpload()
		return runtimeSession.Ingester.DropTable(table.TableName)
	}
	err = sourceService.DeleteWorkspaceSource(r.Context(), sourceID, dropRuntimeTable)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "删除数据源失败", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func TestDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "数据源不存在", http.StatusNotFound)
		return
	}

	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		writeHandlerError(w, http.StatusBadRequest, "不支持的数据源类型", err)
		return
	}
	result, err := connector.Test(r.Context(), service.SourceTestRequest{
		SourceID:   sourceID,
		AuthSecret: config.Cfg.AuthSecret,
	})
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "连接测试失败", err)
		return
	}
	if result.Success {
		result.UISummary = "连接测试成功"
	} else {
		result.UISummary = "连接测试失败"
	}

	if ds.SourceType != domain.SourceTypeFileUpload {
		now := time.Now()
		status := "failed"
		var errMsg *string
		if result.Success {
			status = "success"
		} else if result.Error != "" {
			errMsg = &result.Error
		}
		if err := sourceConfigRepo.UpdateTestResult(r.Context(), sourceID, &now, status, errMsg); err != nil {
			http.Error(w, "保存连接测试结果失败", http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func CatalogDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "数据源不存在", http.StatusNotFound)
		return
	}

	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		writeHandlerError(w, http.StatusBadRequest, "不支持的数据源类型", err)
		return
	}
	objects, err := connector.Catalog(r.Context(), sourceID)
	if err != nil {
		writeHandlerError(w, http.StatusInternalServerError, "获取目录失败", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"objects": objects,
	})
}

type ImportRequest struct {
	SessionID  string `json:"session_id"`
	SchemaName string `json:"schema_name"`
	ObjectName string `json:"object_name"`
	Worksheet  string `json:"worksheet"`
}

func ImportDataSourceHandler(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.UserID == "" {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	ds, err := dataSourceRepo.GetByID(r.Context(), sourceID)
	if writeRepoLookupError(w, err, "数据源不存在") {
		return
	}
	if ds.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "数据源不存在", http.StatusNotFound)
		return
	}

	var req ImportRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求体无效", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "缺少 session_id", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SessionID) != req.SessionID || strings.TrimSpace(req.SchemaName) != req.SchemaName || strings.TrimSpace(req.ObjectName) != req.ObjectName {
		http.Error(w, "session_id、schema_name 和 object_name 必须保持原值", http.StatusBadRequest)
		return
	}
	if ds.SourceType != domain.SourceTypeFileUpload && strings.TrimSpace(req.ObjectName) == "" {
		http.Error(w, "缺少 object_name", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Worksheet) != req.Worksheet {
		http.Error(w, "worksheet 必须保持原值", http.StatusBadRequest)
		return
	}

	sess, _, sessErr := sessionManager.GetOrCreate(r.Context(), req.SessionID, identity.WorkspaceID, identity.UserID)
	if sessErr != nil {
		http.Error(w, "获取会话失败", http.StatusInternalServerError)
		return
	}
	if sess.WorkspaceID != identity.WorkspaceID {
		http.Error(w, "会话不属于当前工作空间", http.StatusForbidden)
		return
	}

	connector, err := sourceConnectors.Get(ds.SourceType)
	if err != nil {
		writeHandlerError(w, http.StatusBadRequest, "不支持的数据源类型", err)
		return
	}

	var result *service.SnapshotImportResult
	var bindMode string
	if connector.Spec().SupportsImport {
		bindMode = string(domain.SnapshotModeImported)
		result, err = importFileSourceObject(r.Context(), connector, sess, service.SourceImportRequest{
			SourceID:    sourceID,
			WorkspaceID: identity.WorkspaceID,
			SessionID:   req.SessionID,
			Object:      service.SourceObjectRef{Schema: req.SchemaName, Name: req.ObjectName},
			AuthSecret:  config.Cfg.AuthSecret,
			Worksheet:   req.Worksheet,
		})
	} else {
		bindMode = string(domain.SnapshotModeLive)
		// Serialize live binds per session: concurrent binds of the same
		// object could interleave snapshot creation, binding upsert, and
		// superseded-snapshot cleanup into a corrupted binding.
		sess.LockUpload()
		result, err = sourceService.BindLiveSourceObject(r.Context(), service.LiveBindRequest{
			SourceID:    sourceID,
			WorkspaceID: identity.WorkspaceID,
			SessionID:   req.SessionID,
			Object:      service.SourceObjectRef{Schema: req.SchemaName, Name: req.ObjectName},
		})
		sess.UnlockUpload()
	}
	if err != nil {
		var wsErr *service.WorksheetSelectionError
		if errors.As(err, &wsErr) && bindMode != string(domain.SnapshotModeLive) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"source_id":     sourceID,
				"ingest_status": "worksheet_selection_required",
				"worksheets":    wsErr.Sheets,
				"agent_capable": true,
				"message":       "文件包含多个工作表，请选择要导入的工作表，或交给智能体处理",
			})
			return
		}
		var structErr *data.StructureError
		if errors.As(err, &structErr) && bindMode != string(domain.SnapshotModeLive) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"source_id":     sourceID,
				"ingest_status": "needs_agent",
				"import_error":  structErr.Detail,
				"agent_capable": true,
				"message":       "结构不符合直接导入要求（需单张矩形表：首行表头、每行等宽）。可在对话中让智能体读取原始文件、清洗后导入",
			})
			return
		}
		if bindMode == string(domain.SnapshotModeLive) {
			writeHandlerError(w, http.StatusInternalServerError, "绑定数据源失败", err)
		} else {
			writeHandlerError(w, http.StatusInternalServerError, "导入失败", err)
		}
		return
	}

	resp := map[string]interface{}{
		"source_id":           sourceID,
		"snapshot_id":         result.SnapshotID,
		"semantic_profile_id": result.ProfileID,
		"analysis_table_name": result.TableName,
		"mode":                bindMode,
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
	}
	resp["ingest_status"] = "success"
	if len(result.CleanupErrors) > 0 {
		resp["cleanup_errors"] = result.CleanupErrors
	}
	auditPayload, err := serviceAuditPayload(map[string]interface{}{
		"source_id":           sourceID,
		"source_type":         string(ds.SourceType),
		"schema_name":         req.SchemaName,
		"object_name":         req.ObjectName,
		"analysis_table_name": result.TableName,
		"row_count":           result.RowCount,
		"column_count":        result.ColCount,
		"rows_imported":       result.RowsImported,
		"rows_skipped":        result.RowsSkipped,
		"import_truncated":    result.ImportTruncated,
		"profile_id":          result.ProfileID,
		"ingest_status":       resp["ingest_status"],
		"cleanup_errors":      result.CleanupErrors,
	})
	if err != nil {
		http.Error(w, "序列化导入审计事实失败", http.StatusInternalServerError)
		return
	}
	if auditErr := sourceService.RecordAuditEvent(r.Context(), domain.AuditEvent{
		WorkspaceID:  identity.WorkspaceID,
		SessionID:    req.SessionID,
		ActorUserID:  identity.UserID,
		EventType:    auditEventTypeForBind(bindMode),
		ResourceType: "source_snapshot",
		ResourceID:   result.SnapshotID,
		PayloadJSON:  auditPayload,
		CreatedAt:    time.Now(),
	}); auditErr != nil {
		log.Printf("record bind audit event failed session_id=%s source_id=%s err=%v", req.SessionID, sourceID, auditErr)
		resp["audit_errors"] = []string{"审计事件记录失败"}
	}

	writeJSON(w, http.StatusOK, resp)
}

func serviceAuditPayload(payload map[string]interface{}) (string, error) {
	out, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func auditEventTypeForBind(bindMode string) string {
	if bindMode == string(domain.SnapshotModeLive) {
		return "data_source_bound"
	}
	return "data_source_imported"
}

func importFileSourceObject(ctx context.Context, connector service.SourceConnector, sess *session.Session, req service.SourceImportRequest) (*service.SnapshotImportResult, error) {
	importer, ok := connector.(service.ImportingConnector)
	if !ok {
		return nil, fmt.Errorf("source type %s does not support importing", connector.Type())
	}
	sess.LockUpload()
	defer sess.UnlockUpload()
	req.Ingester = sess.Ingester
	return importer.Import(ctx, req)
}
