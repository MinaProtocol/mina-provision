//go:build integration

// End-to-end checks of the archive command against a real PostgreSQL, with a
// file provider and a small dump shaped like the published ones. Excluded
// from the default run by the `integration` build tag. Provide a superuser
// URI with no database:
//
//	PROVISION_TEST_PG_URI=postgres://postgres:postgres@localhost:5432 \
//	  go test -tags integration ./cmd/...
package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MinaProtocol/mina-provision/internal/pg"
)

// The database the fixture dump creates. The published dumps create
// "archive"; a different name keeps the test away from a real one.
const e2eDB = "mina_provision_e2e"

const e2eDate, e2eHour = "2026-09-22", "0000"

// e2eDump is the fixture: the same header as a published dump (made with
// pg_dump --create, from the 2025 releases on), then a table and its rows.
func e2eDump(db string) string {
	return fmt.Sprintf(`--
-- PostgreSQL database dump
--

\restrict e2e

SET client_encoding = 'UTF8';

CREATE DATABASE %[1]s WITH TEMPLATE = template0 ENCODING = 'UTF8';

\unrestrict e2e
\connect %[1]s
\restrict e2e

CREATE TABLE public.blocks (id integer PRIMARY KEY, height bigint);

COPY public.blocks (id, height) FROM stdin;
1	1
2	2
\.

\unrestrict e2e
`, db)
}

type e2e struct {
	t        *testing.T
	server   string // superuser URI with no database
	dumps    string // the file provider's directory
	provider string // the provider configuration file
}

