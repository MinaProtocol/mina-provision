// Package source moves bytes for one artifact, whichever backend publishes it.
//
// The commands work against this interface, so adding a publisher that serves
// over plain HTTP or from a mounted directory is a configuration change rather
// than a code change.
package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/MinaProtocol/mina-provision/internal/atomicfile"
	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// Source reads the objects of one artifact.
type Source interface {
	// Get writes the object named name to dst. On any error, dst is left as
	// it was: absent, or with its previous content.
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
	getRaw(ctx context.Context, name string, w io.Writer) error
}

// getVerified writes the object named name to dst, and places it there only
// when the transfer is complete and the configured checksum matches. The
// content goes to a temporary file beside dst first, so a failed or
// interrupted Get leaves no file at dst, or leaves the file that was there
// before. The consumers of these files -- mina-archive reading --out, psql
// reading a dump -- read whatever is at the path.
func getVerified(ctx context.Context, s rawGetter, a *provider.Artifact, name, dst string) error {
	return atomicfile.Write(dst, func(f *os.File) error {
		if err := s.getRaw(ctx, name, f); err != nil {
			return err
		}
		if err := verifySidecar(ctx, s, a, name, f.Name()); err != nil {
			return err
		}
		// A cancelled run places nothing, even when the last byte arrived
		// before the cancellation was seen.
		return ctx.Err()
	})
}

// maxSidecarSize bounds the read of a digest file. A sidecar holds one line;
// anything larger is not a sidecar and is not read into memory.
const maxSidecarSize = 4096

// verifySidecar checks the file at path against "<name>.sha256" published
// beside the object, when the artifact asks for that. A mirror that publishes
// digests is the only way a non-repository backend can be trusted beyond the
// transport, so the check is performed whenever it is configured, and a
// missing sidecar is a failure rather than a silent pass.
func verifySidecar(ctx context.Context, s rawGetter, a *provider.Artifact, name, path string) error {
	if a.Checksum != provider.ChecksumSidecar {
		return nil
	}
	var body limitedBuffer
	body.limit = maxSidecarSize
	if err := s.getRaw(ctx, name+".sha256", &body); err != nil {
		return fmt.Errorf("checksum: sidecar is configured but %s.sha256 could not be read: %w", name, err)
	}
	want := strings.ToLower(strings.TrimSpace(body.String()))
	// A sidecar written by sha256sum is "<digest>  <filename>".
	if i := strings.IndexAny(want, " \t"); i > 0 {
		want = want[:i]
	}

	got, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: checksum mismatch (sidecar says %s, download is %s)", name, want, got)
	}
	slog.Info("sidecar checksum verified", "object", name, "sha256", got)
	return nil
}

// limitedBuffer is a bytes.Buffer that refuses to grow past limit.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("larger than %d bytes", b.limit)
	}
	return b.Buffer.Write(p)
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
