package data

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeReadOnlyQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{name: "select", query: "SELECT * FROM sales", wantErr: false},
		{name: "trailing semicolon is not rewritten", query: "SELECT * FROM sales;", wantErr: true},
		{name: "with", query: "WITH cte AS (SELECT 1 AS n) SELECT * FROM cte", wantErr: false},
		{name: "update", query: "UPDATE sales SET amount = 1", wantErr: true},
		{name: "multi statement", query: "SELECT 1; SELECT 2", wantErr: true},
		{name: "with body is delegated to sqlite query-only enforcement", query: "WITH gone AS (DELETE FROM sales RETURNING *) SELECT * FROM gone", wantErr: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeReadOnlyQuery(tc.query)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for query %q", tc.query)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for query %q: %v", tc.query, err)
			}
		})
	}
}

func TestExecuteQueryRejectsOverRowLimit(t *testing.T) {
	t.Parallel()

	db := openTestSQLiteDB(t)
	if _, err := db.Exec(`CREATE TABLE sales (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < queryProbeRows; i++ {
		if _, err := db.Exec(`INSERT INTO sales (name) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	_, err := ExecuteQueryDetailedContext(context.Background(), db, `SELECT id, name FROM sales ORDER BY id`, queryTimeout)
	if err == nil {
		t.Fatal("expected row limit error")
	}
}

func TestExecuteQueryReturnsRowsWithinLimit(t *testing.T) {
	t.Parallel()

	db := openTestSQLiteDB(t)
	if _, err := db.Exec(`CREATE TABLE sales (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(`INSERT INTO sales (name) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	result, err := ExecuteQueryDetailedContext(context.Background(), db, `SELECT id, name FROM sales ORDER BY id LIMIT 3`, queryTimeout)
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
}

func TestExecuteQueryRestoresWritableConnection(t *testing.T) {
	t.Parallel()

	db := openTestSQLiteDB(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE sales (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sales (name) VALUES ('row-1')`); err != nil {
		t.Fatalf("insert initial row: %v", err)
	}

	if _, err := ExecuteQueryDetailedContext(context.Background(), db, `SELECT id, name FROM sales ORDER BY id LIMIT 1`, queryTimeout); err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sales (name) VALUES ('row-2')`); err != nil {
		t.Fatalf("expected database connection to be writable after ExecuteQuery, got %v", err)
	}
}

func TestIngesterInitDBConfiguresSQLite(t *testing.T) {
	t.Parallel()

	ing := NewIngester(t.TempDir())
	if err := ing.InitDB("sess_config"); err != nil {
		t.Fatalf("InitDB returned error: %v", err)
	}
	t.Cleanup(func() {
		if ing.db != nil {
			_ = ing.db.Close()
		}
	})

	var journalMode string
	if err := ing.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("expected WAL journal mode, got %q", journalMode)
	}

	var busyTimeout int
	if err := ing.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("expected busy_timeout=5000, got %d", busyTimeout)
	}
}

func TestExtractSchemaReturnsStructuralFactsWithoutSemanticInference(t *testing.T) {
	t.Parallel()

	db := openTestSQLiteDB(t)
	if _, err := db.Exec(`CREATE TABLE spend (dt TEXT, channel TEXT, ad_spend INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO spend (dt, channel, ad_spend) VALUES
		('2025-01-05','Search',980),
		('2025-01-12','Search',1040),
		('2025-01-19','Search',1120),
		('2025-01-26','Search',1190),
		('2025-01-06','Social',760),
		('2025-01-13','Social',790),
		('2025-01-20','Social',820),
		('2025-01-27','Social',850),
		('2025-02-02','Search',1210),
		('2025-02-09','Search',1260),
		('2025-02-16','Social',870),
		('2025-02-23','Social',910)
	`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	schema, err := ExtractSchema(db, "spend")
	if err != nil {
		t.Fatalf("ExtractSchema returned error: %v", err)
	}
	found := false
	for _, column := range schema.Columns {
		if column.Name != "dt" {
			continue
		}
		found = true
		if column.DeclaredType != "TEXT" {
			t.Fatalf("expected declared SQLite type to remain TEXT, got %#v", column.DeclaredType)
		}
		if len(column.SampleValues) == 0 || column.UniqueCount != 12 {
			t.Fatalf("expected observed values and counts, got %#v", column)
		}
	}
	if !found {
		t.Fatalf("dt column not found in schema columns: %#v", schema.Columns)
	}
}

func openTestSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
