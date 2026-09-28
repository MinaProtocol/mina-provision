package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWritePlacesTheFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.json")
	err := Write(dst, func(f *os.File) error {
		_, err := f.WriteString("content")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "content" {
		t.Errorf("dst holds %q", body)
	}
	stat, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != Mode {
		t.Errorf("mode %v, want %v", stat.Mode().Perm(), os.FileMode(Mode))
	}
	assertNoPartFiles(t, filepath.Dir(dst))
}

// When fill fails, dst keeps what it held before and the temporary file is
// removed.
func TestWriteFailureKeepsThePreviousFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.json")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("rejected")
	err := Write(dst, func(f *os.File) error {
		f.WriteString("new")
		return rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("got %v, want the error fill returned", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "old" {
		t.Errorf("dst holds %q, want %q", body, "old")
	}
	assertNoPartFiles(t, filepath.Dir(dst))
}

func assertNoPartFiles(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, ".*.part-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}
