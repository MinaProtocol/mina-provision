package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
		{name: "non-digit but 4 chars accepted", hour: "abcd", wantErr: false},
		{name: "too short", hour: "000", wantErr: true},
		{name: "too long", hour: "00000", wantErr: true},
		{name: "empty", hour: "", wantErr: true},
		{name: "single", hour: "0", wantErr: true},
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
