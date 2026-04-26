# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run tests
go test ./...

# Run a single package's tests
go test ./internal/syncer/...

# Run with dry-run (no writes)
go run ./cmd/lastfm-sheet-sync sync --dry-run

# Backfill from all Last.fm history
go run ./cmd/lastfm-sheet-sync backfill --reset-state

# Import an existing manual sheet
go run ./cmd/lastfm-sheet-sync import-legacy

# Build a binary
go build -o bin/lastfm-sheet-sync ./cmd/lastfm-sheet-sync
```

Requires Go 1.23+. Uses only the standard library — no external dependencies.

## Architecture

The service pulls scrobbles from Last.fm, resolves album metadata (via Last.fm then MusicBrainz as fallback), tracks per-album listening progress in local state, and writes one deduplicated row per release into one of three Google Sheets tabs.

### Data flow

```
Last.fm API → lastfm.Client → syncer.Service → sheets.Client → Google Sheets
                ↑                    ↑
           MusicBrainz API      state.Store (./data/state/state.json)
           disk cache           album cache (./data/cache/*.json)
```

### Key packages

- **`internal/model`** — all shared types: `Config`, `State`, `AlbumState`, `SheetRow`, `Scrobble`, `AlbumMetadata`, and `NormalizeKey`/`NormalizeText` used everywhere for deduplication keys.
- **`internal/syncer`** — the core logic. `Service.Run` dispatches to `runSync`, `runBackfill`, or `runImportLegacy`. The central method is `applyScrobble`, which determines which sheet index to use (Albums/Singles/EP), creates or updates the row, tracks heard track ranks, and marks completion.
- **`internal/lastfm`** — Last.fm XML API client plus MusicBrainz JSON lookup. `ResolveAlbum` first checks disk cache, then queries Last.fm by artist+album (preferred over MBID), then supplements from MusicBrainz if the MBID is present and data is incomplete. Rate-limited separately for each API (Last.fm: configurable, MusicBrainz: 1 req/s).
- **`internal/sheets`** — Google Sheets REST client. Reads full rows from a named tab; writes via batch update.
- **`internal/googleauth`** — JWT/OAuth2 service-account flow, no external library.
- **`internal/state`** — JSON state file with atomic write (write-to-temp + rename).
- **`internal/config`** — loads `.env` then environment variables; validates required vars per mode.

### Sheet routing

Releases are routed to one of three tabs based on MusicBrainz `primary-type`:
- `Album` → `Albums (Auto)` (`ALBUMS_SHEET_NAME`)
- `EP` → `EP (Auto)` (`EP_SHEET_NAME`)
- `Single`, or releases with no MBID and exactly one track → `Singles (Auto)` (`SINGLES_SHEET_NAME`)

### Deduplication / state key

`model.NormalizeKey(artist, album)` produces a `"normalized_artist|normalized_album"` string used as the map key in both `state.json` and the in-memory `sheetIndex.rowsByKey`. Multiple key variants (raw scrobble, canonical from Last.fm) are aliased to the same `*SheetRow` pointer.

### Various Artists handling

When `artist == "Various Artists"`, individual track artists are accumulated in `AlbumState.VArtists` and written to the sheet as a `\\`-delimited string in the Artist column.

### Album completion

An album is complete when every track rank 1..N has been heard. While incomplete, `Notes` holds a comma-separated list of missing track numbers and `DateListened` is blank. On completion, `DateListened` is set to the date of the first scrobble of that album and `Notes` is cleared.

### Core Prompt Fix (drop-in)

When processing album metadata for spreadsheet output, NEVER split artists into separate rows under any circumstance.

If an album contains multiple artists (e.g. Various Artists compilations or featured tracks), they must be combined into a single cell in the artist column.

Use \\ as the delimiter between artists in the same cell.

STRICT RULES:
One album = one row
One cell per field (no row expansion for arrays)
Artists must be concatenated into a single string
Preserve original order of artists
Do NOT normalize, explode, or flatten artist arrays into multiple records
FORMAT EXAMPLE:

Artist cell:

Torus\\The Chariot\\Bladee

Album cell:

YEAR0001 - RIFT ONE
ANTI-PATTERN (NEVER DO THIS):

❌ Row 1: Torus | YEAR0001 - RIFT ONE
❌ Row 2: The Chariot | YEAR0001 - RIFT ONE
❌ Row 3: Bladee | YEAR0001 - RIFT ONE

Any instruction that implies “one artist per row” is incorrect for this task.