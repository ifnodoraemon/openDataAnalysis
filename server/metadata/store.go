package metadata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
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
		_ = db.Close()
		return nil, err
	}

	store := &Store{DB: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
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

		`CREATE TABLE IF NOT EXISTS database_connections (
			source_id TEXT PRIMARY KEY,
			driver TEXT NOT NULL,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			database_name TEXT NOT NULL,
			default_schema TEXT NOT NULL DEFAULT '',
			ssl_mode TEXT NOT NULL DEFAULT 'disable',
			username TEXT NOT NULL,
			secret_ciphertext BLOB NOT NULL,
			allowlist_json TEXT NOT NULL DEFAULT '[]',
			last_tested_at DATETIME,
			last_test_status TEXT NOT NULL DEFAULT '',
			last_error_message TEXT,
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
			profile_mode TEXT NOT NULL DEFAULT 'sampled',
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
			profile_status TEXT NOT NULL DEFAULT 'draft',
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
			scope TEXT NOT NULL DEFAULT 'session',
			overrides_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL,
			FOREIGN KEY (profile_id) REFERENCES semantic_profiles(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_confirmations_profile ON semantic_confirmations(profile_id)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_confirmations_workspace ON semantic_confirmations(workspace_id)`,
	}

	for _, stmt := range stmts {
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("failed to execute metadata migration: %w", err)
		}
	}

	if err := ensureColumn(s.DB, "analysis_runs", "summary", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "analysis_runs", "parent_run_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "analysis_runs", "run_kind", "TEXT NOT NULL DEFAULT 'root'"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "analysis_runs", "delegate_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "analysis_runs", "goal_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "files", "purpose", "TEXT NOT NULL DEFAULT 'source'"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "run_messages", "tool_call_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "source_snapshots", "rows_skipped", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "source_snapshots", "import_row_limit", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(s.DB, "source_snapshots", "import_truncated", "BOOLEAN NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureSessionSourceBindingsShape(s.DB); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`UPDATE files SET purpose = 'report' WHERE purpose = 'source' AND storage_key LIKE '%/report/%'`); err != nil {
		return fmt.Errorf("failed to backfill report file usage: %w", err)
	}
	if _, err := s.DB.Exec(`UPDATE files SET purpose = 'artifact' WHERE purpose = 'source' AND storage_key LIKE '%/artifacts/%'`); err != nil {
		return fmt.Errorf("failed to backfill artifact file usage: %w", err)
	}

	if err := s.backfillFileDataSources(); err != nil {
		return fmt.Errorf("failed to backfill file-backed data sources: %w", err)
	}

	return nil
}

var safeIdentPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const cleanSessionSourceBindingsDDL = `CREATE TABLE session_source_bindings (
	session_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	source_object_key TEXT NOT NULL,
	active_snapshot_id TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	PRIMARY KEY (session_id, source_id, source_object_key),
	FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
	FOREIGN KEY (source_id) REFERENCES data_sources(id) ON DELETE CASCADE
)`

