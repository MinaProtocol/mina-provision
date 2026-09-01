// Package apt is a small read-only client for the Mina Debian repositories.
//
// It exists because some artifacts are published only as Debian packages, and
// the package is the correct source for them. The clearest case is the daemon
// runtime configuration: the daemon auto-loads
// /var/lib/coda/config_<GITHASH_CONFIG>.json, where the hash is derived at
// build time. The same JSON also exists in the mina source tree, but under a
// different name, and the hash cannot be derived from a release version by
// hand. The package carries both the right content and the right name.
//
// Only the read paths are implemented: resolve a package in an index, fetch
// it, and verify it against the checksum the index publishes.
package apt

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Channel is one of the Mina Debian repositories. The signed repositories are
// separate hosts rather than components of one repository, so the channel
// selects the host and the component selects the section within it.
type Channel struct {
	Name             string
	BaseURL          string
	DefaultComponent string
	Signed           bool
}

var channels = map[string]Channel{
	"stable": {
		Name:             "stable",
		BaseURL:          "https://stable.apt.packages.minaprotocol.com",
		DefaultComponent: "stable",
		Signed:           true,
	},
	"unstable": {
		Name:             "unstable",
		BaseURL:          "https://unstable.apt.packages.minaprotocol.com",
		DefaultComponent: "beta",
		Signed:           true,
	},
	"nightly": {
		Name:             "nightly",
		BaseURL:          "https://nightly.apt.packages.minaprotocol.com",
		DefaultComponent: "compatible",
		Signed:           true,
	},
	// Legacy unsigned multi-purpose repository. Kept because packages still
	// land here that are not yet promoted to a signed channel.
	"o1test": {
		Name:             "o1test",
		BaseURL:          "https://packages.o1test.net",
		DefaultComponent: "stable",
		Signed:           false,
	},
}

// ChannelNames lists the known channels, for flag help and error messages.
func ChannelNames() []string {
	names := make([]string, 0, len(channels))
	for n := range channels {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

// LookupChannel resolves a channel by name.
func LookupChannel(name string) (Channel, error) {
	c, ok := channels[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Channel{}, fmt.Errorf("unknown channel %q; known channels: %s",
			name, strings.Join(ChannelNames(), ", "))
	}
	return c, nil
}

// Package is one stanza of a Packages index, reduced to the fields needed to
// fetch and verify the file.
type Package struct {
	Name         string
	Version      string
	Architecture string
	Filename     string
	Size         int64
	SHA256       string
}

// Query names one package in one repository.
type Query struct {
	Channel   Channel
	Component string
	Codename  string
	Package   string
	// Version pins an exact version. Empty means "the highest version present",
	// ordered by Debian rules (see CompareVersions).
	Version string
}

// architectures is the order in which binary-<arch> index directories are
// tried. Architecture-independent packages are published under binary-all in
// the repositories that declare that architecture, and folded into the
// per-architecture indexes in the ones that do not, so both have to be tried.
var architectures = []string{"all", "amd64"}

// Resolve finds the package a Query names and returns its index entry.
func Resolve(ctx context.Context, q Query) (Package, error) {
	component := q.Component
	if component == "" {
		component = q.Channel.DefaultComponent
	}

	var candidates []Package
	var tried []string
	for _, arch := range architectures {
		base := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages",
			q.Channel.BaseURL, q.Codename, component, arch)
		tried = append(tried, base)

		body, err := fetchIndex(ctx, base)
		if err != nil {
			slog.Debug("index not available", "url", base, "err", err)
			continue
		}
		pkgs, err := parsePackages(body, q.Package)
		body.Close()
		if err != nil {
			return Package{}, fmt.Errorf("parse %s: %w", base, err)
		}
		candidates = append(candidates, pkgs...)
	}

	if len(candidates) == 0 {
		return Package{}, notFoundError(ctx, q, component, tried)
	}

	if q.Version != "" {
		for _, p := range candidates {
			if p.Version == q.Version {
				return p, nil
			}
		}
		return Package{}, fmt.Errorf("%s version %q not found in %s/%s/%s (found: %s)",
			q.Package, q.Version, q.Channel.Name, q.Codename, component,
			strings.Join(versionsOf(candidates), ", "))
	}

	best := candidates[0]
	for _, p := range candidates[1:] {
		if CompareVersions(p.Version, best.Version) > 0 {
			best = p
		}
	}
	return best, nil
}

// Download fetches a resolved package into dir and returns the local path.
//
// The SHA256 published in the index is always checked. A repository fetch that
// is not verified is only transport security, and the point of preferring the
// package over an ad-hoc download is that the publisher states what the bytes
// should be.
func Download(ctx context.Context, ch Channel, p Package, dir string) (string, error) {
	if p.SHA256 == "" {
		return "", fmt.Errorf("%s %s: index publishes no SHA256; refusing to use an unverifiable package",
			p.Name, p.Version)
	}
	url := ch.BaseURL + "/" + strings.TrimPrefix(p.Filename, "/")
	slog.Info("downloading package", "url", url, "size", p.Size)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get %s: %s", url, resp.Status)
	}

	dst := filepath.Join(dir, filepath.Base(p.Filename))
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, sum), resp.Body)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if p.Size > 0 && written != p.Size {
		return "", fmt.Errorf("%s: expected %d bytes, got %d", url, p.Size, written)
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if !strings.EqualFold(got, p.SHA256) {
		return "", fmt.Errorf("%s: checksum mismatch (index says %s, download is %s)",
			url, p.SHA256, got)
	}
	slog.Info("package verified", "path", dst, "sha256", got)
	return dst, nil
}

