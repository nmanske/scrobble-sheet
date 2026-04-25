package syncer

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lastfm-sheet-sync/internal/lastfm"
	"lastfm-sheet-sync/internal/model"
	"lastfm-sheet-sync/internal/sheets"
	statestore "lastfm-sheet-sync/internal/state"
)

type Service struct {
	cfg        model.Config
	lastfm     *lastfm.Client
	sheets     *sheets.Client
	stateStore *statestore.Store
	logger     *log.Logger
	loc        *time.Location
}

type Summary struct {
	ScrobblesProcessed     int
	ScrobblesSkipped       int
	AlbumsCreated          int
	AlbumsCompleted        int
	AlbumsUpdated          int
	ExistingAlbumsIgnored  int
	MetadataLookupFailures int
	TracklistFallbacks     int
	ImportedLegacyRows     int
	UpdatedLegacyRows      int
}

type sheetIndex struct {
	rows      []*model.SheetRow
	rowsByKey map[string]*model.SheetRow
	nextRow   int
}

func NewService(cfg model.Config, lastfmClient *lastfm.Client, sheetsClient *sheets.Client, stateStore *statestore.Store, logger *log.Logger) (*Service, error) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", cfg.Timezone, err)
	}
	return &Service{
		cfg:        cfg,
		lastfm:     lastfmClient,
		sheets:     sheetsClient,
		stateStore: stateStore,
		logger:     logger,
		loc:        loc,
	}, nil
}

func (s *Service) Run(ctx context.Context, opts model.RuntimeOptions) (Summary, error) {
	switch opts.Mode {
	case model.ModeSync:
		return s.runSync(ctx, opts)
	case model.ModeBackfill:
		return s.runBackfill(ctx, opts)
	case model.ModeImportLegacy:
		return s.runImportLegacy(ctx, opts)
	default:
		return Summary{}, fmt.Errorf("unsupported mode %q", opts.Mode)
	}
}

