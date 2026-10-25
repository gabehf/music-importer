package main

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultSettleTime is how long a folder must remain unchanged before the
// callback fires.
const DefaultSettleTime = 10 * time.Minute

func isAudio(path string) bool {
	return slices.Contains(ACCEPTED_AUDIO_EXTENSIONS, strings.ToLower(filepath.Ext(path)))
}

func hasAudio(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && isAudio(e.Name()) {
			return true
		}
	}
	return false
}

// Watch recursively watches root for new folders containing .flac or .mp3
// files. Once a folder has seen no activity for `settle`, onSettled is called
// (in its own goroutine) with the folder's path.
//
// Folders that already exist when Watch starts are not reported unless they
// change afterwards. Audio files placed directly in root (not in a subfolder)
// are ignored. Watch blocks until ctx is cancelled or the watcher fails.
func Watch(ctx context.Context, root string, settle time.Duration, onSettled func(dir string)) error {
	if settle <= 0 {
		settle = DefaultSettleTime
	}
	root = filepath.Clean(root)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	// pending maps a folder to the time of its last observed activity.
	pending := make(map[string]time.Time)

	// addTree adds watches for dir and all subdirs. If track is true, any
	// folder found containing audio is marked as pending (used for folders
	// that appear fully populated, e.g. moved in from elsewhere).
	addTree := func(dir string, track bool) error {
		return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// folder may have vanished mid-walk; skip it
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if err := w.Add(p); err != nil {
				log.Printf("watcher: add %s: %v", p, err)
				return nil
			}
			if track && p != root && hasAudio(p) {
				pending[p] = time.Now()
			}
			return nil
		})
	}

	if err := addTree(root, false); err != nil {
		return err
	}

	// Check often enough to be accurate, but not wastefully.
	interval := settle / 10
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}

			// New directory: watch it (and anything already inside it).
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					if err := addTree(ev.Name, true); err != nil {
						log.Printf("watcher: walk %s: %v", ev.Name, err)
					}
					continue
				}
			}

			// Directory removed or renamed away: stop tracking it.
			if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				delete(pending, ev.Name)
			}

			dir := filepath.Dir(ev.Name)
			if dir == root {
				continue
			}
			// An audio file event starts tracking a folder; any event in an
			// already-tracked folder (cover art, .cue, etc.) resets its timer.
			if _, tracked := pending[dir]; tracked || isAudio(ev.Name) {
				pending[dir] = time.Now()
			}

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("watcher: error: %v", err)

		case now := <-ticker.C:
			for dir, last := range pending {
				if now.Sub(last) < settle {
					continue
				}
				delete(pending, dir)
				if hasAudio(dir) { // make sure it wasn't deleted/emptied
					go onSettled(dir)
				}
			}
		}
	}
}
