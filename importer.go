package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

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

		fmt.Println("→ Cleaning album tags:")
		if err = cleanAlbumTags(albumPath); err != nil {
			fmt.Println("Cleaning album tags failed:", err)
		}

		fmt.Println("→ Tagging album metadata:")
		md, err := getAlbumMetadata(albumPath, tracks[0])
		if err != nil {
			fmt.Println("Metadata failed, skipping album:", err)
			continue
		}

		fmt.Println("→ Fetching synced lyrics from LRCLIB:")
		if err := DownloadAlbumLyrics(albumPath); err != nil {
			fmt.Println("Failed to download synced lyrics.")
		}

		fmt.Println("→ Applying ReplayGain to album:", albumPath)
		if err := applyReplayGain(albumPath); err != nil {
			fmt.Println("ReplayGain failed, skipping album:", err)
			continue
		}

		fmt.Println("→ Embedding cover art for album:", albumPath)
		if err := EmbedAlbumArtIntoFolder(albumPath); err != nil {
			fmt.Println("Cover embed failed, skipping album:", err)
			continue
		}

		fmt.Println("→ Moving tracks into library for album:", albumPath)
		for _, track := range tracks {
			if err := moveToLibrary(libraryDir, md, track); err != nil {
				fmt.Println("Failed to move track:", track, err)
			}
		}

		lyrics, _ := getLyricFiles(albumPath)

		fmt.Println("→ Moving lyrics into library for album:", albumPath)
		for _, file := range lyrics {
			if err := moveToLibrary(libraryDir, md, file); err != nil {
				fmt.Println("Failed to move lyrics:", file, err)
			}
		}

		fmt.Println("→ Moving album cover into library for album:", albumPath)
		if coverImg, err := FindCoverImage(albumPath); err == nil {
			if err := moveToLibrary(libraryDir, md, coverImg); err != nil {
				fmt.Println("Failed to cover image:", coverImg, err)
			}
		}

		os.Remove(albumPath)
	}

	fmt.Println("\n=== Import Complete ===")
}
