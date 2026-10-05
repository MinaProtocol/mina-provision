package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
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
  --range 500000               single block at height 500000
  --range 500000-501000        explicit range, inclusive on both ends
  --range 500000-              open-ended, up to the chain tip

This command only places files on disk. Applying them to an archive database
is mina-archive's work, and reading them is an indexer's; both take a local
directory of block files as input.`,
	RunE: runBlocks,
}

func init() {
	blocksCmd.Flags().StringVar(&blocksRange, "range", "", "Height range, e.g. 500000-501000 (inclusive), 500000, or 500000- for open-ended. Required.")
	blocksCmd.Flags().StringVar(&blocksOut, "out", "./blocks", "Directory to write the block files into.")
}

func runBlocks(cmd *cobra.Command, _ []string) error {
	if blocksRange == "" {
		return errors.New("--range is required, e.g. --range 500000-501000 or --range 500000- (open-ended)")
	}
	start, end, openEnded, err := parseRange(blocksRange)
	if err != nil {
		return err
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

	ctx := commandContext(cmd)
	wanted, err := discoverBlocks(ctx, src, art, start, end, openEnded)
	if err != nil {
		return err
	}
	slog.Info("found blocks in range", "count", len(wanted), "range_start", start, "open_ended", openEnded)
	if !openEnded {
		if err := checkCoverage(wanted, art, start, end, src.Describe()); err != nil {
			return err
		}
	}

	if _, err := downloadBlocks(ctx, src, wanted, blocksOut); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Fetched %d precomputed blocks from %s into %s\n",
		len(wanted), src.Describe(), blocksOut)
	return nil
}

// checkCoverage reports the heights of a closed range that have no block.
// A range with no block at all is an error: it is a range before the first
// published block or after the tip, or the wrong network, and "fetched 0"
// with a zero exit status would hide that. Some missing heights are only a
// warning; the publisher's bucket has gaps.
//
// An open-ended range is not checked: finding nothing past the tip is how a
// caught-up run ends.
func checkCoverage(names []string, art *provider.Artifact, start, end int, from string) error {
	if len(names) == 0 {
		return fmt.Errorf("no precomputed blocks at heights %d-%d in %s. "+
			"Check --network and the range: the bucket may not hold blocks that old or that new", start, end, from)
	}
	height, err := blockHeight(art.Name)
	if err != nil {
		return err
	}
	found := map[int]bool{}
	for _, n := range names {
		if h, ok := height(n); ok {
			found[h] = true
		}
	}
	var missing []int
	for h := start; h <= end; h++ {
		if !found[h] {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		first := missing[:min(len(missing), 10)]
		slog.Warn("some heights in the range have no block", "missing", len(missing), "first", first)
	}
	return nil
}

// blockHeight returns a function that reads the height from a block name.
// The name must match the whole template, with digits for {height} and any
// text for {state_hash}, whatever order the template puts them in.
func blockHeight(tmpl string) (func(name string) (int, bool), error) {
	var re strings.Builder
	re.WriteString("^")
	rest := tmpl
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			re.WriteString(regexp.QuoteMeta(rest))
			break
		}
		j := strings.Index(rest[i:], "}")
		if j < 0 {
			return nil, fmt.Errorf("name template %q has an unclosed brace", tmpl)
		}
		re.WriteString(regexp.QuoteMeta(rest[:i]))
		if rest[i+1:i+j] == provider.FieldHeight {
			re.WriteString(`(?P<height>[0-9]+)`)
		} else {
			re.WriteString(`.+?`)
		}
		rest = rest[i+j+1:]
	}
	re.WriteString("$")
	r, err := regexp.Compile(re.String())
	if err != nil {
		return nil, err
	}
	idx := r.SubexpIndex("height")
	return func(name string) (int, bool) {
		m := r.FindStringSubmatch(name)
		if m == nil || idx < 0 {
			return 0, false
		}
		h, err := strconv.Atoi(m[idx])
		return h, err == nil
	}, nil
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
		if h == math.MaxInt {
			// h++ would wrap to a negative height. Only an open-ended range
			// can get here, because parseRange keeps a closed end below it.
			break
		}
	}
	return wanted, nil
}

// maxHeight is the highest height --range accepts. The walk in discoverBlocks
// stops after end, so end+1 must not overflow.
const maxHeight = math.MaxInt - 1

// parseRange accepts:
//
//	"N"        single height — returns (N, N, false)
//	"N-"       open-ended    — returns (N, 0, true)
//	"N-M"      explicit      — returns (N, M, false)
//
// Heights must be in 0..maxHeight, and a closed range must not cover more than
// maxBlocksPerInvocation heights.
func parseRange(s string) (start, end int, openEnded bool, err error) {
	if !strings.Contains(s, "-") {
		v, perr := parseHeight(s)
		if perr != nil {
			return 0, 0, false, fmt.Errorf("--range must be N, N-, or N-M, with heights from 0 to %d; got %q", maxHeight, s)
		}
		return v, v, false, nil
	}
	parts := strings.SplitN(s, "-", 2)
	startV, perr := parseHeight(parts[0])
	if perr != nil {
		return 0, 0, false, fmt.Errorf("--range start must be an integer from 0 to %d, got %q", maxHeight, parts[0])
	}
	if parts[1] == "" {
		return startV, 0, true, nil
	}
	endV, perr := parseHeight(parts[1])
	if perr != nil {
		return 0, 0, false, fmt.Errorf("--range end must be an integer from 0 to %d, got %q", maxHeight, parts[1])
	}
	if endV < startV {
		return 0, 0, false, fmt.Errorf("--range end (%d) is less than start (%d)", endV, startV)
	}
	// endV-startV cannot overflow, because both are in 0..maxHeight. The
	// block count, endV-startV+1, can.
	if endV-startV >= maxBlocksPerInvocation {
		return 0, 0, false, fmt.Errorf("--range %d-%d covers more than the %d-block safety cap. "+
			"Split into smaller ranges and re-run", startV, endV, maxBlocksPerInvocation)
	}
	return startV, endV, false, nil
}

// parseHeight parses one height of a --range and checks that it is in
// 0..maxHeight.
func parseHeight(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > maxHeight {
		return 0, fmt.Errorf("height %d is outside 0..%d", v, maxHeight)
	}
	return v, nil
}