func (s *Service) runSync(ctx context.Context, opts model.RuntimeOptions) (Summary, error) {
	if s.lastfm == nil {
		return Summary{}, fmt.Errorf("last.fm client not configured")
	}
	if err := s.prepareTargetSheet(ctx); err != nil {
		return Summary{}, err
	}

	idx, err := s.loadSheetIndex(ctx, s.cfg.TargetSheetName)
	if err != nil {
		return Summary{}, err
	}
	singlesIdx, err := s.loadSheetIndex(ctx, s.cfg.SinglesSheetName)
	if err != nil {
		return Summary{}, err
	}
	epIdx, err := s.loadSheetIndex(ctx, s.cfg.EPSheetName)
	if err != nil {
		return Summary{}, err
	}
	st, err := s.stateStore.Load()
	if err != nil {
		return Summary{}, err
	}
	s.seedStateFromTargetRows(&st, idx)
	s.seedStateFromTargetRows(&st, singlesIdx)
	s.seedStateFromTargetRows(&st, epIdx)

	fromUnix, toUnix, err := s.computeSyncWindow(st, opts)
	if err != nil {
		return Summary{}, err
	}
	s.logger.Printf("sync window: from=%d to=%d (%s to %s)", fromUnix, toUnix, time.Unix(fromUnix, 0).In(s.loc).Format(time.RFC3339), time.Unix(toUnix, 0).In(s.loc).Format(time.RFC3339))

	scrobbles, err := s.lastfm.FetchWindowScrobbles(ctx, fromUnix, toUnix)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, scrobble := range scrobbles {
		if err := s.applyScrobble(ctx, &summary, idx, singlesIdx, epIdx, &st, scrobble); err != nil {
			return summary, err
		}
	}

	st.LastSuccessfulSyncUTC = time.Unix(toUnix, 0).UTC().Format(time.RFC3339)
	if err := s.persist(ctx, idx, singlesIdx, epIdx, st, opts.DryRun || s.cfg.DryRun); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *Service) runBackfill(ctx context.Context, opts model.RuntimeOptions) (Summary, error) {
	if s.lastfm == nil {
		return Summary{}, fmt.Errorf("last.fm client not configured")
	}
	if err := s.prepareTargetSheet(ctx); err != nil {
		return Summary{}, err
	}
	if opts.ResetState {
		if err := s.stateStore.Delete(); err != nil {
			return Summary{}, err
		}
	}

	idx, err := s.loadSheetIndex(ctx, s.cfg.TargetSheetName)
	if err != nil {
		return Summary{}, err
	}
	singlesIdx, err := s.loadSheetIndex(ctx, s.cfg.SinglesSheetName)
	if err != nil {
		return Summary{}, err
	}
	epIdx, err := s.loadSheetIndex(ctx, s.cfg.EPSheetName)
	if err != nil {
		return Summary{}, err
	}
	st, err := s.stateStore.Load()
	if err != nil {
		return Summary{}, err
	}
	s.seedStateFromTargetRows(&st, singlesIdx)
	s.seedStateFromTargetRows(&st, epIdx)

	s.logger.Printf("backfill: fetching full Last.fm history...")
	scrobbles, err := s.lastfm.FetchAllScrobbles(ctx)
	if err != nil {
		return Summary{}, err
	}
	s.logger.Printf("backfill: fetched %d scrobbles", len(scrobbles))

	summary := Summary{}
	for i, scrobble := range scrobbles {
		if err := s.applyScrobble(ctx, &summary, idx, singlesIdx, epIdx, &st, scrobble); err != nil {
			return summary, err
		}

		if i > 0 && i%500 == 0 {
			s.logger.Printf("backfill: checkpoint at %d / %d", i, len(scrobbles))
			if err := s.persist(ctx, idx, singlesIdx, epIdx, st, opts.DryRun || s.cfg.DryRun); err != nil {
				return summary, err
			}
		}
	}

	st.LastSuccessfulSyncUTC = time.Now().UTC().Format(time.RFC3339)
	s.logger.Printf("backfill: persisting %d dirty row(s)", len(idx.dirtyRows()))
	if err := s.persist(ctx, idx, singlesIdx, epIdx, st, opts.DryRun || s.cfg.DryRun); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *Service) runImportLegacy(ctx context.Context, opts model.RuntimeOptions) (Summary, error) {
	if err := s.prepareTargetSheet(ctx); err != nil {
		return Summary{}, err
	}
	idx, err := s.loadSheetIndex(ctx, s.cfg.TargetSheetName)
	if err != nil {
		return Summary{}, err
	}
	st, err := s.stateStore.Load()
	if err != nil {
		return Summary{}, err
	}
	sourceRows, err := s.sheets.ReadRows(ctx, s.cfg.LegacySourceSheetName)
	if err != nil {
		return Summary{}, fmt.Errorf("read legacy source sheet %q: %w", s.cfg.LegacySourceSheetName, err)
	}

	candidates := make(map[string]*model.SheetRow)
	for rowIndex, row := range sourceRows {
		if rowIndex == 0 {
			continue
		}
		candidate := rowToSheetRow(rowIndex+1, row)
		key := model.NormalizeKey(candidate.Artist, candidate.Album)
		if key == "|" || key == "" {
			continue
		}
		candidate.Key = key
		if existing, ok := candidates[key]; ok {
			candidates[key] = betterLegacyRow(existing, candidate, s.loc)
		} else {
			candidates[key] = candidate
		}
	}

	summary := Summary{}
	for key, candidate := range candidates {
		row, _ := idx.lookup(key)
		if row == nil {
			row = candidate.Clone()
			row.Key = key
			row.Existing = false
			row.RowNumber = 0
			row.Dirty = true
			idx.addRow(row)
			summary.ImportedLegacyRows++
		} else if mergeIntoTargetRow(row, candidate, s.loc) {
			summary.UpdatedLegacyRows++
		}
		upsertStateFromRow(&st, key, row, s.loc)
	}

	singlesIdx := &sheetIndex{rowsByKey: make(map[string]*model.SheetRow), nextRow: 2}
	epIdx := &sheetIndex{rowsByKey: make(map[string]*model.SheetRow), nextRow: 2}
	if err := s.persist(ctx, idx, singlesIdx, epIdx, st, opts.DryRun || s.cfg.DryRun); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *Service) prepareTargetSheet(ctx context.Context) error {
	if err := s.sheets.EnsureSheet(ctx, s.cfg.TargetSheetName); err != nil {
		return fmt.Errorf("ensure target sheet: %w", err)
	}
	if err := s.sheets.EnsureHeaderRow(ctx, s.cfg.TargetSheetName); err != nil {
		return fmt.Errorf("ensure header row: %w", err)
	}
	if s.cfg.SinglesSheetName != "" {
		if err := s.sheets.EnsureSheet(ctx, s.cfg.SinglesSheetName); err != nil {
			return fmt.Errorf("ensure singles sheet: %w", err)
		}
		if err := s.sheets.EnsureHeaderRow(ctx, s.cfg.SinglesSheetName); err != nil {
			return fmt.Errorf("ensure singles header row: %w", err)
		}
	}
	if s.cfg.EPSheetName != "" {
		if err := s.sheets.EnsureSheet(ctx, s.cfg.EPSheetName); err != nil {
			return fmt.Errorf("ensure EP sheet: %w", err)
		}
		if err := s.sheets.EnsureHeaderRow(ctx, s.cfg.EPSheetName); err != nil {
			return fmt.Errorf("ensure EP header row: %w", err)
		}
	}
	return nil
}

