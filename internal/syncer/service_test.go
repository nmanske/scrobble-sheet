package syncer

import (
	"testing"
	"time"

	"lastfm-sheet-sync/internal/model"
)

func TestLowestMissingTrack(t *testing.T) {
	heard := map[int]bool{1: true, 2: true, 4: true}
	if got := lowestMissingTrack(heard, 5); got != 3 {
		t.Fatalf("lowestMissingTrack = %d, want 3", got)
	}
	heard[3] = true
	heard[5] = true
	if got := lowestMissingTrack(heard, 5); got != 0 {
		t.Fatalf("lowestMissingTrack complete album = %d, want 0", got)
	}
}

func TestMatchTrackToRankUsesNextDuplicate(t *testing.T) {
	meta := model.AlbumMetadata{Tracks: []model.AlbumTrack{
		{Rank: 1, Name: "Intro"},
		{Rank: 2, Name: "Song"},
		{Rank: 3, Name: "Intro"},
	}}
	heard := map[int]bool{1: true}
	rank, ok := matchTrackToRank("Intro", meta, heard)
	if !ok {
		t.Fatal("expected a duplicate track match")
	}
	if rank != 3 {
		t.Fatalf("matchTrackToRank returned %d, want 3", rank)
	}
}

func TestStripArtistFeatures(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Ty Dolla $ign feat. Future", "Ty Dolla $ign"},
		{"Drake ft. Lil Wayne", "Drake"},
		{"Artist featuring Guest", "Artist"},
		{"Artist with Guest", "Artist"},
		{"Jay-Z & Kanye West", "Jay-Z & Kanye West"},
		{"Artist (feat. Guest)", "Artist"},
		{"Solo Artist", "Solo Artist"},
	}
	for _, c := range cases {
		if got := stripArtistFeatures(c.in); got != c.want {
			t.Errorf("stripArtistFeatures(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMissingTracksNote(t *testing.T) {
	cases := []struct {
		heard       map[int]bool
		totalTracks int
		want        string
	}{
		{map[int]bool{1: true, 2: true, 4: true}, 5, "3, 5"},
		{map[int]bool{}, 3, "1, 2, 3"},
		{map[int]bool{1: true, 2: true, 3: true}, 3, ""},
		{map[int]bool{1: true}, 1, ""},
	}
	for _, c := range cases {
		if got := missingTracksNote(c.heard, c.totalTracks); got != c.want {
			t.Errorf("missingTracksNote(%v, %d) = %q, want %q", c.heard, c.totalTracks, got, c.want)
		}
	}
}

func TestParseMissingTracks(t *testing.T) {
	cases := []struct {
		in   string
		want map[int]bool
	}{
		{"3, 5", map[int]bool{3: true, 5: true}},
		{"1", map[int]bool{1: true}},
		{"", nil},
		{"not a number", nil},
		{"3, bad", nil},
	}
	for _, c := range cases {
		got := parseMissingTracks(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseMissingTracks(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for k := range c.want {
			if !got[k] {
				t.Errorf("parseMissingTracks(%q): missing key %d", c.in, k)
			}
		}
	}
}

func TestParseFlexibleDate(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{"12/19/17", "12/19/2017", "2017-12-19"}
	for _, input := range cases {
		if _, ok := parseFlexibleDate(input, loc); !ok {
			t.Fatalf("parseFlexibleDate(%q) failed", input)
		}
	}
}
