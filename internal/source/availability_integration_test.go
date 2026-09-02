//go:build integration

// Integration tests that read the live artifacts the built-in defaults point
// at. They are excluded from the default `go test ./...` run by the
// `integration` build tag, so the normal suite stays offline. Run them with:
//
//	go test -tags integration ./internal/source/...
//
// What they check is the shipped configuration, not the transport: if a bucket
// is renamed or a naming rule changes, the default provider is wrong and every
// operator who has not overridden it is affected.
package source

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

func TestDefaultProviderArtifactsAreReachable(t *testing.T) {
	cfg, path, err := provider.Load("")
	if err != nil {
		t.Fatalf("load built-in defaults: %v", err)
	}
	if path != "" {
		t.Skipf("a user configuration at %s is in effect; this test checks the built-in defaults", path)
	}

	kinds := []provider.Kind{provider.KindArchiveDump, provider.KindPrecomputedBlocks}
	for _, network := range []string{"mainnet", "devnet"} {
		for _, kind := range kinds {
			network, kind := network, kind
			t.Run(network+"/"+string(kind), func(t *testing.T) {
				art, err := cfg.Resolve("", network, kind)
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				src, err := New(art)
				if err != nil {
					t.Fatalf("source: %v", err)
				}
				// With no fields supplied, the prefix is the fixed part of the
				// name, which is what a listing can be narrowed to.
				prefix, err := provider.Prefix(art.Name, nil)
				if err != nil {
					t.Fatalf("prefix: %v", err)
				}

				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()

				names, err := src.List(ctx, prefix)
				if err != nil {
					t.Skipf("could not list %s with prefix %q (offline?): %v", src.Describe(), prefix, err)
				}
				if len(names) == 0 {
					t.Fatalf("no objects at %s with prefix %q; the default configuration is stale",
						src.Describe(), prefix)
				}
				for _, n := range names[:min(3, len(names))] {
					if !strings.HasPrefix(n, prefix) {
						t.Errorf("listed name %q does not start with %q", n, prefix)
					}
				}
				t.Logf("%s: %d objects, e.g. %s", src.Describe(), len(names), names[0])
			})
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
