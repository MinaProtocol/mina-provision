package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// The names the default provider produces are the ones the Mina Foundation
// actually publishes. Moving the naming rule into configuration must not have
// changed them, so the expectations here are the names the tool built before
// the rule became data.
func TestDefaultProviderProducesTheKnownNames(t *testing.T) {
	cfg, path, err := provider.Load("")
	if err != nil {
		t.Fatalf("load built-in defaults: %v", err)
	}
	if path != "" {
		t.Skipf("a user configuration at %s is in effect", path)
	}

	tests := []struct {
		network string
		kind    provider.Kind
		fields  map[string]string
		want    string
	}{
		{
			network: "mainnet",
			kind:    provider.KindArchiveDump,
			fields:  map[string]string{provider.FieldDate: "2026-06-23", provider.FieldHour: "0000"},
			want:    "mainnet-archive-dump-2026-06-23_0000.sql.tar.gz",
		},
		{
			network: "devnet",
			kind:    provider.KindArchiveDump,
			fields:  map[string]string{provider.FieldDate: "2026-01-02", provider.FieldHour: "1200"},
			want:    "devnet-archive-dump-2026-01-02_1200.sql.tar.gz",
		},
	}
	for _, tt := range tests {
		art, err := cfg.Resolve("", tt.network, tt.kind)
		if err != nil {
			t.Fatalf("resolve %s/%s: %v", tt.network, tt.kind, err)
		}
		got, err := provider.Render(art.Name, tt.fields)
		if err != nil {
			t.Fatalf("render %s/%s: %v", tt.network, tt.kind, err)
		}
		if got != tt.want {
			t.Errorf("%s %s = %q, want %q", tt.network, tt.kind, got, tt.want)
		}
	}
}