func (s *Service) computeSyncWindow(st model.State, opts model.RuntimeOptions) (int64, int64, error) {
	now := time.Now().UTC().Unix()
	toUnix := opts.ToUnix
	if toUnix <= 0 {
		toUnix = now
	}

	fromUnix := opts.FromUnix
	if fromUnix <= 0 {
		if strings.TrimSpace(st.LastSuccessfulSyncUTC) != "" {
			if parsed, err := time.Parse(time.RFC3339, st.LastSuccessfulSyncUTC); err == nil {
				fromUnix = parsed.Unix()
			}
		}
	}
	if fromUnix <= 0 {
		fromUnix = toUnix - int64(s.cfg.SyncWindowHours*3600)
	}
	if fromUnix < 0 {
		fromUnix = 0
	}
	if fromUnix > toUnix {
		return 0, 0, fmt.Errorf("computed invalid sync window: from %d > to %d", fromUnix, toUnix)
	}
	return fromUnix, toUnix, nil
}

func (s *Service) loadSheetIndex(ctx context.Context, sheetName string) (*sheetIndex, error) {
	rows, err := s.sheets.ReadRows(ctx, sheetName)
	if err != nil {
		return nil, err
	}
	idx := &sheetIndex{rowsByKey: make(map[string]*model.SheetRow), nextRow: len(rows) + 1}
	for i, row := range rows {
		rowNumber := i + 1
		if rowNumber == 1 {
			continue
		}
		current := rowToSheetRow(rowNumber, row)
		key := model.NormalizeKey(stripArtistFeatures(current.Artist), current.Album)
		if key == "|" || key == "" {
			continue
		}
		current.Key = key
		current.Existing = true
		if _, exists := idx.rowsByKey[key]; exists {
			s.logger.Printf("warning: duplicate key %q already exists in target sheet; leaving later row %d untouched", key, rowNumber)
			continue
		}
		idx.addRow(current)
	}
	if idx.nextRow < 2 {
		idx.nextRow = 2
	}
	return idx, nil
}

func looksLikeSingleRelease(trackName, albumName string) bool {
	return model.NormalizeText(trackName) == model.NormalizeText(albumName)
}

