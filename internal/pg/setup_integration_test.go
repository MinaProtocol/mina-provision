//go:build integration

// Integration tests exercising the real psql-backed DB setup path. Excluded
// from the default `go test ./...` run via the `integration` build tag.
//
// Requires a reachable Postgres and the postgresql-client (psql) on PATH.
// Provide a superuser URI with no database, as for detect_integration_test.go:
//
//	PROVISION_TEST_PG_URI=postgres://postgres:postgres@localhost:5432 \
//	  go test -tags integration ./internal/pg/...
package pg

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApplyTuningAndLoadSQL applies the tuning settings and loads a tiny SQL
// file, asserting both succeed against a live Postgres.
func TestApplyTuningAndLoadSQL(t *testing.T) {
	uri := serverURI(t) + "/postgres"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := ApplyTuning(ctx, uri); err != nil {
		t.Fatalf("ApplyTuning: %v", err)
	}

	const sql = `
CREATE TABLE IF NOT EXISTS provision_test_marker (id int PRIMARY KEY);
INSERT INTO provision_test_marker (id) VALUES (1) ON CONFLICT DO NOTHING;
DROP TABLE provision_test_marker;
`
	if err := LoadSQLFile(ctx, uri, writeSQL(t, sql)); err != nil {
		t.Fatalf("LoadSQLFile: %v", err)
	}
}

// A failing statement stops the load, and the load reports it. Before, psql
// carried on and exited 0.
func TestLoadSQLFileStopsAtTheFirstError(t *testing.T) {
	uri := serverURI(t) + "/postgres"
	runSQL(t, uri, "DROP TABLE IF EXISTS provision_test_t")
	t.Cleanup(func() { runSQL(t, uri, "DROP TABLE IF EXISTS provision_test_t") })

	err := LoadSQLFile(context.Background(), uri, writeSQL(t,
		"CREATE TABLE provision_test_t (i int);\nSELEC broken;\nINSERT INTO provision_test_t VALUES (1);\n"))
	if err == nil {
		t.Fatal("a file with a syntax error loaded without an error")
	}
	if n := count(t, uri, "provision_test_t"); n != 0 {
		t.Errorf("the statement after the error ran: %d rows", n)
	}
}

// The published dumps start with CREATE DATABASE. Loaded a second time, the
// load stops there, fails, and leaves the first load's rows as they were.
func TestLoadSQLFileTwiceDoesNotAppend(t *testing.T) {
	base := serverURI(t)
	admin := base + "/postgres"
	const db = "mina_provision_load_test"
	runSQL(t, admin, "DROP DATABASE IF EXISTS "+db)
	t.Cleanup(func() { runSQL(t, admin, "DROP DATABASE IF EXISTS "+db) })

	dump := writeSQL(t, "CREATE DATABASE "+db+";\n\\connect "+db+"\n"+
		"CREATE TABLE blocks (id int);\nINSERT INTO blocks VALUES (1), (2);\n")
	ctx := context.Background()
	if err := LoadSQLFile(ctx, admin, dump); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := LoadSQLFile(ctx, admin, dump); err == nil {
		t.Error("a second load over the database it creates reported success")
	}
	if n := count(t, base+"/"+db, "blocks"); n != 2 {
		t.Errorf("after the second load blocks has %d rows, want 2", n)
	}
}

// ALTER SYSTEM needs a superuser. psql used to report only the last -c, so
// whether this failed depended on map order. Run many times to catch that.
func TestApplyTuningAsANonSuperuserFails(t *testing.T) {
	base := serverURI(t)
	admin := base + "/postgres"
	runSQL(t, admin, "DROP ROLE IF EXISTS mina_provision_tuner")
	t.Cleanup(func() { runSQL(t, admin, "DROP ROLE IF EXISTS mina_provision_tuner") })
	runSQL(t, admin, "CREATE ROLE mina_provision_tuner LOGIN PASSWORD 't'")

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("mina_provision_tuner", "t")
	for i := 0; i < 10; i++ {
		if err := ApplyTuning(context.Background(), u.String()); err == nil {
			t.Fatalf("run %d: ALTER SYSTEM as a non-superuser reported success", i)
		}
	}
}

func writeSQL(t *testing.T, sql string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "load.sql")
	if err := os.WriteFile(path, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func count(t *testing.T, uri, table string) int {
	t.Helper()
	out, err := exec.Command("psql", "-X", "-tA", "-d", uri, "-c", "SELECT count(*) FROM "+table).Output()
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	n := 0
	for _, c := range strings.TrimSpace(string(out)) {
		n = n*10 + int(c-'0')
	}
	return n
}
