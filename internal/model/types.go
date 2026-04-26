package model

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	DefaultTargetSheetName  = "Albums (Auto)"
	DefaultSinglesSheetName = "Singles (Auto)"
	DefaultEPSheetName      = "EP (Auto)"
	DateHeader              = "Date Listened"
	ArtistHeader            = "Artist"
	AlbumHeader             = "Album"
	YearHeader              = "Year"
	NotesHeader             = "Notes"
	SheetColumnCount        = 5
)

type Config struct {
	LastFMAPIKey             string
	LastFMUsername           string
	GoogleSpreadsheetID      string
	GoogleServiceAccountJSON string
	TargetSheetName          string
	SinglesSheetName         string
	EPSheetName              string
	LegacySourceSheetName    string
	Timezone                 string
	SyncWindowHours          int
	StateDir                 string
	CacheDir                 string
	UserAgent                string
	HTTPTimeout              time.Duration
	LastFMRequestDelay       time.Duration
	DryRun                   bool
}

type Mode string

const (
	ModeSync         Mode = "sync"
	ModeBackfill     Mode = "backfill"
	ModeImportLegacy Mode = "import-legacy"
)

type RuntimeOptions struct {
	Mode       Mode
	DryRun     bool
	ResetState bool
	FromUnix   int64
	ToUnix     int64
}

type Scrobble struct {
	TrackName   string
	Artist      string
	Album       string
	AlbumMBID   string
	TrackMBID   string
	Timestamp   int64
	NowPlaying  bool
	RawArtist   string
	RawAlbum    string
	DisplayDate string
}

type AlbumTrack struct {
	Rank int    `json:"rank"`
	Name string `json:"name"`
}

type AlbumMetadata struct {
	Artist           string       `json:"artist"`
	Album            string       `json:"album"`
	MBID             string       `json:"mbid,omitempty"`
	ReleaseDate      string       `json:"release_date,omitempty"`
	Year             string       `json:"year,omitempty"`
	Tracks           []AlbumTrack `json:"tracks,omitempty"`
	SourceArtist     string       `json:"source_artist,omitempty"`
	SourceAlbum      string       `json:"source_album,omitempty"`
	ReleaseGroupType string       `json:"release_group_type,omitempty"`
}

type SheetRow struct {
	Key          string
	RowNumber    int
	DateListened string
	Artist       string
	Album        string
	Year         string
	Notes        string
	Dirty        bool
	Existing     bool
}

func (r *SheetRow) ToValues() []interface{} {
	return []interface{}{r.DateListened, r.Artist, r.Album, r.Year, r.Notes}
}

func (r *SheetRow) Clone() *SheetRow {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

type State struct {
	Version               int                   `json:"version"`
	LastSuccessfulSyncUTC string                `json:"last_successful_sync_utc,omitempty"`
	Albums                map[string]AlbumState `json:"albums"`
}

type AlbumState struct {
	Artist            string   `json:"artist"`
	Album             string   `json:"album"`
	Year              string   `json:"year,omitempty"`
	FirstScrobbleUnix int64    `json:"first_scrobble_unix,omitempty"`
	TrackCount        int      `json:"track_count,omitempty"`
	HeardRanks        []int    `json:"heard_ranks,omitempty"`
	Completed         bool     `json:"completed,omitempty"`
}

func (s *AlbumState) HeardSet() map[int]bool {
	out := make(map[int]bool, len(s.HeardRanks))
	for _, rank := range s.HeardRanks {
		out[rank] = true
	}
	return out
}

func (s *AlbumState) SetHeardSet(set map[int]bool) {
	ranks := make([]int, 0, len(set))
	for rank := range set {
		ranks = append(ranks, rank)
	}
	sort.Ints(ranks)
	s.HeardRanks = ranks
}

func (s *State) EnsureDefaults() {
	if s.Version == 0 {
		s.Version = 1
	}
	if s.Albums == nil {
		s.Albums = make(map[string]AlbumState)
	}
}

func Headers() []interface{} {
	return []interface{}{DateHeader, ArtistHeader, AlbumHeader, YearHeader, NotesHeader}
}

func NormalizeKey(artist, album string) string {
	return NormalizeText(artist) + "|" + NormalizeText(album)
}

func NormalizeText(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	if in == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(in))
	prevSpace := false
	for _, r := range in {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case r == '&':
			b.WriteString(" and ")
			prevSpace = false
		case r == '+' || r == '/' || r == '_' || r == '-' || r == ':' || r == ';' || r == ',' || r == '.' || r == '(' || r == ')' || r == '[' || r == ']' || r == '{' || r == '}' || r == '\'' || r == '"' || r == '!' || r == '?' || r == '#':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case unicode.IsSpace(r):
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