func (s *Service) applyScrobble(ctx context.Context, summary *Summary, idx, singlesIdx, epIdx *sheetIndex, st *model.State, scrobble model.Scrobble) error {
	summary.ScrobblesProcessed++
	if scrobble.NowPlaying || scrobble.Timestamp <= 0 || strings.TrimSpace(scrobble.Album) == "" {
		summary.ScrobblesSkipped++
		return nil
	}

	meta, err := s.lastfm.ResolveAlbum(ctx, scrobble.Artist, scrobble.Album, scrobble.AlbumMBID)
	if err != nil {
		summary.MetadataLookupFailures++
		s.logger.Printf("warning: could not resolve metadata for %q / %q: %v", scrobble.Artist, scrobble.Album, err)
		return nil
	}

	rawKey := model.NormalizeKey(scrobble.Artist, scrobble.Album)
	canonicalArtist := stripArtistFeatures(firstNonEmpty(meta.Artist, scrobble.Artist))
	canonicalAlbum := firstNonEmpty(meta.Album, scrobble.Album)
	canonicalKey := model.NormalizeKey(canonicalArtist, canonicalAlbum)

	row, chosenKey := idx.lookup(canonicalKey, rawKey)
	stateKey, albumState := findState(*st, canonicalKey, rawKey)
	if chosenKey == "" {
		chosenKey = firstNonEmpty(stateKey, canonicalKey, rawKey)
	}
	if chosenKey == "" {
		chosenKey = rawKey
	}

	trackCount := highestTrackRank(meta)
	activeIdx := idx
	switch {
	case row == nil && epIdx != nil && meta.ReleaseGroupType == "EP":
		activeIdx = epIdx
		if existing, _ := epIdx.lookup(canonicalKey, rawKey); existing != nil {
			row = existing
		}
	case row == nil && singlesIdx != nil &&
		(meta.ReleaseGroupType == "Single" || (meta.ReleaseGroupType == "" && trackCount == 1)):
		activeIdx = singlesIdx
		if existing, _ := singlesIdx.lookup(canonicalKey, rawKey); existing != nil {
			row = existing
		}
	}

	if row == nil {
		row = &model.SheetRow{
			Key:      chosenKey,
			Artist:   canonicalArtist,
			Album:    canonicalAlbum,
			Year:     firstNonEmpty(meta.Year, ""),
			Existing: false,
			Dirty:    true,
		}
		activeIdx.addRow(row)
		summary.AlbumsCreated++
	}
	activeIdx.addAlias(rawKey, row)
	activeIdx.addAlias(canonicalKey, row)

	if !albumState.Completed && strings.TrimSpace(row.DateListened) != "" {
		albumState.Completed = true
	}
	if albumState.Artist == "" {
		albumState.Artist = row.Artist
	}
	if albumState.Album == "" {
		albumState.Album = row.Album
	}
	if albumState.Year == "" {
		albumState.Year = firstNonEmpty(row.Year, meta.Year)
	}

	if albumState.Completed {
		(*st).Albums[chosenKey] = albumState
		summary.ExistingAlbumsIgnored++
		return nil
	}

	if row.Artist == "" {
		row.Artist = canonicalArtist
		row.Dirty = true
	}
	if row.Album == "" {
		row.Album = canonicalAlbum
		row.Dirty = true
	}
	if row.Year == "" && meta.Year != "" {
		row.Year = meta.Year
		row.Dirty = true
	}

	if albumState.FirstScrobbleUnix == 0 || scrobble.Timestamp < albumState.FirstScrobbleUnix {
		albumState.FirstScrobbleUnix = scrobble.Timestamp
	}

	if trackCount == 0 {
		summary.TracklistFallbacks++
		albumState.TrackCount = 0
		(*st).Albums[chosenKey] = albumState
		return nil
	}

	heard := albumState.HeardSet()
	matchedRank, matched := matchTrackToRank(scrobble.TrackName, meta, heard)

	if !matched {
		fallbackArtist, fallbackAlbum, fallbackPos, fallbackErr := s.lastfm.ResolveTrackPosition(ctx, scrobble.Artist, scrobble.TrackName)
		if fallbackErr == nil &&
			fallbackPos > 0 &&
			model.NormalizeText(fallbackArtist) == model.NormalizeText(canonicalArtist) &&
			model.NormalizeText(fallbackAlbum) == model.NormalizeText(canonicalAlbum) &&
			fallbackPos <= trackCount {

			matchedRank = fallbackPos
			matched = true
			s.logger.Printf(
				"track position fallback matched: artist=%q album=%q track=%q rank=%d",
				canonicalArtist, canonicalAlbum, scrobble.TrackName, matchedRank,
			)
		} else {
			s.logger.Printf(
				"warning: unmatched scrobble track artist=%q album=%q track=%q resolved_artist=%q resolved_album=%q track_count=%d fallback_artist=%q fallback_album=%q fallback_pos=%d fallback_err=%v",
				scrobble.Artist,
				scrobble.Album,
				scrobble.TrackName,
				canonicalArtist,
				canonicalAlbum,
				trackCount,
				fallbackArtist,
				fallbackAlbum,
				fallbackPos,
				fallbackErr,
			)
		}
	}

	if matched {
		heard[matchedRank] = true
	}

	albumState.SetHeardSet(heard)
	albumState.TrackCount = trackCount

	if lowestMissingTrack(heard, trackCount) == 0 {
		oldDate := row.DateListened
		oldNotes := row.Notes
		row.DateListened = firstNonEmpty(row.DateListened, s.formatDate(albumState.FirstScrobbleUnix))
		row.Notes = ""
		if row.DateListened != oldDate || row.Notes != oldNotes {
			row.Dirty = true
		}
		if !albumState.Completed {
			summary.AlbumsCompleted++
		}
		albumState.Completed = true
	} else {
		desiredNote := missingTracksNote(heard, trackCount)
		if row.DateListened != "" || row.Notes != desiredNote {
			row.DateListened = ""
			row.Notes = desiredNote
			row.Dirty = true
			summary.AlbumsUpdated++
		}
	}

	(*st).Albums[chosenKey] = albumState
	if chosenKey != canonicalKey && canonicalKey != "" {
		(*st).Albums[canonicalKey] = albumState
	}
	if chosenKey != rawKey && rawKey != "" {
		(*st).Albums[rawKey] = albumState
	}
	return nil
}

