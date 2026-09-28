package cmd

import (
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
