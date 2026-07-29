package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// schema.sql is applied on every boot and must stay idempotent, so a column
// added after a deployment already exists has to be declared twice: once in
// CREATE TABLE (for fresh databases) and again as ALTER TABLE ... ADD COLUMN
// IF NOT EXISTS (for databases created before it existed).
//
// That duplication is deliberate, but it drifts silently: change a default in
// one place and fresh installs diverge from upgraded ones, with nothing to
// catch it until the two behave differently in production. This test enforces
// the invariant that every ALTER-added column is also present in its
// CREATE TABLE, so the two definitions cannot get out of step unnoticed.
func TestSchemaAlterColumnsAlsoDeclaredInCreateTable(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	schema := string(raw)

	createBodies := map[string]string{}
	createRe := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS ([a-z_]+) \((.*?)\n\);`)
	for _, m := range createRe.FindAllStringSubmatch(schema, -1) {
		createBodies[m[1]] = m[2]
	}
	if len(createBodies) == 0 {
		t.Fatal("no CREATE TABLE statements parsed — has schema.sql's formatting changed?")
	}

	// Matches both the single-line form and the wrapped one where the column
	// name lands on the following line.
	alterRe := regexp.MustCompile(`ALTER TABLE ([a-z_]+) ADD COLUMN IF NOT EXISTS\s+([a-z_]+)`)
	alters := alterRe.FindAllStringSubmatch(schema, -1)
	if len(alters) == 0 {
		t.Skip("no ALTER TABLE ADD COLUMN statements in schema.sql")
	}

	for _, m := range alters {
		table, column := m[1], m[2]

		body, ok := createBodies[table]
		if !ok {
			t.Errorf("ALTER TABLE %s adds %q but there is no CREATE TABLE for %s", table, column, table)
			continue
		}

		found := false
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "--") {
				continue
			}
			if field := strings.Fields(line); len(field) > 0 && field[0] == column {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("column %q is added to %s by ALTER TABLE but is missing from its CREATE TABLE — "+
				"fresh databases would not get it; declare it in both places", column, table)
		}
	}
}
