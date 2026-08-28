package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ifnodoraemon/openDataAnalysis/migrations"
)

// PostgresStore wraps a *sql.DB connected to PostgreSQL via pgx/stdlib,
// used for goose migration execution and the database/sql deletion path.
// Application queries use the native pgxpool.Pool from repository/postgres/pool.go.
type PostgresStore struct {
	DB *sql.DB
}

// OpenPostgres opens a PostgreSQL connection (via database/sql + pgx/stdlib)
// and runs goose migrations.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres: %w", err)
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to ping postgres: %w", err), db.Close())
	}

	store := &PostgresStore{DB: db}
	if err := store.migrate(); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return store, nil
}

func (s *PostgresStore) migrate() error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	if err := goose.Up(s.DB, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	version, err := goose.GetDBVersion(s.DB)
	if err != nil {
		log.Printf("Warning: could not read goose version: %v", err)
	} else {
		log.Printf("postgres migrations applied version=%d", version)
	}

	return nil
}

// Close closes the underlying database/sql connection.
func (s *PostgresStore) Close() error {
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}
