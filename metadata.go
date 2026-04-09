package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type MusicMetadata struct {
	Artist  string
	Album   string
	Title   string
	Year    string // four-digit year, kept for backward compat
	Date    string // normalised as YYYY.MM.DD (or YYYY.MM or YYYY)
	Quality string // e.g. "FLAC-24bit-96kHz" or "MP3-320kbps"
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

	rawDate := firstNonEmpty(t["date"], t["DATE"], t["year"], t["YEAR"], t["ORIGINALYEAR"])
	date := parseDate(rawDate)
	year := ""
	if len(date) >= 4 {
		year = date[:4]
	}

	return &MusicMetadata{
		Artist: firstNonEmpty(t["artist"], t["ARTIST"]),
		Album:  firstNonEmpty(t["album"], t["ALBUM"]),
		Title:  firstNonEmpty(t["title"], t["TITLE"]),
		Year:   year,
		Date:   date,
	}, nil
}

// parseDate normalises a raw DATE/date tag value into YYYY.MM.DD (or YYYY.MM
// or YYYY) dot-separated format, or returns the input unchanged if it cannot
// be recognised.
//
// Supported input formats:
//   - YYYY
//   - YYYY-MM
//   - YYYY-MM-DD
//   - YYYYMMDD
func parseDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// YYYYMMDD (exactly 8 digits, no separators)
	if len(raw) == 8 && isAllDigits(raw) {
		return raw[0:4] + "." + raw[4:6] + "." + raw[6:8]
	}

	// YYYY-MM-DD, YYYY-MM, or YYYY (with dashes)
	parts := strings.Split(raw, "-")
	switch len(parts) {
	case 1:
		if len(parts[0]) == 4 && isAllDigits(parts[0]) {
			return parts[0]
		}
	case 2:
		if len(parts[0]) == 4 && isAllDigits(parts[0]) && len(parts[1]) == 2 && isAllDigits(parts[1]) {
			return parts[0] + "." + parts[1]
		}
	case 3:
		if len(parts[0]) == 4 && isAllDigits(parts[0]) &&
			len(parts[1]) == 2 && isAllDigits(parts[1]) &&
			len(parts[2]) == 2 && isAllDigits(parts[2]) {
			return parts[0] + "." + parts[1] + "." + parts[2]
		}
	}

	// Unrecognised — return as-is so we don't silently drop it.
	return raw
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// readAudioQuality probes the first audio stream of path and returns a
// quality label such as "FLAC-24bit-96kHz" or "MP3-320kbps".
func readAudioQuality(path string) (string, error) {
	out, err := exec.Command(
		"ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_streams", "-select_streams", "a:0",
		path,
	).Output()
	if err != nil {
		return "", err
	}

	var data struct {
		Streams []struct {
			CodecName        string `json:"codec_name"`
			SampleRate       string `json:"sample_rate"`
			BitRate          string `json:"bit_rate"`
			BitsPerRawSample string `json:"bits_per_raw_sample"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(out, &data); err != nil {
		return "", err
	}
	if len(data.Streams) == 0 {
		return "", fmt.Errorf("no audio streams found in %s", path)
	}

	s := data.Streams[0]
	codec := strings.ToUpper(s.CodecName) // e.g. "FLAC", "MP3"

	switch strings.ToLower(s.CodecName) {
	case "flac":
		bits := s.BitsPerRawSample
		if bits == "" || bits == "0" {
			bits = "16" // safe fallback
		}
		khz := sampleRateToKHz(s.SampleRate)
		return fmt.Sprintf("%s-%sbit-%s", codec, bits, khz), nil

	case "mp3":
		kbps := snapMP3Bitrate(s.BitRate)
		return fmt.Sprintf("%s-%dkbps", codec, kbps), nil

	default:
		// Generic fallback: codec + bitrate if available.
		if s.BitRate != "" && s.BitRate != "0" {
			kbps := snapMP3Bitrate(s.BitRate)
			return fmt.Sprintf("%s-%dkbps", codec, kbps), nil
		}
		return codec, nil
	}
}

// sampleRateToKHz converts a sample-rate string in Hz (e.g. "44100") to a
// human-friendly kHz string (e.g. "44.1kHz").
func sampleRateToKHz(hz string) string {
	n, err := strconv.Atoi(strings.TrimSpace(hz))
	if err != nil || n == 0 {
		return "?kHz"
	}
	if n%1000 == 0 {
		return fmt.Sprintf("%dkHz", n/1000)
	}
	return fmt.Sprintf("%.1fkHz", float64(n)/1000.0)
}

// commonMP3Bitrates lists the standard MPEG audio bitrates in kbps.
var commonMP3Bitrates = []int{32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}

// snapMP3Bitrate rounds a raw bitrate string (in bps) to the nearest standard
// MP3 bitrate (in kbps). For example "318731" → 320.
func snapMP3Bitrate(bpsStr string) int {
	bps, err := strconv.Atoi(strings.TrimSpace(bpsStr))
	if err != nil || bps <= 0 {
		return 128 // safe fallback
	}
	kbps := float64(bps) / 1000.0
	best := commonMP3Bitrates[0]
	bestDiff := math.Abs(kbps - float64(best))
	for _, candidate := range commonMP3Bitrates[1:] {
		if d := math.Abs(kbps - float64(candidate)); d < bestDiff {
			bestDiff = d
			best = candidate
		}
	}
	return best
}

// Use beets to fetch metadata and tag all files in a directory.
// A temp log file is passed to beets via -l so that skipped albums
// (which exit 0 but produce a "skip" log entry) are detected and
// returned as errors, triggering the MusicBrainz fallback.
// If mbid is non-empty it is passed as --search-id to pin beets to a specific
// MusicBrainz release.
func tagWithBeets(path, mbid string) error {
	fmt.Println("→ Tagging with beets:", path)

	logFile, err := os.CreateTemp("", "beets-log-*.txt")
	if err != nil {
		return fmt.Errorf("beets: could not create temp log file: %w", err)
	}
	logPath := logFile.Name()
	logFile.Close()
	defer os.Remove(logPath)

	args := []string{"import", "-Cq", "-l", logPath}
	// passing mbid to beet removed temporarily
	// if mbid != "" {
	// 	args = append(args, "--search-id", mbid)
	// }
	args = append(args, path)
	if err := runCmd("beet", args...); err != nil {
		return err
	}

	// Even on exit 0, beets may have skipped the album in quiet mode.
	// The log format is one entry per line: "<action> <path>"
	// We treat any "skip" line as a failure so the caller falls through
	// to the MusicBrainz lookup.
	skipped, err := beetsLogHasSkip(logPath)
	if err != nil {
		// If we can't read the log, assume beets succeeded.
		fmt.Println("beets: could not read log file:", err)
		return nil
	}
	if skipped {
		return errors.New("beets skipped album (no confident match found)")
	}
	return nil
}

// beetsLogHasSkip reads a beets import log file and reports whether any
// entry has the action "skip". The log format is:
//
//	# beets import log
//	<action> <path>
//	...
func beetsLogHasSkip(logPath string) (bool, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip blank lines and the header comment.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		action, _, found := strings.Cut(line, " ")
		if found && strings.EqualFold(action, "skip") {
			return true, nil
		}
	}
	return false, scanner.Err()
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
// If mbid is non-empty it is forwarded to beets as --search-id.
func getAlbumMetadata(albumPath, trackPath, mbid string) (*MusicMetadata, MetadataSource, error) {
	fmt.Println("→ Tagging track with beets:", trackPath)

	beetsErr := tagWithBeets(albumPath, mbid)
	if beetsErr != nil {
		fmt.Println("Beets tagging failed; fallback to manual MusicBrainz lookup:", beetsErr)
	}

	md, err := readTags(trackPath)
	if err == nil && md.Artist != "" && md.Album != "" {
		attachQuality(md, trackPath)
		if beetsErr == nil {
			return md, MetadataSourceBeets, nil
		}
		return md, MetadataSourceFileTags, nil
	}

	fmt.Println("→ Missing tags, attempting MusicBrainz manual lookup...")

	md, err = fetchMusicBrainzInfo(trackPath)
	if err != nil {
		return nil, MetadataSourceUnknown, fmt.Errorf("metadata lookup failed: %w", err)
	}

	attachQuality(md, trackPath)
	return md, MetadataSourceMusicBrainz, nil
}

// attachQuality probes trackPath for audio quality and sets md.Quality.
// Errors are logged but not returned — a missing quality label is non-fatal.
func attachQuality(md *MusicMetadata, trackPath string) {
	q, err := readAudioQuality(trackPath)
	if err != nil {
		fmt.Println("Could not determine audio quality:", err)
		return
	}
	md.Quality = q
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
