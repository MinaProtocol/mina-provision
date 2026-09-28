package source

import (
	"context"
	"fmt"
	"io"

	"github.com/MinaProtocol/mina-provision/internal/download"
	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// gcsSource reads a public Google Cloud Storage bucket.
type gcsSource struct {
	artifact *provider.Artifact
}

func (g *gcsSource) Get(ctx context.Context, name, dst string) error {
	// An archive dump is gigabytes, so a re-run does not fetch a copy that
	// is already in place. The copy is compared by content, not by size.
	same, err := download.GCSObjectMatches(ctx, g.artifact.Bucket, name, dst)
	if err != nil {
		return err
	}
	if same {
		return verifySidecar(ctx, g, g.artifact, name, dst)
	}
	return getVerified(ctx, g, g.artifact, name, dst)
}

func (g *gcsSource) getRaw(ctx context.Context, name string, w io.Writer) error {
	return download.GCSObject(ctx, g.artifact.Bucket, name, w)
}

func (g *gcsSource) List(ctx context.Context, prefix string) ([]string, error) {
	return download.ListGCSObjects(ctx, g.artifact.Bucket, prefix, 0)
}

func (g *gcsSource) Describe() string { return fmt.Sprintf("gs://%s", g.artifact.Bucket) }
