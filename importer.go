package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// StepStatus records the outcome of a single pipeline step for an album.
type StepStatus struct {
	Skipped bool
	Err     error
}

func (s StepStatus) Failed() bool { return s.Err != nil }

// MetadataSource identifies which backend resolved the album metadata.
type MetadataSource string

const (
	MetadataSourceBeets       MetadataSource = "beets"
	MetadataSourceMusicBrainz MetadataSource = "musicbrainz"
	MetadataSourceFileTags    MetadataSource = "file_tags"
	MetadataSourceUnknown     MetadataSource = ""
)

// LyricsStats summarises per-track lyric discovery for an album.
type LyricsStats struct {
	Total      int // total audio tracks examined
	Synced     int // tracks with synced (timestamped) LRC lyrics downloaded
	Plain      int // tracks with plain (un-timestamped) lyrics downloaded
	AlreadyHad int // tracks that already had an .lrc file, skipped
	NotFound   int // tracks for which no lyrics could be found
}

func (l LyricsStats) Downloaded() int { return l.Synced + l.Plain }

// CoverArtStats records what happened with cover art for an album.
type CoverArtStats struct {
	Found    bool   // a cover image file was found in the folder
	Embedded bool   // cover was successfully embedded into tracks
	Source   string // filename of the cover image, e.g. "cover.jpg"
}

// AlbumResult holds the outcome of every pipeline step for one imported album.
type AlbumResult struct {
	Name     string
	Path     string
	Metadata *MusicMetadata

	MetadataSource MetadataSource
	LyricsStats    LyricsStats
	CoverArtStats  CoverArtStats
	TrackCount     int

	CleanTags   StepStatus
	TagMetadata StepStatus
	Lyrics      StepStatus
	ReplayGain  StepStatus
	CoverArt    StepStatus
	Move        StepStatus

	// FatalStep is the name of the step that caused the album to be skipped
	// entirely, or empty if the album completed the full pipeline.
	FatalStep string
}

func (a *AlbumResult) skippedAt(step string) {
	a.FatalStep = step
}

func (a *AlbumResult) Succeeded() bool { return a.FatalStep == "" }
func (a *AlbumResult) HasWarnings() bool {
	if a.CleanTags.Failed() ||
		a.TagMetadata.Failed() ||
		a.Lyrics.Failed() ||
		a.ReplayGain.Failed() ||
		a.CoverArt.Failed() ||
		a.Move.Failed() {
		return true
	} else {
		return false
	}
}

// ImportSession holds the results of a single importer run.
type ImportSession struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Albums     []*AlbumResult
}

func (s *ImportSession) Failed() []*AlbumResult {
	var out []*AlbumResult
	for _, a := range s.Albums {
		if !a.Succeeded() {
			out = append(out, a)
		}
	}
	return out
}

func (s *ImportSession) WithWarnings() []*AlbumResult {
	var out []*AlbumResult
	for _, a := range s.Albums {
		if a.Succeeded() && a.HasWarnings() {
			out = append(out, a)
		}
	}
	return out
}

// lastSession is populated at the end of each RunImporter call.
var lastSession *ImportSession

