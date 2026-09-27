package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MinaProtocol/mina-provision/internal/extract"
	"github.com/MinaProtocol/mina-provision/internal/pg"
	"github.com/MinaProtocol/mina-provision/internal/provider"
	"github.com/MinaProtocol/mina-provision/internal/source"
)

var (
	archivePgURI           string
	archiveDate            string
	archiveHour            string
	archiveWorkDir         string
	archiveSkipPg          bool
	archiveIfPresent       string
	archiveMaxExtractBytes int64
)

// defaultMaxExtractBytes is the default of --max-extract-bytes. It is well
// above the size of a mainnet dump, and is there to stop an archive that
// expands without limit from filling the disk.
const defaultMaxExtractBytes = 200 << 30 // 200 GiB

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
applies the recommended PostgreSQL tuning, and loads the SQL.

Dumps are produced hourly, so a restored database is behind the chain tip.
Fetch the remaining blocks with "mina-provision blocks" and apply them with
mina-archive.

Where the dump goes. The published dumps are made with pg_dump --create: they
create the database "archive" and restore into it, whatever database --pg-uri
names. --pg-uri is then only the way to reach the server. The provider's
"database" setting says which database a dump creates, and the dump's header
is checked against it before anything is loaded. A dump without CREATE
DATABASE restores into the database --pg-uri names.

The load stops at the first SQL error and the command fails. A dump that
creates its database therefore cannot be loaded onto a server that already has
that database: it stops at CREATE DATABASE and changes nothing. To replace an
archive, drop its database first, deliberately, outside this tool.

Use --if-present to decide before the download what happens when the
database already holds an archive, for example when a one-shot bootstrap
container is restarted:

  import   download and load regardless. The default. The load then fails
           on a server that already has the database.
  skip     leave the database alone and exit successfully. Nothing is
           downloaded. This is what a compose stack that may be restarted
           wants.
  fail     leave the database alone and exit with an error, for a context
           where an existing archive means something has gone wrong.

