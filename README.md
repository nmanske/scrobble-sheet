# Last.fm Sheet Sync

A small Go service that pulls scrobbles from Last.fm, resolves album tracklists via Last.fm and MusicBrainz, and writes one deduplicated row per release into a Google Sheets tab.

The workflow it supports is:

1. **Optional:** import your legacy manual sheet into the automation tab.
2. Run a one-time **backfill** against all available Last.fm history.
3. Schedule **sync** once per day (or more often) on your machine.

## What it does

For each release it sees:

- creates at most **one row** in the target sheet
- uses the **first time** the release was heard as the value for `Date Listened`
- leaves `Date Listened` blank while the release is still incomplete
- writes the **lowest missing track number** into `Notes` while incomplete
- ignores future listens after the release is complete
- routes releases to the correct sheet based on MusicBrainz `primary-type`:
  - `Album` → `Albums (Auto)`
  - `EP` → `EP (Auto)`
  - `Single` → `Singles (Auto)`
  - Unknown (no MBID) with one track → `Singles (Auto)`

The row shape is:

- `Date Listened`
- `Artist`
- `Album`
- `Year`
- `Notes`

## Project structure

- `cmd/lastfm-sheet-sync/main.go` – CLI entry point
- `internal/lastfm` – Last.fm API client and album metadata cache
- `internal/sheets` – Google Sheets REST client
- `internal/googleauth` – service-account JWT/OAuth flow
- `internal/syncer` – album progress logic and sheet updates
- `.env.example` – environment variable template

## Requirements

- Go 1.23+
- a Last.fm API key
- a Google Cloud service account JSON key with access to your spreadsheet

## Setup

### 1. Create a Last.fm API account

Create an API application in Last.fm and collect your API key.

You only need the API key for this project because it uses read-only endpoints.

### 2. Enable Google Sheets access

1. Create a Google Cloud project.
2. Enable the **Google Sheets API**.
3. Create a **service account**.
4. Create and download a **JSON key** for that service account.
5. Share your spreadsheet with the service account email as an editor.

### 3. Prepare local files

Copy the env template:

```bash
cp .env.example .env
```

Create a secrets folder and place your service account JSON there:

```bash
mkdir -p secrets
# then copy your downloaded JSON to:
# secrets/google-service-account.json
```

### 4. Fill in `.env`

The most important values are:

```env
LASTFM_API_KEY=your_lastfm_api_key
LASTFM_USERNAME=your_lastfm_username
GOOGLE_SPREADSHEET_ID=your_google_sheet_id
GOOGLE_SERVICE_ACCOUNT_JSON=./secrets/google-service-account.json
TARGET_SHEET_NAME=Albums (Auto)
SINGLES_SHEET_NAME=Singles (Auto)
EP_SHEET_NAME=EP (Auto)
LEGACY_SOURCE_SHEET_NAME=Album Log
TIMEZONE=America/Chicago
```

## Finding your spreadsheet ID

Open the spreadsheet in your browser. The URL looks like:

```text
https://docs.google.com/spreadsheets/d/SPREADSHEET_ID/edit#gid=0
```

Copy the `SPREADSHEET_ID` part into `.env`.

## Recommended first run

If you already have a manual sheet, use this order:

### Optional: import the existing manual sheet

Set `LEGACY_SOURCE_SHEET_NAME` in `.env`, then run:

```bash
go run ./cmd/lastfm-sheet-sync import-legacy
```

This copies rows from your old sheet into `Albums (Auto)` and seeds local progress state. If `Notes` is numeric for incomplete albums, the importer assumes tracks `1..N-1` have already been heard.

### One-time full Last.fm backfill

```bash
go run ./cmd/lastfm-sheet-sync backfill --reset-state
```

This rebuilds progress from all available Last.fm scrobbles and updates the automation tab.

### Daily incremental sync

```bash
go run ./cmd/lastfm-sheet-sync sync
```

By default, `sync` uses the last successful sync timestamp if one exists. Otherwise it falls back to the last `SYNC_WINDOW_HOURS` hours.

## Build a binary

```bash
go build -o bin/lastfm-sheet-sync ./cmd/lastfm-sheet-sync
```

Then run:

```bash
./bin/lastfm-sheet-sync sync
```

## Commands

### `sync`

Incremental sync.

```bash
go run ./cmd/lastfm-sheet-sync sync
```

Optional flags:

```bash
go run ./cmd/lastfm-sheet-sync sync --from 1713916800 --to 1714003200 --dry-run
```

### `backfill`

Full-history import from Last.fm.

```bash
go run ./cmd/lastfm-sheet-sync backfill --reset-state
```

### `import-legacy`

Imports an existing Google Sheets tab into the automation tab.

```bash
go run ./cmd/lastfm-sheet-sync import-legacy
```

## Environment variables

See `.env.example` for the full list.

Required for normal sync/backfill:

- `LASTFM_API_KEY`
- `LASTFM_USERNAME`
- `GOOGLE_SPREADSHEET_ID`
- `GOOGLE_SERVICE_ACCOUNT_JSON`

Common optional values:

- `TARGET_SHEET_NAME` – defaults to `Albums (Auto)`
- `EP_SHEET_NAME` – defaults to `EP (Auto)`
- `SINGLES_SHEET_NAME` – defaults to `Singles (Auto)`
- `LEGACY_SOURCE_SHEET_NAME` – only needed for `import-legacy`
- `TIMEZONE` – defaults to `America/Chicago`
- `SYNC_WINDOW_HOURS` – defaults to `24`
- `STATE_DIR` – defaults to `./data/state`
- `CACHE_DIR` – defaults to `./data/cache`
- `LASTFM_REQUEST_DELAY_MS` – defaults to `300`
- `DRY_RUN` – defaults to `false`

## Data files written locally

The app stores a small amount of local state:

- `./data/state/state.json` – sync cursor and album progress
- `./data/cache/*.json` – cached album metadata from Last.fm and MusicBrainz

## Scheduling with cron

Example cron entry:

```cron
15 2 * * * cd /opt/lastfm-sheet-sync && ./bin/lastfm-sheet-sync sync >> ./logs/sync.log 2>&1
```

## Notes and limitations

- The app uses Last.fm album metadata to get tracklists. If Last.fm has no tracklist for a release, it falls back to MusicBrainz.
- Track completion is matched by normalized track title within the album tracklist.
- Alternate editions, deluxe editions, and compilation albums can occasionally need a manual cleanup pass.
- Google Sheets access is spreadsheet-wide for the service account; the scope is not limited to only one tab.

## Quick sanity check

Run these before scheduling:

```bash
go test ./...
go run ./cmd/lastfm-sheet-sync sync --dry-run
```

## Development

The project intentionally uses only the Go standard library so it is easy to study and modify.
