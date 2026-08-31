package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RevokedTokenRepository struct {
	db DBTX
}

func NewRevokedTokenRepository(db DBTX) *RevokedTokenRepository {
	return &RevokedTokenRepository{db: db}
}

func (r *RevokedTokenRepository) CreateRevokedToken(ctx context.Context, jti string, expiresAt time.Time) error {
	if strings.TrimSpace(jti) == "" || jti != strings.TrimSpace(jti) {
		return fmt.Errorf("revoked token jti must be a non-empty exact value")
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO revoked_tokens (jti, expires_at_unix) VALUES ($1, $2)
		 ON CONFLICT (jti) DO UPDATE SET expires_at_unix = EXCLUDED.expires_at_unix`,
		jti, expiresAt.Unix(),
	)
	return err
}

func (r *RevokedTokenRepository) ListActiveRevokedTokens(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	rows, err := r.db.Query(ctx, `SELECT jti, expires_at_unix FROM revoked_tokens WHERE expires_at_unix > $1`, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("list revoked tokens: %w", err)
	}
	defer rows.Close()

	revocations := make(map[string]time.Time)
	for rows.Next() {
		var jti string
		var expiresAtUnix int64
		if err := rows.Scan(&jti, &expiresAtUnix); err != nil {
			return nil, fmt.Errorf("scan revoked token: %w", err)
		}
		revocations[jti] = time.Unix(expiresAtUnix, 0)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate revoked tokens: %w", err)
	}
	return revocations, nil
}

func (r *RevokedTokenRepository) DeleteExpiredRevokedTokens(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM revoked_tokens WHERE expires_at_unix <= $1`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("delete expired revoked tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}
