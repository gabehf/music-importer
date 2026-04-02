package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// moveToLibrary moves a file to {libDir}/{artist}/[{year}] {album}/filename.
func moveToLibrary(libDir string, md *MusicMetadata, srcPath string) error {
	targetDir := filepath.Join(libDir, sanitize(md.Artist), sanitize(fmt.Sprintf("[%s] %s", md.Year, md.Album)))
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	dst := filepath.Join(targetDir, filepath.Base(srcPath))
	fmt.Println("→ Moving:", srcPath, "→", dst)
	return os.Rename(srcPath, dst)
}

// cluster moves all top-level audio files in dir into subdirectories named
// after their embedded album tag.
func cluster(dir string) error {
	files, err := getAudioFiles(dir)
	if err != nil {
		return err
	}

	for _, f := range files {
		tags, err := readTags(f)
		if err != nil {
			return err
		}
		albumDir := path.Join(dir, sanitize(tags.Album))
		if err = os.MkdirAll(albumDir, 0755); err != nil {
			return err
		}
		if err = os.Rename(f, path.Join(albumDir, path.Base(f))); err != nil {
			return err
		}
	}

	return nil
}

// getAudioFiles returns all .flac and .mp3 files directly inside dir.
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

// getLyricFiles returns all .lrc files directly inside dir.
func getLyricFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var lyrics []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.ToLower(filepath.Ext(e.Name())) == ".lrc" {
			lyrics = append(lyrics, filepath.Join(dir, e.Name()))
		}
	}

	return lyrics, nil
}

// sanitize removes or replaces characters that are unsafe in file system paths.
func sanitize(s string) string {
	r := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "-",
		"?", "",
		"*", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "",
	)
	return r.Replace(s)
}
