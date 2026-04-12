package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// slskdAttr is a Soulseek file attribute (bitrate, sample rate, bit depth, etc.).
// Attribute types: 0 = bitrate (kbps), 1 = duration (s), 2 = VBR flag,
//
//	4 = sample rate (Hz), 5 = bit depth.
type slskdAttr struct {
	Type  int `json:"type"`
	Value int `json:"value"`
}

// slskdFile is a single file in a slskd search response.
type slskdFile struct {
	Filename   string      `json:"filename"`
	Size       int64       `json:"size"`
	Extension  string      `json:"extension"`
	Attributes []slskdAttr `json:"attributes"`
}

// slskdPeerResponse is one peer's response to a search.
type slskdPeerResponse struct {
	Username string      `json:"username"`
	Files    []slskdFile `json:"files"`
}

// slskdSearch is the search-state object returned by GET /api/v0/searches/{id}.
// File responses are not included here; fetch them from /searches/{id}/responses.
type slskdSearch struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Quality tiers; higher value = more preferred.
const (
	qualityUnknown   = 0
	qualityMP3Any    = 1
	qualityMP3_320   = 2
	qualityFLACOther = 3 // FLAC at unspecified or uncommon specs
	qualityFLAC24_96 = 4
	qualityFLAC16_44 = 5 // most preferred: standard CD-quality lossless
)

// albumFolder groups audio files from the same peer and directory path.
type albumFolder struct {
	Username string
	Dir      string
	Files    []slskdFile
	Quality  int
}

func slskdBaseURL() string {
	return strings.TrimRight(os.Getenv("SLSKD_URL"), "/")
}

// slskdDo performs an authenticated HTTP request against the slskd API.
func slskdDo(method, endpoint string, body interface{}) (*http.Response, error) {
	base := slskdBaseURL()
	if base == "" {
		return nil, fmt.Errorf("SLSKD_URL is not configured")
	}

	var br io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		br = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, base+endpoint, br)
	if err != nil {
		return nil, err
	}
	if key := os.Getenv("SLSKD_API_KEY"); key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return http.DefaultClient.Do(req)
}

