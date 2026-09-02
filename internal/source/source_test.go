package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

func TestFileSourceGetAndList(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "mainnet-1-aaa.json"), "one")
	write(t, filepath.Join(root, "mainnet-2-bbb.json"), "two")
	write(t, filepath.Join(root, "devnet-1-ccc.json"), "other")

	src, err := New(&provider.Artifact{Backend: provider.BackendFile, Path: root, Name: "x-{height}"})
	if err != nil {
		t.Fatal(err)
	}
	names, err := src.List(context.Background(), "mainnet-")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("listed %v, want the two mainnet files", names)
	}

	dst := filepath.Join(t.TempDir(), "out.json")
	if err := src.Get(context.Background(), "mainnet-1-aaa.json", dst); err != nil {
		t.Fatal(err)
	}
	if read(t, dst) != "one" {
		t.Errorf("wrong content copied")
	}
}

// A name must not be able to reach outside the configured directory.
func TestFileSourceRefusesEscape(t *testing.T) {
	src, err := New(&provider.Artifact{Backend: provider.BackendFile, Path: t.TempDir(), Name: "x-{height}"})
	if err != nil {
		t.Fatal(err)
	}
	err = src.Get(context.Background(), "../../etc/passwd", filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected an escape error, got %v", err)
	}
}

func TestHTTPSourceGetWithSidecarChecksum(t *testing.T) {
	body := "block contents"
	digest := sha256.Sum256([]byte(body))
	hexDigest := hex.EncodeToString(digest[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blocks/b.json":
			w.Write([]byte(body))
		case "/blocks/b.json.sha256":
			// sha256sum writes "<digest>  <name>"; both forms must be accepted.
			w.Write([]byte(hexDigest + "  b.json\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	art := &provider.Artifact{
		Backend:  provider.BackendHTTP,
		BaseURL:  srv.URL,
		Name:     "blocks/b-{height}.json",
		Checksum: provider.ChecksumSidecar,
	}
	src, err := New(art)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "b.json")
	if err := src.Get(context.Background(), "blocks/b.json", dst); err != nil {
		t.Fatal(err)
	}
	if read(t, dst) != body {
		t.Error("wrong content")
	}
}

func TestHTTPSourceDetectsAWrongChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			w.Write([]byte(strings.Repeat("0", 64)))
			return
		}
		w.Write([]byte("contents"))
	}))
	defer srv.Close()

	src, err := New(&provider.Artifact{
		Backend:  provider.BackendHTTP,
		BaseURL:  srv.URL,
		Name:     "b-{height}.json",
		Checksum: provider.ChecksumSidecar,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = src.Get(context.Background(), "b.json", filepath.Join(t.TempDir(), "b.json"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch, got %v", err)
	}
}

// A missing sidecar must fail rather than pass quietly: the operator asked for
// the download to be verified.
func TestHTTPSourceFailsWhenTheSidecarIsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("contents"))
	}))
	defer srv.Close()

	src, err := New(&provider.Artifact{
		Backend:  provider.BackendHTTP,
		BaseURL:  srv.URL,
		Name:     "b-{height}.json",
		Checksum: provider.ChecksumSidecar,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Get(context.Background(), "b.json", filepath.Join(t.TempDir(), "b.json")); err == nil {
		t.Fatal("expected a failure when the sidecar is absent")
	}
}

// Without an index, an http provider cannot be enumerated. Saying so is
// necessary: an empty list would read as "the block is not published".
func TestHTTPSourceListNeedsAnIndex(t *testing.T) {
	src, err := New(&provider.Artifact{
		Backend: provider.BackendHTTP,
		BaseURL: "https://example.invalid",
		Name:    "b-{height}.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.List(context.Background(), "b-")
	if err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("expected an error naming the missing index, got %v", err)
	}
}

func TestHTTPSourceListReadsTheIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mainnet-1-aaa.json\nmainnet-2-bbb.json\ndevnet-1-ccc.json\n"))
	}))
	defer srv.Close()

	src, err := New(&provider.Artifact{
		Backend: provider.BackendHTTP,
		BaseURL: srv.URL,
		Index:   srv.URL + "/index.txt",
		Name:    "mainnet-{height}-{state_hash}.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	names, err := src.List(context.Background(), "mainnet-")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("got %v, want the two mainnet entries", names)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
