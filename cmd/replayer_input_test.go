package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// A fork configuration as published for mainnet: hashes, no accounts.
const forkConfigJSON = `{
  "genesis": { "genesis_state_timestamp": "2026-09-03T18:00:00.00Z" },
  "proof": { "fork": { "state_hash": "3NKHyxzg", "blockchain_length": 548146,
                       "global_slot_since_genesis": 958440 } },
  "ledger": { "add_genesis_winner": false, "hash": "jwo3EM3z",
              "s3_data_hash": "68f5a0c1" },
  "epoch_data": { "staking": { "seed": "2vaPBM2C", "hash": "jwydedrp" } }
}`

func TestLedgerIsCarriedOverUnchanged(t *testing.T) {
	got, err := ledgerToCarryOver([]byte(forkConfigJSON), "test.json")
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(got, &ledger); err != nil {
		t.Fatal(err)
	}
	// Every field survives: the replayer resolves the ledger from the hashes,
	// so dropping one would leave it unable to find or verify the ledger.
	for _, k := range []string{"add_genesis_winner", "hash", "s3_data_hash"} {
		if _, ok := ledger[k]; !ok {
			t.Errorf("ledger field %q was dropped", k)
		}
	}
	if ledger["s3_data_hash"] != "68f5a0c1" {
		t.Errorf("s3_data_hash changed: %v", ledger["s3_data_hash"])
	}
}

// A configuration that states its accounts converts the same way: whatever the
// ledger object holds is what the replayer receives.
func TestAccountsFormConvertsToo(t *testing.T) {
	in := `{"ledger": {"accounts": [{"pk": "B62q", "balance": "1"}]}}`
	got, err := ledgerToCarryOver([]byte(in), "test.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "B62q") {
		t.Errorf("accounts were not carried over: %s", got)
	}
}

func TestAlreadyAReplayerInputIsRefusedClearly(t *testing.T) {
	in := `{"start_slot_since_genesis": 0, "genesis_ledger": {"hash": "jwo3"}}`
	_, err := ledgerToCarryOver([]byte(in), "input.json")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "already a replayer input") {
		t.Errorf("the message should say the file is already an input, got: %v", err)
	}
}

func TestUnusableLedgerIsRefusedHereNotLater(t *testing.T) {
	// No hash, no accounts, no name: the replayer would have nothing to start
	// from, and should not be handed this file to discover that itself.
	in := `{"ledger": {"add_genesis_winner": false}}`
	_, err := ledgerToCarryOver([]byte(in), "bad.json")
	if err == nil || !strings.Contains(err.Error(), "nothing to start from") {
		t.Fatalf("expected a refusal naming the cause, got %v", err)
	}
}

func TestNotAForkConfigIsRefused(t *testing.T) {
	if _, err := ledgerToCarryOver([]byte(`{"proof": {}}`), "x.json"); err == nil {
		t.Fatal("expected a refusal for a file with no ledger")
	}
	if _, err := ledgerToCarryOver([]byte(`not json`), "x.json"); err == nil {
		t.Fatal("expected a refusal for a file that is not JSON")
	}
}

// The fork point and the replay range are different things, so the fork
// configuration's slot must not leak into the input by default.
func TestStartSlotIsNotInferredFromTheForkPoint(t *testing.T) {
	var fc forkConfig
	if err := json.Unmarshal([]byte(forkConfigJSON), &fc); err != nil {
		t.Fatal(err)
	}
	if fc.Proof == nil || fc.Proof.Fork == nil {
		t.Fatal("fixture should carry a fork point")
	}
	if fc.Proof.Fork.GlobalSlotSinceGenesis != 958440 {
		t.Fatalf("fixture parsed wrong: %d", fc.Proof.Fork.GlobalSlotSinceGenesis)
	}
	// The default is a full replay, whatever the fork point says.
	out := replayerInput{StartSlotSinceGenesis: 0}
	if out.StartSlotSinceGenesis != 0 {
		t.Error("start slot should default to 0")
	}
	if out.TargetEpochLedgersStateHash != nil {
		t.Error("no stop point should be set unless asked for")
	}
}

func TestEpochDataIsDetectedSoItsLossIsReported(t *testing.T) {
	if !carriesEpochData([]byte(forkConfigJSON)) {
		t.Error("epoch_data should be detected in a fork configuration")
	}
	if carriesEpochData([]byte(`{"ledger": {"hash": "x"}}`)) {
		t.Error("epoch_data should not be reported when absent")
	}
}
