package lastfm

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lastfm-sheet-sync/internal/model"
)

const apiBase = "https://ws.audioscrobbler.com/2.0/"

type Client struct {
	apiKey    string
	username  string
	cacheDir  string
	userAgent string
	delay     time.Duration
	http      *http.Client

	mu       sync.Mutex
	lastCall time.Time
}

type recentTracksResponse struct {
	XMLName      xml.Name          `xml:"lfm"`
	Status       string            `xml:"status,attr"`
	Error        *lastfmError      `xml:"error"`
	RecentTracks recentTracksBlock `xml:"recenttracks"`
}

type recentTracksBlock struct {
	User       string           `xml:"user,attr"`
	Page       int              `xml:"page,attr"`
	PerPage    int              `xml:"perPage,attr"`
	TotalPages int              `xml:"totalPages,attr"`
	Total      int              `xml:"total,attr"`
	Tracks     []recentXMLTrack `xml:"track"`
}

type recentXMLTrack struct {
	NowPlaying string     `xml:"nowplaying,attr"`
	Artist     textMBID   `xml:"artist"`
	Name       string     `xml:"name"`
	MBID       string     `xml:"mbid"`
	Album      textMBID   `xml:"album"`
	Date       *dateValue `xml:"date"`
}

type textMBID struct {
	MBID string `xml:"mbid,attr"`
	Text string `xml:",chardata"`
}

type dateValue struct {
	UTS  int64  `xml:"uts,attr"`
	Text string `xml:",chardata"`
}

type albumInfoResponse struct {
	XMLName xml.Name       `xml:"lfm"`
	Status  string         `xml:"status,attr"`
	Error   *lastfmError   `xml:"error"`
	Album   albumInfoBlock `xml:"album"`
}

type albumInfoBlock struct {
	Name        string          `xml:"name"`
	Artist      string          `xml:"artist"`
	MBID        string          `xml:"mbid"`
	ReleaseDate string          `xml:"releasedate"`
	Tracks      albumTrackBlock `xml:"tracks"`
}

type albumTrackBlock struct {
	Tracks []albumInfoTrack `xml:"track"`
}

type albumInfoTrack struct {
	Rank int    `xml:"rank,attr"`
	Name string `xml:"name"`
}

type lastfmError struct {
	Code int    `xml:"code,attr"`
	Text string `xml:",chardata"`
}

func NewClient(apiKey, username, cacheDir, userAgent string, delay time.Duration, httpClient *http.Client) *Client {
	return &Client{
		apiKey:    apiKey,
		username:  username,
		cacheDir:  cacheDir,
		userAgent: userAgent,
		delay:     delay,
		http:      httpClient,
	}
}

func (c *Client) FetchWindowScrobbles(ctx context.Context, fromUnix, toUnix int64) ([]model.Scrobble, error) {
	if fromUnix > 0 && toUnix > 0 && fromUnix > toUnix {
		fromUnix, toUnix = toUnix, fromUnix
	}

	page := 1
	var all []model.Scrobble
	for {
		resp, err := c.getRecentTracksPage(ctx, page, 200, fromUnix, toUnix)
		if err != nil {
			return nil, err
		}
		for _, track := range resp.RecentTracks.Tracks {
			if track.Date == nil || strings.TrimSpace(track.Album.Text) == "" {
				continue
			}
			all = append(all, toScrobble(track))
		}
		if page >= resp.RecentTracks.TotalPages || resp.RecentTracks.TotalPages == 0 {
			break
		}
		page++
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Timestamp < all[j].Timestamp })
	return all, nil
}

func (c *Client) FetchAllScrobbles(ctx context.Context) ([]model.Scrobble, error) {
	page := 1
	var all []model.Scrobble
	for {
		resp, err := c.getRecentTracksPage(ctx, page, 200, 0, 0)
		if err != nil {
			return nil, err
		}
		for _, track := range resp.RecentTracks.Tracks {
			if track.Date == nil || strings.TrimSpace(track.Album.Text) == "" {
				continue
			}
			all = append(all, toScrobble(track))
		}
		if page >= resp.RecentTracks.TotalPages || resp.RecentTracks.TotalPages == 0 {
			break
		}
		page++
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Timestamp < all[j].Timestamp })
	return all, nil
}

