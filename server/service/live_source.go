package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

const liveQueryRowLimit = 200
const liveQueryProbeRows = liveQueryRowLimit + 1

func scanLiveQueryRows(ctx context.Context, rows *sql.Rows, dialect string) (*LiveQueryRows, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to read live query columns: %w", err)
	}
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, exists := seenColumns[column]; exists {
			return nil, fmt.Errorf("live query result contains duplicate column name %q; use explicit aliases", column)
		}
		seenColumns[column] = struct{}{}
	}
	result := make([]map[string]interface{}, 0, 16)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan live query row: %w", err)
		}
		row := make(map[string]interface{}, len(columns))
		for i, column := range columns {
			if raw, ok := values[i].([]byte); ok {
				row[column] = string(raw)
			} else {
				row[column] = values[i]
			}
		}
		result = append(result, row)
		if len(result) >= liveQueryProbeRows {
			return nil, fmt.Errorf("query exceeds the %d row limit", liveQueryRowLimit)
		}
	}
	if err := rows.Err(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("live query timeout")
		}
		return nil, fmt.Errorf("failed while reading live query results: %w", err)
	}
	return &LiveQueryRows{Columns: append([]string(nil), columns...), Rows: result, Dialect: dialect}, nil
}

type LiveColumn struct {
	Name         string `json:"name"`
	DeclaredType string `json:"declared_type"`
}

type LiveObjectMetadata struct {
	Columns          []LiveColumn `json:"columns"`
	RowCountEstimate int64        `json:"row_count_estimate"`
}

type LiveQueryRows struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
	Dialect string                   `json:"dialect"`
}

type LiveQueryConnector interface {
	FetchLiveObjectMetadata(ctx context.Context, sourceID, authSecret string, object SourceObjectRef) (*LiveObjectMetadata, error)
	ExecuteLiveQuery(ctx context.Context, sourceID, authSecret, sql string, timeoutSeconds, maxRows int) (*LiveQueryRows, error)
	Dialect() string
}

type LiveBindRequest struct {
	SourceID    string
	WorkspaceID string
	SessionID   string
	Object      SourceObjectRef
}

type LiveQueryCall struct {
	SourceID       string
	SessionID      string
	WorkspaceID    string
	SQL            string
	TimeoutSeconds int
}

type LiveDescribeCall struct {
	SourceID    string
	SessionID   string
	WorkspaceID string
	Schema      string
	Name        string
	SampleRows  int
}

type LiveTableFact struct {
	Schema           string `json:"schema"`
	Name             string `json:"name"`
	QualifiedName    string `json:"qualified_name"`
	Kind             string `json:"kind"`
	RowCountEstimate int64  `json:"row_count_estimate"`
	Estimated        bool   `json:"estimated"`
	ProfileID        string `json:"profile_id"`
	SnapshotID       string `json:"snapshot_id"`
	Dialect          string `json:"dialect"`
}

type LiveTableDescription struct {
	SourceID         string         `json:"source_id"`
	Schema           string         `json:"schema"`
	Name             string         `json:"name"`
	QualifiedName    string         `json:"qualified_name"`
	Dialect          string         `json:"dialect"`
	RowCountEstimate int64          `json:"row_count_estimate"`
	Estimated        bool           `json:"estimated"`
	ColumnCount      int            `json:"column_count"`
	Columns          []LiveColumn   `json:"columns"`
	Sample           *LiveQueryRows `json:"sample,omitempty"`
	SampleRows       int            `json:"sample_rows"`
	Warnings         []string       `json:"warnings,omitempty"`
}

func (s *SourceService) SetCredentialSecret(authSecret string) {
	if len(authSecret) < 32 {
		panic("AUTH_SECRET too short for live source credentials")
	}
	s.credentialSecret = authSecret
}

func (s *SourceService) SetLiveConnectorResolver(resolver func(domain.SourceType) (LiveQueryConnector, error)) {
	s.liveConnectorResolver = resolver
}

func (s *SourceService) resolveLiveConnector(sourceType domain.SourceType) (LiveQueryConnector, error) {
	if s.liveConnectorResolver == nil {
		return nil, fmt.Errorf("live connector resolver is not configured")
	}
	connector, err := s.liveConnectorResolver(sourceType)
	if err != nil {
		return nil, err
	}
	if connector == nil {
		return nil, fmt.Errorf("source type %s does not support live read-only queries", sourceType)
	}
	return connector, nil
}

