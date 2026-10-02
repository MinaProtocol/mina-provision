package pg

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/mainnet-archive-dump-head.sql is the first 60 lines of
// mainnet-archive-dump-2026-09-24_0000.sql from the mina-archive-dumps
// bucket, made by pg_dump 17.11.
func TestScanDumpHeaderOfAPublishedDump(t *testing.T) {
	h, err := ScanDumpHeader("testdata/mainnet-archive-dump-head.sql")
	if err != nil {
		t.Fatal(err)
	}
	if h.Database != "archive" {
		t.Errorf("database = %q, want archive", h.Database)
	}
	if !h.Restrict {
		t.Error(`the \restrict line was not seen`)
	}
}

// Only the CREATE DATABASE line goes; every other byte of a published dump
// reaches psql as it is.
func TestWithoutCreateDatabaseOfAPublishedDump(t *testing.T) {
	raw, err := os.ReadFile("testdata/mainnet-archive-dump-head.sql")
	if err != nil {
		t.Fatal(err)
	}
	r, err := withoutCreateDatabase(strings.NewReader(string(raw)), "archive")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	var want strings.Builder
	removed := 0
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if strings.HasPrefix(line, "CREATE DATABASE archive ") {
			removed++
			continue
		}
		want.WriteString(line)
	}
	if removed != 1 {
		t.Fatalf("the fixture has %d CREATE DATABASE lines, want 1", removed)
	}
	if string(got) != want.String() {
		t.Errorf("output differs from the dump without its CREATE DATABASE line:\n%s", got)
	}
	if !strings.Contains(string(got), "\\connect archive\n") {
		t.Error(`the \connect line was removed too`)
	}
}

