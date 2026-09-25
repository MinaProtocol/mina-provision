//go:build integration

// Live checks for archive detection, against a real PostgreSQL. Excluded from
// the default run by the `integration` build tag. Provide a superuser URI with
// no database, and the tests create and drop their own:
//
//	PROVISION_TEST_PG_URI=postgres://postgres:postgres@localhost:5432 \
//	  go test -tags integration ./internal/pg/...
package pg

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

const detectDB = "mina_provision_detect_test"

func serverURI(t *testing.T) string {
	t.Helper()
	base := os.Getenv("PROVISION_TEST_PG_URI")
	if base == "" {
		t.Skip("PROVISION_TEST_PG_URI not set")
	}
	return base
}

func TestDetectArchiveOnlyReportsPositiveEvidence(t *testing.T) {
	base := serverURI(t)
	ctx := context.Background()
	admin := base + "/postgres"
	target := base + "/" + detectDB

	runSQL(t, admin, "DROP DATABASE IF EXISTS "+detectDB)
	t.Cleanup(func() { runSQL(t, admin, "DROP DATABASE IF EXISTS "+detectDB) })

	// The URI is only the way to reach the server. Whatever database it
	// names, the database that is checked is the one given.
	for _, uri := range []string{admin, target, base} {
		p, err := DetectArchive(ctx, uri, detectDB)
		if err != nil {
			t.Fatalf("via %s: a database that does not exist is empty, not an error: %v", redact(uri), err)
		}
		if p.HasArchive {
			t.Fatalf("via %s: a database that does not exist was reported as holding an archive", redact(uri))
		}
	}

	runSQL(t, admin, "CREATE DATABASE "+detectDB)
	if p, err := DetectArchive(ctx, admin, detectDB); err != nil || p.HasArchive {
		t.Errorf("an empty database: got %+v, %v; want no archive and no error", p, err)
	}

	runSQL(t, target, "CREATE TABLE blocks (id serial primary key, height bigint)")
	if p, err := DetectArchive(ctx, admin, detectDB); err != nil || p.HasArchive {
		t.Errorf("a schema with no rows: got %+v, %v; want no archive and no error", p, err)
	}

	runSQL(t, target, "INSERT INTO blocks (height) SELECT generate_series(1, 428)")
	for _, uri := range []string{admin, target, base} {
		p, err := DetectArchive(ctx, uri, detectDB)
		if err != nil {
			t.Fatalf("via %s: %v", redact(uri), err)
		}
		if !p.HasArchive || p.Blocks != 428 || p.MaxHeight != 428 {
			t.Errorf("via %s: got %+v, want an archive of 428 blocks up to 428", redact(uri), p)
		}
	}
}

// A server that cannot be reached is an error, never "no archive": that would
// let a dump be loaded over a live archive the probe could not see.
func TestDetectArchiveOnAnUnreachableServer(t *testing.T) {
	old := ProbeRetryFor
	ProbeRetryFor = 3 * time.Second
	t.Cleanup(func() { ProbeRetryFor = old })

	start := time.Now()
	p, err := DetectArchive(context.Background(), "postgres://nobody@127.0.0.1:1/postgres", "archive")
	if err == nil {
		t.Fatalf("an unreachable server was reported as %+v with no error", p)
	}
	if time.Since(start) < time.Second {
		t.Errorf("gave up after %s, without trying again", time.Since(start))
	}
}

// A role that cannot read the blocks table cannot see whether it holds an
// archive, and must not be told that it does not.
func TestDetectArchiveWithoutPermission(t *testing.T) {
	base := serverURI(t)
	admin := base + "/postgres"
	target := base + "/" + detectDB

	runSQL(t, admin, "DROP DATABASE IF EXISTS "+detectDB)
	runSQL(t, admin, "DROP ROLE IF EXISTS mina_provision_reader")
	t.Cleanup(func() {
		runSQL(t, admin, "DROP DATABASE IF EXISTS "+detectDB)
		runSQL(t, admin, "DROP ROLE IF EXISTS mina_provision_reader")
	})
	runSQL(t, admin, "CREATE DATABASE "+detectDB)
	runSQL(t, target, "CREATE TABLE blocks (id serial primary key, height bigint)")
	runSQL(t, target, "INSERT INTO blocks (height) VALUES (1)")
	runSQL(t, admin, "CREATE ROLE mina_provision_reader LOGIN PASSWORD 'r'")

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("mina_provision_reader", "r")
	if p, err := DetectArchive(context.Background(), u.String()+"/postgres", detectDB); err == nil {
		t.Fatalf("a blocks table the role cannot read was reported as %+v with no error", p)
	}
}

func runSQL(t *testing.T, uri, sql string) {
	t.Helper()
	cmd := exec.Command("psql", "-X", "-v", "ON_ERROR_STOP=1", "-d", uri, "-c", sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psql %q: %v\n%s", sql, err, out)
	}
}

func redact(uri string) string {
	if u, err := url.Parse(uri); err == nil {
		return u.Redacted()
	}
	return "<uri>"
}