// fetchIndex retrieves a Packages index, preferring the gzipped form. The
// caller closes the returned reader.
func fetchIndex(ctx context.Context, base string) (io.ReadCloser, error) {
	for _, url := range []string{base + ".gz", base} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}
		if strings.HasSuffix(url, ".gz") {
			gz, err := gzip.NewReader(resp.Body)
			if err != nil {
				resp.Body.Close()
				return nil, fmt.Errorf("gunzip %s: %w", url, err)
			}
			return readCloser{Reader: gz, closers: []io.Closer{gz, resp.Body}}, nil
		}
		return resp.Body, nil
	}
	return nil, fmt.Errorf("no index at %s[.gz]", base)
}

type readCloser struct {
	io.Reader
	closers []io.Closer
}

func (r readCloser) Close() error {
	var err error
	for _, c := range r.closers {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// parsePackages reads a Packages index and returns every stanza whose Package
// field equals name. Stanzas are separated by blank lines; continuation lines
// begin with a space and belong to the previous field, which none of the
// fields read here ever use.
func parsePackages(r io.Reader, name string) ([]Package, error) {
	var out []Package
	cur := map[string]string{}

	flush := func() {
		if cur["Package"] == name && cur["Filename"] != "" {
			size, _ := strconv.ParseInt(cur["Size"], 10, 64)
			out = append(out, Package{
				Name:         cur["Package"],
				Version:      cur["Version"],
				Architecture: cur["Architecture"],
				Filename:     cur["Filename"],
				Size:         size,
				SHA256:       strings.ToLower(cur["SHA256"]),
			})
		}
		cur = map[string]string{}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuation of the previous field
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

// notFoundError explains a miss in the terms the operator can act on: which
// indexes were read, and which components the repository actually publishes
// for that codename. Components differ per channel and are not guessable.
func notFoundError(ctx context.Context, q Query, component string, tried []string) error {
	msg := fmt.Sprintf("package %q not found in channel %s, codename %s, component %s",
		q.Package, q.Channel.Name, q.Codename, component)
	if comps, err := Components(ctx, q.Channel, q.Codename); err == nil && len(comps) > 0 {
		msg += fmt.Sprintf("\ncomponents published for %s: %s", q.Codename, strings.Join(comps, ", "))
	}
	msg += "\nindexes read: " + strings.Join(tried, ", ")
	return fmt.Errorf("%s", msg)
}

// Components lists the components a repository publishes for a codename, read
// from its Release file.
func Components(ctx context.Context, ch Channel, codename string) ([]string, error) {
	url := fmt.Sprintf("%s/dists/%s/Release", ch.BaseURL, codename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: %s", url, resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Components:"); ok {
			return strings.Fields(v), nil
		}
	}
	return nil, sc.Err()
}

func versionsOf(pkgs []Package) []string {
	v := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		v = append(v, p.Version)
	}
	return v
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