func (s *Service) seedStateFromTargetRows(st *model.State, idx *sheetIndex) {
	for _, row := range idx.rows {
		if row == nil || row.Key == "" {
			continue
		}
		upsertStateFromRow(st, row.Key, row, s.loc)
	}
}

func (s *Service) persist(ctx context.Context, idx, singlesIdx, epIdx *sheetIndex, st model.State, dryRun bool) error {
	dirty := idx.dirtyRows()
	singlesDirty := singlesIdx.dirtyRows()
	epDirty := epIdx.dirtyRows()
	if dryRun {
		s.logger.Printf("dry run enabled: would write %d row(s) to main sheet, %d row(s) to singles sheet, %d row(s) to EP sheet", len(dirty), len(singlesDirty), len(epDirty))
		return nil
	}
	if err := s.flushIndex(ctx, idx, s.cfg.TargetSheetName); err != nil {
		return err
	}
	if err := s.flushIndex(ctx, singlesIdx, s.cfg.SinglesSheetName); err != nil {
		return err
	}
	if err := s.flushIndex(ctx, epIdx, s.cfg.EPSheetName); err != nil {
		return err
	}
	return s.stateStore.Save(st)
}

func (s *Service) flushIndex(ctx context.Context, idx *sheetIndex, sheetName string) error {
	dirty := idx.dirtyRows()
	if len(dirty) == 0 {
		return nil
	}
	for _, row := range dirty {
		if !row.Existing {
			row.RowNumber = idx.nextRow
			idx.nextRow++
			row.Existing = true
		}
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].RowNumber < dirty[j].RowNumber })
	if err := s.sheets.BatchWriteRows(ctx, sheetName, dirty); err != nil {
		return err
	}
	for _, row := range dirty {
		row.Dirty = false
	}
	return nil
}

func (s *Service) formatDate(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).In(s.loc).Format("1/2/06")
}

func rowToSheetRow(rowNumber int, row []string) *model.SheetRow {
	padded := make([]string, model.SheetColumnCount)
	copy(padded, row)
	return &model.SheetRow{
		RowNumber:         rowNumber,
		DateListened:      strings.TrimSpace(padded[0]),
		Artist:            strings.TrimSpace(padded[1]),
		Album:             strings.TrimSpace(padded[2]),
		Year:              strings.TrimSpace(padded[3]),
		LiveMusicLocation: strings.TrimSpace(padded[4]),
		Download:          strings.TrimSpace(padded[5]),
		Notes:             strings.TrimSpace(padded[6]),
	}
}

