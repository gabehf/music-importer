package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/gabehf/music-import/media"
)

type MusicMetadata struct {
	Artist string
	Album  string
	Title  string
}

// Run a shell command and return combined stdout/stderr.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Use beets to fetch metadata and tag the file.
// The -A flag is "autotag" with no import", -W is "write tags".
func tagWithBeets(path string) error {
	fmt.Println("→ Tagging with beets:", path)
	return runCmd("beet", "import", "-Cq", path)
}

// Fallback: query MusicBrainz API manually if beets fails.
// (very basic lookup using "track by name" search)
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

	artist := rel.ArtistCredit[0].Name
	album := rel.Title
	title := r.Title

	return &MusicMetadata{Artist: artist, Album: album, Title: title}, nil
}

// Apply ReplayGain using rsgain in "easy" mode.
func applyReplayGain(path string) error {
	fmt.Println("→ Applying ReplayGain:", path)
	return runCmd("rsgain", "easy", path)
}

// Move file to {LIBRARY_DIR}/{artist}/{album}/filename
func moveToLibrary(libDir string, md *MusicMetadata, srcPath string) error {
	targetDir := filepath.Join(libDir, sanitize(md.Artist), sanitize(md.Album))
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	dst := filepath.Join(targetDir, filepath.Base(srcPath))
	fmt.Println("→ Moving:", srcPath, "→", dst)
	return os.Rename(srcPath, dst)
}

// Remove filesystem-unsafe chars
func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "-", "?", "", "*", "", "\"", "", "<", "", ">", "", "|", "")
	return r.Replace(s)
}

// Read embedded tags using ffprobe (works for most formats).
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
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func RunImporter() {
	importDir := os.Getenv("IMPORT_DIR")
	libraryDir := os.Getenv("LIBRARY_DIR")

	if importDir == "" || libraryDir == "" {
		log.Println("IMPORT_DIR and LIBRARY_DIR must be set")
		return
	}

	fmt.Println("=== Starting Import ===")

	entries, err := os.ReadDir(importDir)
	if err != nil {
		log.Println("Failed to read import dir:", err)
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue // skip files
		}

		albumPath := filepath.Join(importDir, e.Name())

		// Check if the folder contains audio files
		tracks, err := getAudioFiles(albumPath)
		if err != nil {
			fmt.Println("Skipping (error scanning):", albumPath, err)
			continue
		}
		if len(tracks) == 0 {
			continue // no valid audio files → not an album folder
		}

		fmt.Println("\n===== Album:", e.Name(), "=====")

		// Get metadata for this album (using first track)
		md, err := getAlbumMetadata(albumPath, tracks[0])
		if err != nil {
			fmt.Println("Metadata failed, skipping album:", err)
			continue
		}

		// Apply album-wide ReplayGain
		fmt.Println("→ Applying ReplayGain to album:", albumPath)
		if err := applyReplayGain(albumPath); err != nil {
			fmt.Println("ReplayGain failed, skipping album:", err)
			continue
		}

		// embed cover img if available
		fmt.Println("→ Applying ReplayGain to album:", albumPath)
		if err := media.EmbedAlbumArtIntoFolder(albumPath); err != nil {
			fmt.Println("Cover embed failed, skipping album:", err)
			continue
		}

		// Move files to library
		for _, track := range tracks {
			if err := moveToLibrary(libraryDir, md, track); err != nil {
				fmt.Println("Failed to move track:", track, err)
			}
		}

		// Move album cover image
		if coverImg, err := media.FindCoverImage(albumPath); err == nil {
			if err := moveToLibrary(libraryDir, md, coverImg); err != nil {
				fmt.Println("Failed to cover image:", coverImg, err)
			}
		}

		// Remove empty album directory after moving files
		os.Remove(albumPath)
	}

	fmt.Println("\n=== Import Complete ===")
}

func getAudioFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var tracks []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".flac" || ext == ".mp3" {
			tracks = append(tracks, filepath.Join(dir, e.Name()))
		}
	}

	return tracks, nil
}

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

// --- WEB SERVER --- //
var importerMu sync.Mutex
var importerRunning bool
var tmpl = template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html>
<head>
	<title>Music Importer</title>
	<style>
		body {
			font-family: sans-serif;
			background: #111;
			color: #eee;
			text-align: center;
			padding-top: 80px;
		}
		button {
			font-size: 32px;
			padding: 20px 40px;
			border-radius: 10px;
			border: none;
			cursor: pointer;
			background: #4CAF50;
			color: white;
		}
		button:disabled {
			background: #555;
			cursor: not-allowed;
		}
	</style>
</head>
<body>
	<h1>Music Importer</h1>
	<form action="/run" method="POST">
		<button type="submit" {{if .Running}}disabled{{end}}>
			{{if .Running}}Importer Running...{{else}}Run Importer{{end}}
		</button>
	</form>
</body>
</html>
`))

func handleHome(w http.ResponseWriter, r *http.Request) {
	importerMu.Lock()
	running := importerRunning
	importerMu.Unlock()

	tmpl.Execute(w, struct{ Running bool }{Running: running})
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	importerMu.Lock()
	running := importerRunning
	importerMu.Unlock()

	if running {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Run importer in a background goroutine
	go RunImporter()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func main() {
	http.HandleFunc("/", handleHome)
	http.HandleFunc("/run", handleRun)

	fmt.Println("Web server listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
