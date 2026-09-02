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
	if err := f.getRaw(ctx, name, dst); err != nil {
		return err
	}
	return verifySidecar(ctx, f, f.artifact, name, dst)
}

func (f *fileSource) getRaw(_ context.Context, name, dst string) error {
	src := filepath.Join(f.artifact.Path, filepath.FromSlash(name))
	if err := ensureInside(f.artifact.Path, src); err != nil {
		return err
	}
	slog.Info("copying", "src", src, "dst", dst)

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func (f *fileSource) List(_ context.Context, prefix string) ([]string, error) {
	root := f.artifact.Path
	var names []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, prefix) {
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

func (f *fileSource) Describe() string { return f.artifact.Path }

// ensureInside refuses a name that would read outside the configured
// directory, so a crafted object name cannot reach the rest of the file system.
func ensureInside(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(os.PathSeparator)) {
		return fmt.Errorf("object name escapes %s", root)
	}
	return nil
}
