// Package pg wraps the small set of psql operations mina-provision performs.
//
// We shell out to psql rather than using a native Go Postgres driver because
// (a) loading a multi-gigabyte SQL dump streams better via psql's -f than via
// any go-pg flavor and (b) the operator already needs postgresql-client
// installed for ongoing maintenance, so there's no extra dependency to ship.
package pg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TuningSettings are the ALTER SYSTEM values applied before loading the
// archive dump. Mirrors the values the existing Rosetta docker-compose
// bootstrap service uses.
var TuningSettings = map[string]string{
	"max_connections":                "500",
	"max_locks_per_transaction":      "100",
	"max_pred_locks_per_relation":    "100",
	"max_pred_locks_per_transaction": "5000",
}

// psqlArgs are the arguments every psql call starts with.
//
//   - -X skips ~/.psqlrc, which could change how a script runs.
//   - ON_ERROR_STOP=1 makes psql stop at the first failing statement and exit
//     non-zero. Without it psql carries on after an error and exits 0, so a
//     failed restore or tuning looked like a success.
//   - -d keeps a URI that starts with "-" from being read as an option.
func psqlArgs(uri string) []string {
	return []string{"-X", "-v", "ON_ERROR_STOP=1", "-d", uri}
}

// ApplyTuning runs ALTER SYSTEM for each TuningSettings entry.
//
// Note: ALTER SYSTEM writes to postgresql.auto.conf and requires a Postgres
// restart to take effect. Most operators will restart the postgres container
// after the database is provisioned; see README.md.
func ApplyTuning(ctx context.Context, uri string) error {
	// Sorted, so the statements run in the same order every time. With a map
	// order a failure could be reported or not from one run to the next.
	keys := make([]string, 0, len(TuningSettings))
	for k := range TuningSettings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := psqlArgs(uri)
	for _, k := range keys {
		args = append(args, "-c", fmt.Sprintf("ALTER SYSTEM SET %s = %s", k, TuningSettings[k]))
	}
	return run(ctx, "psql", args...)
}

// LoadSQLFile applies the contents of sqlPath to the database at uri.
//
// The first failing statement stops the load with an error. The load is not
// one transaction: the published dumps start with CREATE DATABASE, which
// cannot run inside one. A failure can therefore leave what was loaded before
// it in place; the dumps create their own database, so a failure on a server
// that already has it stops at that first statement and changes nothing.
func LoadSQLFile(ctx context.Context, uri, sqlPath string) error {
	slog.Info("loading sql dump", "path", sqlPath)
	return run(ctx, "psql", append(psqlArgs(uri), "-f", sqlPath)...)
}

// MaxBlockHeight returns the highest height present in the archive DB's
// `blocks` table, or 0 when the table is empty. It is used to work out which
// precomputed blocks still need to be backfilled after a dump restore.
func MaxBlockHeight(ctx context.Context, uri string) (int, error) {
	out, err := query(ctx, uri, "SELECT COALESCE(MAX(height), 0) FROM blocks")
	if err != nil {
		return 0, err
	}
	return parseMaxHeight(out)
}

// parseMaxHeight parses the raw stdout of the MAX(height) query into an int.
func parseMaxHeight(out string) (int, error) {
	s := strings.TrimSpace(out)
	h, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse max block height %q: %w", s, err)
	}
	return h, nil
}

// HeightsBetween returns the distinct block heights present in [lo, hi]
// (inclusive), ascending. Used to verify a catchup left no gap.
func HeightsBetween(ctx context.Context, uri string, lo, hi int) ([]int, error) {
	out, err := query(ctx, uri, fmt.Sprintf(
		"SELECT DISTINCT height FROM blocks WHERE height BETWEEN %d AND %d ORDER BY height", lo, hi))
	if err != nil {
		return nil, err
	}
	return parseHeights(out)
}

// parseHeights parses newline-separated integer heights from psql -tA output.
func parseHeights(out string) ([]int, error) {
	var heights []int
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		h, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("parse height %q: %w", line, err)
		}
		heights = append(heights, h)
	}
	return heights, nil
}

// query runs a single SQL statement with psql in tuples-only, unaligned mode
// (-tA) and returns its stdout. Stderr is streamed through so psql connection
// errors stay visible.
func query(ctx context.Context, uri, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "psql", append(psqlArgs(uri), "-tA", "-c", sql)...)
	cmd.Stderr = os.Stderr
	slog.Debug("exec", "cmd", "psql", "sql", sql)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("psql query: %w", err)
	}
	return string(out), nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	slog.Debug("exec", "cmd", name, "args", args)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Presence describes what a target database already holds.
type Presence struct {
	// HasArchive is true when the database exists and its blocks table
	// holds at least one row.
	HasArchive bool

	// MaxHeight and Blocks describe what is there, for reporting. They are
	// meaningful only when HasArchive is true.
	MaxHeight int
	Blocks    int
}

