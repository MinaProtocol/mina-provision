package pg

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// DumpHeader is what the start of a plain-format pg_dump file says about how
// it restores.
type DumpHeader struct {
	// Database is the database the dump creates and connects to, from its
	// CREATE DATABASE and \connect lines (pg_dump --create). It is "" for a
	// dump without them, which restores into the database psql connects to.
	Database string

	// Restrict is true when the dump uses \restrict. pg_dump writes it from
	// the 2025 security releases on; an older psql rejects it.
	Restrict bool
}

// headerLines bounds the scan. The lines that matter come before the first
// table; a dump without CREATE DATABASE may define many functions and types
// first, but not this many lines of them.
const headerLines = 20000

// ScanDumpHeader reads the start of the SQL dump at path, up to the first
// CREATE TABLE or COPY.
//
// It fails when the dump names two different databases, so that a dump it
// does not understand is not taken to restore where it does not.
func ScanDumpHeader(path string) (DumpHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return DumpHeader{}, err
	}
	defer f.Close()

	var h DumpHeader
	set := func(db, line string) error {
		if h.Database != "" && h.Database != db {
			return fmt.Errorf("%s names two databases, %q and %q (at %q)", path, h.Database, db, line)
		}
		h.Database = db
		return nil
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for n := 0; n < headerLines && sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "CREATE TABLE "), strings.HasPrefix(line, "COPY "):
			return h, nil
		case strings.HasPrefix(line, `\restrict `):
			h.Restrict = true
		case strings.HasPrefix(line, "CREATE DATABASE "):
			db, err := identifier(strings.TrimPrefix(line, "CREATE DATABASE "))
			if err != nil {
				return h, fmt.Errorf("%s: %w", path, err)
			}
			if err := set(db, line); err != nil {
				return h, err
			}
		case strings.HasPrefix(line, `\connect `):
			db, err := connectTarget(strings.TrimPrefix(line, `\connect `))
			if err != nil {
				return h, fmt.Errorf("%s: %w", path, err)
			}
			if err := set(db, line); err != nil {
				return h, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return h, fmt.Errorf("read %s: %w", path, err)
	}
	return h, nil
}

// identifier reads the SQL identifier at the start of s: a plain name, which
// PostgreSQL folds to lower case, or a double-quoted one.
func identifier(s string) (string, error) {
	if strings.HasPrefix(s, `"`) {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				return b.String(), nil
			}
			b.WriteByte(s[i])
		}
		return "", fmt.Errorf("unterminated quoted name in %q", s)
	}
	end := strings.IndexAny(s, " \t;")
	if end < 0 {
		end = len(s)
	}
	if end == 0 {
		return "", fmt.Errorf("no database name in %q", s)
	}
	return strings.ToLower(s[:end]), nil
}

// connectTarget reads the database of a \connect line as pg_dump writes it:
// "\connect archive", "\connect "Odd Name"", or, for a name psql would not
// parse, "\connect -reuse-previous=on "dbname='odd name'"".
func connectTarget(s string) (string, error) {
	if rest, ok := strings.CutPrefix(s, "-reuse-previous=on "); ok {
		m := regexp.MustCompile(`^"dbname='((?:[^'\\]|\\.|'')*)'"`).FindStringSubmatch(rest)
		if m == nil {
			return "", fmt.Errorf(`cannot read the database of \connect %s`, s)
		}
		v := strings.ReplaceAll(m[1], `\'`, `'`)
		v = strings.ReplaceAll(v, `''`, `'`)
		return strings.ReplaceAll(v, `\\`, `\`), nil
	}
	if strings.HasPrefix(s, `"`) {
		return identifier(s)
	}
	// psql does not fold the case of a \connect argument.
	if end := strings.IndexAny(s, " \t"); end >= 0 {
		s = s[:end]
	}
	if s == "" {
		return "", fmt.Errorf(`\connect with no database`)
	}
	return s, nil
}

// restrictSince is the first minor release of each major version whose psql
// understands \restrict (the fix for CVE-2025-8714). Every major version
// after the last one here has it from its first release.
var restrictSince = map[int]int{13: 22, 14: 19, 15: 14, 16: 10, 17: 6}

// RestrictMinimum describes the psql versions that understand \restrict, for
// error messages.
const RestrictMinimum = "17.6 or later (on older branches: 16.10, 15.14, 14.19 or 13.22)"

// SupportsRestrict reports whether psql major.minor understands \restrict.
func SupportsRestrict(major, minor int) bool {
	if major > 17 {
		return true
	}
	since, ok := restrictSince[major]
	return ok && minor >= since
}

// ClientVersion returns the major and minor version of the psql on PATH.
func ClientVersion(ctx context.Context) (major, minor int, err error) {
	out, err := exec.CommandContext(ctx, "psql", "--version").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("psql --version: %w", err)
	}
	return parseClientVersion(string(out))
}

var clientVersionRE = regexp.MustCompile(`\(PostgreSQL\)\s+(\d+)(?:\.(\d+))?`)

// parseClientVersion reads the output of psql --version, for example
// "psql (PostgreSQL) 17.6 (Debian 17.6-1.pgdg12+1)" or "psql (PostgreSQL) 18beta1".
func parseClientVersion(out string) (major, minor int, err error) {
	m := clientVersionRE.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, fmt.Errorf("cannot read the psql version from %q", strings.TrimSpace(out))
	}
	major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major, minor, nil
}
