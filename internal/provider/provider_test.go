package provider

import (
	"os"
	"path/filepath"
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
	for _, network := range []string{"mainnet", "devnet", "mesa"} {
		if _, err := cfg.Resolve("", network, KindArchiveDump); err != nil {
			t.Errorf("default provider has no archive dump for %s: %v", network, err)
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

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