// ProbeRetryFor is how long DetectArchive keeps trying while the server
// cannot be reached. A compose stack restarts PostgreSQL and the bootstrap
// together, and the probe can run while PostgreSQL is still starting.
var ProbeRetryFor = 60 * time.Second

// DetectArchive reports whether the database db on the server at uri already
// holds an archive. uri is used only to reach the server; see MaintenanceURI.
//
// "No archive" is returned only on positive evidence: the database does not
// exist, it has no blocks table, or the table has no rows. Anything that
// stops the check -- a server that cannot be reached, is still starting, or
// refuses to show the table -- is an error. Treating it as "no archive" would
// let the caller restore a dump over a live archive it could not see.
func DetectArchive(ctx context.Context, uri, db string) (Presence, error) {
	admin, err := MaintenanceURI(uri, db)
	if err != nil {
		return Presence{}, err
	}
	out, err := queryRetry(ctx, admin,
		fmt.Sprintf("SELECT count(*) FROM pg_database WHERE datname = %s", quoteLiteral(db)))
	if err != nil {
		return Presence{}, fmt.Errorf("check whether database %q exists: %w", db, err)
	}
	if strings.TrimSpace(out) == "0" {
		slog.Debug("the target database does not exist", "database", db)
		return Presence{}, nil
	}

	target, err := WithDatabase(uri, db)
	if err != nil {
		return Presence{}, err
	}
	// to_regclass returns NULL rather than raising when the table is absent.
	out, err = queryRetry(ctx, target, "SELECT to_regclass('public.blocks') IS NOT NULL")
	if err != nil {
		return Presence{}, fmt.Errorf("inspect database %q: %w", db, err)
	}
	if strings.TrimSpace(out) != "t" {
		slog.Debug("no blocks table in the target database", "database", db)
		return Presence{}, nil
	}

	out, err = queryRetry(ctx, target, "SELECT count(*), COALESCE(MAX(height), 0) FROM blocks")
	if err != nil {
		return Presence{}, fmt.Errorf("read the blocks table of database %q: %w", db, err)
	}
	count, height, err := parseCountAndHeight(out)
	if err != nil {
		return Presence{}, err
	}
	if count == 0 {
		// A schema with no rows is a database waiting to be filled, not an
		// archive worth protecting.
		slog.Debug("blocks table is present but empty", "database", db)
		return Presence{}, nil
	}
	return Presence{HasArchive: true, MaxHeight: height, Blocks: count}, nil
}

// queryRetry is query, tried again for up to ProbeRetryFor while psql reports
// that it could not reach the server (exit status 2). Other failures, such as
// a missing permission, are returned at once.
func queryRetry(ctx context.Context, uri, sql string) (string, error) {
	deadline := time.Now().Add(ProbeRetryFor)
	wait := time.Second
	for {
		out, err := query(ctx, uri, sql)
		var exit *exec.ExitError
		if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 2 || time.Now().Add(wait).After(deadline) {
			return out, err
		}
		slog.Info("the server cannot be reached yet; trying again", "in", wait)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
		if wait < 8*time.Second {
			wait *= 2
		}
	}
}

// MaintenanceDatabases are the databases that exist on every server and that
// a --pg-uri can name only to reach the server.
var MaintenanceDatabases = []string{"postgres", "template1"}

// DatabaseOf returns the database a postgres:// URI names, or "" when it
// names none.
func DatabaseOf(uri string) (string, error) {
	u, err := parseURI(uri)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

// WithDatabase returns uri with its database replaced by db. Everything else,
// including the query parameters, is kept.
func WithDatabase(uri, db string) (string, error) {
	u, err := parseURI(uri)
	if err != nil {
		return "", err
	}
	u.Path = "/" + db
	u.RawPath = ""
	return u.String(), nil
}

// MaintenanceURI returns a URI to reach the server at uri while db may not
// exist yet. That is uri itself when it names another database, and uri with
// the database "postgres" otherwise.
func MaintenanceURI(uri, db string) (string, error) {
	named, err := DatabaseOf(uri)
	if err != nil {
		return "", err
	}
	if named != "" && named != db {
		return uri, nil
	}
	return WithDatabase(uri, "postgres")
}

func parseURI(uri string) (*url.URL, error) {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		// Not echoed: a URI can hold a password.
		return nil, fmt.Errorf("--pg-uri must be a postgres:// or postgresql:// URI")
	}
	return u, nil
}

// quoteLiteral quotes s as an SQL string literal.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// parseCountAndHeight reads the "count|height" pair psql -tA prints.
func parseCountAndHeight(out string) (count, height int, err error) {
	fields := strings.Split(strings.TrimSpace(out), "|")
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected psql output %q", out)
	}
	count, err = strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse block count %q: %w", fields[0], err)
	}
	height, err = strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse max height %q: %w", fields[1], err)
	}
	return count, height, nil
}
