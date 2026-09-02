package source

import (
	"context"
	"fmt"

	"github.com/MinaProtocol/mina-provision/internal/download"
	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// gcsSource reads a public Google Cloud Storage bucket.
type gcsSource struct {
	artifact *provider.Artifact
}

func (g *gcsSource) Get(ctx context.Context, name, dst string) error {
	if err := g.getRaw(ctx, name, dst); err != nil {
		return err
	}
	return verifySidecar(ctx, g, g.artifact, name, dst)
}

func (g *gcsSource) getRaw(ctx context.Context, name, dst string) error {
	return download.GCSObject(ctx, g.artifact.Bucket, name, dst)
}

func (g *gcsSource) List(ctx context.Context, prefix string) ([]string, error) {
	return download.ListGCSObjects(ctx, g.artifact.Bucket, prefix, 0)
}

func (g *gcsSource) Describe() string { return fmt.Sprintf("gs://%s", g.artifact.Bucket) }