skip and fail treat the database as empty only on positive evidence: it does
not exist, it has no blocks table, or the table has no rows. When the check
cannot be made -- the server cannot be reached, is starting, or refuses to
show the table -- the command fails and downloads nothing. It keeps trying for
up to a minute while the server cannot be reached.`,
	RunE: runArchive,
}

func init() {
	archiveCmd.Flags().StringVar(&archivePgURI, "pg-uri", "", "PostgreSQL URI (postgres://user:pw@host:port/db). Required unless --skip-pg.")
	archiveCmd.Flags().StringVar(&archiveDate, "date", "", "Dump date in YYYY-MM-DD form. Defaults to today (UTC).")
	archiveCmd.Flags().StringVar(&archiveHour, "hour", "0000", "Dump hour in HHMM form (dumps are produced hourly).")
	archiveCmd.Flags().StringVar(&archiveWorkDir, "work-dir", ".", "Where to download and extract intermediate files. Created if missing.")
	archiveCmd.Flags().BoolVar(&archiveSkipPg, "skip-pg", false, "Download and extract only; skip the psql restore step.")
	archiveCmd.Flags().StringVar(&archiveIfPresent, "if-present", ifPresentImport,
		"What to do when the database already holds an archive: import (default), skip, or fail.")
	archiveCmd.Flags().Int64Var(&archiveMaxExtractBytes, "max-extract-bytes", defaultMaxExtractBytes,
		"Most bytes the extracted dump may hold. Extraction stops with an error above it.")
}

func runArchive(cmd *cobra.Command, _ []string) error {
	if !archiveSkipPg && archivePgURI == "" {
		return fmt.Errorf("--pg-uri is required (or pass --skip-pg to download only)")
	}
	switch archiveIfPresent {
	case ifPresentImport, ifPresentSkip, ifPresentFail:
	default:
		return fmt.Errorf("--if-present must be import, skip or fail, got %q", archiveIfPresent)
	}
	now := time.Now().UTC()
	date := archiveDate
	if date == "" {
		date = now.Format("2006-01-02")
	}
	if err := validateDate(date, now); err != nil {
		return err
	}
	if err := validateHour(archiveHour); err != nil {
		return err
	}
	if archiveMaxExtractBytes <= 0 {
		return fmt.Errorf("--max-extract-bytes must be positive, got %d", archiveMaxExtractBytes)
	}

	ctx := commandContext(cmd)

	art, err := resolveArtifact(provider.KindArchiveDump)
	if err != nil {
		return err
	}

	// connectURI is what psql connects to for the tuning and the load. A
	// dump that creates its own database must connect to another one first:
	// its database may not exist yet.
	var targetDB, connectURI string
	if !archiveSkipPg {
		if targetDB, connectURI, err = restoreTarget(art, archivePgURI); err != nil {
			return err
		}
	}

	// Checked before anything is fetched. The point of skipping is to avoid
	// the download, which is gigabytes, not merely to avoid the restore.
	if archiveIfPresent != ifPresentImport && !archiveSkipPg {
		p, err := pg.DetectArchive(ctx, archivePgURI, targetDB)
		if err != nil {
			return fmt.Errorf("cannot tell whether database %q already holds an archive, so "+
				"nothing was downloaded or changed (--if-present=%s): %w", targetDB, archiveIfPresent, err)
		}
		if p.HasArchive {
			switch archiveIfPresent {
			case ifPresentSkip:
				fmt.Fprintf(os.Stdout,
					"Database %q already holds an archive: %d blocks, highest at %d. "+
						"Nothing was downloaded or changed (--if-present=skip).\n", targetDB, p.Blocks, p.MaxHeight)
				return nil
			case ifPresentFail:
				return fmt.Errorf("database %q already holds an archive: %d blocks, "+
					"highest at %d. Importing a dump over it would replace it with an older "+
					"snapshot and lose everything collected since (--if-present=fail)",
					targetDB, p.Blocks, p.MaxHeight)
			}
		}
	}

	src, err := source.New(art)
	if err != nil {
		return err
	}

	name, err := provider.Render(art.Name, map[string]string{
		provider.FieldDate: date,
		provider.FieldHour: archiveHour,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(archiveWorkDir, 0o755); err != nil {
		return fmt.Errorf("create --work-dir: %w", err)
	}
	dst := filepath.Join(archiveWorkDir, filepath.Base(name))

	slog.Info("fetching archive dump", "from", src.Describe(), "object", name, "dst", dst)
	if err := src.Get(ctx, name, dst); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	slog.Info("extracting", "src", dst, "dst", archiveWorkDir)
	// Only the .sql is used. Other entries are not written, so an archive
	// cannot place a file in --work-dir that a later run would read, such as
	// a provider configuration.
	files, err := extract.TarGz(dst, archiveWorkDir, extract.Options{
		MaxBytes: archiveMaxExtractBytes,
		Keep:     isSQL,
	})
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no .sql file found in %s", dst)
	}
	sqlPath := files[0]
	slog.Info("found sql dump", "path", sqlPath)

	if archiveSkipPg {
		fmt.Fprintf(os.Stdout, "Downloaded and extracted to %s. Skipping psql restore (--skip-pg).\n", sqlPath)
		return nil
	}

	if err := checkDump(ctx, sqlPath, art.Database); err != nil {
		return err
	}

	slog.Info("applying postgres tuning")
	if err := pg.ApplyTuning(ctx, connectURI); err != nil {
		return fmt.Errorf("tuning: %w", err)
	}
	if err := pg.LoadSQLFile(ctx, connectURI, sqlPath); err != nil {
		return fmt.Errorf("load: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Archive database %q provisioned. Restart postgres to apply the tuning settings.\n", targetDB)
	return nil
}

// restoreTarget works out which database a dump restores into, and which URI
// psql connects to for the tuning and the load.
func restoreTarget(art *provider.Artifact, uri string) (db, connectURI string, err error) {
	named, err := pg.DatabaseOf(uri)
	if err != nil {
		return "", "", err
	}
	if art.Database == "" {
		// The dump has no CREATE DATABASE and restores where psql connects.
		if named == "" {
			return "", "", fmt.Errorf("--pg-uri must name the database to restore into: " +
				"this provider's dumps do not create their own")
		}
		return named, uri, nil
	}

	if named != "" && named != art.Database && !slices.Contains(pg.MaintenanceDatabases, named) {
		slog.Warn("the dump restores into its own database, not into the one --pg-uri names; "+
			"--pg-uri is used only to reach the server",
			"restores_into", art.Database, "pg_uri_names", named)
	}
	connectURI, err = pg.MaintenanceURI(uri, art.Database)
	return art.Database, connectURI, err
}

// checkDump reads the dump's header before it is loaded. It refuses a dump
// that restores into a database other than the one that was checked, and
// one that the installed psql cannot run.
func checkDump(ctx context.Context, sqlPath, wantDB string) error {
	h, err := pg.ScanDumpHeader(sqlPath)
	if err != nil {
		return err
	}
	if h.Database != wantDB {
		switch {
		case wantDB == "":
			return fmt.Errorf("the dump creates database %q, but the provider does not say so. "+
				"Set database: %s on its archive_dump entry", h.Database, h.Database)
		case h.Database == "":
			return fmt.Errorf("the provider says the dump creates database %q, but the dump has "+
				"no CREATE DATABASE and would restore into the database psql connects to", wantDB)
		default:
			return fmt.Errorf("the dump creates database %q, but the provider says %q. Nothing was "+
				"loaded; the check before the download looked at %q", h.Database, wantDB, wantDB)
		}
	}

	if h.Restrict {
		major, minor, err := pg.ClientVersion(ctx)
		if err != nil {
			slog.Warn("cannot read the psql version; loading anyway", "err", err)
			return nil
		}
		if !pg.SupportsRestrict(major, minor) {
			return fmt.Errorf("the dump uses \\restrict, which psql %d.%d does not understand. "+
				"Install psql %s", major, minor, pg.RestrictMinimum)
		}
	}
	return nil
}

// isSQL selects the tar entries that archive extracts.
func isSQL(name string) bool {
	return strings.HasSuffix(name, ".sql")
}

// hourPattern is an hour and a minute of the day, HHMM, from 0000 to 2359.
var hourPattern = regexp.MustCompile(`^([01][0-9]|2[0-3])[0-5][0-9]$`)

// validateHour checks that an --hour value is a time of day in HHMM form.
func validateHour(hour string) error {
	if !hourPattern.MatchString(hour) {
		return fmt.Errorf("--hour must be a time of day in HHMM form, 0000 to 2359, got %q", hour)
	}
	return nil
}

// validateDate checks that a --date value is a calendar date in YYYY-MM-DD
// form, and that it is not after the current date in UTC: no dump exists for
// a future date.
func validateDate(date string, now time.Time) error {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("--date must be a date in YYYY-MM-DD form, got %q", date)
	}
	y, m, day := now.UTC().Date()
	if today := time.Date(y, m, day, 0, 0, 0, 0, time.UTC); d.After(today) {
		return fmt.Errorf("--date %s is in the future (today is %s, UTC)", date, today.Format("2006-01-02"))
	}
	return nil
}
