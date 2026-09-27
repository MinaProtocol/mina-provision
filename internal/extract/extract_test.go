package extract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// testOpts is a limit well above what any test archive holds.
var testOpts = Options{MaxBytes: 1 << 20}

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

	files, err := TarGz(src, dst, testOpts)
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

	files, err := TarGz(src, dst, testOpts)
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

	files, err := TarGz(src, dst, testOpts)
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
	_, err := TarGz(filepath.Join(dir, "nope.tar.gz"), filepath.Join(dir, "out"), testOpts)
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
	_, err := TarGz(src, filepath.Join(dir, "out"), testOpts)
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

			files, err := TarGz(src, dst, testOpts)
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

	files, err := TarGz(src, dst, testOpts)
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
				if files, err := TarGz(src, dst, testOpts); err == nil {
					t.Errorf("dst %q: accepted %q, wrote %v", dst, name, files)
				}
			}
		})
	}
}

// writeTarGz writes a .tar.gz at path with the given headers. A regular
// file's body is hdr.Size bytes of "x".
func writeTarGz(t *testing.T, path string, hdrs ...*tar.Header) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, hdr := range hdrs {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(hdr.Size))); err != nil {
				t.Fatalf("write body %q: %v", hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A symlink already at the target is replaced. Opening it for writing would
// truncate and overwrite the file it points to.
func TestTarGzReplacesSymlinkAtTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dst, "x.sql")); err != nil {
		t.Fatal(err)
	}
	makeTarGz(t, src, map[string]string{"x.sql": "select 1;"})

	if _, err := TarGz(src, dst, testOpts); err != nil {
		t.Fatalf("TarGz: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("the symlink target was overwritten: %q", got)
	}
	info, err := os.Lstat(filepath.Join(dst, "x.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("x.sql is %v, want a regular file", info.Mode())
	}
}

// A symlinked directory in the target must not lead the write outside it.
func TestTarGzRefusesSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{dst, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dst, "sub")); err != nil {
		t.Fatal(err)
	}
	makeTarGz(t, src, map[string]string{"sub/x.sql": "select 1;"})

	if files, err := TarGz(src, dst, testOpts); err == nil {
		t.Errorf("TarGz wrote %v through a symlinked directory, want an error", files)
	}
	if _, err := os.Stat(filepath.Join(outside, "x.sql")); err == nil {
		t.Errorf("a file was written outside the target")
	}
}

// The modes in the archive are ignored: files get 0644 and directories 0755.
// A file with mode 0 would otherwise be unreadable to psql.
func TestTarGzIgnoresEntryModes(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	writeTarGz(t, src,
		&tar.Header{Name: "d/", Mode: 0, Typeflag: tar.TypeDir},
		&tar.Header{Name: "d/x.sql", Mode: 0, Size: 3, Typeflag: tar.TypeReg},
		&tar.Header{Name: "y.sql", Mode: 0o4777, Size: 3, Typeflag: tar.TypeReg},
	)

	if _, err := TarGz(src, dst, testOpts); err != nil {
		t.Fatalf("TarGz: %v", err)
	}
	for name, want := range map[string]os.FileMode{
		"d":       0o755 | os.ModeDir,
		"d/x.sql": 0o644,
		"y.sql":   0o644,
	} {
		info, err := os.Stat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("%s: mode %v, want %v", name, info.Mode(), want)
		}
	}
}

// An archive need not have a directory entry before the files in it.
func TestTarGzCreatesMissingParent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	makeTarGz(t, src, map[string]string{"sub/f": "select 1;"})

	files, err := TarGz(src, dst, testOpts)
	if err != nil {
		t.Fatalf("TarGz: %v", err)
	}
	if len(files) != 1 || files[0] != filepath.Join(dst, "sub", "f") {
		t.Errorf("wrote %v, want [%s]", files, filepath.Join(dst, "sub", "f"))
	}
}

// The files together may not hold more than MaxBytes. The file that would
// cross the limit is not written.
func TestTarGzEnforcesSizeLimit(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	writeTarGz(t, src,
		&tar.Header{Name: "a.sql", Mode: 0o644, Size: 600, Typeflag: tar.TypeReg},
		&tar.Header{Name: "b.sql", Mode: 0o644, Size: 600, Typeflag: tar.TypeReg},
	)

	_, err := TarGz(src, dst, Options{MaxBytes: 1024})
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("TarGz = %v, want a size-limit error", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "b.sql")); err == nil {
		t.Errorf("b.sql was written past the limit")
	}

	// Exactly at the limit is accepted.
	if _, err := TarGz(src, filepath.Join(dir, "out2"), Options{MaxBytes: 1200}); err != nil {
		t.Errorf("TarGz at the limit: %v", err)
	}
}

// A header that claims fewer bytes than follow it does not let the extra
// bytes into the file. tar.Reader reads the extra bytes as the next header
// and fails.
func TestTarGzRejectsDataPastTheHeaderSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{Name: "x.sql", Mode: 0o644, Size: 1024, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tarBuf.Write(bytes.Repeat([]byte("x"), 1024+4096))
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := TarGz(src, dst, Options{MaxBytes: 2048}); err == nil {
		t.Fatal("TarGz accepted data past the header size")
	}
	if info, err := os.Stat(filepath.Join(dst, "x.sql")); err == nil && info.Size() != 1024 {
		t.Errorf("x.sql holds %d bytes, want 1024", info.Size())
	}
}

func TestTarGzNeedsALimit(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	makeTarGz(t, src, map[string]string{"x.sql": "select 1;"})
	if _, err := TarGz(src, filepath.Join(dir, "out"), Options{}); err == nil {
		t.Fatal("TarGz without a limit = nil error, want error")
	}
}

// Keep selects the files that are written; the others never reach the disk.
func TestTarGzKeepSkipsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dump.tar.gz")
	dst := filepath.Join(dir, "out")
	makeTarGz(t, src, map[string]string{
		"x.sql":               "select 1;",
		"mina-provision.yaml": "version: 1",
	})

	opts := testOpts
	opts.Keep = func(name string) bool { return strings.HasSuffix(name, ".sql") }
	files, err := TarGz(src, dst, opts)
	if err != nil {
		t.Fatalf("TarGz: %v", err)
	}
	if len(files) != 1 || files[0] != filepath.Join(dst, "x.sql") {
		t.Errorf("wrote %v, want [%s]", files, filepath.Join(dst, "x.sql"))
	}
	if _, err := os.Stat(filepath.Join(dst, "mina-provision.yaml")); err == nil {
		t.Errorf("a file that Keep refused was written")
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
