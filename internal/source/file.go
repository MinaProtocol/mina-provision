package source

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// fileSource reads a local or mounted directory. It is what makes an
// air-gapped or mirrored setup work: the artifacts are staged once, and every
// later run reads them without leaving the host.
type fileSource struct {
	artifact *provider.Artifact
}

func (f *fileSource) Get(ctx context.Context, name, dst string) error {
	return getVerified(ctx, f, f.artifact, name, dst)
}

func (f *fileSource) getRaw(_ context.Context, name string, w io.Writer) error {
	// A crafted object name must not read outside the configured directory.
	// The name is checked here; a symlink in the directory that points out
	// of it is refused by the root when the file is opened.
	rel := filepath.FromSlash(name)
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("object name escapes %s", f.artifact.Path)
	}
	root, err := os.OpenRoot(f.artifact.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	slog.Info("copying", "src", filepath.Join(f.artifact.Path, rel))

	in, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(w, in)
	return err
}

func (f *fileSource) List(_ context.Context, prefix string) ([]string, error) {
	root, err := os.OpenRoot(f.artifact.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var names []string
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(path, prefix) {
			return nil
		}
		// A symlink is listed only when Get can read it: it must point to a
		// regular file inside the directory.
		if d.Type()&fs.ModeSymlink != 0 {
			info, err := root.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				slog.Warn("skipping symlink that does not point to a file inside the directory",
					"path", filepath.Join(f.artifact.Path, path), "err", err)
				return nil
			}
		}
		names = append(names, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

func (f *fileSource) Describe() string { return f.artifact.Path }
