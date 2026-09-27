// Package download fetches objects from public buckets with a progress bar.
//
// Objects are fetched from GCS via cloud.google.com/go/storage (archive dumps
// and precomputed blocks live there).
//
// Authentication for GCS uses option.WithoutAuthentication() since the
// archive-dumps bucket allows anonymous reads. If that ever changes, the
// standard Google SDK auth chain kicks in.
package download

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path"

	"cloud.google.com/go/storage"
	"github.com/schollz/progressbar/v3"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCSObject fetches a single object from a public GCS bucket and writes it
// to w. The storage client checks the CRC32C of a whole-object read, so a
// transfer that returns without an error delivered the stored bytes.
func GCSObject(ctx context.Context, bucket, object string, w io.Writer) error {
	client, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	defer client.Close()

	reader, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("open gs://%s/%s: %w", bucket, object, err)
	}
	defer reader.Close()

	bar := progressbar.DefaultBytes(reader.Attrs.Size, fmt.Sprintf("downloading %s", path.Base(object)))
	if _, err := io.Copy(io.MultiWriter(w, bar), reader); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	return nil
}

// GCSObjectMatches reports whether the file at dst already holds the content
// of a GCS object, so that an idempotent re-run does not fetch it again. A dst
// that does not exist is not a match, and costs no request.
//
// The size alone is not enough: a partial or corrupted file of the right size
// would be accepted. The object's CRC32C is compared with one computed over
// the local file.
func GCSObjectMatches(ctx context.Context, bucket, object, dst string) (bool, error) {
	if _, err := os.Stat(dst); err != nil {
		return false, nil
	}
	client, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return false, fmt.Errorf("storage client: %w", err)
	}
	defer client.Close()

	attrs, err := client.Bucket(bucket).Object(object).Attrs(ctx)
	if err != nil {
		return false, fmt.Errorf("stat gs://%s/%s: %w", bucket, object, err)
	}
	same, err := fileMatches(dst, attrs.Size, attrs.CRC32C)
	if err != nil {
		return false, err
	}
	if same {
		slog.Info("destination already matches the remote object, skipping download",
			"path", dst, "size", attrs.Size, "crc32c", attrs.CRC32C)
	}
	return same, nil
}

// castagnoli is the CRC32C polynomial table that GCS uses for object
// checksums.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// fileMatches reports whether the file at path has the given size and CRC32C.
// The size is compared first, so a file of another size is not read.
func fileMatches(path string, size int64, crc uint32) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return false, err
	}
	if stat.Size() != size {
		return false, nil
	}
	sum := crc32.New(castagnoli)
	if _, err := io.Copy(sum, f); err != nil {
		return false, fmt.Errorf("checksum %s: %w", path, err)
	}
	return sum.Sum32() == crc, nil
}

// ListGCSObjects returns the names of all objects in a GCS bucket matching
// prefix. Bounded by max — pass 0 for no limit.
func ListGCSObjects(ctx context.Context, bucket, prefix string, max int) ([]string, error) {
	client, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	defer client.Close()

	it := client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	var names []string
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list gs://%s/%s*: %w", bucket, prefix, err)
		}
		names = append(names, attrs.Name)
		if max > 0 && len(names) >= max {
			break
		}
	}
	return names, nil
}
