package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltInDefaultsAreValid(t *testing.T) {
	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("built-in defaults do not load: %v", err)
	}
	if path != "" {
		t.Skipf("a user configuration at %s is in effect", path)
	}
	if cfg.DefaultProvider != "o1labs" {
		t.Errorf("default_provider = %q, want o1labs", cfg.DefaultProvider)
	}
	for _, network := range []string{"mainnet", "devnet"} {
		dump, err := cfg.Resolve("", network, KindArchiveDump)
		if err != nil {
			t.Errorf("default provider has no archive dump for %s: %v", network, err)
		} else if dump.Database != "archive" {
			// The published dumps start with CREATE DATABASE archive.
			t.Errorf("%s archive dump database = %q, want archive", network, dump.Database)
		}
		if _, err := cfg.Resolve("", network, KindPrecomputedBlocks); err != nil {
			t.Errorf("default provider has no blocks for %s: %v", network, err)
		}
	}
}

// A user file adds a provider without restating the defaults, and the defaults
// stay reachable. This is the property that lets a mirror be added without the
// o1labs entries going stale.
func TestUserFileMergesOverDefaults(t *testing.T) {
	path := writeConfig(t, `
version: 1
default_provider: acme
providers:
  acme:
    description: internal mirror
    networks:
      mainnet:
        precomputed_blocks:
          backend: file
          path: /srv/mina/blocks
          name: "mainnet-{height}-{state_hash}.json"
          checksum: none
`)
	cfg, used, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if used != path {
		t.Fatalf("used %q, want %q", used, path)
	}
	if cfg.DefaultProvider != "acme" {
		t.Errorf("default_provider = %q, want acme", cfg.DefaultProvider)
	}
	if _, err := cfg.Resolve("acme", "mainnet", KindPrecomputedBlocks); err != nil {
		t.Errorf("the added provider is not resolvable: %v", err)
	}
	if _, err := cfg.Resolve("o1labs", "mainnet", KindArchiveDump); err != nil {
		t.Errorf("the built-in provider was lost by merging: %v", err)
	}
}

// Overriding one artifact must leave the provider's other artifacts alone.
func TestOverrideReplacesOnlyTheNamedArtifact(t *testing.T) {
	path := writeConfig(t, `
version: 1
providers:
  o1labs:
    networks:
      mainnet:
        precomputed_blocks:
          backend: file
          path: /srv/blocks
          name: "mainnet-{height}-{state_hash}.json"
`)
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := cfg.Resolve("o1labs", "mainnet", KindPrecomputedBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if blocks.Backend != BackendFile || blocks.Path != "/srv/blocks" {
		t.Errorf("override did not apply: %+v", blocks)
	}
	dump, err := cfg.Resolve("o1labs", "mainnet", KindArchiveDump)
	if err != nil {
		t.Fatalf("the untouched artifact was lost: %v", err)
	}
	if dump.Bucket != "mina-archive-dumps" {
		t.Errorf("the untouched artifact changed: %+v", dump)
	}
	// The default provider name is not restated in the user file, so it must
	// still come from the defaults.
	if cfg.DefaultProvider != "o1labs" {
		t.Errorf("default_provider = %q, want o1labs", cfg.DefaultProvider)
	}
}

func TestInvalidConfigsAreRejected(t *testing.T) {
	cases := map[string]string{
		"unknown backend": `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: carrier-pigeon, name: "a-{date}"}
`,
		"gcs without a bucket": `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, name: "a-{date}"}
`,
		"template field the kind does not have": `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, bucket: b, name: "a-{height}"}
`,
		"checksum index without an index": `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, bucket: b, name: "a-{date}", checksum: index}
`,
		"default provider that is not defined": `
version: 1
default_provider: nope
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, bucket: b, name: "a-{date}"}
`,
		"database on an artifact that is not a dump": `
version: 1
providers:
  x:
    networks:
      mainnet:
        precomputed_blocks: {backend: gcs, bucket: b, name: "m-{height}-{state_hash}.json", database: archive}
`,
		"database name that needs quoting": `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, bucket: b, name: "a-{date}", database: "Archive; drop"}
`,
		"misspelt key": `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, buckets: b, name: "a-{date}"}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatal("expected the configuration to be rejected")
			}
		})
	}
}

// An endpoint that is not https is refused when the configuration is loaded,
// unless it is on a loopback host or the artifact says `insecure: true`.
func TestPlainHTTPEndpoints(t *testing.T) {
	const blocks = `
version: 1
providers:
  x:
    networks:
      mainnet:
        precomputed_blocks: {backend: http, base_url: %q, index: %q, name: "m-{height}.json"%s}
`
	const apt = `
version: 1
providers:
  x:
    networks:
      mainnet:
        daemon_config: {backend: apt, repository: %q, codename: noble, component: stable, package: p%s}
`
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"https", fmt.Sprintf(blocks, "https://example.com", "https://example.com/i.txt", ""), true},
		{"http base_url", fmt.Sprintf(blocks, "http://example.com", "https://example.com/i.txt", ""), false},
		{"http index", fmt.Sprintf(blocks, "https://example.com", "http://example.com/i.txt", ""), false},
		{"http repository", fmt.Sprintf(apt, "http://example.com", ""), false},
		{"other scheme", fmt.Sprintf(blocks, "ftp://example.com", "https://example.com/i.txt", ""), false},
		{"http on 127.0.0.1", fmt.Sprintf(blocks, "http://127.0.0.1:8080", "http://127.0.0.1:8080/i.txt", ""), true},
		{"http on localhost", fmt.Sprintf(apt, "http://localhost:8080", ""), true},
		{"http on ::1", fmt.Sprintf(apt, "http://[::1]:8080", ""), true},
		{"http with insecure", fmt.Sprintf(blocks, "http://example.com", "http://example.com/i.txt", ", insecure: true"), true},
		{"repository with insecure", fmt.Sprintf(apt, "http://example.com", ", insecure: true"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Load(writeConfig(t, c.body))
			if c.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !c.ok && (err == nil || !strings.Contains(err.Error(), "https")) {
				t.Fatalf("got %v, want a rejection that names https", err)
			}
		})
	}
}

// insecure on a backend that has no URL would read as if it changed
// something.
func TestInsecureOnlyForURLBackends(t *testing.T) {
	_, _, err := Load(writeConfig(t, `
version: 1
providers:
  x:
    networks:
      mainnet:
        archive_dump: {backend: gcs, bucket: b, name: "a-{date}", insecure: true}
`))
	if err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("got %v, want a rejection of insecure", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
