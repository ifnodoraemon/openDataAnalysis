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

type UserRepository struct {
	db DBTX
}

func NewUserRepository(db DBTX) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) GetByID(ctx context.Context, userID string) (*domain.User, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, email, password_hash, name, avatar_url, status, created_at, updated_at, last_login_at FROM users WHERE id = $1`,
		userID,
	)
	var user domain.User
	var status string
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Name, &user.AvatarURL, &status, &user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}
	user.Status = domain.UserStatus(status)
	return &user, nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, email, password_hash, name, avatar_url, status, created_at, updated_at, last_login_at FROM users WHERE email = $1`,
		email,
	)
	var user domain.User
	var status string
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Name, &user.AvatarURL, &status, &user.CreatedAt, &user.UpdatedAt, &user.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}
	user.Status = domain.UserStatus(status)
	return &user, nil
}

func (r *UserRepository) Create(ctx context.Context, user *domain.User) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name, avatar_url, status, created_at, updated_at, last_login_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		user.ID, user.Email, user.PasswordHash, user.Name, user.AvatarURL, string(user.Status), user.CreatedAt, user.UpdatedAt, user.LastLoginAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

func (r *UserRepository) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = $2 WHERE id = $3`,
		passwordHash, time.Now(), userID,
	)
	if err != nil {
		return fmt.Errorf("failed to update password hash: %w", err)
	}
	return nil
}
