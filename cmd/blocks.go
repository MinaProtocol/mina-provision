package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/provider"
	"github.com/MinaProtocol/mina-provision/internal/source"
)

var (
	blocksRange string
	blocksOut   string
)

// maxBlocksPerInvocation bounds one run. Operators who genuinely need more
// should run the tool in chunks; the cap keeps a typo from turning into tens of
// thousands of requests against a publisher.
const maxBlocksPerInvocation = 50_000

// openEndedMissThreshold is how many consecutive heights with no block are
// taken to mean the chain tip has been passed. Mina has empty slots, so the
// value has to sit well above any realistic run of them: 1000 slots is about
// three hours.
const openEndedMissThreshold = 1000

var blocksCmd = &cobra.Command{
	Use:   "blocks",
	Short: "Fetch precomputed blocks onto local disk",
	Long: `Fetches precomputed block files for a height range from the configured
provider.

Block names embed the network, the height and the state hash, so a height on
its own only narrows the name to a prefix. The provider is asked to list that
prefix, which a bucket does directly and a plain web server can only do with
an index.

Range formats:
  --range 50000                single block at height 50000
  --range 50000-51000          explicit range, inclusive on both ends
  --range 50000-               open-ended, up to the chain tip

This command only places files on disk. Applying them to an archive database
is mina-archive's work, and reading them is an indexer's; both take a local
directory of block files as input.`,
	RunE: runBlocks,
}

func init() {
	blocksCmd.Flags().StringVar(&blocksRange, "range", "", "Height range, e.g. 50000-51000 (inclusive), 50000, or 50000- for open-ended. Required.")
	blocksCmd.Flags().StringVar(&blocksOut, "out", "./blocks", "Directory to write the block files into.")
}

func runBlocks(_ *cobra.Command, _ []string) error {
	if blocksRange == "" {
		return errors.New("--range is required, e.g. --range 50000-51000 or --range 50000- (open-ended)")
	}
	start, end, openEnded, err := parseRange(blocksRange)
	if err != nil {
		return err
	}
	if !openEnded && end-start+1 > maxBlocksPerInvocation {
		return fmt.Errorf("range %d-%d covers %d blocks, exceeds the %d-block safety cap. "+
			"Split into smaller ranges and re-run", start, end, end-start+1, maxBlocksPerInvocation)
	}

	art, err := resolveArtifact(provider.KindPrecomputedBlocks)
	if err != nil {
		return err
	}
	src, err := source.New(art)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(blocksOut, 0o755); err != nil {
		return err
	}

	ctx := context.Background()
	wanted, err := discoverBlocks(ctx, src, art, start, end, openEnded)
	if err != nil {
		return err
	}
	slog.Info("found blocks in range", "count", len(wanted), "range_start", start, "open_ended", openEnded)

	if _, err := downloadBlocks(ctx, src, wanted, blocksOut); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Fetched %d precomputed blocks from %s into %s\n",
		len(wanted), src.Describe(), blocksOut)
	return nil
}

// downloadBlocks fetches each named block into outDir and returns the local
// paths in the same order.
func downloadBlocks(ctx context.Context, src source.Source, names []string, outDir string) ([]string, error) {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		dst := filepath.Join(outDir, filepath.Base(name))
		if err := src.Get(ctx, name, dst); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", name, err)
		}
		paths = append(paths, dst)
	}
	return paths, nil
}

// discoverBlocks walks heights and asks the provider which block names exist
// at each. One query per height keeps the work proportional to the range asked
// for: listing a whole network prefix would return hundreds of thousands of
// names.
func discoverBlocks(ctx context.Context, src source.Source, art *provider.Artifact, start, end int, openEnded bool) ([]string, error) {
	var wanted []string
	consecutiveMisses := 0
	for h := start; openEnded || h <= end; h++ {
		prefix, err := provider.Prefix(art.Name, map[string]string{
			provider.FieldHeight: strconv.Itoa(h),
		})
		if err != nil {
			return nil, err
		}
		names, err := src.List(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("list height %d: %w", h, err)
		}

		hit := false
		for _, n := range names {
			if strings.HasSuffix(n, ".json") {
				wanted = append(wanted, n)
				hit = true
			}
		}
		if openEnded {
			if hit {
				consecutiveMisses = 0
			} else {
				consecutiveMisses++
				if consecutiveMisses >= openEndedMissThreshold {
					slog.Info("hit consecutive-miss threshold, stopping",
						"last_height_checked", h, "threshold", openEndedMissThreshold)
					break
				}
			}
		}
		if len(wanted) >= maxBlocksPerInvocation {
			return nil, fmt.Errorf("hit %d-block safety cap while walking from %d (currently at height %d). "+
				"Re-run with a closed --range to fetch the rest", maxBlocksPerInvocation, start, h)
		}
	}
	return wanted, nil
}

// parseRange accepts:
//
//	"N"        single height — returns (N, N, false)
//	"N-"       open-ended    — returns (N, 0, true)
//	"N-M"      explicit      — returns (N, M, false)
func parseRange(s string) (start, end int, openEnded bool, err error) {
	if !strings.Contains(s, "-") {
		v, perr := strconv.Atoi(s)
		if perr != nil {
			return 0, 0, false, fmt.Errorf("range must be N, N-, or N-M; got %q", s)
		}
		return v, v, false, nil
	}
	parts := strings.SplitN(s, "-", 2)
	startV, perr := strconv.Atoi(parts[0])
	if perr != nil {
		return 0, 0, false, fmt.Errorf("range start must be an integer, got %q", parts[0])
	}
	if parts[1] == "" {
		return startV, 0, true, nil
	}
	endV, perr := strconv.Atoi(parts[1])
	if perr != nil {
		return 0, 0, false, fmt.Errorf("range end must be an integer, got %q", parts[1])
	}
	if endV < startV {
		return 0, 0, false, fmt.Errorf("range end (%d) is less than start (%d)", endV, startV)
	}
	return startV, endV, false, nil
}
