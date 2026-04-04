package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// moveToLibrary moves a file to {libDir}/{artist}/[{date}] {album} [{quality}]/filename.
func moveToLibrary(libDir string, md *MusicMetadata, srcPath string) error {
	date := md.Date
	if date == "" {
		date = md.Year
	}
	albumDir := fmt.Sprintf("[%s] %s", date, md.Album)
	if md.Quality != "" {
		albumDir += fmt.Sprintf(" [%s]", md.Quality)
	}
	targetDir := filepath.Join(libDir, sanitize(md.Artist), sanitize(albumDir))
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	dst := filepath.Join(targetDir, filepath.Base(srcPath))
	fmt.Println("→ Moving:", srcPath, "→", dst)
	if strings.ToLower(os.Getenv("COPYMODE")) == "true" {
		return copy(srcPath, dst)
	} else {
		return os.Rename(srcPath, dst)
	}
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

// CopyFile copies a file from src to dst. If src and dst files exist, and are
// the same, then return success. Otherise, attempt to create a hard link
// between the two files. If that fail, copy the file contents from src to dst.
func copy(src, dst string) (err error) {
	sfi, err := os.Stat(src)
	if err != nil {
		return
	}
	if !sfi.Mode().IsRegular() {
		// cannot copy non-regular files (e.g., directories,
		// symlinks, devices, etc.)
		return fmt.Errorf("CopyFile: non-regular source file %s (%q)", sfi.Name(), sfi.Mode().String())
	}
	dfi, err := os.Stat(dst)
	if err != nil {
		if !os.IsNotExist(err) {
			return
		}
	} else {
		if !(dfi.Mode().IsRegular()) {
			return fmt.Errorf("CopyFile: non-regular destination file %s (%q)", dfi.Name(), dfi.Mode().String())
		}
		if os.SameFile(sfi, dfi) {
			return
		}
	}
	if err = os.Link(src, dst); err == nil {
		return
	}
	err = copyFileContents(src, dst)
	return
}

// copyFileContents copies the contents of the file named src to the file named
// by dst. The file will be created if it does not already exist. If the
// destination file exists, all it's contents will be replaced by the contents
// of the source file.
func copyFileContents(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return
	}
	err = out.Sync()
	return
}
