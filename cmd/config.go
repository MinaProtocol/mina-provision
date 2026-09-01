package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/apt"
	"github.com/MinaProtocol/mina-provision/internal/networks"
)

var (
	configOut       string
	configChannel   string
	configComponent string
	configCodename  string
	configVersion   string
	configSource    string
	configRef       string
)

// configDir is where the daemon looks for its runtime configuration, and
// therefore the directory inside the package that holds the files worth
// extracting.
const configDir = "var/lib/coda"

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Provision the runtime configuration file a daemon auto-loads",
	Long: `Fetches the runtime configuration for a network and writes it into a
local directory.

The default source is the mina-<network>-config Debian package, and that is
deliberate. The daemon auto-loads /var/lib/coda/config_<hash>.json, where the
hash is derived from the commit at build time and is not the same length as
the hash in the package version. The package is the only published artifact
that carries both the right content and the right file name, and the
repository index publishes a SHA256 for it, which this command always checks.

The same JSON exists in the mina source tree as genesis_ledgers/<network>.json.
It can be fetched with --source github --ref <tag>, but it arrives under the
source-tree name only, so the daemon will not auto-load it.`,
	RunE: runConfig,
}

func init() {
	configCmd.Flags().StringVar(&configOut, "out", ".", "Directory to write the configuration files into.")
	configCmd.Flags().StringVar(&configChannel, "channel", "o1test",
		"Debian channel to read: "+strings.Join(apt.ChannelNames(), ", ")+".")
	configCmd.Flags().StringVar(&configComponent, "component", "",
		"Component within the channel. Defaults to the channel's own default.")
	configCmd.Flags().StringVar(&configCodename, "codename", "noble",
		"Distribution codename: bullseye, focal, buster, jammy, noble, bookworm.")
	configCmd.Flags().StringVar(&configVersion, "version", "",
		"Exact package version. Default: the highest version present, ordered by Debian rules.")
	configCmd.Flags().StringVar(&configSource, "source", "apt", "Where to fetch from: apt or github.")
	configCmd.Flags().StringVar(&configRef, "ref", "compatible", "Git ref, with --source github.")
}

func runConfig(_ *cobra.Command, _ []string) error {
	net, err := networks.Lookup(network)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configOut, 0o755); err != nil {
		return err
	}
	ctx := context.Background()

	switch configSource {
	case "apt":
		return configFromApt(ctx, net)
	case "github":
		return configFromGitHub(ctx, net)
	default:
		return fmt.Errorf("--source must be apt or github, got %q", configSource)
	}
}

func configFromApt(ctx context.Context, net networks.Network) error {
	ch, err := apt.LookupChannel(configChannel)
	if err != nil {
		return err
	}
	pkgName := fmt.Sprintf("mina-%s-config", net.Name)

	pkg, err := apt.Resolve(ctx, apt.Query{
		Channel:   ch,
		Component: configComponent,
		Codename:  configCodename,
		Package:   pkgName,
		Version:   configVersion,
	})
	if err != nil {
		return err
	}
	slog.Info("resolved package", "package", pkg.Name, "version", pkg.Version, "size", pkg.Size)

	tmp, err := os.MkdirTemp("", "mina-provision-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	debPath, err := apt.Download(ctx, ch, pkg, tmp)
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

// copyConfigFiles moves the JSON files the package carries under
// /var/lib/coda into outDir, keeping their published names. Anything else in
// the package (a systemd unit, for instance) is not configuration and is left
// behind.
func copyConfigFiles(files []string, unpackedRoot, outDir string) ([]string, error) {
	var written []string
	for _, f := range files {
		rel, err := filepath.Rel(unpackedRoot, f)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(rel), "./"))
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

// configFromGitHub fetches genesis_ledgers/<network>.json from the mina source
// tree at a ref. This is the escape hatch for an operator who wants the file a
// specific commit carries; it is not the default, because the file arrives
// under the source-tree name and the daemon will not auto-load it.
func configFromGitHub(ctx context.Context, net networks.Network) error {
	url := fmt.Sprintf("https://raw.githubusercontent.com/MinaProtocol/mina/%s/genesis_ledgers/%s.json",
		configRef, net.Name)
	slog.Info("fetching config from github", "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: %s", url, resp.Status)
	}

	dst := filepath.Join(configOut, net.Name+".json")
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Wrote %s from ref %s.\n", dst, configRef)
	fmt.Fprintf(os.Stdout, "Note: this is the source-tree name. The daemon auto-loads "+
		"config_<hash>.json, which only the Debian package carries.\n")
	return nil
}
