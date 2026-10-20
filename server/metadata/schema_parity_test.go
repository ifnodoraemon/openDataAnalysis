package metadata

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/migrations"
)

// The metadata schema exists in two dialects: goose SQL files drive Postgres,
// and an inline statement list drives SQLite. Nothing keeps them in step, so a
// column added to one and forgotten in the other only surfaces at runtime on
// the dialect that was missed. These tests turn that silent drift into a
// failing build.
//
// They compare structure (table and column names), not types: the dialects
// legitimately differ there (timestamptz vs DATETIME, bigserial vs INTEGER).

// dialectInfrastructureTables are per-dialect bookkeeping rather than product
// schema: Postgres tracks applied migrations in goose's own table, SQLite in
// schema_migrations.
var dialectInfrastructureTables = map[string]struct{}{
	"schema_migrations": {},
	"goose_db_version":  {},
}

var (
	createTablePattern = regexp.MustCompile(`(?is)create\s+table\s+(?:if\s+not\s+exists\s+)?["']?(\w+)["']?\s*\((.*?)\n\s*\)\s*;`)
	addColumnPattern   = regexp.MustCompile(`(?is)alter\s+table\s+["']?(\w+)["']?\s+add\s+column\s+(?:if\s+not\s+exists\s+)?["']?(\w+)["']?`)
)

// constraintKeywords open table-constraint clauses rather than a column
// definition. They are matched as whole first words: a prefix test would also
// skip a column named "checksum" because it starts with "check".
var constraintKeywords = map[string]struct{}{
	"primary":    {},
	"foreign":    {},
	"unique":     {},
	"constraint": {},
	"check":      {},
	"key":        {},
	"index":      {},
}

func parseMigrationSchema(t *testing.T, files fs.FS) map[string][]string {
	t.Helper()

	entries, err := fs.Glob(files, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no migration files were embedded")
	}

	schema := map[string][]string{}
	for _, name := range entries {
		content, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range createTablePattern.FindAllStringSubmatch(string(content), -1) {
			table := strings.ToLower(match[1])
			if _, infra := dialectInfrastructureTables[table]; infra {
				continue
			}
			schema[table] = parseColumnNames(match[2])
		}
		// Later migrations extend tables with ALTER TABLE ... ADD COLUMN.
		for _, match := range addColumnPattern.FindAllStringSubmatch(string(content), -1) {
			table := strings.ToLower(match[1])
			column := strings.ToLower(match[2])
			if _, known := schema[table]; !known {
				continue
			}
			schema[table] = append(schema[table], column)
		}
	}
	for table := range schema {
		sort.Strings(schema[table])
	}
	return schema
}

func parseColumnNames(body string) []string {
	var columns []string
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rawLine), ","))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// A constraint may be written without a space before its parenthesis
		// ("unique(workspace_id, user_id)"), so compare the bare keyword.
		first := strings.ToLower(strings.Trim(fields[0], "\"'"))
		if idx := strings.Index(first, "("); idx >= 0 {
			first = first[:idx]
		}
		if _, isConstraint := constraintKeywords[first]; isConstraint {
			continue
		}
		columns = append(columns, first)
	}
	sort.Strings(columns)
	return columns
}

func sqliteSchema(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list sqlite tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if _, infra := dialectInfrastructureTables[name]; infra {
			continue
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}

	schema := map[string][]string{}
	for _, table := range tables {
		info, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatalf("pragma table_info %s: %v", table, err)
		}
		var columns []string
		for info.Next() {
			var (
				cid        int
				name       string
				ctype      string
				notNull    int
				defaultV   sql.NullString
				primaryKey int
			)
			if err := info.Scan(&cid, &name, &ctype, &notNull, &defaultV, &primaryKey); err != nil {
				info.Close()
				t.Fatalf("scan column of %s: %v", table, err)
			}
			columns = append(columns, strings.ToLower(name))
		}
		info.Close()
		sort.Strings(columns)
		schema[table] = columns
	}
	return schema
}

func openSQLiteSchema(t *testing.T) *sql.DB {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })
	return store.DB
}

func TestSQLiteSchemaDefinesEveryMigratedTable(t *testing.T) {
	t.Parallel()

	migrated := parseMigrationSchema(t, migrations.FS)
	actual := sqliteSchema(t, openSQLiteSchema(t))

	for table := range migrated {
		if _, ok := actual[table]; !ok {
			t.Errorf("migration files define table %q but the SQLite schema does not", table)
		}
	}
	for table := range actual {
		if _, ok := migrated[table]; !ok {
			t.Errorf("the SQLite schema defines table %q but no migration file does", table)
		}
	}
}

func TestSQLiteSchemaColumnsMatchMigrationFiles(t *testing.T) {
	t.Parallel()

	migrated := parseMigrationSchema(t, migrations.FS)
	actual := sqliteSchema(t, openSQLiteSchema(t))

	for table, wantColumns := range migrated {
		gotColumns, ok := actual[table]
		if !ok {
			continue // reported by the table-set test
		}
		want := map[string]struct{}{}
		for _, column := range wantColumns {
			want[column] = struct{}{}
		}
		got := map[string]struct{}{}
		for _, column := range gotColumns {
			got[column] = struct{}{}
		}
		for column := range want {
			if _, ok := got[column]; !ok {
				t.Errorf("table %q: migration files define column %q but the SQLite schema does not", table, column)
			}
		}
		for column := range got {
			if _, ok := want[column]; !ok {
				t.Errorf("table %q: the SQLite schema defines column %q but no migration file does", table, column)
			}
		}
	}
}

// The parity tests above compare two parsed schemas. A parser regression that
// silently yields nothing would make them pass vacuously, so the parse itself
// is asserted to be substantive.
func TestMigrationSchemaParseIsSubstantive(t *testing.T) {
	t.Parallel()

	schema := parseMigrationSchema(t, migrations.FS)
	if len(schema) < 18 {
		t.Fatalf("expected every migrated table to be parsed, got %d", len(schema))
	}
	for table, columns := range schema {
		if len(columns) == 0 {
			t.Errorf("table %q parsed with no columns", table)
		}
	}
	// A table whose body contains a line starting with "check..." must still
	// contribute that column, and constraint clauses must not be mistaken for
	// columns.
	runMessages, ok := schema["run_messages"]
	if !ok {
		t.Fatalf("run_messages was not parsed")
	}
	found := false
	for _, column := range runMessages {
		if column == "tool_call_id" {
			found = true
		}
		if column == "unique" || column == "primary" || column == "check" {
			t.Errorf("a constraint clause was parsed as a column: %q", column)
		}
	}
	if !found {
		t.Fatalf("expected run_messages.tool_call_id in %#v", runMessages)
	}
}

func TestSQLiteSchemaVersionIsRecordedAndGuarded(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	defer store.DB.Close()

	version, err := store.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != SQLiteSchemaVersion {
		t.Fatalf("expected the current generation to be recorded, got %d", version)
	}

	// A database written by a newer binary must refuse to open rather than be
	// silently used with an older, incompatible schema.
	if _, err := store.DB.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, datetime('now'))`, SQLiteSchemaVersion+1); err != nil {
		t.Fatalf("record a future version: %v", err)
	}
	if err := store.recordSchemaVersion(); err == nil {
		t.Fatalf("a newer recorded schema version must be refused")
	}
}