func ensureSessionSourceBindingsShape(db *sql.DB) error {
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
	if cols["session_id"].PK == 1 && cols["source_id"].PK == 2 && cols["source_object_key"].PK == 3 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`ALTER TABLE session_source_bindings RENAME TO session_source_bindings_legacy`); err != nil {
		return fmt.Errorf("failed to replace session_source_bindings: %w", err)
	}
	if _, err := tx.Exec(cleanSessionSourceBindingsDDL); err != nil {
		return fmt.Errorf("failed to create session_source_bindings: %w", err)
	}

	hasObjectKey := false
	if _, ok := cols["source_object_key"]; ok {
		hasObjectKey = true
	}
	selectSQL := `SELECT b.session_id, b.source_id, b.active_snapshot_id, b.created_at, b.updated_at, COALESCE(ss.upstream_kind, ''), COALESCE(ss.upstream_schema, ''), COALESCE(ss.upstream_object, '')`
	if hasObjectKey {
		selectSQL += `, COALESCE(b.source_object_key, '')`
	}
	selectSQL += ` FROM session_source_bindings_legacy b LEFT JOIN source_snapshots ss ON ss.id = b.active_snapshot_id`
	legacyRows, err := tx.Query(selectSQL)
	if err != nil {
		return fmt.Errorf("failed to read old session_source_bindings: %w", err)
	}
	for legacyRows.Next() {
		var sessionID, sourceID, snapshotID, createdAt, updatedAt, kind, schema, object string
		objectKey := ""
		scanArgs := []interface{}{&sessionID, &sourceID, &snapshotID, &createdAt, &updatedAt, &kind, &schema, &object}
		if hasObjectKey {
			scanArgs = append(scanArgs, &objectKey)
		}
		if err := legacyRows.Scan(scanArgs...); err != nil {
			return fmt.Errorf("failed to scan old session_source_bindings: %w", err)
		}
		if strings.TrimSpace(objectKey) == "" {
			objectKey = metadataSourceObjectKey(sourceID, kind, schema, object)
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO session_source_bindings (session_id, source_id, source_object_key, active_snapshot_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, sourceID, objectKey, snapshotID, createdAt, updatedAt,
		); err != nil {
			return fmt.Errorf("failed to write session_source_bindings row: %w", err)
		}
	}
	if err := legacyRows.Err(); err != nil {
		_ = legacyRows.Close()
		return err
	}
	if err := legacyRows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE session_source_bindings_legacy`); err != nil {
		return fmt.Errorf("failed to drop old session_source_bindings: %w", err)
	}
	return tx.Commit()
}

func metadataSourceObjectKey(sourceID, upstreamKind, upstreamSchema, upstreamObject string) string {
	kind := strings.TrimSpace(upstreamKind)
	if kind == "" {
		kind = "source"
	}
	if kind == "file_upload" {
		return "file_upload:" + strings.TrimSpace(sourceID)
	}
	schema := strings.TrimSpace(upstreamSchema)
	object := strings.TrimSpace(upstreamObject)
	if schema != "" || object != "" {
		return kind + ":" + schema + "." + object
	}
	return kind + ":" + strings.TrimSpace(sourceID)
}

func (s *Store) backfillFileDataSources() error {
	rows, err := s.DB.Query(`SELECT f.id, f.workspace_id, f.display_name, f.uploaded_by, f.created_at, f.updated_at FROM files f WHERE f.purpose = 'source' AND NOT EXISTS (SELECT 1 FROM data_sources ds WHERE ds.file_id = f.id)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fileRow struct {
		ID          string
		WorkspaceID string
		DisplayName string
		UploadedBy  string
		CreatedAt   string
		UpdatedAt   string
	}
	var files []fileRow
	for rows.Next() {
		var f fileRow
		if err := rows.Scan(&f.ID, &f.WorkspaceID, &f.DisplayName, &f.UploadedBy, &f.CreatedAt, &f.UpdatedAt); err != nil {
			continue
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, f := range files {
		sourceID := "ds_" + f.ID
		_, _ = s.DB.Exec(`INSERT OR IGNORE INTO data_sources (id, workspace_id, name, source_type, status, file_id, created_by, created_at, updated_at) VALUES (?, ?, ?, 'file_upload', 'active', ?, ?, ?, ?)`,
			sourceID, f.WorkspaceID, f.DisplayName, f.ID, f.UploadedBy, f.CreatedAt, f.UpdatedAt)
	}

	return nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	if !safeIdentPattern.MatchString(table) || !safeIdentPattern.MatchString(column) {
		return fmt.Errorf("invalid identifier: table=%s column=%s", table, column)
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(\"%s\")", table))
	if err != nil {
		return fmt.Errorf("failed to check %s.%s: %w", table, column, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("failed to read %s table structure: %w", table, err)
		}
		if strings.EqualFold(name, column) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate %s table structure: %w", table, err)
	}

	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE \"%s\" ADD COLUMN \"%s\" %s", table, column, definition)); err != nil {
		return fmt.Errorf("failed to add column %s to %s: %w", table, column, err)
	}
	return nil
}