func betterLegacyRow(existing, candidate *model.SheetRow, loc *time.Location) *model.SheetRow {
	exDate, exOK := parseFlexibleDate(existing.DateListened, loc)
	candDate, candOK := parseFlexibleDate(candidate.DateListened, loc)
	switch {
	case exOK && candOK:
		if candDate.Before(exDate) {
			return candidate.Clone()
		}
	case !exOK && candOK:
		return candidate.Clone()
	}

	out := existing.Clone()
	if out.Year == "" && candidate.Year != "" {
		out.Year = candidate.Year
	}
	if out.LiveMusicLocation == "" && candidate.LiveMusicLocation != "" {
		out.LiveMusicLocation = candidate.LiveMusicLocation
	}
	if out.Download == "" && candidate.Download != "" {
		out.Download = candidate.Download
	}
	if numericNote(candidate.Notes) > numericNote(out.Notes) {
		out.Notes = candidate.Notes
	}
	return out
}

func mergeIntoTargetRow(target, candidate *model.SheetRow, loc *time.Location) bool {
	original := target.Clone()

	target.Year = firstNonEmpty(target.Year, candidate.Year)
	target.LiveMusicLocation = firstNonEmpty(target.LiveMusicLocation, candidate.LiveMusicLocation)
	target.Download = firstNonEmpty(target.Download, candidate.Download)

	targetDate, targetOK := parseFlexibleDate(target.DateListened, loc)
	candidateDate, candOK := parseFlexibleDate(candidate.DateListened, loc)
	switch {
	case !targetOK && candOK:
		target.DateListened = candidate.DateListened
		target.Notes = ""
	case targetOK && candOK && candidateDate.Before(targetDate):
		target.DateListened = candidate.DateListened
		target.Notes = ""
	case !targetOK && !candOK:
		if numericNote(candidate.Notes) > numericNote(target.Notes) {
			target.Notes = candidate.Notes
		}
	}

	changed := target.DateListened != original.DateListened ||
		target.Year != original.Year ||
		target.LiveMusicLocation != original.LiveMusicLocation ||
		target.Download != original.Download ||
		target.Notes != original.Notes
	if changed {
		target.Dirty = true
	}
	return changed
}

func upsertStateFromRow(st *model.State, key string, row *model.SheetRow, loc *time.Location) {
	current := (*st).Albums[key]
	current.Artist = row.Artist
	current.Album = row.Album
	current.Year = firstNonEmpty(current.Year, row.Year)

	if dt, ok := parseFlexibleDate(row.DateListened, loc); ok {
		current.Completed = true
		if current.FirstScrobbleUnix == 0 || dt.Unix() < current.FirstScrobbleUnix {
			current.FirstScrobbleUnix = dt.Unix()
		}
		current.HeardRanks = nil
		current.TrackCount = 0
	} else if missingSet := parseMissingTracks(row.Notes); len(missingSet) > 0 {
		maxMissing := 0
		for rank := range missingSet {
			if rank > maxMissing {
				maxMissing = rank
			}
		}
		heard := make(map[int]bool)
		for rank := 1; rank <= maxMissing; rank++ {
			if !missingSet[rank] {
				heard[rank] = true
			}
		}
		current.SetHeardSet(heard)
		current.Completed = false
		if current.TrackCount < maxMissing {
			current.TrackCount = maxMissing
		}
	}

	(*st).Albums[key] = current
}

func highestTrackRank(meta model.AlbumMetadata) int {
	maxRank := 0
	for _, track := range meta.Tracks {
		if track.Rank > maxRank {
			maxRank = track.Rank
		}
	}
	return maxRank
}

var reArtistFeat = regexp.MustCompile(`(?i)\s*[\[(]?\s*(feat\.?|ft\.?|featuring|with)\b.*`)

func stripArtistFeatures(artist string) string {
	return strings.TrimSpace(reArtistFeat.ReplaceAllString(artist, ""))
}

