package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lastfm-sheet-sync/internal/envfile"
	"lastfm-sheet-sync/internal/model"
)

func Load() (model.Config, error) {
	_ = envfile.Load(".env")

	cfg := model.Config{
		LastFMAPIKey:             strings.TrimSpace(os.Getenv("LASTFM_API_KEY")),
		LastFMUsername:           strings.TrimSpace(os.Getenv("LASTFM_USERNAME")),
		GoogleSpreadsheetID:      strings.TrimSpace(os.Getenv("GOOGLE_SPREADSHEET_ID")),
		GoogleServiceAccountJSON: strings.TrimSpace(os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON")),
		TargetSheetName:          firstNonEmpty(strings.TrimSpace(os.Getenv("ALBUMS_SHEET_NAME")), model.DefaultTargetSheetName),
		SinglesSheetName:         firstNonEmpty(strings.TrimSpace(os.Getenv("SINGLES_SHEET_NAME")), model.DefaultSinglesSheetName),
		EPSheetName:              firstNonEmpty(strings.TrimSpace(os.Getenv("EP_SHEET_NAME")), model.DefaultEPSheetName),
		LegacySourceSheetName:    strings.TrimSpace(os.Getenv("LEGACY_SOURCE_SHEET_NAME")),
		Timezone:                 firstNonEmpty(strings.TrimSpace(os.Getenv("TIMEZONE")), "America/Chicago"),
		SyncWindowHours:          getInt("SYNC_WINDOW_HOURS", 24),
		StateDir:                 firstNonEmpty(strings.TrimSpace(os.Getenv("STATE_DIR")), "./data/state"),
		CacheDir:                 firstNonEmpty(strings.TrimSpace(os.Getenv("CACHE_DIR")), "./data/cache"),
		UserAgent:                firstNonEmpty(strings.TrimSpace(os.Getenv("USER_AGENT")), "lastfm-sheet-sync/1.0 (+local)"),
		HTTPTimeout:              time.Duration(getInt("HTTP_TIMEOUT_SECONDS", 30)) * time.Second,
		LastFMRequestDelay:       time.Duration(getInt("LASTFM_REQUEST_DELAY_MS", 300)) * time.Millisecond,
		DryRun:                   getBool("DRY_RUN", false),
	}

	if cfg.GoogleServiceAccountJSON == "" {
		cfg.GoogleServiceAccountJSON = "./secrets/google-service-account.json"
	}

	if cfg.SyncWindowHours <= 0 {
		return cfg, errors.New("SYNC_WINDOW_HOURS must be greater than 0")
	}
	if cfg.HTTPTimeout <= 0 {
		return cfg, errors.New("HTTP_TIMEOUT_SECONDS must be greater than 0")
	}
	if cfg.LastFMRequestDelay < 0 {
		return cfg, errors.New("LASTFM_REQUEST_DELAY_MS must be >= 0")
	}

	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return cfg, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return cfg, fmt.Errorf("create cache dir: %w", err)
	}
	return cfg, nil
}

func ValidateForMode(cfg model.Config, mode model.Mode) error {
	var missing []string
	if cfg.LastFMAPIKey == "" && mode != model.ModeImportLegacy {
		missing = append(missing, "LASTFM_API_KEY")
	}
	if cfg.LastFMUsername == "" && mode != model.ModeImportLegacy {
		missing = append(missing, "LASTFM_USERNAME")
	}
	if cfg.GoogleSpreadsheetID == "" {
		missing = append(missing, "GOOGLE_SPREADSHEET_ID")
	}
	if cfg.GoogleServiceAccountJSON == "" {
		missing = append(missing, "GOOGLE_SERVICE_ACCOUNT_JSON")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	info, err := os.Stat(cfg.GoogleServiceAccountJSON)
	if err != nil {
		return fmt.Errorf("service account JSON path %q: %w", cfg.GoogleServiceAccountJSON, err)
	}
	if info.IsDir() {
		return fmt.Errorf("service account JSON path %q is a directory", cfg.GoogleServiceAccountJSON)
	}
	if mode == model.ModeImportLegacy && cfg.LegacySourceSheetName == "" {
		return errors.New("LEGACY_SOURCE_SHEET_NAME is required for import-legacy")
	}
	return nil
}

func StateFile(cfg model.Config) string {
	return filepath.Join(cfg.StateDir, "state.json")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func getInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func getBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}