// A height alone cannot name a block, because the state hash is part of the
// name. What it can do is narrow the name to a prefix to list.
func TestBlockPrefixForHeight(t *testing.T) {
	cfg, _, err := provider.Load("")
	if err != nil {
		t.Fatal(err)
	}
	art, err := cfg.Resolve("", "mainnet", provider.KindPrecomputedBlocks)
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Prefix(art.Name, map[string]string{provider.FieldHeight: "50000"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "mainnet-50000-" {
		t.Errorf("prefix = %q, want %q", got, "mainnet-50000-")
	}
}

func TestValidateHour(t *testing.T) {
	tests := []struct {
		name    string
		hour    string
		wantErr bool
	}{
		{name: "midnight", hour: "0000", wantErr: false},
		{name: "noon", hour: "1200", wantErr: false},
		{name: "late", hour: "2359", wantErr: false},
		{name: "morning", hour: "0700", wantErr: false},
		{name: "evening", hour: "1900", wantErr: false},
		{name: "not on the hour", hour: "1724", wantErr: false},
		{name: "too short", hour: "000", wantErr: true},
		{name: "too long", hour: "00000", wantErr: true},
		{name: "empty", hour: "", wantErr: true},
		{name: "single", hour: "0", wantErr: true},
		{name: "letters", hour: "abcd", wantErr: true},
		{name: "hour 24", hour: "2400", wantErr: true},
		{name: "hour 99", hour: "9999", wantErr: true},
		{name: "minute 60", hour: "0960", wantErr: true},
		{name: "colon", hour: "12:0", wantErr: true},
		{name: "path", hour: "../x", wantErr: true},
		{name: "url characters", hour: "#?/.", wantErr: true},
		{name: "sign", hour: "+123", wantErr: true},
		{name: "trailing newline", hour: "1200\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHour(tt.hour)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateHour(%q) error = %v, wantErr %v", tt.hour, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDate(t *testing.T) {
	now := time.Date(2026, 9, 22, 23, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		date    string
		wantErr bool
	}{
		{name: "today", date: "2026-09-22", wantErr: false},
		{name: "yesterday", date: "2026-09-21", wantErr: false},
		{name: "last year", date: "2025-12-31", wantErr: false},
		{name: "leap day", date: "2024-02-29", wantErr: false},
		{name: "tomorrow", date: "2026-09-23", wantErr: true},
		{name: "next year", date: "2027-01-01", wantErr: true},
		{name: "month 13", date: "2026-13-01", wantErr: true},
		{name: "day 32", date: "2026-01-32", wantErr: true},
		{name: "not a leap year", date: "2026-02-29", wantErr: true},
		{name: "one-digit month", date: "2026-9-01", wantErr: true},
		{name: "path after the date", date: "2026-09-22/../x", wantErr: true},
		{name: "url characters", date: "2026-09-22?x", wantErr: true},
		{name: "word", date: "yesterday", wantErr: true},
		{name: "empty", date: "", wantErr: true},
		{name: "other order", date: "22-09-2026", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDate(tt.date, now)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDate(%q) error = %v, wantErr %v", tt.date, err, tt.wantErr)
			}
		})
	}
}

// The current date is taken in UTC. Just after midnight UTC, the new date is
// already valid, even where the local date is still the day before.
func TestValidateDateUsesUTC(t *testing.T) {
	west := time.FixedZone("UTC-5", -5*60*60)
	now := time.Date(2026, 9, 22, 19, 30, 0, 0, west) // 2026-09-23 00:30 UTC
	if err := validateDate("2026-09-23", now); err != nil {
		t.Errorf("the UTC date was refused: %v", err)
	}
	if err := validateDate("2026-09-24", now); err == nil {
		t.Error("a date after the UTC date was accepted")
	}
}

// An invalid --date or --hour must stop the command before any request, and
// the message must name the flag.
func TestArchiveRejectsInvalidInputBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name, date, hour, flag string
	}{
		{name: "month 13", date: "2026-13-01", hour: "0000", flag: "--date"},
		{name: "date with a path", date: "2026-09-22/../x", hour: "0000", flag: "--date"},
		{name: "date is a word", date: "yesterday", hour: "0000", flag: "--date"},
		{name: "future date", date: "2999-01-01", hour: "0000", flag: "--date"},
		{name: "hour 24", date: "", hour: "2400", flag: "--hour"},
		{name: "hour with a path", date: "", hour: "../x", flag: "--hour"},
		{name: "hour with url characters", date: "", hour: "#?/.", flag: "--hour"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := useCountingProvider(t)
			setFlag(t, &archiveSkipPg, true)
			setFlag(t, &archiveWorkDir, t.TempDir())
			setFlag(t, &archiveIfPresent, ifPresentImport)
			setFlag(t, &archiveDate, tt.date)
			setFlag(t, &archiveHour, tt.hour)

			err := runArchive(nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.flag) {
				t.Fatalf("err = %v, want an error naming %s", err, tt.flag)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("%d requests were made, want none", n)
			}
		})
	}
}

// The control case for the test above: with valid input, the same setup does
// reach the server. The server answers 404, so the command then fails.
func TestArchiveWithValidInputReachesTheProvider(t *testing.T) {
	requests := useCountingProvider(t)
	setFlag(t, &archiveSkipPg, true)
	setFlag(t, &archiveWorkDir, t.TempDir())
	setFlag(t, &archiveIfPresent, ifPresentImport)
	setFlag(t, &archiveDate, "2026-01-02")
	setFlag(t, &archiveHour, "1200")

	if err := runArchive(nil, nil); err == nil {
		t.Fatal("expected the fetch to fail with 404")
	}
	if n := requests.Load(); n == 0 {
		t.Error("no request was made; the counting provider is not in use")
	}
}