func TestWithoutCreateDatabase(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		db      string
		want    string
		wantErr bool
	}{
		{
			name: "quoted name",
			sql:  "SET x = 1;\nCREATE DATABASE \"Odd\" WITH TEMPLATE = template0;\n\\connect \"Odd\"\nCREATE TABLE t (i int);\n",
			db:   "Odd",
			want: "SET x = 1;\n\\connect \"Odd\"\nCREATE TABLE t (i int);\n",
		},
		{
			name:    "no CREATE DATABASE",
			sql:     "SET x = 1;\nCREATE TABLE t (i int);\n",
			db:      "archive",
			wantErr: true,
		},
		{
			name:    "CREATE DATABASE for another database",
			sql:     "CREATE DATABASE other WITH TEMPLATE = template0;\n\\connect other\n",
			db:      "archive",
			wantErr: true,
		},
		{
			name:    "statement continues on the next line",
			sql:     "CREATE DATABASE archive\n  WITH TEMPLATE = template0;\n",
			db:      "archive",
			wantErr: true,
		},
		{
			name:    "CREATE DATABASE after the first table is not the header",
			sql:     "CREATE TABLE t (i int);\nCREATE DATABASE archive;\n",
			db:      "archive",
			wantErr: true,
		},
		{
			name:    "empty input",
			sql:     "",
			db:      "archive",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := withoutCreateDatabase(strings.NewReader(tt.sql), tt.db)
			if tt.wantErr {
				if err == nil {
					t.Fatal("no error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r)
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScanDumpHeader(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		want    DumpHeader
		wantErr bool
	}{
		{
			name: "no create database",
			sql:  "SET x = 0;\nCREATE TABLE blocks (id int);\n",
			want: DumpHeader{},
		},
		{
			name: "plain name is folded",
			sql:  "CREATE DATABASE Archive WITH TEMPLATE = template0;\n\\connect archive\n",
			want: DumpHeader{Database: "archive"},
		},
		{
			name: "quoted name",
			sql:  "CREATE DATABASE \"My \"\"Db\" WITH TEMPLATE = template0;\n\\connect \"My \"\"Db\"\n",
			want: DumpHeader{Database: `My "Db`},
		},
		{
			name: "reuse-previous connect",
			sql:  "CREATE DATABASE \"it's\";\n\\connect -reuse-previous=on \"dbname='it\\'s'\"\n",
			want: DumpHeader{Database: "it's"},
		},
		{
			name: "restrict",
			sql:  "\\restrict abc\nCREATE DATABASE archive;\n\\unrestrict abc\n\\connect archive\n",
			want: DumpHeader{Database: "archive", Restrict: true},
		},
		{
			name: "stops at the first table",
			sql:  "CREATE TABLE t (i int);\nCREATE DATABASE other;\n",
			want: DumpHeader{},
		},
		{
			name:    "two databases",
			sql:     "CREATE DATABASE archive;\n\\connect other\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "d.sql")
			if err := os.WriteFile(path, []byte(tt.sql), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ScanDumpHeader(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSupportsRestrict(t *testing.T) {
	tests := []struct {
		out  string
		want bool
	}{
		{"psql (PostgreSQL) 12.22 (Ubuntu 12.22-0ubuntu0.20.04.4)", false},
		{"psql (PostgreSQL) 13.21", false},
		{"psql (PostgreSQL) 13.22", true},
		{"psql (PostgreSQL) 14.18", false},
		{"psql (PostgreSQL) 14.19", true},
		{"psql (PostgreSQL) 15.13", false},
		{"psql (PostgreSQL) 15.19 (Debian 15.19-0+deb12u1)", true},
		{"psql (PostgreSQL) 16.9", false},
		{"psql (PostgreSQL) 16.10", true},
		{"psql (PostgreSQL) 17.5", false},
		{"psql (PostgreSQL) 17.6 (Debian 17.6-1.pgdg12+1)", true},
		{"psql (PostgreSQL) 18beta1", true},
		{"psql (PostgreSQL) 18.0", true},
	}
	for _, tt := range tests {
		major, minor, err := parseClientVersion(tt.out)
		if err != nil {
			t.Errorf("%q: %v", tt.out, err)
			continue
		}
		if got := SupportsRestrict(major, minor); got != tt.want {
			t.Errorf("%q: SupportsRestrict(%d, %d) = %v, want %v", tt.out, major, minor, got, tt.want)
		}
	}
	if _, _, err := parseClientVersion("not psql"); err == nil {
		t.Error("parsed a version from text with none")
	}
}

func TestURIHelpers(t *testing.T) {
	const base = "postgres://u:p%40ss@h:5432"
	tests := []struct {
		uri, db            string
		named, maint, with string
	}{
		{base + "/mina_archive?sslmode=require", "archive", "mina_archive",
			base + "/mina_archive?sslmode=require", base + "/archive?sslmode=require"},
		{base + "/archive", "archive", "archive", base + "/postgres", base + "/archive"},
		{base, "archive", "", base + "/postgres", base + "/archive"},
		{base + "/", "archive", "", base + "/postgres", base + "/archive"},
	}
	for _, tt := range tests {
		named, err := DatabaseOf(tt.uri)
		if err != nil || named != tt.named {
			t.Errorf("DatabaseOf(%q) = %q, %v; want %q", tt.uri, named, err, tt.named)
		}
		maint, err := MaintenanceURI(tt.uri, tt.db)
		if err != nil || maint != tt.maint {
			t.Errorf("MaintenanceURI(%q, %q) = %q, %v; want %q", tt.uri, tt.db, maint, err, tt.maint)
		}
		with, err := WithDatabase(tt.uri, tt.db)
		if err != nil || with != tt.with {
			t.Errorf("WithDatabase(%q, %q) = %q, %v; want %q", tt.uri, tt.db, with, err, tt.with)
		}
	}
	for _, bad := range []string{"host=h dbname=x", "mysql://h/x", "://"} {
		if _, err := DatabaseOf(bad); err == nil {
			t.Errorf("DatabaseOf(%q) accepted a non-postgres URI", bad)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	if got := quoteLiteral("it's"); got != "'it''s'" {
		t.Errorf("quoteLiteral = %s", got)
	}
}
