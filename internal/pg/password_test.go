package pg

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakePSQL is a psql that records its arguments, one per line, in
// $FAKE_PSQL_DIR/argv and its PGPASSWORD in $FAKE_PSQL_DIR/env. It answers the
// DetectArchive queries as for a database that holds an archive, and exits
// with $FAKE_PSQL_EXIT.
const fakePSQL = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done >> "$FAKE_PSQL_DIR/argv"
printf '%s\n' "$PGPASSWORD" >> "$FAKE_PSQL_DIR/env"
case "$*" in
*pg_database*) echo 1 ;;
*to_regclass*) echo t ;;
*"count(*), COALESCE"*) echo '3|7' ;;
*"MAX(height)"*) echo 7 ;;
*"DISTINCT height"*) printf '5\n6\n7\n' ;;
esac
exit "${FAKE_PSQL_EXIT:-0}"
`

// installFakePSQL puts fakePSQL first on PATH and returns the directory it
// records into.
func installFakePSQL(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake psql is a shell script")
	}
	bin, rec := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(fakePSQL), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PSQL_DIR", rec)
	t.Setenv("FAKE_PSQL_EXIT", "0")
	t.Setenv("PGPASSWORD", "")
	return rec
}

func readRecord(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// captureDebugLog sends slog output at debug level to the returned buffer
// until the test ends.
func captureDebugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// The ways a --pg-uri can hold a password. leaks are the forms of the password
// that must not be in an argument or a log line; want is what psql must get
// in PGPASSWORD.
var passwordURIs = []struct {
	name  string
	uri   string
	leaks []string
	want  string
}{
	{"user info", "postgres://u:S3cret@h:5432/db", []string{"S3cret"}, "S3cret"},
	{"url-encoded user info", "postgresql://u:S3%40c%2Fret@h/db?sslmode=disable",
		[]string{"S3%40c%2Fret", "S3@c/ret"}, "S3@c/ret"},
	{"query parameter", "postgres://u@h/db?sslmode=disable&password=S3cret",
		[]string{"S3cret"}, "S3cret"},
	{"encoded query parameter", "postgres://u@h/db?pass%77ord=S3%26cret",
		[]string{"S3%26cret", "S3&cret"}, "S3&cret"},
	{"user info and query parameter", "postgres://u:0ther@h/db?password=S3cret",
		[]string{"0ther", "S3cret"}, "S3cret"},
	{"repeated query parameter", "postgres://u@h/db?password=0ther&password=S3cret",
		[]string{"0ther", "S3cret"}, "S3cret"},
	{"no user name", "postgres://:S3cret@h/db", []string{"S3cret"}, "S3cret"},
	// libpq reads "+" as itself, not as a space, and ";" as part of a value.
	{"plus sign", "postgres://u@h/db?password=S3+cret", []string{"S3+cret", "S3 cret"}, "S3+cret"},
	{"semicolon", "postgres://u@h/db?password=S3cret;sslmode=disable",
		[]string{"S3cret"}, "S3cret;sslmode=disable"},
}

// No psql argument and no log line holds the password; psql gets it in
// PGPASSWORD only. The exported functions are all run, so that a psql call
// that does not go through psqlCmd is caught.
func TestPasswordIsNotInArgvOrLog(t *testing.T) {
	for _, tt := range passwordURIs {
		t.Run(tt.name, func(t *testing.T) {
			rec := installFakePSQL(t)
			log := captureDebugLog(t)
			ctx := context.Background()

			if p, err := DetectArchive(ctx, tt.uri, "archive"); err != nil || !p.HasArchive {
				t.Fatalf("DetectArchive = %+v, %v; want an archive", p, err)
			}
			if err := ApplyTuning(ctx, tt.uri); err != nil {
				t.Fatalf("ApplyTuning: %v", err)
			}
			if err := LoadSQLFile(ctx, tt.uri, "/dev/null"); err != nil {
				t.Fatalf("LoadSQLFile: %v", err)
			}
			if h, err := MaxBlockHeight(ctx, tt.uri); err != nil || h != 7 {
				t.Fatalf("MaxBlockHeight = %d, %v; want 7", h, err)
			}
			if hs, err := HeightsBetween(ctx, tt.uri, 5, 7); err != nil || len(hs) != 3 {
				t.Fatalf("HeightsBetween = %v, %v; want 3 heights", hs, err)
			}

			// A psql that fails: the errors must not hold the password either.
			t.Setenv("FAKE_PSQL_EXIT", "1")
			_, detectErr := DetectArchive(ctx, tt.uri, "archive")
			errs := []error{detectErr, ApplyTuning(ctx, tt.uri), LoadSQLFile(ctx, tt.uri, "/dev/null")}
			for _, err := range errs {
				if err == nil {
					t.Fatal("a psql that exits 1 was not reported")
				}
			}

			argv := readRecord(t, filepath.Join(rec, "argv"))
			env := strings.Split(strings.TrimSuffix(readRecord(t, filepath.Join(rec, "env")), "\n"), "\n")
			if len(env) != 10 {
				t.Fatalf("psql ran %d times, want 10", len(env))
			}
			for _, leak := range tt.leaks {
				if strings.Contains(argv, leak) {
					t.Errorf("psql arguments hold %q:\n%s", leak, argv)
				}
				if strings.Contains(log.String(), leak) {
					t.Errorf("the log holds %q:\n%s", leak, log)
				}
				for _, err := range errs {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("error %q holds %q", err, leak)
					}
				}
			}
			for i, pw := range env {
				if pw != tt.want {
					t.Errorf("run %d: PGPASSWORD = %q, want %q", i, pw, tt.want)
				}
			}
		})
	}
}

func TestPsqlCmd(t *testing.T) {
	t.Setenv("PGPASSWORD", "from-env")
	tests := []struct {
		name, uri string
		wantURI   string
		wantPW    string // "" when cmd.Env must be left to the environment
	}{
		{"password in user info", "postgres://u:pw@h:5432/db", "postgres://u@h:5432/db", "pw"},
		{"other parameters are kept as written", "postgres://u@h/db?application_name=a%20b+c&password=pw&sslmode=require",
			"postgres://u@h/db?application_name=a%20b+c&sslmode=require", "pw"},
		{"no user name", "postgres://:pw@h/db", "postgres://h/db", "pw"},
		{"no password", "postgres://u@h/db?sslmode=require", "postgres://u@h/db?sslmode=require", ""},
		{"no user info", "postgresql://h/db", "postgresql://h/db", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := psqlCmd(context.Background(), tt.uri, "-c", "SELECT 1")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"-X", "-v", "ON_ERROR_STOP=1", "-d", tt.wantURI, "-c", "SELECT 1"}
			if !slices.Equal(cmd.Args[1:], want) {
				t.Errorf("args = %q, want %q", cmd.Args[1:], want)
			}
			if tt.wantPW == "" {
				// nil: psql inherits PGPASSWORD and ~/.pgpass as they are.
				if cmd.Env != nil {
					t.Errorf("cmd.Env was set with no password in the URI")
				}
				return
			}
			// exec keeps the last of two entries with the same name.
			if got := cmd.Env[len(cmd.Env)-1]; got != "PGPASSWORD="+tt.wantPW {
				t.Errorf("last environment entry = %q, want PGPASSWORD=%s", got, tt.wantPW)
			}
		})
	}
}

// A key=value connection string, or a query parameter that cannot be
// decoded, would carry its password to psql as an argument. Both are refused before
// psql runs, and the error does not echo them.
func TestPsqlCmdRefusesWhatItCannotRedact(t *testing.T) {
	rec := installFakePSQL(t)
	ctx := context.Background()
	for _, uri := range []string{
		"host=h dbname=db password=S3cret",
		"postgres://u@h/db?password=S3cret%zz",
		"postgres://u@h/db?sslmode=%zz&password=S3cret",
	} {
		errs := []error{ApplyTuning(ctx, uri), LoadSQLFile(ctx, uri, "/dev/null")}
		_, err := DetectArchive(ctx, uri, "archive")
		errs = append(errs, err)
		_, err = MaxBlockHeight(ctx, uri)
		errs = append(errs, err)
		for _, err := range errs {
			if err == nil {
				t.Errorf("%q was accepted", uri)
			} else if strings.Contains(err.Error(), "S3cret") {
				t.Errorf("error %q echoes the password", err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); !os.IsNotExist(err) {
		t.Errorf("psql ran for a URI that was refused")
	}
}
