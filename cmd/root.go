package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/provider"
)

var (
	verbose        bool
	network        string
	providerName   string
	providerConfig string
)

var rootCmd = &cobra.Command{
	Use:   "mina-provision",
	Short: "Fetch and place the trusted artifacts a Mina node needs.",
	// A failure inside a subcommand is a runtime problem -- a package that is
	// not in that channel, a checksum that does not match -- and reprinting
	// the full usage text buries the message that explains it. Usage is still
	// printed for a malformed command line, which cobra handles separately.
	SilenceUsage: true,
	Long: `mina-provision obtains the published artifacts that a Mina daemon,
archive node, block producer or Rosetta stack needs before it can start:
archive database dumps, precomputed blocks, runtime configuration files and
similar files that today live as long curl, gsutil and psql sequences in
operator documentation and compose stacks.

Each subcommand names the thing being provisioned:

  archive         an archive database, from a published dump
  blocks          precomputed blocks, onto local disk
  daemon-config   the runtime configuration a daemon auto-loads
  genesis-config  the fork configuration a chain starts from

It also converts between them:

  replayer-input  a fork configuration, rewritten as a replayer input

The tool only fetches, verifies and places files. It does not write blocks
into an archive database -- that is mina-archive's own work.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		return applyEnvDefaults(cmd)
	},
}

// envForFlag maps a persistent flag name to the environment variable that can
// supply it. Precedence is flag > environment > flag default, which is what
// applyEnvDefaults implements: the environment is consulted only when the user
// did not type the flag.
//
// Only genuinely repetitive flags belong here. --network is on every
// subcommand and never changes within one host, so typing it every time is
// pure noise.
var envForFlag = map[string]string{
	"network":         "MINA_NETWORK",
	"provider":        "MINA_PROVIDER",
	"provider-config": "MINA_PROVISION_CONFIG",
}

// applyEnvDefaults fills any flag listed in envForFlag from its environment
// variable when the flag was not set on the command line. An empty variable is
// treated as unset, so `MINA_NETWORK=` does not override the default with "".
func applyEnvDefaults(cmd *cobra.Command) error {
	for name, env := range envForFlag {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.Changed {
			continue
		}
		value, ok := os.LookupEnv(env)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if err := cmd.Flags().Set(name, value); err != nil {
			return fmt.Errorf("%s=%q: %w", env, value, err)
		}
		slog.Debug("flag taken from environment", "flag", name, "env", env, "value", value)
	}
	return nil
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	rootCmd.PersistentFlags().StringVar(&network, "network", "mainnet",
		"Mina network the artifacts belong to. May also be set with MINA_NETWORK.")
	rootCmd.PersistentFlags().StringVar(&providerName, "provider", "",
		"Which publisher to read from. Default: the configured default_provider. May also be set with MINA_PROVIDER.")
	rootCmd.PersistentFlags().StringVar(&providerConfig, "provider-config", "",
		"Path to a provider configuration file. May also be set with MINA_PROVISION_CONFIG.")

	rootCmd.AddCommand(archiveCmd)
	rootCmd.AddCommand(blocksCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(genesisConfigCmd)
	rootCmd.AddCommand(replayerInputCmd)
}

// SetVersion records the build version so `mina-provision --version` reports
// which build an operator is running. Provenance matters for a tool whose job
// is to fetch artifacts that other software then trusts.
func SetVersion(v string) {
	rootCmd.Version = v
}

// resolveArtifact loads the provider configuration and returns the artifact of
// the given kind for the selected provider and network. Every command starts
// here, so a single file governs every endpoint the tool will contact.
func resolveArtifact(kind provider.Kind) (*provider.Artifact, error) {
	cfg, path, err := provider.Load(providerConfig)
	if err != nil {
		return nil, err
	}
	if path == "" {
		slog.Debug("using built-in provider defaults")
	} else {
		slog.Info("provider configuration loaded", "path", path)
	}
	return cfg.Resolve(providerName, network, kind)
}

func Execute() error {
	return rootCmd.Execute()
}
