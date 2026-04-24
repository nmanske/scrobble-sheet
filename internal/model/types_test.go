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