func (s *SourceService) BindLiveSourceObject(ctx context.Context, req LiveBindRequest) (*SnapshotImportResult, error) {
	if req.SourceID == "" || req.SessionID == "" || req.WorkspaceID == "" {
		return nil, fmt.Errorf("source_id, workspace_id and session_id are required")
	}
	if err := validateExactConfigText("schema_name", req.Object.Schema); err != nil {
		return nil, err
	}
	if err := validateExactConfigText("object_name", req.Object.Name); err != nil {
		return nil, err
	}

	source, err := s.DataSourceRepo.GetByID(ctx, req.SourceID)
	if err != nil {
		return nil, err
	}
	if source.WorkspaceID != req.WorkspaceID {
		return nil, repository.ErrNotFound
	}
	connector, err := s.resolveLiveConnector(source.SourceType)
	if err != nil {
		return nil, err
	}

	snapshotID := "snap_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	preSnapshot := &domain.SourceSnapshot{
		ID:                snapshotID,
		SessionID:         req.SessionID,
		SourceID:          req.SourceID,
		UpstreamKind:      string(source.SourceType),
		UpstreamSchema:    req.Object.Schema,
		UpstreamObject:    req.Object.Name,
		AnalysisTableName: "",
		Status:            domain.SnapshotStatusCreating,
		ImportedAt:        time.Now(),
		Mode:              domain.SnapshotModeLive,
	}
	if err := s.SnapshotRepo.Create(ctx, preSnapshot); err != nil {
		return nil, fmt.Errorf("failed to create live snapshot record: %w", err)
	}

	metadataStart := time.Now()
	metadata, err := connector.FetchLiveObjectMetadata(ctx, req.SourceID, s.credentialSecret, req.Object)
	if err != nil {
		factErr := s.failSnapshotIfPresent(ctx, snapshotID, err)
		return nil, errors.Join(fmt.Errorf("live object metadata fetch failed: %w", err), factErr)
	}
	if len(metadata.Columns) == 0 {
		err := fmt.Errorf("upstream object %s.%s exposes no columns", req.Object.Schema, req.Object.Name)
		factErr := s.failSnapshotIfPresent(ctx, snapshotID, err)
		return nil, errors.Join(err, factErr)
	}

	columns := make([]data.ColumnInfo, 0, len(metadata.Columns))
	for _, column := range metadata.Columns {
		columns = append(columns, data.ColumnInfo{
			Name:         column.Name,
			DeclaredType: column.DeclaredType,
			Estimated:    true,
		})
	}
	schemaInfo := &data.SchemaInfo{
		TableName: connectorQualifyName(connector, req.Object.Schema, req.Object.Name),
		RowCount:  int(metadata.RowCountEstimate),
		Columns:   columns,
		Sampling: data.SamplingInfo{
			Method:     "live_catalog",
			SourceRows: int(metadata.RowCountEstimate),
			SampleRows: 0,
			Estimated:  true,
		},
	}
	facts := buildLiveProfileFacts(schemaInfo)

	schemaSignature := ComputeSchemaSignature(schemaInfo)
	profileStart := time.Now()
	profile, err := s.CreateSemanticProfile(ctx, req.SessionID, req.SourceID, snapshotID, "", schemaSignature, facts)
	if err != nil {
		factErr := s.failSnapshotIfPresent(ctx, snapshotID, err)
		return nil, errors.Join(err, factErr)
	}

	if err := s.SnapshotRepo.UpdateSnapshotCompletion(ctx, snapshotID,
		int(metadata.RowCountEstimate), len(metadata.Columns), schemaSignature,
		0, 0, 0, false,
		int(time.Since(metadataStart).Milliseconds()), int(time.Since(profileStart).Milliseconds()),
		0, domain.ProfileModeLive,
	); err != nil {
		return nil, fmt.Errorf("failed to finalize live snapshot facts: %w", err)
	}
	if err := s.SnapshotRepo.UpdateStatus(ctx, snapshotID, domain.SnapshotStatusReady, nil); err != nil {
		return nil, fmt.Errorf("failed to activate live snapshot: %w", err)
	}

	sourceObjectKey := SourceObjectKey(req.SourceID, string(source.SourceType), req.Object.Schema, req.Object.Name)
	binding := &domain.SessionSourceBinding{
		SessionID:        req.SessionID,
		SourceID:         req.SourceID,
		SourceObjectKey:  sourceObjectKey,
		ActiveSnapshotID: snapshotID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := s.SessionSourceBindingRepo.Upsert(ctx, binding); err != nil {
		return nil, fmt.Errorf("failed to bind live source object: %w", err)
	}
	for _, cleanupErr := range s.cleanupSupersededSnapshots(ctx, nil, req.SessionID, req.SourceID, sourceObjectKey, snapshotID) {
		log.Printf("live bind cleanup issue session_id=%s source_id=%s err=%s", req.SessionID, req.SourceID, cleanupErr)
	}

	return &SnapshotImportResult{
		SnapshotID:        snapshotID,
		ProfileID:         profile.ID,
		TableName:         "",
		RowCount:          int(metadata.RowCountEstimate),
		ColCount:          len(metadata.Columns),
		RowsImported:      0,
		RowsSkipped:       0,
		ProfileDurationMs: int(time.Since(profileStart).Milliseconds()),
		ProfileMode:       domain.ProfileModeLive,
	}, nil
}

