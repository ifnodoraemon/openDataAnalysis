package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type SourceConfigRepository struct{ db *sql.DB }

func NewSourceConfigRepository(db *sql.DB) *SourceConfigRepository {
	return &SourceConfigRepository{db: db}
}

func (r *SourceConfigRepository) Create(ctx context.Context, cfg *domain.SourceConfig) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO source_configs (source_id, connector_type, config_json, credential_ciphertext, last_tested_at, last_test_status, last_error_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cfg.SourceID, string(cfg.ConnectorType), cfg.ConfigJSON, cfg.CredentialCiphertext, cfg.LastTestedAt, cfg.LastTestStatus, cfg.LastErrorMessage, cfg.CreatedAt, cfg.UpdatedAt)
	return err
}

func (r *SourceConfigRepository) GetBySourceID(ctx context.Context, sourceID string) (*domain.SourceConfig, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT source_id, connector_type, config_json, credential_ciphertext, last_tested_at, last_test_status, last_error_message, created_at, updated_at FROM source_configs WHERE source_id = ?`, sourceID)
	var cfg domain.SourceConfig
	var connectorType string
	var lastTestedAt sql.NullTime
	var lastTestStatus, lastErrMsg sql.NullString
	if err := row.Scan(&cfg.SourceID, &connectorType, &cfg.ConfigJSON, &cfg.CredentialCiphertext, &lastTestedAt, &lastTestStatus, &lastErrMsg, &cfg.CreatedAt, &cfg.UpdatedAt); err != nil {
		return nil, normalizeLookupError(err)
	}
	cfg.ConnectorType = domain.SourceType(connectorType)
	if lastTestedAt.Valid {
		cfg.LastTestedAt = &lastTestedAt.Time
	}
	if lastTestStatus.Valid {
		cfg.LastTestStatus = lastTestStatus.String
	}
	if lastErrMsg.Valid {
		cfg.LastErrorMessage = &lastErrMsg.String
	}
	return &cfg, nil
}

func (r *SourceConfigRepository) Update(ctx context.Context, cfg *domain.SourceConfig) error {
	var lastTestedAt interface{}
	if cfg.LastTestedAt != nil {
		lastTestedAt = *cfg.LastTestedAt
	}
	var lastErrMsg interface{}
	if cfg.LastErrorMessage != nil {
		lastErrMsg = *cfg.LastErrorMessage
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE source_configs SET connector_type=?, config_json=?, credential_ciphertext=?, last_tested_at=?, last_test_status=?, last_error_message=?, updated_at=? WHERE source_id=?`,
		string(cfg.ConnectorType), cfg.ConfigJSON, cfg.CredentialCiphertext, lastTestedAt, cfg.LastTestStatus, lastErrMsg, cfg.UpdatedAt, cfg.SourceID)
	return err
}

func (r *SourceConfigRepository) UpdateTestResult(ctx context.Context, sourceID string, testedAt *time.Time, status string, errMsg *string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE source_configs SET last_tested_at=?, last_test_status=?, last_error_message=?, updated_at=? WHERE source_id=?`,
		testedAt, status, errMsg, time.Now(), sourceID)
	return err
}
