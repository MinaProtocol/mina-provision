package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	replayerInputFrom            string
	replayerInputOut             string
	replayerInputStartSlot       int64
	replayerInputTargetStateHash string
)

var replayerInputCmd = &cobra.Command{
	Use:   "replayer-input",
	Short: "Convert a fork configuration into a replayer input file",
	Long: `Rewrites a fork configuration into the input file the replayer reads.

The replayer takes three fields: the ledger to start from, the slot to start
at, and optionally a state hash to stop at. The ledger it takes is the same
type as the "ledger" object in a fork configuration, so the conversion copies
that object across unchanged.

The ledger is carried over by its hashes, not expanded into accounts. The
replayer resolves a ledger given only its hash, downloading and verifying the
ledger itself, so this command never needs the ledger data and the result
stays small.

A configuration that already states its accounts is converted just as well:
whatever the "ledger" object contains is what the replayer receives.

Two values are NOT inferred from the fork configuration, because the fork point
and the replay range are different things:

  --start-slot          where replay begins. Defaults to 0, a full replay.
  --target-state-hash   where replay stops. Omitted means run to the end.

The epoch ledger hashes and seeds in the fork configuration are not carried
over. The replayer input has no field for them; the replayer rebuilds the
epoch ledgers from the archive database.`,
	RunE: runReplayerInput,
}

func init() {
	replayerInputCmd.Flags().StringVar(&replayerInputFrom, "from", "",
		"Fork configuration to convert, as written by genesis-config. Required.")
	replayerInputCmd.Flags().StringVar(&replayerInputOut, "out", "replayer-input.json",
		"Where to write the replayer input. Use - for standard output.")
	replayerInputCmd.Flags().Int64Var(&replayerInputStartSlot, "start-slot", 0,
		"Global slot since genesis to start replaying from.")
	replayerInputCmd.Flags().StringVar(&replayerInputTargetStateHash, "target-state-hash", "",
		"State hash to stop at. Omitted means replay to the end.")
}

// replayerInput is the replayer's input file. The field names and the shape
// are the replayer's, not this tool's: target_epoch_ledgers_state_hash is
// optional, start_slot_since_genesis defaults to 0, and genesis_ledger is a
// runtime-config ledger.
type replayerInput struct {
	TargetEpochLedgersStateHash *string         `json:"target_epoch_ledgers_state_hash,omitempty"`
	StartSlotSinceGenesis       int64           `json:"start_slot_since_genesis"`
	GenesisLedger               json.RawMessage `json:"genesis_ledger"`
}

func runReplayerInput(_ *cobra.Command, _ []string) error {
	if replayerInputFrom == "" {
		return fmt.Errorf("--from is required: the fork configuration to convert")
	}
	if replayerInputStartSlot < 0 {
		return fmt.Errorf("--start-slot must not be negative, got %d", replayerInputStartSlot)
	}

	body, err := os.ReadFile(replayerInputFrom)
	if err != nil {
		return err
	}
	ledger, err := ledgerToCarryOver(body, replayerInputFrom)
	if err != nil {
		return err
	}

	out := replayerInput{
		StartSlotSinceGenesis: replayerInputStartSlot,
		GenesisLedger:         ledger,
	}
	if replayerInputTargetStateHash != "" {
		out.TargetEpochLedgersStateHash = &replayerInputTargetStateHash
	}

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	if replayerInputOut == "-" {
		_, err := os.Stdout.Write(encoded)
		return err
	}
	if err := os.WriteFile(replayerInputOut, encoded, 0o644); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Wrote %s (start slot %d",
		replayerInputOut, out.StartSlotSinceGenesis)
	if out.TargetEpochLedgersStateHash != nil {
		fmt.Fprintf(os.Stdout, ", stopping at %s", *out.TargetEpochLedgersStateHash)
	} else {
		fmt.Fprintf(os.Stdout, ", no stop point")
	}
	fmt.Fprintf(os.Stdout, ").\n")
	if carriesEpochData(body) {
		fmt.Fprintf(os.Stdout, "The epoch ledger hashes and seeds were not carried over: "+
			"the replayer input has no field for them and rebuilds epoch ledgers from the "+
			"archive database.\n")
	}
	return nil
}

// ledgerToCarryOver returns the ledger object to hand the replayer, and
// explains what is wrong when the file is not a configuration this command can
// convert. Being specific matters: the three formats in circulation look alike
// at a glance, and one of them is already the output of this command.
func ledgerToCarryOver(body []byte, path string) (json.RawMessage, error) {
	var probe struct {
		Ledger        json.RawMessage `json:"ledger"`
		GenesisLedger json.RawMessage `json:"genesis_ledger"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("%s: not JSON: %w", path, err)
	}
	if len(probe.Ledger) == 0 {
		if len(probe.GenesisLedger) > 0 {
			return nil, fmt.Errorf("%s has a genesis_ledger field, so it is already a "+
				"replayer input and needs no conversion", path)
		}
		return nil, fmt.Errorf("%s has no ledger field, so it is not a fork configuration", path)
	}

	// A ledger the replayer cannot resolve would fail much later, inside the
	// replayer, with a message about the ledger rather than about this file.
	var ledger struct {
		Hash     *string          `json:"hash"`
		Name     *string          `json:"name"`
		Accounts *json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(probe.Ledger, &ledger); err != nil {
		return nil, fmt.Errorf("%s: ledger is not an object: %w", path, err)
	}
	if ledger.Hash == nil && ledger.Name == nil && ledger.Accounts == nil {
		return nil, fmt.Errorf("%s: the ledger names no hash, no accounts and no name, "+
			"so the replayer would have nothing to start from", path)
	}
	return probe.Ledger, nil
}

func carriesEpochData(body []byte) bool {
	var probe struct {
		EpochData json.RawMessage `json:"epoch_data"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.EpochData) > 0
}