func newE2E(t *testing.T, database string) *e2e {
	t.Helper()
	server := os.Getenv("PROVISION_TEST_PG_URI")
	if server == "" {
		t.Skip("PROVISION_TEST_PG_URI not set")
	}
	major, minor, err := pg.ClientVersion(context.Background())
	if err != nil || !pg.SupportsRestrict(major, minor) {
		t.Skipf("psql %d.%d cannot load a dump with \\restrict (%v)", major, minor, err)
	}

	e := &e2e{t: t, server: server, dumps: t.TempDir()}
	e.sql("postgres", "DROP DATABASE IF EXISTS "+e2eDB)
	e.sql("postgres", "DROP DATABASE IF EXISTS mina_provision_e2e_named")
	t.Cleanup(func() {
		e.sql("postgres", "DROP DATABASE IF EXISTS "+e2eDB)
		e.sql("postgres", "DROP DATABASE IF EXISTS mina_provision_e2e_named")
	})

	e.writeDump(e2eDump(e2eDB))
	dbLine := ""
	if database != "" {
		dbLine = ", database: " + database
	}
	e.provider = filepath.Join(t.TempDir(), "p.yaml")
	cfg := fmt.Sprintf(`version: 1
default_provider: local
providers:
  local:
    networks:
      mainnet:
        archive_dump: {backend: file, path: %s, name: "mainnet-archive-dump-{date}_{hour}.sql.tar.gz", checksum: none%s}
`, e.dumps, dbLine)
	if err := os.WriteFile(e.provider, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	old := pg.ProbeRetryFor
	pg.ProbeRetryFor = 2 * time.Second
	t.Cleanup(func() { pg.ProbeRetryFor = old })
	return e
}

func (e *e2e) writeDump(sql string) {
	e.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	name := fmt.Sprintf("mainnet-archive-dump-%s_%s.sql", e2eDate, e2eHour)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(sql)), Typeflag: tar.TypeReg}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tw.Write([]byte(sql)); err != nil {
		e.t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(filepath.Join(e.dumps, name+".tar.gz"), buf.Bytes(), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// run runs the archive command with the given flags, in a new working
// directory unless --work-dir is among them.
func (e *e2e) run(pgDB, ifPresent string, extra ...string) (string, error) {
	e.t.Helper()
	providerConfig, network, providerName = e.provider, "mainnet", ""
	archivePgURI = e.server + "/" + pgDB
	archiveDate, archiveHour = e2eDate, e2eHour
	archiveWorkDir, archiveSkipPg, archiveIfPresent = ".", false, ifPresent
	for _, f := range extra {
		switch {
		case f == "--skip-pg":
			archiveSkipPg = true
		case strings.HasPrefix(f, "--work-dir="):
			archiveWorkDir = strings.TrimPrefix(f, "--work-dir=")
		}
	}
	chdir(e.t, e.t.TempDir())

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		e.t.Fatal(err)
	}
	os.Stdout = w
	runErr := runArchive(nil, nil)
	w.Close()
	os.Stdout = stdout
	out, _ := readAll(r)
	return out, runErr
}

func (e *e2e) sql(db, sql string) string {
	e.t.Helper()
	out, err := exec.Command("psql", "-X", "-v", "ON_ERROR_STOP=1", "-tA", "-d", e.server+"/"+db, "-c", sql).CombinedOutput()
	if err != nil {
		e.t.Fatalf("psql %q: %v\n%s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *e2e) rows() string { return e.sql(e2eDB, "SELECT count(*) FROM blocks") }

// #7: with the default --work-dir the dump is extracted into the current
// directory, and a --work-dir that does not exist is created.
func TestArchiveWorkDir(t *testing.T) {
	e := newE2E(t, e2eDB)
	if _, err := e.run("postgres", "import", "--skip-pg"); err != nil {
		t.Fatalf("default --work-dir: %v", err)
	}
	if _, err := os.Stat(fmt.Sprintf("mainnet-archive-dump-%s_%s.sql", e2eDate, e2eHour)); err != nil {
		t.Errorf("the .sql is not in the current directory: %v", err)
	}

	missing := filepath.Join(t.TempDir(), "a", "b")
	if _, err := e.run("postgres", "import", "--skip-pg", "--work-dir="+missing); err != nil {
		t.Fatalf("--work-dir that does not exist: %v", err)
	}
}

// #8 and #9: the first run loads into the database the dump creates,
// whatever --pg-uri names; later runs with skip see it and download nothing;
// a later import fails at CREATE DATABASE and appends nothing.
func TestArchiveRestoreAndRerun(t *testing.T) {
	e := newE2E(t, e2eDB)

	out, err := e.run("postgres", "skip")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !strings.Contains(out, "provisioned") || e.rows() != "2" {
		t.Fatalf("first run: output %q, %s rows; want provisioned and 2", out, e.rows())
	}

	// Remove the dumps: a run that downloads now fails, so a run that
	// succeeds did not download.
	if err := os.RemoveAll(e.dumps); err != nil {
		t.Fatal(err)
	}
	e.sql("postgres", "CREATE DATABASE mina_provision_e2e_named")
	for _, db := range []string{"postgres", e2eDB, "mina_provision_e2e_named"} {
		out, err := e.run(db, "skip")
		if err != nil || !strings.Contains(out, "already holds an archive") {
			t.Errorf("--pg-uri naming %s: %q, %v; want skip without download", db, out, err)
		}
		if _, err := e.run(db, "fail"); err == nil {
			t.Errorf("--pg-uri naming %s: --if-present=fail succeeded over an archive", db)
		}
	}

	if err := os.MkdirAll(e.dumps, 0o755); err != nil {
		t.Fatal(err)
	}
	e.writeDump(e2eDump(e2eDB))
	if out, err := e.run("postgres", "import"); err == nil {
		t.Errorf("import over an existing archive succeeded: %q", out)
	}
	if e.rows() != "2" {
		t.Errorf("import over an existing archive changed it: %s rows, want 2", e.rows())
	}
}

// #9: a probe that cannot run is an error, and nothing is downloaded.
func TestArchiveUnreachableServer(t *testing.T) {
	// A copy: the cleanup that newE2E registered still needs the real server.
	e := *newE2E(t, e2eDB)
	e.server = "postgres://nobody@127.0.0.1:1"
	for _, mode := range []string{"skip", "fail"} {
		if _, err := e.run("postgres", mode); err == nil {
			t.Errorf("--if-present=%s against an unreachable server succeeded", mode)
		}
		if matches, _ := filepath.Glob("*.tar.gz"); len(matches) != 0 {
			t.Errorf("--if-present=%s downloaded %v", mode, matches)
		}
	}
}

// #9: a dump that creates a database other than the configured one is refused
// before anything is loaded.
func TestArchiveRefusesADumpForAnotherDatabase(t *testing.T) {
	e := newE2E(t, "somewhere_else")
	if out, err := e.run("postgres", "import"); err == nil {
		t.Fatalf("a dump for %s was loaded while the provider says somewhere_else: %q", e2eDB, out)
	}
	if n := e.sql("postgres", "SELECT count(*) FROM pg_database WHERE datname = '"+e2eDB+"'"); n != "0" {
		t.Error("the dump was loaded")
	}

	e = newE2E(t, "")
	if _, err := e.run("postgres", "import"); err == nil {
		t.Fatal("a dump that creates its database was loaded while the provider names none")
	}
}

// --pg-uri may name the database the dump creates before it exists: the load
// connects through a maintenance database.
func TestArchivePgURINamingTheDumpDatabase(t *testing.T) {
	e := newE2E(t, e2eDB)
	if out, err := e.run(e2eDB, "skip"); err != nil {
		t.Fatalf("--pg-uri naming the database the dump creates: %q, %v", out, err)
	}
	if e.rows() != "2" {
		t.Errorf("%s rows, want 2", e.rows())
	}
}

// A new PostgreSQL container with POSTGRES_DB set to the dump's database
// creates that database, empty. The dump's CREATE DATABASE would fail on it;
// the dump is restored into it instead.
func TestArchiveIntoAnExistingEmptyDatabase(t *testing.T) {
	e := newE2E(t, e2eDB)
	e.sql("postgres", "CREATE DATABASE "+e2eDB)
	if out, err := e.run(e2eDB, "skip"); err != nil {
		t.Fatalf("restore into an existing, empty database: %q, %v", out, err)
	}
	if e.rows() != "2" {
		t.Errorf("%s rows, want 2", e.rows())
	}
}

// A database that holds anything of its own is not restored into: the dump's
// CREATE DATABASE fails, and the database is left as it was.
func TestArchiveLeavesANonEmptyDatabaseAlone(t *testing.T) {
	e := newE2E(t, e2eDB)
	e.sql("postgres", "CREATE DATABASE "+e2eDB)
	e.sql(e2eDB, "CREATE TABLE notes (body text); INSERT INTO notes VALUES ('keep me')")
	if out, err := e.run(e2eDB, "import"); err == nil {
		t.Fatalf("restored into a database that holds a table: %q", out)
	}
	if got := e.sql(e2eDB, "SELECT body FROM notes"); got != "keep me" {
		t.Errorf("notes = %q, want the row to be untouched", got)
	}
	if got := e.sql(e2eDB, "SELECT to_regclass('public.blocks') IS NULL"); got != "t" {
		t.Error("the dump's blocks table was created in the existing database")
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func readAll(f *os.File) (string, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(f)
	f.Close()
	return b.String(), err
}
