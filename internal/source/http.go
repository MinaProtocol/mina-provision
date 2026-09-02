package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

// httpSource reads objects from a plain web server: a mirror, an artifact
// store, or any static hosting.
type httpSource struct {
	artifact *provider.Artifact
}

func (h *httpSource) Get(ctx context.Context, name, dst string) error {
	if err := h.getRaw(ctx, name, dst); err != nil {
		return err
	}
	return verifySidecar(ctx, h, h.artifact, name, dst)
}

func (h *httpSource) getRaw(ctx context.Context, name, dst string) error {
	url := strings.TrimSuffix(h.artifact.BaseURL, "/") + "/" + strings.TrimPrefix(name, "/")
	slog.Info("downloading", "url", url, "dst", dst)

	body, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	return f.Close()
}

// List reads the index the artifact names. A web server cannot be enumerated,
// so without an index there is no way to discover a name that is only known in
// part -- a block whose state hash is unknown, for instance. Saying so is
// better than returning nothing, which would read as "it is not published".
func (h *httpSource) List(ctx context.Context, prefix string) ([]string, error) {
	if h.artifact.Index == "" {
		return nil, fmt.Errorf(
			"provider serves %s over http with no index, so names cannot be discovered by prefix %q; "+
				"add an `index:` URL listing one object name per line", h.artifact.BaseURL, prefix)
	}
	body, err := get(ctx, h.artifact.Index)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var names []string
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && strings.HasPrefix(line, prefix) {
			names = append(names, line)
		}
	}
	return names, sc.Err()
}

func (h *httpSource) Describe() string { return h.artifact.BaseURL }

func get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("get %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}