func buildLiveProfileFacts(schema *data.SchemaInfo) ProfiledFacts {
	facts := ProfiledFacts{
		Schema:      schema,
		ProfileMode: string(domain.ProfileModeLive),
	}
	facts.Warnings = append(facts.Warnings,
		"live source: row_count is an engine statistics estimate; column statistics are structural facts from the upstream catalog, not computed values")
	return facts
}

func connectorQualifyName(connector LiveQueryConnector, schema, name string) string {
	if qualifier, ok := connector.(LiveObjectQualifier); ok {
		return qualifier.QualifyObject(schema, name)
	}
	return schema + "." + name
}

type LiveObjectQualifier interface {
	QualifyObject(schema, name string) string
}

func (s *SourceService) requireLiveBinding(ctx context.Context, sessionID, workspaceID, sourceID string) (*domain.DataSource, error) {
	source, err := s.DataSourceRepo.GetByID(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if source.WorkspaceID != workspaceID {
		return nil, repository.ErrNotFound
	}
	bindings, err := s.SessionSourceBindingRepo.GetBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	bound := false
	for _, binding := range bindings {
		if binding.SourceID == sourceID {
			bound = true
			break
		}
	}
	if !bound {
		return nil, fmt.Errorf("source %s is not bound to session %s", sourceID, sessionID)
	}
	return source, nil
}

func (s *SourceService) ExecuteSessionLiveQuery(ctx context.Context, call LiveQueryCall) (*LiveQueryRows, error) {
	if call.SessionID == "" || call.WorkspaceID == "" || call.SourceID == "" {
		return nil, fmt.Errorf("session_id, workspace_id and source_id are required")
	}
	if call.TimeoutSeconds < 1 || call.TimeoutSeconds > int(data.QueryTimeoutLarge/time.Second) {
		return nil, fmt.Errorf("timeout_seconds must be between 1 and %d", int(data.QueryTimeoutLarge/time.Second))
	}
	normalizedSQL, err := data.NormalizeReadOnlyQuery(call.SQL)
	if err != nil {
		return nil, err
	}
	source, err := s.requireLiveBinding(ctx, call.SessionID, call.WorkspaceID, call.SourceID)
	if err != nil {
		return nil, err
	}
	connector, err := s.resolveLiveConnector(source.SourceType)
	if err != nil {
		return nil, err
	}
	return connector.ExecuteLiveQuery(ctx, call.SourceID, s.credentialSecret, normalizedSQL, call.TimeoutSeconds, liveQueryProbeRows)
}

func (s *SourceService) ListSessionLiveTables(ctx context.Context, sessionID, workspaceID, sourceID string) ([]LiveTableFact, error) {
	source, err := s.requireLiveBinding(ctx, sessionID, workspaceID, sourceID)
	if err != nil {
		return nil, err
	}
	connector, err := s.resolveLiveConnector(source.SourceType)
	if err != nil {
		return nil, err
	}
	bindings, err := s.SessionSourceBindingRepo.GetBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	facts := make([]LiveTableFact, 0)
	for _, binding := range bindings {
		if binding.SourceID != sourceID {
			continue
		}
		snapshot, err := s.SnapshotRepo.GetByID(ctx, binding.ActiveSnapshotID)
		if err != nil {
			return nil, err
		}
		if snapshot.Mode != domain.SnapshotModeLive {
			continue
		}
		profileID := ""
		profiles, profErr := s.SemanticProfileRepo.ListBySource(ctx, sourceID)
		if profErr == nil {
			if profile, selErr := selectProfileForSnapshot(profiles, sessionID, snapshot.ID); selErr == nil && profile != nil {
				profileID = profile.ID
			}
		}
		facts = append(facts, LiveTableFact{
			Schema:           snapshot.UpstreamSchema,
			Name:             snapshot.UpstreamObject,
			QualifiedName:    connectorQualifyName(connector, snapshot.UpstreamSchema, snapshot.UpstreamObject),
			Kind:             "table",
			RowCountEstimate: int64(snapshot.RowCount),
			Estimated:        true,
			ProfileID:        profileID,
			SnapshotID:       snapshot.ID,
			Dialect:          connector.Dialect(),
		})
	}
	return facts, nil
}

func (s *SourceService) DescribeSessionLiveTable(ctx context.Context, call LiveDescribeCall) (*LiveTableDescription, error) {
	if call.SampleRows < 0 || call.SampleRows > 10000 {
		return nil, fmt.Errorf("sample_rows must be between 0 and 10000")
	}
	source, err := s.requireLiveBinding(ctx, call.SessionID, call.WorkspaceID, call.SourceID)
	if err != nil {
		return nil, err
	}
	connector, err := s.resolveLiveConnector(source.SourceType)
	if err != nil {
		return nil, err
	}

	binding, err := s.SessionSourceBindingRepo.GetBySessionSourceObject(
		ctx, call.SessionID, call.SourceID,
		SourceObjectKey(call.SourceID, string(source.SourceType), call.Schema, call.Name),
	)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("object %s.%s is not bound to session %s", call.Schema, call.Name, call.SessionID)
	}
	if err != nil {
		return nil, err
	}
	snapshot, err := s.SnapshotRepo.GetByID(ctx, binding.ActiveSnapshotID)
	if err != nil {
		return nil, err
	}
	if snapshot.Mode != domain.SnapshotModeLive {
		return nil, fmt.Errorf("object %s.%s is not a live-bound object", call.Schema, call.Name)
	}
	profiles, err := s.SemanticProfileRepo.ListBySource(ctx, call.SourceID)
	if err != nil {
		return nil, err
	}
	profile, err := selectProfileForSnapshot(profiles, call.SessionID, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("live object %s.%s has no structural profile", call.Schema, call.Name)
	}

	var facts ProfiledFacts
	if err := json.Unmarshal([]byte(profile.ProfileJSON), &facts); err != nil {
		return nil, fmt.Errorf("failed to parse live profile: %w", err)
	}
	columns := make([]LiveColumn, 0, len(facts.Schema.Columns))
	for _, column := range facts.Schema.Columns {
		columns = append(columns, LiveColumn{Name: column.Name, DeclaredType: column.DeclaredType})
	}

	description := &LiveTableDescription{
		SourceID:         call.SourceID,
		Schema:           call.Schema,
		Name:             call.Name,
		QualifiedName:    connectorQualifyName(connector, call.Schema, call.Name),
		Dialect:          connector.Dialect(),
		RowCountEstimate: int64(snapshot.RowCount),
		Estimated:        true,
		ColumnCount:      len(columns),
		Columns:          columns,
		SampleRows:       call.SampleRows,
		Warnings:         append([]string(nil), facts.Warnings...),
	}
	if call.SampleRows > 0 {
		qualified := connectorQualifyName(connector, call.Schema, call.Name)
		sampleSQL := fmt.Sprintf("SELECT * FROM %s LIMIT %d", qualified, call.SampleRows)
		rows, err := connector.ExecuteLiveQuery(ctx, call.SourceID, s.credentialSecret, sampleSQL, int(data.QueryTimeoutLarge/time.Second), call.SampleRows+1)
		if err != nil {
			description.Warnings = append(description.Warnings, fmt.Sprintf("sample query failed: %v", err))
		} else {
			description.Sample = rows
		}
	}
	return description, nil
}
