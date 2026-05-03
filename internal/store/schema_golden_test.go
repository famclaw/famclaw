package store

import (
	"os"
	"strings"
	"testing"
)

func TestSchemaGolden(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	rows, err := db.sql.Query("SELECT type, name, sql FROM sqlite_master WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%' ORDER BY type, name")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var typ, name string
		var sqlText *string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if sqlText == nil {
			continue
		}
		sb.WriteString(strings.TrimSpace(*sqlText) + ";\n\n")
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	expectedBytes, err := os.ReadFile("testdata/schema.sql")
	if err != nil {
		t.Fatalf("read expected schema: %v", err)
	}
	expected := string(expectedBytes)

	actual := sb.String()
	normalizedExpected := normalize(expected)
	normalizedActual := normalize(actual)

	if normalizedExpected == normalizedActual {
		return
	}

	if os.Getenv("UPDATE_SCHEMA_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/schema.sql", []byte(actual), 0644); err != nil {
			t.Fatalf("write updated schema: %v", err)
		}
		t.Logf("updated testdata/schema.sql")
		return
	}

	t.Fatalf("schema drift detected.\n--- expected (testdata/schema.sql) ---\n%s\n--- actual ---\n%s\nIf intentional, regenerate via UPDATE_SCHEMA_GOLDEN=1 go test ./internal/store/ -run TestSchemaGolden", normalizedExpected, normalizedActual)
}

func normalize(s string) string {
	lines := strings.Split(s, "\n")
	var filtered []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		filtered = append(filtered, strings.TrimRight(line, " \t\r"))
	}

	var collapsed []string
	for _, line := range filtered {
		if line == "" {
			if len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
				continue
			}
		}
		collapsed = append(collapsed, line)
	}

	result := strings.Join(collapsed, "\n")
	return strings.TrimSpace(result)
}