func (c *Client) ResolveAlbum(ctx context.Context, artist, album, mbid string) (model.AlbumMetadata, error) {
	key := albumCacheKey(artist, album, mbid)
	if cached, ok := c.readAlbumCache(key); ok {
		return cached, nil
	}

	params := url.Values{}
	params.Set("method", "album.getInfo")
	params.Set("api_key", c.apiKey)
	params.Set("autocorrect", "1")
	if strings.TrimSpace(mbid) != "" {
		params.Set("mbid", strings.TrimSpace(mbid))
	} else {
		params.Set("artist", artist)
		params.Set("album", album)
	}

	body, err := c.request(ctx, params)
	if err != nil {
		return model.AlbumMetadata{}, err
	}
	var resp albumInfoResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return model.AlbumMetadata{}, fmt.Errorf("parse album.getInfo response: %w", err)
	}
	if strings.EqualFold(resp.Status, "failed") || resp.Error != nil {
		if resp.Error != nil {
			return model.AlbumMetadata{}, fmt.Errorf("last.fm album.getInfo error %d: %s", resp.Error.Code, strings.TrimSpace(resp.Error.Text))
		}
		return model.AlbumMetadata{}, fmt.Errorf("last.fm album.getInfo failed")
	}

	meta := model.AlbumMetadata{
		Artist:       strings.TrimSpace(resp.Album.Artist),
		Album:        strings.TrimSpace(resp.Album.Name),
		MBID:         strings.TrimSpace(resp.Album.MBID),
		ReleaseDate:  strings.TrimSpace(resp.Album.ReleaseDate),
		Year:         extractYear(resp.Album.ReleaseDate),
		SourceArtist: artist,
		SourceAlbum:  album,
	}
	for _, tr := range resp.Album.Tracks.Tracks {
		name := strings.TrimSpace(tr.Name)
		rank := tr.Rank
		if name == "" || rank <= 0 {
			continue
		}
		meta.Tracks = append(meta.Tracks, model.AlbumTrack{Rank: rank, Name: name})
	}
	sort.Slice(meta.Tracks, func(i, j int) bool { return meta.Tracks[i].Rank < meta.Tracks[j].Rank })
	if meta.Artist == "" {
		meta.Artist = artist
	}
	if meta.Album == "" {
		meta.Album = album
	}
	_ = c.writeAlbumCache(key, meta)
	return meta, nil
}

func (c *Client) getRecentTracksPage(ctx context.Context, page, limit int, fromUnix, toUnix int64) (*recentTracksResponse, error) {
	params := url.Values{}
	params.Set("method", "user.getRecentTracks")
	params.Set("user", c.username)
	params.Set("api_key", c.apiKey)
	params.Set("page", strconv.Itoa(page))
	params.Set("limit", strconv.Itoa(limit))
	if fromUnix > 0 {
		params.Set("from", strconv.FormatInt(fromUnix, 10))
	}
	if toUnix > 0 {
		params.Set("to", strconv.FormatInt(toUnix, 10))
	}

	body, err := c.request(ctx, params)
	if err != nil {
		return nil, err
	}
	var resp recentTracksResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse user.getRecentTracks response: %w", err)
	}
	if strings.EqualFold(resp.Status, "failed") || resp.Error != nil {
		if resp.Error != nil {
			return nil, fmt.Errorf("last.fm user.getRecentTracks error %d: %s", resp.Error.Code, strings.TrimSpace(resp.Error.Text))
		}
		return nil, fmt.Errorf("last.fm user.getRecentTracks failed")
	}
	return &resp, nil
}

func (c *Client) request(ctx context.Context, params url.Values) ([]byte, error) {
	params.Set("format", "xml")
	if err := c.wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build last.fm request: %w", err)
	}
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request last.fm API: %w", err)
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("last.fm API error: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

func (c *Client) wait(ctx context.Context) error {
	if c.delay <= 0 {
		c.mu.Lock()
		c.lastCall = time.Now()
		c.mu.Unlock()
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	next := c.lastCall.Add(c.delay)
	now := time.Now()
	if now.Before(next) {
		timer := time.NewTimer(next.Sub(now))
		defer timer.Stop()
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			c.mu.Lock()
			return ctx.Err()
		case <-timer.C:
		}
		c.mu.Lock()
	}
	c.lastCall = time.Now()
	return nil
}

func toScrobble(track recentXMLTrack) model.Scrobble {
	return model.Scrobble{
		TrackName:   strings.TrimSpace(track.Name),
		Artist:      strings.TrimSpace(track.Artist.Text),
		Album:       strings.TrimSpace(track.Album.Text),
		AlbumMBID:   strings.TrimSpace(track.Album.MBID),
		TrackMBID:   strings.TrimSpace(track.MBID),
		Timestamp:   track.Date.UTS,
		NowPlaying:  strings.EqualFold(strings.TrimSpace(track.NowPlaying), "true"),
		RawArtist:   strings.TrimSpace(track.Artist.Text),
		RawAlbum:    strings.TrimSpace(track.Album.Text),
		DisplayDate: strings.TrimSpace(track.Date.Text),
	}
}

func extractYear(releaseDate string) string {
	releaseDate = strings.TrimSpace(releaseDate)
	if releaseDate == "" {
		return ""
	}
	layouts := []string{
		"2 Jan 2006, 15:04",
		"02 Jan 2006, 15:04",
		time.RFC1123Z,
		time.RFC1123,
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, releaseDate); err == nil {
			return strconv.Itoa(t.Year())
		}
	}
	fields := strings.Fields(releaseDate)
	for _, field := range fields {
		if len(field) == 4 {
			if _, err := strconv.Atoi(field); err == nil {
				return field
			}
		}
	}
	return ""
}

func albumCacheKey(artist, album, mbid string) string {
	if strings.TrimSpace(mbid) != "" {
		return "mbid:" + strings.TrimSpace(mbid)
	}
	return model.NormalizeKey(artist, album)
}

func (c *Client) readAlbumCache(key string) (model.AlbumMetadata, bool) {
	filename := filepath.Join(c.cacheDir, hashKey(key)+".json")
	raw, err := os.ReadFile(filename)
	if err != nil {
		return model.AlbumMetadata{}, false
	}
	var meta model.AlbumMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return model.AlbumMetadata{}, false
	}
	return meta, true
}

func (c *Client) writeAlbumCache(key string, meta model.AlbumMetadata) error {
	filename := filepath.Join(c.cacheDir, hashKey(key)+".json")
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, raw, 0o644)
}

func hashKey(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])
}
