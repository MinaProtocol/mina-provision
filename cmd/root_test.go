package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// newNetworkCmd builds a command carrying the same --network flag the root
// command defines, so the environment binding can be exercised in isolation.
func newNetworkCmd() *cobra.Command {
	c := &cobra.Command{Use: "test"}
	c.Flags().String("network", "mainnet", "")
	return c
}

func TestEnvFillsUnsetFlag(t *testing.T) {
	t.Setenv("MINA_NETWORK", "devnet")
	c := newNetworkCmd()
	if err := applyEnvDefaults(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Flags().GetString("network"); got != "devnet" {
		t.Errorf("network = %q, want devnet", got)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	t.Setenv("MINA_NETWORK", "devnet")
	c := newNetworkCmd()
	if err := c.Flags().Set("network", "mesa"); err != nil {
		t.Fatal(err)
	}
	if err := applyEnvDefaults(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Flags().GetString("network"); got != "mesa" {
		t.Errorf("network = %q, want mesa (the flag must win over the environment)", got)
	}
}

func TestEmptyEnvDoesNotOverrideDefault(t *testing.T) {
	t.Setenv("MINA_NETWORK", "")
	c := newNetworkCmd()
	if err := applyEnvDefaults(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Flags().GetString("network"); got != "mainnet" {
		t.Errorf("network = %q, want the default mainnet", got)
	}
}
