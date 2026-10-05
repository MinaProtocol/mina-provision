package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRange(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		start     int
		end       int
		openEnded bool
		wantErr   bool
	}{
		{name: "single height", in: "50000", start: 50000, end: 50000, openEnded: false},
		{name: "single height zero", in: "0", start: 0, end: 0, openEnded: false},
		{name: "closed range", in: "50000-51000", start: 50000, end: 51000, openEnded: false},
		{name: "closed range equal ends", in: "100-100", start: 100, end: 100, openEnded: false},
		{name: "open ended", in: "50000-", start: 50000, end: 0, openEnded: true},
		{name: "open ended zero", in: "0-", start: 0, end: 0, openEnded: true},
		{name: "exactly at cap", in: "0-49999", start: 0, end: 49999, openEnded: false},
		{name: "exactly at cap, from 1", in: "1-50000", start: 1, end: 50000, openEnded: false},
		{name: "highest height", in: "9223372036854775806", start: maxHeight, end: maxHeight, openEnded: false},
		{name: "open ended from the highest height", in: "9223372036854775806-", start: maxHeight, end: 0, openEnded: true},

		{name: "empty", in: "", wantErr: true},
		{name: "non-numeric single", in: "abc", wantErr: true},
		{name: "non-numeric start", in: "abc-100", wantErr: true},
		{name: "non-numeric end", in: "100-xyz", wantErr: true},
		{name: "end less than start", in: "200-100", wantErr: true},
		{name: "leading dash open start empty", in: "-100", wantErr: true},
		{name: "just dash", in: "-", wantErr: true},
		{name: "float", in: "1.5", wantErr: true},
		{name: "negative single", in: "-5", wantErr: true},
		{name: "negative start", in: "-5-10", wantErr: true},
		{name: "negative end", in: "5--10", wantErr: true},
		{name: "one over cap", in: "0-50000", wantErr: true},
		{name: "well over cap", in: "0-200000", wantErr: true},
		{name: "size overflows", in: "0-9223372036854775807", wantErr: true},
		{name: "end is max int", in: "9223372036854775807-9223372036854775807", wantErr: true},
		{name: "single height is max int", in: "9223372036854775807", wantErr: true},
		{name: "open ended from max int", in: "9223372036854775807-", wantErr: true},
		{name: "beyond int64", in: "9223372036854775808", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, openEnded, err := parseRange(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRange(%q) = (%d,%d,%v), want error", tt.in, start, end, openEnded)
				}
				if !strings.Contains(err.Error(), "--range") {
					t.Errorf("parseRange(%q) error %q does not name --range", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q) unexpected error: %v", tt.in, err)
			}
			if start != tt.start || end != tt.end || openEnded != tt.openEnded {
				t.Errorf("parseRange(%q) = (%d,%d,%v), want (%d,%d,%v)",
					tt.in, start, end, openEnded, tt.start, tt.end, tt.openEnded)
			}
		})
	}
}

// An invalid --range must stop the command before any request. Before the
// size was computed without overflow, "0-9223372036854775807" passed the cap
// and the command listed one height after another without end.
func TestBlocksRejectsInvalidRangeBeforeAnyRequest(t *testing.T) {
	for _, in := range []string{"0-9223372036854775807", "0-50000", "-5-10", "9223372036854775807"} {
		t.Run(in, func(t *testing.T) {
			requests := useCountingProvider(t)
			setFlag(t, &blocksOut, t.TempDir())
			setFlag(t, &blocksRange, in)

			err := runBlocks(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "--range") {
				t.Fatalf("err = %v, want an error naming --range", err)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("%d requests were made, want none", n)
			}
		})
	}
}

// The control case for the test above: a valid range reaches the provider.
// The server answers 404 for the index, so the command then fails.
func TestBlocksWithValidRangeReachesTheProvider(t *testing.T) {
	requests := useCountingProvider(t)
	setFlag(t, &blocksOut, t.TempDir())
	setFlag(t, &blocksRange, "50000-50010")

	if err := runBlocks(nil, nil); err == nil {
		t.Fatal("expected the index fetch to fail with 404")
	}
	if n := requests.Load(); n == 0 {
		t.Error("no request was made; the counting provider is not in use")
	}
}

func TestConstants(t *testing.T) {
	if maxBlocksPerInvocation != 50000 {
		t.Errorf("maxBlocksPerInvocation = %d, want 50000", maxBlocksPerInvocation)
	}
	if openEndedMissThreshold != 1000 {
		t.Errorf("openEndedMissThreshold = %d, want 1000", openEndedMissThreshold)
	}
}

func TestBlockHeight(t *testing.T) {
	tests := []struct {
		tmpl, name string
		want       int
		ok         bool
	}{
		{"mainnet-{height}-{state_hash}.json", "mainnet-500000-3NKZ1WhC9KPj3kBD.json", 500000, true},
		// The state hash starts with a digit; it must not be read as the height.
		{"{state_hash}-{height}.json", "3NKZ1WhC9KPj3kBD-500000.json", 500000, true},
		{"mainnet-{height}-{state_hash}.json", "devnet-500000-3NKZ.json", 0, false},
		{"mainnet-{height}-{state_hash}.json", "mainnet-500000-3NKZ.json.sha256", 0, false},
		{"mainnet-{height}-{state_hash}.json", "mainnet-x-3NKZ.json", 0, false},
	}
	for _, tt := range tests {
		height, err := blockHeight(tt.tmpl)
		if err != nil {
			t.Fatalf("%s: %v", tt.tmpl, err)
		}
		got, ok := height(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("blockHeight(%q)(%q) = %d, %v; want %d, %v", tt.tmpl, tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// useBlockDir points the blocks command at a file provider whose directory
// holds an empty block file for each height given.
func useBlockDir(t *testing.T, heights ...int) {
	t.Helper()
	dir := t.TempDir()
	for _, h := range heights {
		name := fmt.Sprintf("mainnet-%d-3NKhash%d.json", h, h)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	body := fmt.Sprintf(`version: 1
providers:
  local:
    networks:
      mainnet:
        precomputed_blocks: {backend: file, path: %s, name: "mainnet-{height}-{state_hash}.json", checksum: none}
`, dir)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	setFlag(t, &providerConfig, cfg)
	setFlag(t, &providerName, "local")
	setFlag(t, &network, "mainnet")
	setFlag(t, &blocksOut, t.TempDir())
}

// A closed range with no block at all is an error, not "fetched 0".
func TestBlocksClosedRangeWithNoBlockFails(t *testing.T) {
	useBlockDir(t, 500000, 500001)
	setFlag(t, &blocksRange, "50000-50010")
	err := runBlocks(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no precomputed blocks at heights 50000-50010") {
		t.Fatalf("err = %v, want a no-blocks error naming the range", err)
	}
}

// Gaps in a closed range are reported, but what exists is fetched.
func TestBlocksClosedRangeWithGapsSucceeds(t *testing.T) {
	useBlockDir(t, 500000, 500002)
	setFlag(t, &blocksRange, "500000-500003")
	if err := runBlocks(nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.Glob(filepath.Join(blocksOut, "*.json"))
	if len(got) != 2 {
		t.Errorf("fetched %d files, want 2", len(got))
	}
}

// Past the tip, an open-ended range finds nothing new. That is a caught-up
// run, not an error.
func TestBlocksOpenEndedPastTheTipSucceeds(t *testing.T) {
	useBlockDir(t, 500000)
	setFlag(t, &blocksRange, "600000-")
	if err := runBlocks(nil, nil); err != nil {
		t.Fatal(err)
	}
}
