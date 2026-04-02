package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type MusicMetadata struct {
	Artist string
	Album  string
	Title  string
	Year   string
}

// Read embedded tags from an audio file using ffprobe.
func readTags(path string) (*MusicMetadata, error) {
	out, err := exec.Command(
		"ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_format", path,
	).Output()
	if err != nil {
		return nil, err
	}

	var data struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}

	json.Unmarshal(out, &data)

	t := data.Format.Tags
	if t == nil {
		return &MusicMetadata{}, nil
	}

	return &MusicMetadata{
		Artist: firstNonEmpty(t["artist"], t["ARTIST"]),
		Album:  firstNonEmpty(t["album"], t["ALBUM"]),
		Title:  firstNonEmpty(t["title"], t["TITLE"]),
		Year:   firstNonEmpty(t["year"], t["YEAR"], t["ORIGINALYEAR"]),
	}, nil
}

// Use beets to fetch metadata and tag all files in a directory.
func tagWithBeets(path string) error {
	fmt.Println("→ Tagging with beets:", path)
	return runCmd("beet", "import", "-Cq", path)
}

// Fallback: query MusicBrainz API manually if beets fails.
func fetchMusicBrainzInfo(filename string) (*MusicMetadata, error) {
	fmt.Println("→ Fallback: querying MusicBrainz:", filename)

	query := fmt.Sprintf("recording:%q", strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	url := "https://musicbrainz.org/ws/2/recording/?query=" + query + "&fmt=json"

	resp, err := exec.Command("curl", "-s", url).Output()
	if err != nil {
		return nil, err
	}

	var data struct {
		Recordings []struct {
			Title    string `json:"title"`
			Releases []struct {
				Title        string `json:"title"`
				ArtistCredit []struct {
					Name string `json:"name"`
				} `json:"artist-credit"`
			} `json:"releases"`
			FirstReleaseDate string `json:"first-release-date"`
		} `json:"recordings"`
	}

	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, err
	}

	if len(data.Recordings) == 0 || len(data.Recordings[0].Releases) == 0 {
		return nil, errors.New("no MusicBrainz match")
	}

	r := data.Recordings[0]
	rel := r.Releases[0]

	return &MusicMetadata{
		Artist: rel.ArtistCredit[0].Name,
		Album:  rel.Title,
		Title:  r.Title,
		Year:   strings.Split(r.FirstReleaseDate, "-")[0],
	}, nil
}

// getAlbumMetadata attempts beets tagging on the album directory, reads tags
// back from the first track, and falls back to MusicBrainz if tags are missing.
func getAlbumMetadata(albumPath, trackPath string) (*MusicMetadata, error) {
	fmt.Println("→ Tagging track with beets:", trackPath)

	if err := tagWithBeets(albumPath); err != nil {
		fmt.Println("Beets tagging failed; fallback to manual MusicBrainz lookup:", err)
	}

	md, err := readTags(trackPath)
	if err == nil && md.Artist != "" && md.Album != "" {
		return md, nil
	}

	fmt.Println("→ Missing tags, attempting MusicBrainz manual lookup...")

	md, err = fetchMusicBrainzInfo(trackPath)
	if err != nil {
		return nil, fmt.Errorf("metadata lookup failed: %w", err)
	}

	return md, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
