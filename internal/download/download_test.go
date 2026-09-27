package download

import (
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// A local copy is reused only when its content matches the object. A file of
// the right size with other bytes -- a partial write that was padded, or a
// corrupted one -- must be fetched again.
func TestFileMatchesComparesCRC32C(t *testing.T) {
	remote := []byte("the published object")
	size := int64(len(remote))
	crc := crc32.Checksum(remote, crc32.MakeTable(crc32.Castagnoli))

	cases := []struct {
		name  string
		local string
		want  bool
	}{
		{"same content", "the published object", true},
		{"same size, other content", "the published objecX", false},
		{"other size", "the published", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "obj")
			if err := os.WriteFile(path, []byte(c.local), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := fileMatches(path, size, crc)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("fileMatches = %v, want %v", got, c.want)
			}
		})
	}
}

// The table must be the Castagnoli polynomial GCS uses, not the IEEE default
// of crc32.ChecksumIEEE.
func TestFileMatchesUsesCastagnoli(t *testing.T) {
	body := []byte("abc")
	path := filepath.Join(t.TempDir(), "obj")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := fileMatches(path, int64(len(body)), crc32.ChecksumIEEE(body))
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an IEEE CRC32 was accepted as a CRC32C")
	}
}
