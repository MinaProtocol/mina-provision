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
	archivePgURI   string
	archiveDate    string
	archiveHour    string
	archiveWorkDir string
	archiveSkipPg  bool
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
mina-archive.`,
	RunE: runArchive,
}

func init() {
	archiveCmd.Flags().StringVar(&archivePgURI, "pg-uri", "", "Postgres URI (postgres://user:pw@host:port/db). Required unless --skip-pg.")
	archiveCmd.Flags().StringVar(&archiveDate, "date", "", "Dump date in YYYY-MM-DD form. Defaults to today (UTC).")
	archiveCmd.Flags().StringVar(&archiveHour, "hour", "0000", "Dump hour in HHMM form (dumps are produced hourly).")
	archiveCmd.Flags().StringVar(&archiveWorkDir, "work-dir", ".", "Where to download and extract intermediate files.")
	archiveCmd.Flags().BoolVar(&archiveSkipPg, "skip-pg", false, "Download and extract only; skip the psql restore step.")
}

func runArchive(_ *cobra.Command, _ []string) error {
	if !archiveSkipPg && archivePgURI == "" {
		return fmt.Errorf("--pg-uri is required (or pass --skip-pg to download only)")
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

	ctx := context.Background()
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
