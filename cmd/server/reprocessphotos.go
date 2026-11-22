package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/media"
)

// reprocessPhotosCmd removes the white frame that product photos uploaded
// before 23.11.2026 carry (media.ReprocessLegacyPhotos):
// `server reprocess-photos` lists what would change, `--apply` does it.
// Runs inside the backend container, which already has the full config
// (DATABASE_URL and MINIO_*).
const reprocessPhotosCmd = "reprocess-photos"

const reprocessPhotosTimeout = 30 * time.Minute

func parseReprocessPhotosArgs(args []string, stderr io.Writer) (apply bool, err error) {
	fs := flag.NewFlagSet(reprocessPhotosCmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&apply, "apply", false, "write the reframed photos and update the database (default: dry run)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: server %s [--apply]\n\n"+
			"Removes the white frame from product photos uploaded before 23.11.2026, so the\n"+
			"shoe fills the square. Without --apply only reports what would change.\n"+
			"Reframed photos get new object keys; the old objects are left in the bucket.\n\n", reprocessPhotosCmd)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() > 0 {
		return false, fmt.Errorf("unexpected arguments: %q", fs.Args())
	}
	return apply, nil
}

// reprocessPhotosMain runs the subcommand and returns the process exit code.
func reprocessPhotosMain(args []string, stdout, stderr io.Writer) int {
	apply, err := parseReprocessPhotosArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, reprocessPhotosCmd+":", err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, reprocessPhotosCmd+": config:", err)
		return 2
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, reprocessPhotosCmd+": database:", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	store, err := media.NewClient(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, reprocessPhotosCmd+": minio:", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), reprocessPhotosTimeout)
	defer cancel()
	stats, err := media.ReprocessLegacyPhotos(ctx, db, store, !apply, stdout)
	mode := "dry run, nothing written"
	if apply {
		mode = "applied"
	}
	_, _ = fmt.Fprintf(stdout, "%s (%s): checked %d, reframed %d, already without frame %d, failed %d\n",
		reprocessPhotosCmd, mode, stats.Checked, stats.Reframed, stats.Skipped, stats.Failed)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, reprocessPhotosCmd+":", err)
		return 1
	}
	if stats.Failed > 0 {
		return 1
	}
	return 0
}
