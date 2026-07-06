package lastfm

import (
	"encoding/json"
	"encoding/xml"
	"testing"
)

func TestAlbumInfoTrackArtistParsing(t *testing.T) {
	payload := `<?xml version="1.0" encoding="UTF-8"?>
<lfm status="ok">
  <album>
    <name>NOW That's What I Call Music! 10</name>
    <artist>Various Artists</artist>
    <mbid>c7383197-1b49-41f7-a1a4-eb26c971871e</mbid>
    <tracks>
      <track rank="1">
        <name>Radio</name>
        <artist>
          <name>Robbie Williams</name>
          <mbid>db4624cf-0e44-481e-a9dc-2142b833ec2f</mbid>
          <url>https://www.last.fm/music/Robbie+Williams</url>
        </artist>
      </track>
    </tracks>
  </album>
</lfm>`
	var resp albumInfoResponse
	if err := xml.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatalf("unmarshal album.getInfo: %v", err)
	}
	if resp.Album.Artist != "Various Artists" {
		t.Fatalf("album artist = %q", resp.Album.Artist)
	}
	tracks := resp.Album.Tracks.Tracks
	if len(tracks) != 1 || tracks[0].Rank != 1 || tracks[0].Name != "Radio" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
	if tracks[0].Artist.Name != "Robbie Williams" {
		t.Fatalf("track artist = %q", tracks[0].Artist.Name)
	}
}

func TestMBTrackArtistCreditParsing(t *testing.T) {
	payload := `{
  "media": [{
    "position": 1,
    "tracks": [{
      "position": 1,
      "title": "Radio",
      "artist-credit": [
        {"name": "Robbie Williams", "joinphrase": " & "},
        {"name": "Kylie Minogue", "joinphrase": ""}
      ]
    }]
  }]
}`
	var resp mbReleaseResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatalf("unmarshal MB release: %v", err)
	}
	tracks := tracksFromMBMedia(resp.Media)
	if len(tracks) != 1 {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
	if tracks[0].Artist != "Robbie Williams & Kylie Minogue" {
		t.Fatalf("joined artist credit = %q", tracks[0].Artist)
	}
}
