package extract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarGz writes a .tar.gz at path containing the given regular-file entries
// (name -> contents). It does not sanitize names, so callers can craft
// path-traversal entries.
func makeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, contents := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(contents)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			t.Fatalf("write body %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestTarGzExtractsSQLFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")

	const sqlName = "mainnet-archive-dump-2026-06-23.sql"
	const sqlBody = "-- mina archive dump\nCREATE TABLE blocks (id int);\n"
	makeTarGz(t, src, map[string]string{sqlName: sqlBody})

	files, err := TarGz(src, dst)
	if err != nil {
		t.Fatalf("TarGz returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("TarGz wrote %d files, want 1: %v", len(files), files)
	}

	want := filepath.Join(dst, sqlName)
	if files[0] != want {
		t.Errorf("written file = %q, want %q", files[0], want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != sqlBody {
		t.Errorf("extracted contents = %q, want %q", string(got), sqlBody)
	}
}

func TestTarGzMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "multi.tar.gz")
	dst := filepath.Join(dir, "out")

	makeTarGz(t, src, map[string]string{
		"a.sql":      "select 1;",
		"readme.txt": "hello",
	})

	files, err := TarGz(src, dst)
	if err != nil {
		t.Fatalf("TarGz returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("TarGz wrote %d files, want 2: %v", len(files), files)
	}
}

func TestTarGzRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.tar.gz")
	dst := filepath.Join(dir, "out")

	makeTarGz(t, src, map[string]string{
		"../escape.sql": "rm -rf /",
	})

	files, err := TarGz(src, dst)
	if err == nil {
		t.Fatalf("TarGz accepted path-traversal entry, wrote: %v", files)
	}
	if !strings.Contains(err.Error(), "escapes target dir") {
		t.Errorf("error = %v, want it to mention escaping target dir", err)
	}
	// Ensure nothing was written outside dst.
	if _, statErr := os.Stat(filepath.Join(dir, "escape.sql")); statErr == nil {
		t.Errorf("path-traversal file was written outside dst")
	}
}

func TestTarGzMissingSource(t *testing.T) {
	dir := t.TempDir()
	_, err := TarGz(filepath.Join(dir, "nope.tar.gz"), filepath.Join(dir, "out"))
	if err == nil {
		t.Fatalf("TarGz on missing source = nil error, want error")
	}
}

func TestTarGzNotGzip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.tar.gz")
	if err := os.WriteFile(src, []byte("not gzip data"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := TarGz(src, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatalf("TarGz on non-gzip source = nil error, want error")
	}
}

// The default --work-dir of `archive` is ".". A containment test on the
// joined path rejected every entry for "." and "./", because Join drops the
// leading "./" that the test then looked for.
func TestTarGzIntoCurrentDirectory(t *testing.T) {
	for _, dst := range []string{".", "./"} {
		t.Run(dst, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "dump.tar.gz")
			makeTarGz(t, src, map[string]string{"x.sql": "select 1;"})
			chdir(t, dir)

			files, err := TarGz(src, dst)
			if err != nil {
				t.Fatalf("TarGz(%q): %v", dst, err)
			}
			if len(files) != 1 {
				t.Fatalf("wrote %v, want one file", files)
			}
			if _, err := os.Stat(filepath.Join(dir, "x.sql")); err != nil {
				t.Errorf("x.sql not written into the current directory: %v", err)
			}
		})
	}
}

// Archives made with `tar -C dir -czf out.tar.gz .` name their entries "./x"
// and start with a "./" directory entry. Both are inside the target.
func TestTarGzAcceptsDotSlashEntries(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	body := "select 1;"
	if err := tw.WriteHeader(&tar.Header{Name: "./x.sql", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := TarGz(src, dst)
	if err != nil {
		t.Fatalf("TarGz: %v", err)
	}
	if len(files) != 1 || files[0] != filepath.Join(dst, "x.sql") {
		t.Errorf("wrote %v, want [%s]", files, filepath.Join(dst, "x.sql"))
	}
}

func TestTarGzRejectsEscapingNames(t *testing.T) {
	for _, name := range []string{"../x", "a/../../x", "/etc/x", ".."} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "evil.tar.gz")
			makeTarGz(t, src, map[string]string{name: "x"})
			chdir(t, dir)

			for _, dst := range []string{".", filepath.Join(dir, "out")} {
				if files, err := TarGz(src, dst); err == nil {
					t.Errorf("dst %q: accepted %q, wrote %v", dst, name, files)
				}
			}
		})
	}
}

// chdir changes the working directory for the rest of the test. t.Chdir
// needs Go 1.24.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}
