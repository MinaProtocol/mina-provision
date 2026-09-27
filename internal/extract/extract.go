// Package extract decompresses gzip + tar archives produced by Mina's
// archive-dump pipeline. The dumps land as "<prefix>-YYYY-MM-DD.sql.tar.gz"
// and contain a single "<prefix>-YYYY-MM-DD.sql" file.
package extract

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// Options limits what an extraction writes.
type Options struct {
	// MaxBytes is the most the regular files may hold together, after
	// decompression. It must be positive. A small archive can expand
	// without limit, so every caller states the size it expects.
	MaxBytes int64

	// Keep, when set, selects the regular files to write by their entry
	// name. Other regular files are skipped and do not count against
	// MaxBytes.
	Keep func(name string) bool
}

// TarGz extracts the contents of src (a .tar.gz file) into dstDir and returns
// the list of files written.
//
// Refuses anything that escapes dstDir (path traversal protection).
func TarGz(src, dstDir string, opts Options) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", src, err)
	}
	defer f.Close()
	return TarGzReader(f, dstDir, opts)
}

// TarGzReader is TarGz over an already-open stream. Debian packages carry
// their payload as a tar.gz member inside an ar archive, so it is read as a
// stream rather than written out as a file first.
func TarGzReader(src io.Reader, dstDir string, opts Options) ([]string, error) {
	if opts.MaxBytes <= 0 {
		return nil, fmt.Errorf("extract: the size limit must be positive, got %d", opts.MaxBytes)
	}
	gz, err := gzip.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dstDir, err)
	}
	// Every write goes through the root, so no path can leave dstDir, also
	// through a symlink that is already in it, a file or a parent directory.
	root, err := os.OpenRoot(dstDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	tr := tar.NewReader(gz)
	var written []string
	remaining := opts.MaxBytes
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		// Decided on the entry name alone, so it does not depend on how
		// dstDir is spelt. A prefix test on the joined path rejected every
		// entry when dstDir was "." or "./", because Join drops the "./".
		// IsLocal refuses absolute names, ".." and names that climb out
		// through ".." after cleaning.
		if !filepath.IsLocal(hdr.Name) {
			return nil, fmt.Errorf("tar entry escapes target dir: %s", hdr.Name)
		}
		name := filepath.Clean(hdr.Name)
		target := filepath.Join(dstDir, name)

		// The modes in the archive are ignored. A file with mode 0 would be
		// unreadable to psql unless it runs as root.
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if opts.Keep != nil && !opts.Keep(hdr.Name) {
				slog.Debug("skipping tar entry", "name", hdr.Name)
				continue
			}
			if hdr.Size > remaining {
				return nil, fmt.Errorf("tar entry %s: extracted size exceeds the limit of %d bytes", hdr.Name, opts.MaxBytes)
			}
			if err := writeFile(root, name, io.LimitReader(tr, hdr.Size)); err != nil {
				return nil, err
			}
			remaining -= hdr.Size
			written = append(written, target)
			slog.Debug("extracted", "file", target, "size", hdr.Size)
		default:
			slog.Debug("skipping non-regular tar entry", "name", hdr.Name, "type", hdr.Typeflag)
		}
	}
	return written, nil
}

// writeFile creates name in root and copies r into it. The parent is created
// when the archive has no entry for it. Whatever is already at name is
// removed first and the new file is created exclusively, so a symlink there
// is replaced rather than followed.
func writeFile(root *os.Root, name string, r io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
