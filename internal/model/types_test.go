package model

import "testing"

func TestNormalizeText(t *testing.T) {
	cases := map[string]string{
		"Remain in Light":        "remain in light",
		"Sun Giant EP":           "sun giant ep",
		"Cobra Juicy":            "cobra juicy",
		"Björk":                  "björk",
		"Artist & Album":         "artist and album",
		"  Spaces   Everywhere ": "spaces everywhere",
		"Track-03 (Remastered)":  "track 03 remastered",
		"❖":                      "❖",
		"  ✝✝✝  ":                "✝✝✝",
		"★ ★ ★":                  "★ ★ ★",
	}
	for in, want := range cases {
		if got := NormalizeText(in); got != want {
			t.Fatalf("NormalizeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	got := NormalizeKey("Talking Heads", "Remain in Light")
	if got != "talking heads|remain in light" {
		t.Fatalf("NormalizeKey returned %q", got)
	}
}

func TestIsVariousArtists(t *testing.T) {
	cases := map[string]bool{
		"Various Artists":     true,
		"various artists":     true,
		"  VARIOUS ARTISTS  ": true,
		"Kate Bush" + VariousArtistsDelimiter + "Peter Gabriel": true,
		"Various":   false,
		"Kate Bush": false,
		"":          false,
		`A \ B`:     false,
	}
	for in, want := range cases {
		if got := IsVariousArtists(in); got != want {
			t.Errorf("IsVariousArtists(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTrackArtists(t *testing.T) {
	meta := AlbumMetadata{Tracks: []AlbumTrack{
		{Rank: 1, Name: "One", Artist: "Robbie Williams"},
		{Rank: 2, Name: "Two", Artist: "  "},
		{Rank: 3, Name: "Three", Artist: "Kylie Minogue"},
		{Rank: 4, Name: "Four", Artist: "robbie williams"},
		{Rank: 5, Name: "Five", Artist: "Outkast"},
	}}
	got := meta.TrackArtists()
	want := []string{"Robbie Williams", "Kylie Minogue", "Outkast"}
	if len(got) != len(want) {
		t.Fatalf("TrackArtists() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TrackArtists()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if empty := (AlbumMetadata{}).TrackArtists(); len(empty) != 0 {
		t.Fatalf("TrackArtists() on empty metadata = %v", empty)
	}
}

func TestNormalizeKeySymbolOnlyNames(t *testing.T) {
	// Symbol-only names must not collapse to empty: a "|" key is skipped
	// when indexing existing sheet rows, causing duplicate rows every sync.
	if got := NormalizeKey("❖", "❖"); got == "|" {
		t.Fatalf("NormalizeKey(❖, ❖) collapsed to %q", got)
	}
	a := NormalizeKey("Artist", "❖")
	b := NormalizeKey("Artist", "✝")
	if a == b {
		t.Fatalf("distinct symbol-only albums collide on key %q", a)
	}
	if a == NormalizeKey("Artist", "") {
		t.Fatalf("symbol-only album key %q equals empty-album key", a)
	}
}
