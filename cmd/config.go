package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/apt"
	"github.com/MinaProtocol/mina-provision/internal/provider"
	"github.com/MinaProtocol/mina-provision/internal/source"
)

var (
	configOut       string
	configComponent string
	configCodename  string
	configVersion   string
	configRepo      string
	configPackage   string
	configRef       string
)

// configDir is where the daemon looks for its runtime configuration, and so
// the directory inside a package whose contents are worth keeping.
const configDir = "var/lib/coda"

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Provision the runtime configuration file a daemon auto-loads",
	Long: `Fetches the runtime configuration for a network and writes it into a
local directory.

The default provider serves this as a Debian package, and that is deliberate.
The daemon auto-loads /var/lib/coda/config_<hash>.json, where the hash is
derived from the commit at build time and is not the hash in the package
version. The package is the only published artifact carrying both the right
content and the right file name, and the repository index publishes a SHA256
for it, which is always checked.

The same JSON exists in the mina source tree. "--provider github --ref <tag>"
fetches it from there, but it arrives under the source-tree name, so the
daemon will not auto-load it.`,
	RunE: runConfig,
}

func init() {
	configCmd.Flags().StringVar(&configOut, "out", ".", "Directory to write the configuration files into.")
	configCmd.Flags().StringVar(&configRepo, "repository", "", "Override the provider's Debian repository URL.")
	configCmd.Flags().StringVar(&configComponent, "component", "", "Override the provider's repository component.")
	configCmd.Flags().StringVar(&configCodename, "codename", "", "Override the provider's distribution codename.")
	configCmd.Flags().StringVar(&configPackage, "package", "", "Override the provider's package name.")
	configCmd.Flags().StringVar(&configVersion, "version", "",
		"Exact package version. Default: the highest, ordered by Debian rules.")
	configCmd.Flags().StringVar(&configRef, "ref", "compatible", "Git ref, for a provider that serves a source tree.")
}

func runConfig(_ *cobra.Command, _ []string) error {
	art, err := resolveArtifact(provider.KindConfig)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configOut, 0o755); err != nil {
		return err
	}
	ctx := context.Background()

	if art.Backend == provider.BackendAPT {
		return configFromPackage(ctx, art)
	}
	return configFromFile(ctx, art)
}

// configFromPackage fetches the configuration package, verifies it against the
// checksum the repository index publishes, and keeps the JSON files it carries.
func configFromPackage(ctx context.Context, art *provider.Artifact) error {
	q := apt.Query{
		BaseURL:   firstNonEmpty(configRepo, art.Repository),
		Component: firstNonEmpty(configComponent, art.Component),
		Codename:  firstNonEmpty(configCodename, art.Codename),
		Package:   firstNonEmpty(configPackage, art.Package),
		Version:   configVersion,
	}
	pkg, err := apt.Resolve(ctx, q)
	if err != nil {
		return err
	}
	slog.Info("resolved package", "package", pkg.Name, "version", pkg.Version, "size", pkg.Size)

	tmp, err := os.MkdirTemp("", "mina-provision-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	debPath, err := apt.Download(ctx, q.BaseURL, pkg, tmp)
	if err != nil {
		return err
	}

	unpacked := filepath.Join(tmp, "unpacked")
	files, err := apt.ExtractData(debPath, unpacked)
	if err != nil {
		return err
	}

	written, err := copyConfigFiles(files, unpacked, configOut)
	if err != nil {
		return err
	}
	if len(written) == 0 {
		return fmt.Errorf("%s %s contains no files under /%s", pkg.Name, pkg.Version, configDir)
	}

	fmt.Fprintf(os.Stdout, "Provisioned %s %s into %s:\n", pkg.Name, pkg.Version, configOut)
	for _, w := range written {
		fmt.Fprintf(os.Stdout, "  %s\n", w)
	}
	fmt.Fprintf(os.Stdout, "The daemon auto-loads the config_<hash>.json name; keep it as it is.\n")
	return nil
}

// configFromFile fetches the configuration as a plain file, for a provider that
// serves it directly rather than as a package.
func configFromFile(ctx context.Context, art *provider.Artifact) error {
	src, err := source.New(art)
	if err != nil {
		return err
	}
	name, err := provider.Render(art.Name, map[string]string{provider.FieldRef: configRef})
	if err != nil {
		return err
	}
	dst := filepath.Join(configOut, filepath.Base(name))
	if err := src.Get(ctx, name, dst); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Wrote %s from %s.\n", dst, src.Describe())
	if art.Checksum == provider.ChecksumNone {
		fmt.Fprintf(os.Stdout, "This provider publishes no digest for the file, so only the "+
			"transfer was checked. The Debian package the default provider serves is verified "+
			"against a published SHA256, and carries the config_<hash>.json name the daemon "+
			"auto-loads.\n")
	}
	return nil
}

// copyConfigFiles keeps the JSON files a package carries under /var/lib/coda,
// under their published names. Anything else in the package is not
// configuration and is left behind.
func copyConfigFiles(files []string, unpackedRoot, outDir string) ([]string, error) {
	var written []string
	for _, f := range files {
		rel, err := filepath.Rel(unpackedRoot, f)
		if err != nil {
			continue
		}
		rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
		if !strings.HasPrefix(rel, configDir+"/") || !strings.HasSuffix(rel, ".json") {
			continue
		}
		dst := filepath.Join(outDir, filepath.Base(rel))
		if err := copyFile(f, dst); err != nil {
			return nil, err
		}
		written = append(written, dst)
	}
	return written, nil
}

func copyFile(src, dst string) error {
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
