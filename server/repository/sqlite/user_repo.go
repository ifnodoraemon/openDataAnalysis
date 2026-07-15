package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type UserRepository struct{ db *sql.DB }

func NewUserRepository(db *sql.DB) *UserRepository { return &UserRepository{db: db} }
func (r *UserRepository) GetByID(ctx context.Context, userID string) (*domain.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, email, password_hash, name, avatar_url, status, created_at, updated_at, last_login_at FROM users WHERE id = ?`, userID)
	var user domain.User
	var avatarURL string
	var status string
	var lastLogin sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Name, &avatarURL, &status, &user.CreatedAt, &user.UpdatedAt, &lastLogin); err != nil {
		return nil, err
	}
	user.AvatarURL = avatarURL
	user.Status = domain.UserStatus(status)
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	return &user, nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, email, password_hash, name, avatar_url, status, created_at, updated_at, last_login_at FROM users WHERE email = ?`, email)
	var user domain.User
	var avatarURL string
	var status string
	var lastLogin sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Name, &avatarURL, &status, &user.CreatedAt, &user.UpdatedAt, &lastLogin); err != nil {
		return nil, err
	}
	user.AvatarURL = avatarURL
	user.Status = domain.UserStatus(status)
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	return &user, nil
}

func (r *UserRepository) Create(ctx context.Context, user *domain.User) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, name, avatar_url, status, created_at, updated_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Email, user.PasswordHash, user.Name, user.AvatarURL, string(user.Status), user.CreatedAt, user.UpdatedAt, user.LastLoginAt)
	return err
}

func (r *UserRepository) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, time.Now(), userID)
	return err
}
