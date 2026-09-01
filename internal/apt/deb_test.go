package apt

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The fixture is the real mina-mainnet-config package published at
// packages.o1test.net (noble/stable, 3.4.0-bd0fe9e, 1574 bytes). It is used
// as-is so the test proves the extractor handles what the build actually
// produces, rather than a synthetic archive.
const fixture = "testdata/mina-mainnet-config_3.4.0-bd0fe9e_all.deb"

func TestExtractDataUnpacksRealPackage(t *testing.T) {
	dst := t.TempDir()
	files, err := ExtractData(fixture, dst)
	if err != nil {
		t.Fatal(err)
	}

	var rel []string
	for _, f := range files {
		r, err := filepath.Rel(dst, f)
		if err != nil {
			t.Fatal(err)
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	sort.Strings(rel)

	want := []string{"var/lib/coda/config_bd0fe9e9.json", "var/lib/coda/mainnet.json"}
	if len(rel) != len(want) {
		t.Fatalf("got %v, want %v", rel, want)
	}
	for i := range want {
		if rel[i] != want[i] {
			t.Fatalf("got %v, want %v", rel, want)
		}
	}

	// The point of preferring the package over the source tree: the file the
	// daemon auto-loads carries a 9-character hash, while the package version
	// carries a 7-character one. Neither can be derived from the other.
	body, err := os.ReadFile(filepath.Join(dst, "var/lib/coda/config_bd0fe9e9.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("extracted config is empty")
	}
}

func TestExtractDataRejectsNonDeb(t *testing.T) {
	dir := t.TempDir()
	notADeb := filepath.Join(dir, "x.deb")
	if err := os.WriteFile(notADeb, []byte("this is not an ar archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractData(notADeb, dir); err == nil {
		t.Fatal("expected an error for a file that is not a .deb")
	}
}
