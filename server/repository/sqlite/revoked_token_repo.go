package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type RevokedTokenRepository struct{ db *sql.DB }

func NewRevokedTokenRepository(db *sql.DB) *RevokedTokenRepository {
	return &RevokedTokenRepository{db: db}
}

func (r *RevokedTokenRepository) CreateRevokedToken(ctx context.Context, jti string, expiresAt time.Time) error {
	if strings.TrimSpace(jti) == "" || jti != strings.TrimSpace(jti) {
		return fmt.Errorf("revoked token jti must be a non-empty exact value")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO revoked_tokens (jti, expires_at_unix) VALUES (?, ?)
		 ON CONFLICT(jti) DO UPDATE SET expires_at_unix = excluded.expires_at_unix`,
		jti, expiresAt.Unix(),
	)
	return err
}

func (r *RevokedTokenRepository) ListActiveRevokedTokens(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT jti, expires_at_unix FROM revoked_tokens WHERE expires_at_unix > ?`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	revocations := make(map[string]time.Time)
	for rows.Next() {
		var jti string
		var expiresAtUnix int64
		if err := rows.Scan(&jti, &expiresAtUnix); err != nil {
			return nil, err
		}
		revocations[jti] = time.Unix(expiresAtUnix, 0)
	}
	return revocations, rows.Err()
}

func (r *RevokedTokenRepository) DeleteExpiredRevokedTokens(ctx context.Context, now time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM revoked_tokens WHERE expires_at_unix <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
