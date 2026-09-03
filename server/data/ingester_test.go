package data

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestCSVImportPreservesTextValuesWithoutSemanticInference(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	ing := NewIngester(cacheDir)
	if err := ing.InitDB("sess_csv_facts"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = ing.Destroy() })

	csvPath := filepath.Join(cacheDir, "facts.csv")
	content := "code,marker,empty,padded\n001,\" N/A \",null,\"  padded  \"\n002,-,,\" \"\n"
	if err := os.WriteFile(csvPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tableName, rowCount, colCount, err := ing.ImportFileRaw(csvPath)
	if err != nil {
		t.Fatalf("ImportFileRaw: %v", err)
	}
	if rowCount != 2 || colCount != 4 {
		t.Fatalf("unexpected shape: rows=%d columns=%d", rowCount, colCount)
	}

	rows, err := ing.db.Query(`SELECT code, marker, empty, padded, typeof(code), typeof(empty) FROM facts ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query imported data: %v", err)
	}
	defer rows.Close()

	want := [][6]string{
		{"001", " N/A ", "null", "  padded  ", "text", "text"},
		{"002", "-", "", " ", "text", "text"},
	}
	var got [][6]string
	for rows.Next() {
		var row [6]string
		if err := rows.Scan(&row[0], &row[1], &row[2], &row[3], &row[4], &row[5]); err != nil {
			t.Fatalf("scan imported row: %v", err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate imported rows: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if tableName != "facts" {
		t.Fatalf("table name = %q, want facts", tableName)
	}
}

func TestCSVImportPreservesObservedColumnNames(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	ing := NewIngester(cacheDir)
	if err := ing.InitDB("sess_csv_columns"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = ing.Destroy() })

	csvPath := filepath.Join(cacheDir, "columns.csv")
	if err := os.WriteFile(csvPath, []byte("\" Order ID \",客户,\"a\"\"b\"\n001,张三,value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, _, _, err := ing.ImportFileRaw(csvPath); err != nil {
		t.Fatalf("ImportFileRaw: %v", err)
	}

	var orderID, customer, quoted string
	if err := ing.db.QueryRow(`SELECT " Order ID ", "客户", "a""b" FROM columns`).Scan(&orderID, &customer, &quoted); err != nil {
		t.Fatalf("query exact column names: %v", err)
	}
	if orderID != "001" || customer != "张三" || quoted != "value" {
		t.Fatalf("unexpected values: %q %q %q", orderID, customer, quoted)
	}
	schema, err := ExtractSchema(ing.db, "columns")
	if err != nil {
		t.Fatalf("ExtractSchema: %v", err)
	}
	wantNames := []string{" Order ID ", "客户", `a"b`}
	if len(schema.Columns) != len(wantNames) {
		t.Fatalf("columns = %#v", schema.Columns)
	}
	for i, want := range wantNames {
		if schema.Columns[i].Name != want {
			t.Fatalf("column %d = %q, want %q", i, schema.Columns[i].Name, want)
		}
	}
}

func TestCSVImportRejectsAmbiguousDuplicateHeaders(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	ing := NewIngester(cacheDir)
	if err := ing.InitDB("sess_csv_duplicate_columns"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = ing.Destroy() })

	csvPath := filepath.Join(cacheDir, "duplicates.csv")
	if err := os.WriteFile(csvPath, []byte("value,value\n1,2\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, _, _, err := ing.ImportFileRaw(csvPath); err == nil {
		t.Fatal("expected duplicate headers to be rejected instead of renamed")
	}
}

func TestDropTableRejectsMissingAnalysisDatabase(t *testing.T) {
	t.Parallel()

	ing := NewIngester(t.TempDir())
	if err := ing.DropTable("facts"); err == nil {
		t.Fatal("expected missing analysis database to be reported")
	}
}

func openTestSQLiteDB2(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCSVImportRaggedRowReturnsStructureError(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	ing := NewIngester(cacheDir)
	if err := ing.InitDB("sess_struct_csv"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = ing.Destroy() })

	csvPath := filepath.Join(cacheDir, "ragged.csv")
	if err := os.WriteFile(csvPath, []byte("a,b\n1,2,3\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, _, err := ing.ImportFileRaw(csvPath)
	if err == nil {
		t.Fatal("expected ragged CSV to be rejected")
	}
	var structErr *StructureError
	if !errors.As(err, &structErr) {
		t.Fatalf("expected StructureError, got %T: %v", err, err)
	}
	if !strings.Contains(structErr.Detail, "failed to read CSV row 2") {
		t.Fatalf("unexpected detail: %s", structErr.Detail)
	}
}

func TestExcelImportTitleRowReturnsStructureError(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	ing := NewIngester(cacheDir)
	if err := ing.InitDB("sess_struct_xlsx"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = ing.Destroy() })

	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	if err := f.SetSheetRow(sheet, "A1", &[]interface{}{"报表标题", "型号A"}); err != nil {
		t.Fatalf("SetSheetRow: %v", err)
	}
	if err := f.SetSheetRow(sheet, "A2", &[]interface{}{"时间", "调用次数", "Token量"}); err != nil {
		t.Fatalf("SetSheetRow: %v", err)
	}
	if err := f.SetSheetRow(sheet, "A3", &[]interface{}{"2026-09-01", "5", "1200"}); err != nil {
		t.Fatalf("SetSheetRow: %v", err)
	}
	xlsxPath := filepath.Join(cacheDir, "titled.xlsx")
	if err := f.SaveAs(xlsxPath); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}

	_, _, _, err := ing.ImportFileRaw(xlsxPath)
	if err == nil {
		t.Fatal("expected title-row workbook to be rejected by the deterministic importer")
	}
	var structErr *StructureError
	if !errors.As(err, &structErr) {
		t.Fatalf("expected StructureError, got %T: %v", err, err)
	}
	if !strings.Contains(structErr.Detail, "row 2 has 3 cells; header has 2") {
		t.Fatalf("unexpected detail: %s", structErr.Detail)
	}
}
