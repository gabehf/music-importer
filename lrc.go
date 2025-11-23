package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type LRCLibResponse struct {
	SyncedLyrics string `json:"syncedLyrics"`
	PlainLyrics  string `json:"plainLyrics"`
}

func TrackDuration(path string) (int, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe error: %w (%s)", err, stderr.String())
	}

	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return 0, fmt.Errorf("empty duration output from ffprobe")
	}

	flt, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration: %w (raw=%q)", err, raw)
	}

	return int(flt + 0.5), nil // round to nearest second
}

// DownloadAlbumLyrics downloads synced lyrics (LRC format) for each track in the album directory.
// Assumes metadata is already final (tags complete).
func DownloadAlbumLyrics(albumDir string) error {
	err := filepath.Walk(albumDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))
		if ext != ".mp3" && ext != ".flac" {
			return nil
		}

		// Skip if LRC already exists next to the file
		lrcPath := strings.TrimSuffix(path, ext) + ".lrc"
		if _, err := os.Stat(lrcPath); err == nil {
			fmt.Println("→ Skipping (already has lyrics):", filepath.Base(path))
			return nil
		}

		// Read metadata
		md, err := readTags(path)
		if err != nil {
			fmt.Println("Skipping (unable to read tags):", path, "error:", err)
			return nil
		}
		if md.Title == "" || md.Artist == "" || md.Album == "" {
			fmt.Println("Skipping (missing metadata):", path)
			return nil
		}

		duration, _ := TrackDuration(path)

		lyrics, err := fetchLRCLibLyrics(md.Artist, md.Title, md.Album, duration)
		if err != nil {
			fmt.Println("No lyrics found:", md.Artist, "-", md.Title)
			return nil
		}

		// Write .lrc file
		if err := os.WriteFile(lrcPath, []byte(lyrics), 0644); err != nil {
			return fmt.Errorf("writing lrc file for %s: %w", path, err)
		}

		fmt.Println("→ Downloaded lyrics:", filepath.Base(lrcPath))
		return nil
	})

	return err
}

// fetchLRCLibLyrics calls the LRCLIB API and returns synced lyrics if available.
func fetchLRCLibLyrics(artist, title, album string, duration int) (string, error) {

	url := fmt.Sprintf(
		"https://lrclib.net/api/get?artist_name=%s&track_name=%s&album_name=%s&duration=%d",
		urlEncode(artist), urlEncode(title), urlEncode(album), duration,
	)

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("lrclib fetch error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lrclib returned status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading lrclib response: %w", err)
	}

	var out LRCLibResponse
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return "", fmt.Errorf("parsing lrclib json: %w", err)
	}

	if out.SyncedLyrics != "" {
		return out.SyncedLyrics, nil
	}

	// If no syncedLyrics, fallback to plain
	if out.PlainLyrics != "" {
		// Convert plain text to a fake LRC wrapper
		return plainToLRC(out.PlainLyrics), nil
	}

	return "", fmt.Errorf("no lyrics found")
}

// URL escape helper
func urlEncode(s string) string {
	r := strings.ReplaceAll(s, " ", "+")
	return r
}

// Convert plaintext lyrics to a basic unsynced LRC (fallback)
func plainToLRC(plain string) string {
	lines := strings.Split(plain, "\n")
	var out strings.Builder

	for _, line := range lines {
		// LRC format with [00:00.00] prefix when no timing exists
		out.WriteString("[00:00.00] ")
		out.WriteString(strings.TrimSpace(line))
		out.WriteByte('\n')
	}

	return out.String()
}
