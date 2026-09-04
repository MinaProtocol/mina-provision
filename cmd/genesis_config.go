package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/provider"
	"github.com/MinaProtocol/mina-provision/internal/source"
)

var genesisConfigOut string

var genesisConfigCmd = &cobra.Command{
	Use:   "genesis-config",
	Short: "Provision the fork configuration a chain starts from",
	Long: `Fetches the fork configuration for a network: the fork point, the
ledger and epoch ledger hashes, and the genesis timestamp. An archive node and
the replayer both need it to start.

It names no accounts. The ledger it refers to is resolved from the hashes it
carries, by whichever program consumes it, so this file stays about a kilobyte
however large the ledger is.

This is not the same thing as "daemon-config". That command fetches the
runtime configuration a daemon auto-loads from its own installation, which is
published as a Debian package.

Convert the result into a replayer input with "mina-provision replayer-input".`,
	RunE: runGenesisConfig,
}

func init() {
	genesisConfigCmd.Flags().StringVar(&genesisConfigOut, "out", ".",
		"Directory to write the configuration into.")
}

func runGenesisConfig(_ *cobra.Command, _ []string) error {
	art, err := resolveArtifact(provider.KindGenesisConfig)
	if err != nil {
		return err
	}
	src, err := source.New(art)
	if err != nil {
		return err
	}
	name, err := provider.Render(art.Name, nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(genesisConfigOut, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(genesisConfigOut, filepath.Base(name))

	if err := src.Get(context.Background(), name, dst); err != nil {
		return err
	}

	// Report the fork the file describes. It is regenerated as the chain
	// advances, so which fork point arrived is the thing worth knowing, and it
	// costs one read of a file that is about a kilobyte.
	if fc, err := readForkConfig(dst); err == nil && fc.Proof != nil && fc.Proof.Fork != nil {
		fmt.Fprintf(os.Stdout, "Wrote %s\n  fork state hash: %s\n  blockchain length: %d\n  global slot since genesis: %d\n",
			dst, fc.Proof.Fork.StateHash, fc.Proof.Fork.BlockchainLength,
			fc.Proof.Fork.GlobalSlotSinceGenesis)
	} else {
		fmt.Fprintf(os.Stdout, "Wrote %s\n", dst)
	}
	return nil
}

// forkConfig is the part of a fork configuration this tool reads. Everything
// else is carried through untouched, so a field added upstream does not need a
// change here.
type forkConfig struct {
	Genesis json.RawMessage `json:"genesis,omitempty"`
	Proof   *struct {
		Fork *struct {
			StateHash              string `json:"state_hash"`
			BlockchainLength       int64  `json:"blockchain_length"`
			GlobalSlotSinceGenesis int64  `json:"global_slot_since_genesis"`
		} `json:"fork,omitempty"`
	} `json:"proof,omitempty"`
	Ledger    json.RawMessage `json:"ledger,omitempty"`
	EpochData json.RawMessage `json:"epoch_data,omitempty"`
}

func readForkConfig(path string) (*forkConfig, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc forkConfig
	if err := json.Unmarshal(body, &fc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &fc, nil
}
