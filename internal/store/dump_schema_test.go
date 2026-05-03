//go:build dumpschema

package store

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestDumpSchema is a one-off helper used to regenerate testdata/schema.sql.
// Run with: go test -tags dumpschema -run TestDumpSchema ./internal/store/
func TestDumpSchema(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	rows, err := db.sql.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	b.WriteString("-- FamClaw schema golden file. Regenerate with:\n")
	b.WriteString("--   go test -tags dumpschema -run TestDumpSchema ./internal/store/\n")
	b.WriteString("-- (or set UPDATE_SCHEMA_GOLDEN=1 and run TestSchemaGolden)\n\n")

	for rows.Next() {
		var typ, name string
		var sqlText *string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if sqlText == nil {
			continue
		}
		fmt.Fprintf(&b, "%s;\n\n", strings.TrimSpace(*sqlText))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	out := "testdata/schema.sql"
	if err := os.WriteFile(out, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("wrote %s (%d bytes)", out, b.Len())
}
