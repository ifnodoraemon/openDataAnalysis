package metadata

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	DialectSQLite   = "sqlite"
	DialectPostgres = "postgres"
)

type Store struct {
	DB      *sql.DB
	Dialect string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create metadata directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open metadata database: %w", err)
	}
	if err := configureSQLite(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	store := &Store{DB: db, Dialect: DialectSQLite}
	if err := store.migrate(); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return store, nil
}

func configureSQLite(db *sql.DB) error {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA synchronous=NORMAL`,
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to configure SQLite (%s): %w", pragma, err)
		}
	}
	return nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			avatar_url TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			last_login_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			owner_user_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_members (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			UNIQUE(workspace_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			last_run_id TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			last_seen_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS files (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			uploaded_by TEXT NOT NULL,
			display_name TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT 'source',
			content_type TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL,
			storage_provider TEXT NOT NULL,
			bucket TEXT NOT NULL DEFAULT '',
			storage_key TEXT NOT NULL,
			checksum TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'uploaded',
			visibility TEXT NOT NULL DEFAULT 'private',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS session_files (
			session_id TEXT NOT NULL,
			file_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
			FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE,
			PRIMARY KEY (session_id, file_id)
		)`,
		`CREATE TABLE IF NOT EXISTS analysis_runs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			parent_run_id TEXT,
			run_kind TEXT NOT NULL DEFAULT 'root',
			delegate_role TEXT NOT NULL DEFAULT '',
			goal_id TEXT,
			status TEXT NOT NULL,
			input_message TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			error_message TEXT,
			report_file_id TEXT,
			started_at DATETIME,
			finished_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
			FOREIGN KEY (parent_run_id) REFERENCES analysis_runs(id) ON DELETE CASCADE,
			FOREIGN KEY (report_file_id) REFERENCES files(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS reports (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL UNIQUE,
			workspace_id TEXT NOT NULL,
			title TEXT NOT NULL,
			author TEXT NOT NULL DEFAULT '',
			html_storage_provider TEXT NOT NULL,
			html_bucket TEXT NOT NULL DEFAULT '',
			html_storage_key TEXT NOT NULL,
			snapshot_json TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (run_id) REFERENCES analysis_runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS run_messages (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			type TEXT NOT NULL,
			name TEXT,
			tool_call_id TEXT,
			content TEXT NOT NULL,
			success BOOLEAN,
			duration INTEGER,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (run_id) REFERENCES analysis_runs(id) ON DELETE CASCADE,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_run_messages_run ON run_messages(run_id)`,

		`CREATE TABLE IF NOT EXISTS data_sources (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			source_type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			file_id TEXT,
			created_by TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_data_sources_workspace ON data_sources(workspace_id)`,

		`CREATE TABLE IF NOT EXISTS source_configs (
			source_id TEXT PRIMARY KEY,
			connector_type TEXT NOT NULL,
			config_json TEXT NOT NULL DEFAULT '{}',
			credential_ciphertext BLOB NOT NULL DEFAULT X'',
			last_tested_at DATETIME,
			last_test_status TEXT NOT NULL DEFAULT '',
			last_error_message TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (source_id) REFERENCES data_sources(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS source_snapshots (
				id TEXT PRIMARY KEY,
				session_id TEXT NOT NULL,
				source_id TEXT NOT NULL,
			upstream_kind TEXT NOT NULL,
			upstream_schema TEXT NOT NULL DEFAULT '',
			upstream_object TEXT NOT NULL DEFAULT '',
			analysis_table_name TEXT NOT NULL,
			row_count INTEGER NOT NULL DEFAULT 0,
			column_count INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'creating',
			error_message TEXT,
				schema_signature TEXT NOT NULL DEFAULT '',
				imported_at DATETIME NOT NULL,
				rows_imported INTEGER NOT NULL DEFAULT 0,
				rows_skipped INTEGER NOT NULL DEFAULT 0,
				import_row_limit INTEGER NOT NULL DEFAULT 0,
				import_truncated BOOLEAN NOT NULL DEFAULT 0,
				import_duration_ms INTEGER NOT NULL DEFAULT 0,
				profile_duration_ms INTEGER NOT NULL DEFAULT 0,
			snapshot_size_bytes INTEGER NOT NULL DEFAULT 0,
			profile_mode TEXT NOT NULL DEFAULT 'pending',
			mode TEXT NOT NULL DEFAULT 'imported',
			FOREIGN KEY (source_id) REFERENCES data_sources(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_source_snapshots_session ON source_snapshots(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_source_snapshots_source ON source_snapshots(source_id)`,

		`CREATE TABLE IF NOT EXISTS session_source_bindings (
				session_id TEXT NOT NULL,
				source_id TEXT NOT NULL,
				source_object_key TEXT NOT NULL,
				active_snapshot_id TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (session_id, source_id, source_object_key),
				FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
				FOREIGN KEY (source_id) REFERENCES data_sources(id) ON DELETE CASCADE
			)`,

		`CREATE TABLE IF NOT EXISTS semantic_profiles (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			snapshot_id TEXT NOT NULL,
			analysis_table_name TEXT NOT NULL,
			schema_signature TEXT NOT NULL DEFAULT '',
			profile_status TEXT NOT NULL DEFAULT 'profiled',
			profile_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (source_id) REFERENCES data_sources(id) ON DELETE CASCADE,
			FOREIGN KEY (snapshot_id) REFERENCES source_snapshots(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_profiles_session ON semantic_profiles(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_profiles_source ON semantic_profiles(source_id)`,

		`CREATE TABLE IF NOT EXISTS semantic_confirmations (
			id TEXT PRIMARY KEY,
			profile_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			confirmed_by TEXT NOT NULL,
			confirmation_receipt_id TEXT NOT NULL DEFAULT '',
			provenance TEXT NOT NULL DEFAULT 'authenticated_request',
			scope TEXT NOT NULL DEFAULT 'session',
			overrides_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL,
			FOREIGN KEY (profile_id) REFERENCES semantic_profiles(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_confirmations_profile ON semantic_confirmations(profile_id)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_confirmations_workspace ON semantic_confirmations(workspace_id)`,

		`CREATE TABLE IF NOT EXISTS semantic_assets (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			source_id TEXT NOT NULL DEFAULT '',
			schema_signature TEXT NOT NULL DEFAULT '',
			asset_kind TEXT NOT NULL,
			asset_key TEXT NOT NULL,
			asset_value_json TEXT NOT NULL DEFAULT '{}',
			created_from_profile_id TEXT NOT NULL DEFAULT '',
			created_from_confirmation_id TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(workspace_id, schema_signature, asset_kind, asset_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_assets_workspace ON semantic_assets(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_assets_schema ON semantic_assets(workspace_id, schema_signature)`,

		`CREATE TABLE IF NOT EXISTS audit_events (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL DEFAULT '',
			actor_user_id TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL,
			resource_type TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			payload_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_workspace ON audit_events(workspace_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_resource ON audit_events(resource_type, resource_id)`,

		`CREATE TABLE IF NOT EXISTS revoked_tokens (
			jti TEXT PRIMARY KEY,
			expires_at_unix INTEGER NOT NULL
		)`,
	}

	for _, stmt := range stmts {
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("failed to execute metadata migration: %w", err)
		}
	}

	for _, required := range []struct {
		table   string
		columns []string
	}{
		{table: "analysis_runs", columns: []string{"summary", "parent_run_id", "run_kind", "delegate_role", "goal_id"}},
		{table: "files", columns: []string{"purpose"}},
		{table: "run_messages", columns: []string{"tool_call_id"}},
		{table: "source_snapshots", columns: []string{"rows_skipped", "import_row_limit", "import_truncated", "mode"}},
		{table: "semantic_confirmations", columns: []string{"confirmation_receipt_id", "provenance"}},
	} {
		if err := validateRequiredColumns(s.DB, required.table, required.columns); err != nil {
			return err
		}
	}
	if _, err := s.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_semantic_confirmations_receipt ON semantic_confirmations(confirmation_receipt_id) WHERE confirmation_receipt_id <> ''`); err != nil {
		return fmt.Errorf("failed to create semantic confirmation receipt index: %w", err)
	}
	if err := validateSessionSourceBindingsShape(s.DB); err != nil {
		return err
	}

	return nil
}

func validateSessionSourceBindingsShape(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(session_source_bindings)`)
	if err != nil {
		return fmt.Errorf("failed to inspect session_source_bindings: %w", err)
	}
	defer rows.Close()

	type colInfo struct {
		Name string
		PK   int
	}
	cols := map[string]colInfo{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("failed to read session_source_bindings structure: %w", err)
		}
		cols[name] = colInfo{Name: name, PK: pk}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if cols["session_id"].PK != 1 || cols["source_id"].PK != 2 || cols["source_object_key"].PK != 3 {
		return fmt.Errorf("session_source_bindings has an unsupported schema; run an explicit metadata migration before starting the server")
	}
	return nil
}

func validateRequiredColumns(db *sql.DB, table string, required []string) (resultErr error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(\"%s\")", table))
	if err != nil {
		return fmt.Errorf("failed to inspect %s schema: %w", table, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close %s schema rows: %w", table, closeErr))
		}
	}()

	observed := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("failed to read %s table structure: %w", table, err)
		}
		observed[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate %s table structure: %w", table, err)
	}
	for _, column := range required {
		if _, ok := observed[column]; !ok {
			return fmt.Errorf("%s is missing required column %s; run an explicit metadata migration before starting the server", table, column)
		}
	}
	return nil
}