// createSlskdSearch starts a new slskd search and returns its ID.
func createSlskdSearch(searchText string) (string, error) {
	payload := map[string]interface{}{
		"searchText":             searchText,
		"fileLimit":              10000,
		"filterResponses":        true,
		"maximumPeerQueueLength": 1000,
		"minimumPeerUploadSpeed": 0,
		"responseLimit":          100,
		"timeout":                15000,
	}

	resp, err := slskdDo("POST", "/api/v0/searches", payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("slskd search failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var s slskdSearch
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	return s.ID, nil
}

// slskdSearchIsTerminal reports whether a slskd SearchStates string has reached
// a terminal state. slskd serialises its [Flags] enum as a comma-separated list
// (e.g. "Completed, TimedOut"), so we check for containment rather than equality.
func slskdSearchIsTerminal(state string) bool {
	for _, term := range []string{"Completed", "TimedOut", "Errored", "Cancelled"} {
		if strings.Contains(state, term) {
			return true
		}
	}
	return false
}

// pollSlskdSearch waits up to 30 s for a search to reach a terminal state,
// then returns the responses from the dedicated /responses sub-endpoint.
// Each poll check-in is reported via logf.
func pollSlskdSearch(id string, logf func(string)) ([]slskdPeerResponse, error) {
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := slskdDo("GET", "/api/v0/searches/"+id, nil)
		if err != nil {
			return nil, err
		}
		var s slskdSearch
		err = json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		logf(fmt.Sprintf("Search state: %s", s.State))

		if slskdSearchIsTerminal(s.State) {
			return fetchSlskdResponses(id, logf)
		}

		if time.Now().After(deadline) {
			logf("Poll deadline reached, fetching current results")
			return fetchSlskdResponses(id, logf)
		}
		time.Sleep(2 * time.Second)
	}
}

// fetchSlskdResponses fetches file responses from the dedicated sub-endpoint.
// The main GET /searches/{id} endpoint only returns metadata; responses live at
// /searches/{id}/responses.
func fetchSlskdResponses(id string, logf func(string)) ([]slskdPeerResponse, error) {
	resp, err := slskdDo("GET", "/api/v0/searches/"+id+"/responses", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetching responses failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var responses []slskdPeerResponse
	if err := json.NewDecoder(resp.Body).Decode(&responses); err != nil {
		return nil, fmt.Errorf("decoding responses: %w", err)
	}
	logf(fmt.Sprintf("Fetched %d peer responses", len(responses)))
	return responses, nil
}

// deleteSlskdSearch removes a search from slskd (best-effort cleanup).
func deleteSlskdSearch(id string) {
	resp, err := slskdDo("DELETE", "/api/v0/searches/"+id, nil)
	if err == nil {
		resp.Body.Close()
	}
}

// fileDir returns the directory portion of a Soulseek filename,
// normalising backslashes to forward slashes first.
func fileDir(filename string) string {
	return path.Dir(strings.ReplaceAll(filename, "\\", "/"))
}

// normaliseExt returns a lower-case extension that always starts with ".".
func normaliseExt(raw string) string {
	ext := strings.ToLower(raw)
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// fileQuality scores a single file by the preferred quality tier.
func fileQuality(f slskdFile) int {
	ext := normaliseExt(f.Extension)
	if ext == "." || ext == "" {
		ext = strings.ToLower(path.Ext(strings.ReplaceAll(f.Filename, "\\", "/")))
	}

	switch ext {
	case ".flac":
		var depth, rate int
		for _, a := range f.Attributes {
			switch a.Type {
			case 4:
				rate = a.Value
			case 5:
				depth = a.Value
			}
		}
		if depth == 16 && rate == 44100 {
			return qualityFLAC16_44
		}
		if depth == 24 && rate == 96000 {
			return qualityFLAC24_96
		}
		return qualityFLACOther

	case ".mp3":
		for _, a := range f.Attributes {
			if a.Type == 0 && a.Value >= 315 {
				return qualityMP3_320
			}
		}
		return qualityMP3Any
	}

	return qualityUnknown
}

// groupAlbumFolders groups audio files by (username, directory) and scores each group.
func groupAlbumFolders(responses []slskdPeerResponse) []albumFolder {
	type key struct{ user, dir string }
	m := make(map[key]*albumFolder)

	for _, r := range responses {
		for _, f := range r.Files {
			ext := normaliseExt(f.Extension)
			if ext == "." || ext == "" {
				ext = strings.ToLower(path.Ext(strings.ReplaceAll(f.Filename, "\\", "/")))
			}
			if ext != ".flac" && ext != ".mp3" {
				continue
			}

			k := key{r.Username, fileDir(f.Filename)}
			if m[k] == nil {
				m[k] = &albumFolder{Username: r.Username, Dir: k.dir}
			}
			m[k].Files = append(m[k].Files, f)
			if q := fileQuality(f); q > m[k].Quality {
				m[k].Quality = q
			}
		}
	}

	out := make([]albumFolder, 0, len(m))
	for _, af := range m {
		out = append(out, *af)
	}
	return out
}

// bestAlbumFolder picks the highest-quality folder; file count breaks ties.
func bestAlbumFolder(folders []albumFolder) *albumFolder {
	if len(folders) == 0 {
		return nil
	}
	best := &folders[0]
	for i := 1; i < len(folders); i++ {
		a := &folders[i]
		if a.Quality > best.Quality || (a.Quality == best.Quality && len(a.Files) > len(best.Files)) {
			best = a
		}
	}
	return best
}

// queueSlskdDownload sends a batch download request to slskd for all files in folder.
func queueSlskdDownload(folder *albumFolder) error {
	type dlFile struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	files := make([]dlFile, len(folder.Files))
	for i, f := range folder.Files {
		files[i] = dlFile{Filename: f.Filename, Size: f.Size}
	}

	resp, err := slskdDo("POST", "/api/v0/transfers/downloads/"+folder.Username, files)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slskd download request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// qualityLabel returns a human-readable label for a quality tier constant.
func qualityLabel(q int) string {
	switch q {
	case qualityFLAC16_44:
		return "FLAC 16bit/44.1kHz"
	case qualityFLAC24_96:
		return "FLAC 24bit/96kHz"
	case qualityFLACOther:
		return "FLAC"
	case qualityMP3_320:
		return "MP3 320kbps"
	case qualityMP3Any:
		return "MP3"
	default:
		return "unknown"
	}
}

// slskdTransferFile is one file entry in a slskd transfers response.
type slskdTransferFile struct {
	Filename      string `json:"filename"`
	LocalFilename string `json:"localFilename"`
	State         string `json:"state"`
	Size          int64  `json:"size"`
}

// slskdTransferDir groups transfer files by remote directory.
type slskdTransferDir struct {
	Directory string              `json:"directory"`
	Files     []slskdTransferFile `json:"files"`
}

// slskdUserTransfers is the object returned by GET /api/v0/transfers/downloads/{username}.
type slskdUserTransfers struct {
	Directories []slskdTransferDir `json:"directories"`
}

// getSlskdTransfers returns all active/pending download transfer directories for a peer.
func getSlskdTransfers(username string) ([]slskdTransferDir, error) {
	resp, err := slskdDo("GET", "/api/v0/transfers/downloads/"+username, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("slskd transfers (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var ut slskdUserTransfers
	if err := json.NewDecoder(resp.Body).Decode(&ut); err != nil {
		return nil, err
	}
	return ut.Directories, nil
}

// fetchRelease searches slskd for an album, queues the best-quality match for
// download, and returns the chosen folder so the caller can monitor completion.
// mbid, if non-empty, will be stored for use during import (beets --search-id).
// trackCount, if > 0, filters candidate folders to those whose audio file count
// matches the expected number of tracks on the release, so alternate editions
// with different track counts are not accidentally selected.
func fetchRelease(artist, album, mbid string, trackCount int, logf func(string)) (*albumFolder, error) {
	query := artist + " " + album
	log.Printf("[discover] fetch started: %q by %s (expected tracks: %d)", album, artist, trackCount)
	logf("Starting fetch for: " + query)

	logf("Creating slskd search…")
	id, err := createSlskdSearch(query)
	if err != nil {
		return nil, fmt.Errorf("create search: %w", err)
	}
	log.Printf("[discover] slskd search created: %s", id)
	logf(fmt.Sprintf("Search created (id: %s)", id))
	defer func() {
		log.Printf("[discover] deleting slskd search %s", id)
		deleteSlskdSearch(id)
	}()

	logf("Polling for results…")
	responses, err := pollSlskdSearch(id, logf)
	if err != nil {
		return nil, fmt.Errorf("poll search: %w", err)
	}
	log.Printf("[discover] search %s finished: %d peer responses", id, len(responses))
	logf(fmt.Sprintf("Search finished: %d peer responses received", len(responses)))

	logf("Grouping results into album folders…")
	folders := groupAlbumFolders(responses)
	log.Printf("[discover] grouped into %d candidate album folders", len(folders))
	logf(fmt.Sprintf("Found %d candidate album folders", len(folders)))

	if len(folders) == 0 {
		return nil, fmt.Errorf("no audio files found for %q by %s", album, artist)
	}

	// When we know the expected track count, prefer folders that match exactly
	// so we don't accidentally grab a bonus-track edition or a different version
	// that won't align with the release MBID we pass to beets.
	candidates := folders
	if trackCount > 0 {
		var matched []albumFolder
		for _, f := range folders {
			if len(f.Files) == trackCount {
				matched = append(matched, f)
			}
		}
		if len(matched) > 0 {
			log.Printf("[discover] %d/%d folders match expected track count (%d)", len(matched), len(folders), trackCount)
			logf(fmt.Sprintf("Filtered to %d/%d folders matching expected track count (%d)",
				len(matched), len(folders), trackCount))
			candidates = matched
		} else {
			log.Printf("[discover] no folders matched expected track count (%d); using best available", trackCount)
			logf(fmt.Sprintf("Warning: no folders matched expected track count (%d); using best available", trackCount))
		}
	}

	best := bestAlbumFolder(candidates)
	log.Printf("[discover] selected folder: %s from %s (%s, %d files)",
		best.Dir, best.Username, qualityLabel(best.Quality), len(best.Files))
	logf(fmt.Sprintf("Selected folder: %s", best.Dir))
	logf(fmt.Sprintf("  Peer: %s | Quality: %s | Files: %d",
		best.Username, qualityLabel(best.Quality), len(best.Files)))

	logf(fmt.Sprintf("Queuing %d files for download…", len(best.Files)))
	if err := queueSlskdDownload(best); err != nil {
		return nil, fmt.Errorf("queue download: %w", err)
	}
	log.Printf("[discover] download queued: %d files from %s", len(best.Files), best.Username)
	logf("Download queued — waiting for completion before import")
	return best, nil
}
