package media

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	id3v2 "github.com/bogem/id3v2" // optional alternative
)

var coverNames = []string{
	"cover.jpg", "cover.jpeg", "cover.png",
	"folder.jpg", "folder.jpeg", "folder.png",
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
		for _, name := range coverNames {
			if l == name {
				return filepath.Join(dir, e.Name()), nil
			}
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
