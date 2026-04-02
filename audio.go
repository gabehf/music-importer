package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// applyReplayGain runs rsgain in "easy" mode on a directory.
func applyReplayGain(path string) error {
	fmt.Println("→ Applying ReplayGain:", path)
	return runCmd("rsgain", "easy", path)
}

// cleanAlbumTags strips COMMENT and DESCRIPTION tags from all files in dir.
func cleanAlbumTags(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := rmDescAndCommentTags(filepath.Join(dir, e.Name())); err != nil {
			fmt.Println("Failed to clean comment and description tags:", err)
		}
	}
	return nil
}

// rmDescAndCommentTags removes COMMENT and DESCRIPTION tags from a single file.
// Currently only handles FLAC; other formats are silently skipped.
func rmDescAndCommentTags(trackpath string) error {
	if strings.HasSuffix(strings.ToLower(trackpath), ".flac") {
		return runCmd("metaflac", "--remove-tag=COMMENT", "--remove-tag=DESCRIPTION", trackpath)
	}
	return nil
}
