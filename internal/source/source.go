// Package source moves bytes for one artifact, whichever backend publishes it.
//
// The commands work against this interface, so adding a publisher that serves
// over plain HTTP or from a mounted directory is a configuration change rather
// than a code change.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// Source reads the objects of one artifact.
type Source interface {
	// Get writes the object named name to dst.
	Get(ctx context.Context, name, dst string) error

	// List returns the object names starting with prefix. A backend that
	// cannot enumerate returns an error saying so rather than an empty list,
	// because an empty list would read as "the block does not exist".
	List(ctx context.Context, prefix string) ([]string, error)

	// Describe names the location, for logs and error messages.
	Describe() string
}

// New builds the source for an artifact.
func New(a *provider.Artifact) (Source, error) {
	switch a.Backend {
	case provider.BackendGCS:
		return &gcsSource{artifact: a}, nil
	case provider.BackendHTTP:
		return &httpSource{artifact: a}, nil
	case provider.BackendFile:
		return &fileSource{artifact: a}, nil
	case provider.BackendAPT:
		return nil, fmt.Errorf("the apt backend is fetched by the config command, not through a source")
	default:
		return nil, fmt.Errorf("unknown backend %q", a.Backend)
	}
}

// rawGetter fetches an object without verifying it. Verification needs to
// fetch the digest file itself, so it must have a way down to the transport
// that does not verify in turn: routing it back through Get would make every
// download recurse.
type rawGetter interface {
	getRaw(ctx context.Context, name, dst string) error
}

// verifySidecar checks a downloaded file against "<name>.sha256" published
// beside it, when the artifact asks for that. A mirror that publishes digests
// is the only way a non-repository backend can be trusted beyond the
// transport, so the check is performed whenever it is configured, and a
// missing sidecar is a failure rather than a silent pass.
func verifySidecar(ctx context.Context, s rawGetter, a *provider.Artifact, name, dst string) error {
	if a.Checksum != provider.ChecksumSidecar {
		return nil
	}
	tmp, err := os.CreateTemp("", "mina-provision-sha256-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := s.getRaw(ctx, name+".sha256", tmpPath); err != nil {
		return fmt.Errorf("checksum: sidecar is configured but %s.sha256 could not be read: %w", name, err)
	}
	body, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	want := strings.ToLower(strings.TrimSpace(string(body)))
	// A sidecar written by sha256sum is "<digest>  <filename>".
	if i := strings.IndexAny(want, " \t"); i > 0 {
		want = want[:i]
	}

	got, err := fileSHA256(dst)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: checksum mismatch (sidecar says %s, download is %s)", name, want, got)
	}
	slog.Info("sidecar checksum verified", "object", name, "sha256", got)
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