// useCountingProvider points the commands at an http provider served by a
// local test server, and returns the number of requests that server receives.
// Every artifact kind uses it, so no command can reach a real endpoint.
func useCountingProvider(t *testing.T) *atomic.Int64 {
	t.Helper()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := fmt.Sprintf(`version: 1
providers:
  probe:
    networks:
      mainnet:
        archive_dump:
          backend: http
          base_url: %[1]s
          name: "mainnet-archive-dump-{date}_{hour}.sql.tar.gz"
          checksum: none
        precomputed_blocks:
          backend: http
          base_url: %[1]s
          index: %[1]s/index.txt
          name: "mainnet-{height}-{state_hash}.json"
          checksum: none
        daemon_config:
          backend: http
          base_url: %[1]s
          name: "{ref}/genesis_ledgers/mainnet.json"
          checksum: none
`, srv.URL)
	path := filepath.Join(t.TempDir(), "mina-provision.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	setFlag(t, &providerConfig, path)
	setFlag(t, &providerName, "probe")
	setFlag(t, &network, "mainnet")
	return &requests
}

// setFlag sets a flag variable for one test and restores it afterwards.
func setFlag[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestRestoreTarget(t *testing.T) {
	const server = "postgres://u:p@h:5432"
	tests := []struct {
		name, database, uri string
		wantDB, wantConnect string
		wantErr             bool
	}{
		{"dump creates its database, uri names a maintenance db", "archive", server + "/postgres", "archive", server + "/postgres", false},
		{"dump creates its database, uri names it", "archive", server + "/archive", "archive", server + "/postgres", false},
		{"dump creates its database, uri names another", "archive", server + "/mina", "archive", server + "/mina", false},
		{"dump creates its database, uri names none", "archive", server, "archive", server + "/postgres", false},
		{"plain dump restores where the uri points", "", server + "/mina", "mina", server + "/mina", false},
		{"plain dump needs a database in the uri", "", server, "", "", true},
		{"not a postgres uri", "archive", "host=h dbname=x", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, connect, err := restoreTarget(&provider.Artifact{Database: tt.database}, tt.uri)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if db != tt.wantDB || connect != tt.wantConnect {
				t.Errorf("got %q, %q; want %q, %q", db, connect, tt.wantDB, tt.wantConnect)
			}
		})
	}
}

// archive writes only the .sql of a dump. A provider configuration carried in
// the archive must not land in --work-dir, where a later run could read it.
func TestArchiveExtractsOnlyTheSQL(t *testing.T) {
	dumps, work := t.TempDir(), t.TempDir()
	const sqlName = "mainnet-archive-dump-2026-09-22_0000.sql"
	sql, err := os.ReadFile("../internal/pg/testdata/mainnet-archive-dump-head.sql")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{
		sqlName:               sql,
		"mina-provision.yaml": []byte("version: 1\n"),
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(filepath.Join(dumps, sqlName+".tar.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	body := fmt.Sprintf(`version: 1
default_provider: local
providers:
  local:
    networks:
      mainnet:
        archive_dump: {backend: file, path: %s, name: "mainnet-archive-dump-{date}_{hour}.sql.tar.gz", checksum: none, database: archive}
`, dumps)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	oldConfig, oldNetwork, oldProvider := providerConfig, network, providerName
	oldDate, oldHour, oldWork, oldSkip := archiveDate, archiveHour, archiveWorkDir, archiveSkipPg
	t.Cleanup(func() {
		providerConfig, network, providerName = oldConfig, oldNetwork, oldProvider
		archiveDate, archiveHour, archiveWorkDir, archiveSkipPg = oldDate, oldHour, oldWork, oldSkip
	})
	providerConfig, network, providerName = cfg, "mainnet", ""
	archiveDate, archiveHour, archiveWorkDir, archiveSkipPg = "2026-09-22", "0000", work, true

	if err := runArchive(nil, nil); err != nil {
		t.Fatalf("runArchive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, sqlName)); err != nil {
		t.Errorf("the .sql was not extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "mina-provision.yaml")); err == nil {
		t.Errorf("mina-provision.yaml was extracted into --work-dir")
	}
}
