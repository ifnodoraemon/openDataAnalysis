package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/jackc/pgx/v5"
)

type SourceConfigRepository struct {
	db DBTX
}

func NewSourceConfigRepository(db DBTX) *SourceConfigRepository {
	return &SourceConfigRepository{db: db}
}

func (r *SourceConfigRepository) Create(ctx context.Context, cfg *domain.SourceConfig) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO source_configs (source_id, connector_type, config_json, credential_ciphertext, last_tested_at, last_test_status, last_error_message, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		cfg.SourceID, string(cfg.ConnectorType), cfg.ConfigJSON, cfg.CredentialCiphertext, cfg.LastTestedAt, cfg.LastTestStatus, cfg.LastErrorMessage, cfg.CreatedAt, cfg.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create source config: %w", err)
	}
	return nil
}

func (r *SourceConfigRepository) GetBySourceID(ctx context.Context, sourceID string) (*domain.SourceConfig, error) {
	row := r.db.QueryRow(ctx,
		`SELECT source_id, connector_type, config_json, credential_ciphertext, last_tested_at, last_test_status, last_error_message, created_at, updated_at FROM source_configs WHERE source_id = $1`, sourceID,
	)
	var cfg domain.SourceConfig
	var connectorType string
	err := row.Scan(&cfg.SourceID, &connectorType, &cfg.ConfigJSON, &cfg.CredentialCiphertext, &cfg.LastTestedAt, &cfg.LastTestStatus, &cfg.LastErrorMessage, &cfg.CreatedAt, &cfg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get source config by source id: %w", err)
	}
	cfg.ConnectorType = domain.SourceType(connectorType)
	return &cfg, nil
}

func (r *SourceConfigRepository) Update(ctx context.Context, cfg *domain.SourceConfig) error {
	_, err := r.db.Exec(ctx,
		`UPDATE source_configs SET connector_type = $1, config_json = $2, credential_ciphertext = $3, last_tested_at = $4, last_test_status = $5, last_error_message = $6, updated_at = $7 WHERE source_id = $8`,
		string(cfg.ConnectorType), cfg.ConfigJSON, cfg.CredentialCiphertext, cfg.LastTestedAt, cfg.LastTestStatus, cfg.LastErrorMessage, cfg.UpdatedAt, cfg.SourceID,
	)
	if err != nil {
		return fmt.Errorf("failed to update source config: %w", err)
	}
	return nil
}

func (r *SourceConfigRepository) UpdateTestResult(ctx context.Context, sourceID string, testedAt *time.Time, status string, errMsg *string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE source_configs SET last_tested_at = $1, last_test_status = $2, last_error_message = $3, updated_at = $4 WHERE source_id = $5`,
		testedAt, status, errMsg, time.Now(), sourceID,
	)
	if err != nil {
		return fmt.Errorf("failed to update source config test result: %w", err)
	}
	return nil
}
