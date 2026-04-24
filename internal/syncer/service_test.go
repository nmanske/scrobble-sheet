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
