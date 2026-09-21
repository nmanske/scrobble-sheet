package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"

	"lastfm-sheet-sync/internal/config"
	"lastfm-sheet-sync/internal/googleauth"
	"lastfm-sheet-sync/internal/lastfm"
	"lastfm-sheet-sync/internal/model"
	"lastfm-sheet-sync/internal/sheets"
	statestore "lastfm-sheet-sync/internal/state"
	"lastfm-sheet-sync/internal/syncer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: lastfm-sheet-sync <sync|backfill|import-legacy> [flags]")
	}

	var mode model.Mode
	switch os.Args[1] {
	case "sync":
		mode = model.ModeSync
	case "backfill":
		mode = model.ModeBackfill
	case "import-legacy":
		mode = model.ModeImportLegacy
	default:
		return fmt.Errorf("unknown command %q (expected sync, backfill, or import-legacy)", os.Args[1])
	}

	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dryRun := flags.Bool("dry-run", false, "log intended writes without touching the sheet or state")
	resetState := flags.Bool("reset-state", false, "delete local state before running (backfill only)")
	fromUnix := flags.Int64("from", 0, "sync window start as unix seconds (sync only)")
	toUnix := flags.Int64("to", 0, "sync window end as unix seconds (sync only)")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := config.ValidateForMode(cfg, mode); err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}

	auth, err := googleauth.NewAuthenticator(cfg.GoogleServiceAccountJSON, httpClient)
	if err != nil {
		return err
	}
	sheetsClient := sheets.NewClient(cfg.GoogleSpreadsheetID, auth, httpClient)

	var lastfmClient *lastfm.Client
	if mode != model.ModeImportLegacy {
		lastfmClient = lastfm.NewClient(cfg.LastFMAPIKey, cfg.LastFMUsername, cfg.CacheDir, cfg.UserAgent, cfg.LastFMRequestDelay, httpClient)
	}

	stateStore := statestore.NewStore(config.StateFile(cfg))
	service, err := syncer.NewService(cfg, lastfmClient, sheetsClient, stateStore, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	summary, err := service.Run(ctx, model.RuntimeOptions{
		Mode:       mode,
		DryRun:     *dryRun,
		ResetState: *resetState,
		FromUnix:   *fromUnix,
		ToUnix:     *toUnix,
	})
	logSummary(logger, mode, summary)
	return err
}

func logSummary(logger *log.Logger, mode model.Mode, s syncer.Summary) {
	logger.Printf("%s summary: processed=%d skipped=%d created=%d completed=%d updated=%d ignored_complete=%d metadata_failures=%d tracklist_fallbacks=%d",
		mode, s.ScrobblesProcessed, s.ScrobblesSkipped, s.AlbumsCreated, s.AlbumsCompleted, s.AlbumsUpdated, s.ExistingAlbumsIgnored, s.MetadataLookupFailures, s.TracklistFallbacks)
	if mode == model.ModeImportLegacy {
		logger.Printf("import-legacy: imported=%d updated=%d", s.ImportedLegacyRows, s.UpdatedLegacyRows)
	}
}
