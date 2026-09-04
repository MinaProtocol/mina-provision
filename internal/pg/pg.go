// Package pg wraps the small set of psql operations mina-provision performs.
//
// We shell out to psql rather than using a native Go Postgres driver because
// (a) loading a multi-gigabyte SQL dump streams better via psql's -f than via
// any go-pg flavor and (b) the operator already needs postgresql-client
// installed for ongoing maintenance, so there's no extra dependency to ship.
package pg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// ApplyTuning runs ALTER SYSTEM for each TuningSettings entry.
//
// Note: ALTER SYSTEM writes to postgresql.auto.conf and requires a Postgres
// restart to take effect. Most operators will restart the postgres container
// after the database is provisioned; see README.md.
func ApplyTuning(ctx context.Context, uri string) error {
	args := []string{uri}
	for k, v := range TuningSettings {
		args = append(args, "-c", fmt.Sprintf("ALTER SYSTEM SET %s = %s", k, v))
	}
	return run(ctx, "psql", args...)
}

// LoadSQLFile applies the contents of sqlPath to the database at uri.
func LoadSQLFile(ctx context.Context, uri, sqlPath string) error {
	slog.Info("loading sql dump", "path", sqlPath)
	return run(ctx, "psql", uri, "-f", sqlPath)
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
	cmd := exec.CommandContext(ctx, "psql", uri, "-tAc", sql)
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
	// HasArchive is true only on positive evidence: the blocks table exists
	// and can be read. A database that cannot be reached, or does not exist
	// yet, leaves this false.
	HasArchive bool

	// MaxHeight and Blocks describe what is there, for reporting. They are
	// meaningful only when HasArchive is true.
	MaxHeight int
	Blocks    int
}

// DetectArchive reports whether uri already holds an archive.
//
// Every failure is reported as "no archive found", never as an error, and the
// reason is logged. The caller acts on this to decide whether to skip work, so
// acting on anything less than positive evidence would be wrong in both
// directions: a database that does not exist yet must be filled, and one that
// cannot be reached must not be declared empty.
func DetectArchive(ctx context.Context, uri string) Presence {
	// to_regclass returns NULL rather than raising when the table is absent,
	// so this one statement is safe against an empty or foreign database.
	out, err := query(ctx, uri, "SELECT to_regclass('public.blocks') IS NOT NULL")
	if err != nil {
		slog.Debug("could not inspect the target database; treating it as empty", "err", err)
		return Presence{}
	}
	if strings.TrimSpace(out) != "t" {
		slog.Debug("no blocks table in the target database")
		return Presence{}
	}

	out, err = query(ctx, uri, "SELECT count(*), COALESCE(MAX(height), 0) FROM blocks")
	if err != nil {
		slog.Debug("blocks table present but unreadable; treating it as empty", "err", err)
		return Presence{}
	}
	count, height, err := parseCountAndHeight(out)
	if err != nil {
		slog.Debug("could not read the blocks table", "err", err)
		return Presence{}
	}
	if count == 0 {
		// A schema with no rows is a database waiting to be filled, not an
		// archive worth protecting.
		slog.Debug("blocks table is present but empty")
		return Presence{}
	}
	return Presence{HasArchive: true, MaxHeight: height, Blocks: count}
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
