package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	id3v2 "github.com/bogem/id3v2" // optional alternative
)

var coverNames = []string{
	"cover.jpg", "cover.jpeg", "cover.png",
	"folder.jpg", "folder.jpeg", "folder.png",
	"album.jpg", "album.jpeg", "album.png",
}

// EmbedAlbumArtIntoFolder scans one album folder and embeds cover art.
func EmbedAlbumArtIntoFolder(albumDir string) error {
	coverFile, err := FindCoverImage(albumDir)
	if err != nil {
		fmt.Println("Could not find cover image. Skipping embed...")
		return nil
	}

	coverData, err := os.ReadFile(coverFile)
	if err != nil {
		return fmt.Errorf("failed to read cover image: %w", err)
	}

	err = filepath.Walk(albumDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		lower := strings.ToLower(info.Name())
		switch {
		case strings.HasSuffix(lower, ".mp3"):
			return embedCoverMP3(path, coverData)
		case strings.HasSuffix(lower, ".flac"):
			return embedCoverFLAC(path, coverData)
		default:
			return nil
		}
	})

	return err
}

// DownloadCoverArt searches MusicBrainz for a release matching md's artist and
// album, then downloads the front cover from the Cover Art Archive and saves it
// as cover.jpg inside albumDir. Returns an error if no cover could be found or
// downloaded.
func DownloadCoverArt(albumDir string, md *MusicMetadata) error {
	mbid, err := searchMusicBrainzRelease(md.Artist, md.Album)
	if err != nil {
		return fmt.Errorf("MusicBrainz release search failed: %w", err)
	}

	data, ext, err := fetchCoverArtArchiveFront(mbid)
	if err != nil {
		return fmt.Errorf("Cover Art Archive fetch failed: %w", err)
	}

	dest := filepath.Join(albumDir, "cover."+ext)
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return fmt.Errorf("writing cover image: %w", err)
	}

	fmt.Println("→ Downloaded cover art:", filepath.Base(dest))
	return nil
}

// searchMusicBrainzRelease queries the MusicBrainz API for a release matching
// the given artist and album and returns its MBID.
func searchMusicBrainzRelease(artist, album string) (string, error) {
	q := fmt.Sprintf(`release:"%s" AND artist:"%s"`,
		strings.ReplaceAll(album, `"`, `\"`),
		strings.ReplaceAll(artist, `"`, `\"`),
	)
	apiURL := "https://musicbrainz.org/ws/2/release/?query=" + url.QueryEscape(q) + "&fmt=json&limit=1"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "music-importer/1.0 (https://github.com/example/music-importer)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MusicBrainz returned status %d", resp.StatusCode)
	}

	var result struct {
		Releases []struct {
			ID string `json:"id"`
		} `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Releases) == 0 {
		return "", fmt.Errorf("no MusicBrainz release found for %q by %q", album, artist)
	}
	return result.Releases[0].ID, nil
}

// fetchCoverArtArchiveFront fetches the front cover image for the given
// MusicBrainz release MBID from coverartarchive.org. It follows the 307
// redirect to the actual image and returns the raw bytes plus the file
// extension (e.g. "jpg" or "png").
func fetchCoverArtArchiveFront(mbid string) ([]byte, string, error) {
	apiURL := "https://coverartarchive.org/release/" + mbid + "/front"

	resp, err := http.Get(apiURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Cover Art Archive returned status %d for MBID %s", resp.StatusCode, mbid)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	// Derive the extension from the final URL after redirect, falling back to
	// sniffing the magic bytes.
	ext := "jpg"
	if finalURL := resp.Request.URL.String(); strings.HasSuffix(strings.ToLower(finalURL), ".png") {
		ext = "png"
	} else if bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
		ext = "png"
	}

	return data, ext, nil
}

// -------------------------
// Find cover image
// -------------------------
func FindCoverImage(dir string) (string, error) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		l := strings.ToLower(e.Name())
		if slices.Contains(coverNames, l) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no cover image found in %s", dir)
}

// -------------------------
// Embed into MP3
// -------------------------
func embedCoverMP3(path string, cover []byte) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("mp3 open: %w", err)
	}
	defer tag.Close()

	mime := guessMimeType(cover)

	pic := id3v2.PictureFrame{
		Encoding:    id3v2.EncodingUTF8,
		MimeType:    mime,
		PictureType: id3v2.PTFrontCover,
		Description: "Cover",
		Picture:     cover,
	}

	tag.AddAttachedPicture(pic)

	if err := tag.Save(); err != nil {
		return fmt.Errorf("mp3 save: %w", err)
	}

	fmt.Println("→ Embedded art into MP3:", filepath.Base(path))
	return nil
}

// embedCoverFLAC writes cover bytes to a tempfile and uses metaflac to import it.
// Requires `metaflac` (from the flac package) to be installed and in PATH.
func embedCoverFLAC(path string, cover []byte) error {
	// Ensure metaflac exists
	if _, err := exec.LookPath("metaflac"); err != nil {
		return fmt.Errorf("metaflac not found in PATH; please install package 'flac' (provides metaflac): %w", err)
	}

	// Create a temp file for the cover image
	tmp, err := os.CreateTemp("", "cover-*.img")
	if err != nil {
		return fmt.Errorf("creating temp file for cover: %w", err)
	}
	tmpPath := tmp.Name()
	// Ensure we remove the temp file later
	defer func() {
		tmp.Close()
		os.Remove(tmpPath)
	}()

	// Write cover bytes
	if _, err := tmp.Write(cover); err != nil {
		return fmt.Errorf("writing cover to temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		// non-fatal, but report if it happens
		return fmt.Errorf("sync temp cover file: %w", err)
	}

	// Remove existing PICTURE blocks (ignore non-zero exit -> continue, but report)
	removeCmd := exec.Command("metaflac", "--remove", "--block-type=PICTURE", path)
	removeOut, removeErr := removeCmd.CombinedOutput()
	if removeErr != nil {
		// metaflac returns non-zero if there were no picture blocks — that's OK.
		// Only fail if it's some unexpected error.
		// We'll print the output for debugging and continue.
		fmt.Printf("metaflac --remove output (may be fine): %s\n", string(removeOut))
	}

	// Import the new picture. metaflac will auto-detect mime type from the file.
	importCmd := exec.Command("metaflac", "--import-picture-from="+tmpPath, path)
	importOut, importErr := importCmd.CombinedOutput()
	if importErr != nil {
		return fmt.Errorf("metaflac --import-picture-from failed: %v; output: %s", importErr, string(importOut))
	}

	fmt.Println("→ Embedded art into FLAC:", filepath.Base(path))
	return nil
}

// -------------------------
// Helpers
// -------------------------
func guessMimeType(data []byte) string {
	if bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
		return "image/jpeg"
	}
	if bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
		return "image/png"
	}
	return "image/jpeg" // fallback
}