func RunImporter() {
	importDir := os.Getenv("IMPORT_DIR")
	libraryDir := os.Getenv("LIBRARY_DIR")

	if importerRunning {
		return
	}

	importerMu.Lock()
	importerRunning = true
	importerMu.Unlock()
	defer func() {
		importerMu.Lock()
		importerRunning = false
		importerMu.Unlock()
	}()

	if importDir == "" || libraryDir == "" {
		log.Println("IMPORT_DIR and LIBRARY_DIR must be set")
		return
	}

	session := &ImportSession{StartedAt: time.Now()}
	defer func() {
		session.FinishedAt = time.Now()
		lastSession = session
	}()

	fmt.Println("=== Starting Import ===")

	if err := cluster(importDir); err != nil {
		log.Println("Failed to cluster top-level audio files:", err)
		return
	}

	entries, err := os.ReadDir(importDir)
	if err != nil {
		log.Println("Failed to read import dir:", err)
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		albumPath := filepath.Join(importDir, e.Name())

		tracks, err := getAudioFiles(albumPath)
		if err != nil {
			fmt.Println("Skipping (error scanning):", albumPath, err)
			continue
		}
		if len(tracks) == 0 {
			continue
		}

		fmt.Println("\n===== Album:", e.Name(), "=====")

		result := &AlbumResult{Name: e.Name(), Path: albumPath}
		session.Albums = append(session.Albums, result)
		result.TrackCount = len(tracks)

		fmt.Println("→ Cleaning album tags:")
		result.CleanTags.Err = cleanAlbumTags(albumPath)
		if result.CleanTags.Failed() {
			fmt.Println("Cleaning album tags failed:", result.CleanTags.Err)
		}

		fmt.Println("→ Tagging album metadata:")
		md, src, err := getAlbumMetadata(albumPath, tracks[0], "")
		result.TagMetadata.Err = err
		result.MetadataSource = src
		if err != nil {
			fmt.Println("Metadata failed, skipping album:", err)
			result.skippedAt("TagMetadata")
			continue
		}
		result.Metadata = md

		fmt.Println("→ Fetching synced lyrics from LRCLIB:")
		lyricsStats, err := DownloadAlbumLyrics(albumPath)
		result.Lyrics.Err = err
		result.LyricsStats = lyricsStats
		if result.Lyrics.Failed() {
			fmt.Println("Failed to download synced lyrics.")
		}

		fmt.Println("→ Applying ReplayGain to album:", albumPath)
		result.ReplayGain.Err = applyReplayGain(albumPath)
		if result.ReplayGain.Failed() {
			fmt.Println("ReplayGain failed, skipping album:", result.ReplayGain.Err)
			result.skippedAt("ReplayGain")
			continue
		}

		fmt.Println("→ Downloading cover art for album:", albumPath)
		if _, err := FindCoverImage(albumPath); err != nil {
			if err := DownloadCoverArt(albumPath, md, ""); err != nil {
				fmt.Println("Cover art download failed:", err)
			}
		}

		fmt.Println("→ Embedding cover art for album:", albumPath)
		result.CoverArt.Err = EmbedAlbumArtIntoFolder(albumPath)
		if coverImg, err := FindCoverImage(albumPath); err == nil {
			result.CoverArtStats.Found = true
			result.CoverArtStats.Source = filepath.Base(coverImg)
			if result.CoverArt.Err == nil {
				result.CoverArtStats.Embedded = true
			}
		}
		if result.CoverArt.Failed() {
			fmt.Println("Cover embed failed, skipping album:", result.CoverArt.Err)
			result.skippedAt("CoverArt")
			continue
		}

		targetDir := albumTargetDir(libraryDir, md)
		if _, err := os.Stat(targetDir); err == nil {
			fmt.Println("→ Album already exists in library, skipping move:", targetDir)
			result.Move.Skipped = true
		} else {
			fmt.Println("→ Moving tracks into library for album:", albumPath)
			for _, track := range tracks {
				if err := moveToLibrary(libraryDir, md, track); err != nil {
					fmt.Println("Failed to move track:", track, err)
					result.Move.Err = err // retains last error; all attempts are still made
				}
			}

			lyrics, _ := getLyricFiles(albumPath)

			fmt.Println("→ Moving lyrics into library for album:", albumPath)
			for _, file := range lyrics {
				if err := moveToLibrary(libraryDir, md, file); err != nil {
					fmt.Println("Failed to move lyrics:", file, err)
					result.Move.Err = err
				}
			}

			fmt.Println("→ Moving album cover into library for album:", albumPath)
			if coverImg, err := FindCoverImage(albumPath); err == nil {
				if err := moveToLibrary(libraryDir, md, coverImg); err != nil {
					fmt.Println("Failed to cover image:", coverImg, err)
					result.Move.Err = err
				}
			}

			os.Remove(albumPath)
		}
	}

	fmt.Println("\n=== Import Complete ===")
}
