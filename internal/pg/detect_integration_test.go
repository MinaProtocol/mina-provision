//go:build integration

// Live checks for archive detection, against a real PostgreSQL. Excluded from
// the default run by the `integration` build tag. Provide a server URI with no
// database, and the test creates and drops its own:
//
//	PROVISION_TEST_PG_URI=postgres://postgres:postgres@localhost:5432 \
//	  go test -tags integration ./internal/pg/...
package pg

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestDetectArchiveOnlyReportsPositiveEvidence(t *testing.T) {
	base := os.Getenv("PROVISION_TEST_PG_URI")
	if base == "" {
		t.Skip("PROVISION_TEST_PG_URI not set")
	}
	ctx := context.Background()
	admin := base + "/postgres"
	target := base + "/mina_provision_detect_test"

	// A database that does not exist is not an archive, and must not be
	// reported as one: a first run would then be skipped and never happen.
	if p := DetectArchive(ctx, target); p.HasArchive {
		t.Fatal("a database that does not exist was reported as holding an archive")
	}

	runSQL(t, admin, "DROP DATABASE IF EXISTS mina_provision_detect_test")
	runSQL(t, admin, "CREATE DATABASE mina_provision_detect_test")
	defer runSQL(t, admin, "DROP DATABASE IF EXISTS mina_provision_detect_test")

	if p := DetectArchive(ctx, target); p.HasArchive {
		t.Error("an empty database was reported as holding an archive")
	}

	runSQL(t, target, "CREATE TABLE blocks (id serial primary key, height bigint)")
	if p := DetectArchive(ctx, target); p.HasArchive {
		t.Error("a schema with no rows was reported as holding an archive")
	}

	runSQL(t, target, "INSERT INTO blocks (height) SELECT generate_series(1, 428)")
	p := DetectArchive(ctx, target)
	if !p.HasArchive {
		t.Fatal("a populated blocks table was not detected")
	}
	if p.Blocks != 428 || p.MaxHeight != 428 {
		t.Errorf("got %d blocks, highest %d; want 428 and 428", p.Blocks, p.MaxHeight)
	}
}

// An unreachable server must be reported as no archive, never as an error, and
// never as a reason to skip.
func TestDetectArchiveOnAnUnreachableServer(t *testing.T) {
	if p := DetectArchive(context.Background(),
		"postgres://nobody@127.0.0.1:1/does_not_exist"); p.HasArchive {
		t.Error("an unreachable server was reported as holding an archive")
	}
}

func runSQL(t *testing.T, uri, sql string) {
	t.Helper()
	cmd := exec.Command("psql", uri, "-v", "ON_ERROR_STOP=1", "-c", sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psql %q: %v\n%s", sql, err, out)
	}
}
