package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/extract"
	"github.com/MinaProtocol/mina-provision/internal/pg"
	"github.com/MinaProtocol/mina-provision/internal/provider"
	"github.com/MinaProtocol/mina-provision/internal/source"
)

var (
	archivePgURI     string
	archiveDate      string
	archiveHour      string
	archiveWorkDir   string
	archiveSkipPg    bool
	archiveIfPresent string
)

// What to do when the target database already holds an archive.
const (
	ifPresentImport = "import"
	ifPresentSkip   = "skip"
	ifPresentFail   = "fail"
)

// archiveCmd provisions an archive database from a published dump: it fetches
// the dump, extracts it, applies the recommended PostgreSQL tuning and loads
// the SQL.
//
// Closing the gap between the dump's tip and the chain tip is deliberately not
// done here. A dump is hours old, so blocks are missing at the top, but writing
// blocks into the archive schema is the archive writer's work. Fetch them with
// `mina-provision blocks` and apply them with mina-archive.
var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Provision an archive database from a published dump",
	Long: `Fetches an archive dump from the configured provider, extracts it,
applies the recommended PostgreSQL tuning, and loads the SQL into the target
database.

Dumps are produced hourly, so a restored database is behind the chain tip.
Fetch the remaining blocks with "mina-provision blocks" and apply them with
mina-archive.

A dump import replaces whatever the target database holds. Re-running it
against an archive that has since advanced therefore moves that archive
backwards and loses the blocks collected since the dump was taken, which is
what happens when a one-shot bootstrap container is restarted. Use
--if-present to say what should happen instead:

  import   restore the dump regardless. The default, and the behaviour of
           earlier versions.
  skip     leave the database alone and exit successfully. Nothing is
           downloaded. This is what a compose stack that may be restarted
           wants.
  fail     leave the database alone and exit with an error, for a context
           where an existing archive means something has gone wrong.

skip and fail act only on positive evidence: a readable blocks table holding
at least one row. A database that does not exist yet, or cannot be reached, is
treated as empty, so a first run is never blocked by them.`,
	RunE: runArchive,
}

func init() {
	archiveCmd.Flags().StringVar(&archivePgURI, "pg-uri", "", "Postgres URI (postgres://user:pw@host:port/db). Required unless --skip-pg.")
	archiveCmd.Flags().StringVar(&archiveDate, "date", "", "Dump date in YYYY-MM-DD form. Defaults to today (UTC).")
	archiveCmd.Flags().StringVar(&archiveHour, "hour", "0000", "Dump hour in HHMM form (dumps are produced hourly).")
	archiveCmd.Flags().StringVar(&archiveWorkDir, "work-dir", ".", "Where to download and extract intermediate files.")
	archiveCmd.Flags().BoolVar(&archiveSkipPg, "skip-pg", false, "Download and extract only; skip the psql restore step.")
	archiveCmd.Flags().StringVar(&archiveIfPresent, "if-present", ifPresentImport,
		"What to do when the target database already holds an archive: import (default), skip, or fail.")
}

func runArchive(_ *cobra.Command, _ []string) error {
	if !archiveSkipPg && archivePgURI == "" {
		return fmt.Errorf("--pg-uri is required (or pass --skip-pg to download only)")
	}
	switch archiveIfPresent {
	case ifPresentImport, ifPresentSkip, ifPresentFail:
	default:
		return fmt.Errorf("--if-present must be import, skip or fail, got %q", archiveIfPresent)
	}

	ctx := context.Background()

	// Checked before anything is fetched. The point of skipping is to avoid
	// the download, which is gigabytes, not merely to avoid the restore.
	if archiveIfPresent != ifPresentImport && !archiveSkipPg {
		if p := pg.DetectArchive(ctx, archivePgURI); p.HasArchive {
			switch archiveIfPresent {
			case ifPresentSkip:
				fmt.Fprintf(os.Stdout,
					"The target database already holds an archive: %d blocks, highest at %d. "+
						"Nothing was downloaded or changed (--if-present=skip).\n", p.Blocks, p.MaxHeight)
				return nil
			case ifPresentFail:
				return fmt.Errorf("the target database already holds an archive: %d blocks, "+
					"highest at %d. Importing a dump over it would replace it with an older "+
					"snapshot and lose everything collected since (--if-present=fail)",
					p.Blocks, p.MaxHeight)
			}
		}
	}

	art, err := resolveArtifact(provider.KindArchiveDump)
	if err != nil {
		return err
	}
	src, err := source.New(art)
	if err != nil {
		return err
	}

	date := archiveDate
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if err := validateHour(archiveHour); err != nil {
		return err
	}

	name, err := provider.Render(art.Name, map[string]string{
		provider.FieldDate: date,
		provider.FieldHour: archiveHour,
	})
	if err != nil {
		return err
	}
	dst := filepath.Join(archiveWorkDir, filepath.Base(name))

	slog.Info("fetching archive dump", "from", src.Describe(), "object", name, "dst", dst)
	if err := src.Get(ctx, name, dst); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	slog.Info("extracting", "src", dst, "dst", archiveWorkDir)
	files, err := extract.TarGz(dst, archiveWorkDir)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	sqlPath := ""
	for _, f := range files {
		if strings.HasSuffix(f, ".sql") {
			sqlPath = f
			break
		}
	}
	if sqlPath == "" {
		return fmt.Errorf("no .sql file found in %s", dst)
	}
	slog.Info("found sql dump", "path", sqlPath)

	if archiveSkipPg {
		fmt.Fprintf(os.Stdout, "Downloaded and extracted to %s. Skipping psql restore (--skip-pg).\n", sqlPath)
		return nil
	}

	slog.Info("applying postgres tuning")
	if err := pg.ApplyTuning(ctx, archivePgURI); err != nil {
		return fmt.Errorf("tuning: %w", err)
	}
	if err := pg.LoadSQLFile(ctx, archivePgURI, sqlPath); err != nil {
		return fmt.Errorf("load: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Archive database provisioned. Restart postgres to apply the tuning settings.\n")
	return nil
}

// validateHour checks that an --hour value is a 4-character HHMM string.
func validateHour(hour string) error {
	if len(hour) != 4 {
		return fmt.Errorf("--hour must be 4 digits (HHMM), got %q", hour)
	}
	return nil
}