func normalizeTrackName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	// Replace common unicode punctuation with spaces or nothing.
	replacer := strings.NewReplacer(
		"’", "'",
		"`", "'",
		"“", "\"",
		"”", "\"",
		"–", "-",
		"—", "-",
		"&", "and",
	)
	s = replacer.Replace(s)

	// Remove parenthetical/bracketed suffixes like:
	// (feat. Shiro), [Remix], (Live), etc.
	reParen := regexp.MustCompile(`\([^)]*\)`)
	s = reParen.ReplaceAllString(s, " ")

	reBracket := regexp.MustCompile(`\[[^\]]*\]`)
	s = reBracket.ReplaceAllString(s, " ")

	// Remove trailing "feat/ft/with ..." even if not parenthesized.
	reFeat := regexp.MustCompile(`(?i)\b(feat\.?|ft\.?|with)\b.*$`)
	s = reFeat.ReplaceAllString(s, " ")

	// Remove punctuation except letters/numbers/spaces.
	rePunct := regexp.MustCompile(`[^a-z0-9\s]`)
	s = rePunct.ReplaceAllString(s, " ")

	// Collapse whitespace.
	s = strings.Join(strings.Fields(s), " ")

	return s
}

func matchTrackToRank(name string, meta model.AlbumMetadata, heard map[int]bool) (int, bool) {
	target := normalizeTrackName(name)
	if target == "" {
		return 0, false
	}

	// Exact normalized match — prefer unheard ranks, fall back to any match.
	var firstExact int
	for _, tr := range meta.Tracks {
		if normalizeTrackName(tr.Name) == target {
			if !heard[tr.Rank] {
				return tr.Rank, true
			}
			if firstExact == 0 {
				firstExact = tr.Rank
			}
		}
	}
	if firstExact != 0 {
		return firstExact, true
	}

	// Then a looser containment fallback — prefer unheard ranks, fall back to any match.
	var firstLoose int
	for _, tr := range meta.Tracks {
		norm := normalizeTrackName(tr.Name)
		if norm == "" {
			continue
		}
		if strings.Contains(norm, target) || strings.Contains(target, norm) {
			if !heard[tr.Rank] {
				return tr.Rank, true
			}
			if firstLoose == 0 {
				firstLoose = tr.Rank
			}
		}
	}
	if firstLoose != 0 {
		return firstLoose, true
	}

	return 0, false
}

func lowestMissingTrack(heard map[int]bool, totalTracks int) int {
	if totalTracks <= 0 {
		return 0
	}
	for rank := 1; rank <= totalTracks; rank++ {
		if !heard[rank] {
			return rank
		}
	}
	return 0
}

func missingTracksNote(heard map[int]bool, totalTracks int) string {
	var parts []string
	for rank := 1; rank <= totalTracks; rank++ {
		if !heard[rank] {
			parts = append(parts, strconv.Itoa(rank))
		}
	}
	return strings.Join(parts, ", ")
}

func parseMissingTracks(value string) map[int]bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make(map[int]bool, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 {
			return nil
		}
		out[n] = true
	}
	return out
}

func findState(st model.State, keys ...string) (string, model.AlbumState) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if value, ok := st.Albums[key]; ok {
			return key, value
		}
	}
	return "", model.AlbumState{}
}

func (idx *sheetIndex) addRow(row *model.SheetRow) {
	idx.rows = append(idx.rows, row)
	if row.Key != "" {
		idx.rowsByKey[row.Key] = row
	}
}

func (idx *sheetIndex) addAlias(key string, row *model.SheetRow) {
	if key == "" || row == nil {
		return
	}
	if _, exists := idx.rowsByKey[key]; !exists {
		idx.rowsByKey[key] = row
	}
}

func (idx *sheetIndex) lookup(keys ...string) (*model.SheetRow, string) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if row, ok := idx.rowsByKey[key]; ok {
			return row, key
		}
	}
	return nil, ""
}

func (idx *sheetIndex) dirtyRows() []*model.SheetRow {
	out := make([]*model.SheetRow, 0)
	seen := make(map[*model.SheetRow]bool)
	for _, row := range idx.rows {
		if row != nil && row.Dirty && !seen[row] {
			out = append(out, row)
			seen[row] = true
		}
	}
	return out
}

func parseFlexibleDate(value string, loc *time.Location) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{"1/2/06", "01/02/06", "1/2/2006", "01/02/2006", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func numericNote(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if idx := strings.Index(value, ","); idx != -1 {
		value = strings.TrimSpace(value[:idx])
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
