package apt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// A small package that expands far beyond its size is refused, rather than
// allowed to fill the disk. The data member here is 4 MiB of zeros, which gzip
// reduces to a few KiB.
func TestExtractDataBoundsTheExpansion(t *testing.T) {
	dir := t.TempDir()
	var tarGz bytes.Buffer
	gz := gzip.NewWriter(&tarGz)
	tw := tar.NewWriter(gz)
	const size = 4 << 20
	if err := tw.WriteHeader(&tar.Header{Name: "./big.json", Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	var deb bytes.Buffer
	deb.WriteString(arMagic)
	for _, m := range []struct {
		name string
		body []byte
	}{
		{"debian-binary", []byte("2.0\n")},
		{"data.tar.gz", tarGz.Bytes()},
	} {
		fmt.Fprintf(&deb, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", m.name, "0", "0", "0", "100644", len(m.body))
		deb.Write(m.body)
		if len(m.body)%2 == 1 {
			deb.WriteByte('\n')
		}
	}
	if deb.Len()*maxExpansion >= size {
		t.Fatalf("the test package is %d bytes, too large to cross the limit", deb.Len())
	}
	debPath := filepath.Join(dir, "bomb.deb")
	if err := os.WriteFile(debPath, deb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ExtractData(debPath, filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("ExtractData = %v, want a size-limit error", err)
	}
}
