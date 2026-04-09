package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pendingDownload tracks a queued slskd download that should be auto-imported
// once all files have transferred successfully.
type pendingDownload struct {
	MBID     string
	Artist   string
	Album    string
	Username string      // slskd peer username
	Dir      string      // remote directory path on the peer
	Files    []slskdFile // files that were queued for download
	Entry    *fetchEntry // fetch card to update with import progress
}

var (
	pendingMu        sync.Mutex
	pendingDownloads = make(map[string]*pendingDownload) // keyed by MBID
)

// registerDownload records a queued slskd download for monitoring and eventual
// auto-import. If entry is nil a new fetchEntry is created, keyed by mbid,
// so the frontend can discover it via /discover/fetch/list.
func registerDownload(mbid, artist, album string, folder *albumFolder, entry *fetchEntry) {
	pd := &pendingDownload{
		MBID:     mbid,
		Artist:   artist,
		Album:    album,
		Username: folder.Username,
		Dir:      folder.Dir,
		Files:    folder.Files,
		Entry:    entry,
	}

	if entry == nil {
		e := newFetchEntry(mbid, artist, album)
		e.appendLog(fmt.Sprintf("Queued %d files from %s — waiting for download",
			len(folder.Files), folder.Username))
		pd.Entry = e
	}

	pendingMu.Lock()
	pendingDownloads[mbid] = pd
	pendingMu.Unlock()

	log.Printf("[monitor] registered: %q by %s (mbid: %s, peer: %s, %d files)",
		album, artist, mbid, folder.Username, len(folder.Files))
}

// startMonitor launches a background goroutine that periodically checks whether
// pending downloads have completed and triggers import when they have.
func startMonitor() {
	go func() {
		for {
			time.Sleep(15 * time.Second)
			checkPendingDownloads()
		}
	}()
	log.Println("[monitor] started")
}

// checkPendingDownloads polls slskd transfer state for every pending download
// and kicks off importPendingRelease for any that are fully complete.
func checkPendingDownloads() {
	pendingMu.Lock()
	if len(pendingDownloads) == 0 {
		pendingMu.Unlock()
		return
	}
	snapshot := make(map[string]*pendingDownload, len(pendingDownloads))
	for k, v := range pendingDownloads {
		snapshot[k] = v
	}
	pendingMu.Unlock()

	log.Printf("[monitor] checking %d pending download(s)", len(snapshot))

	// Group by username to minimise API calls.
	byUser := make(map[string][]*pendingDownload)
	for _, pd := range snapshot {
		byUser[pd.Username] = append(byUser[pd.Username], pd)
	}

	for username, pds := range byUser {
		dirs, err := getSlskdTransfers(username)
		if err != nil {
			log.Printf("[monitor] failed to get transfers for %s: %v", username, err)
			continue
		}

		// Index transfer dirs by normalised path.
		transfersByDir := make(map[string][]slskdTransferFile, len(dirs))
		for _, d := range dirs {
			norm := strings.ReplaceAll(d.Directory, "\\", "/")
			transfersByDir[norm] = d.Files
		}

		for _, pd := range pds {
			normDir := strings.ReplaceAll(pd.Dir, "\\", "/")
			files, ok := transfersByDir[normDir]
			if !ok {
				log.Printf("[monitor] transfer dir not found yet for %q (peer: %s)", pd.Dir, username)
				continue
			}

			if !allFilesCompleted(files) {
				log.Printf("[monitor] %q by %s: download still in progress", pd.Album, pd.Artist)
				continue
			}

			localDir := localDirForDownload(pd, files)
			if localDir == "" {
				log.Printf("[monitor] cannot determine local dir for %q by %s", pd.Album, pd.Artist)
				pd.Entry.appendLog("Error: could not determine local download path from transfer info")
				continue
			}

			log.Printf("[monitor] download complete: %q by %s → %s", pd.Album, pd.Artist, localDir)

			// Remove from pending before starting import to avoid double-import.
			pendingMu.Lock()
			delete(pendingDownloads, pd.MBID)
			pendingMu.Unlock()

			go importPendingRelease(pd, localDir)
		}
	}
}

// allFilesCompleted reports whether every file in a transfer directory has
// reached a terminal Completed state. Returns false if files is empty.
func allFilesCompleted(files []slskdTransferFile) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !strings.Contains(f.State, "Completed") {
			return false
		}
	}
	return true
}

// localDirForDownload resolves the local filesystem path for a completed download.
//
// Strategy 1 — localFilename from transfer metadata: slskd sets this field to
// the absolute path of the downloaded file. Works when paths are consistent
// across containers (same volume mount point).
//
// Strategy 2 — SLSKD_DOWNLOAD_DIR reconstruction: slskd stores files under
// {downloadDir}/{username}/{sanitized_remote_dir}/. Used when localFilename is
// empty or when SLSKD_DOWNLOAD_DIR is explicitly set to override.
func localDirForDownload(pd *pendingDownload, files []slskdTransferFile) string {
	// Strategy 1: use localFilename from transfer response.
	for _, f := range files {
		if f.LocalFilename != "" {
			dir := filepath.Dir(f.LocalFilename)
			log.Printf("[monitor] local dir from localFilename: %s", dir)
			return dir
		}
	}

	// Strategy 2: reconstruct from SLSKD_DOWNLOAD_DIR.
	// slskd places downloaded files directly into {downloadDir}/{album_folder_name}/,
	// where the folder name is the last path component of the remote directory.
	dlDir := os.Getenv("SLSKD_DOWNLOAD_DIR")
	if dlDir == "" {
		log.Printf("[monitor] localFilename empty and SLSKD_DOWNLOAD_DIR not set — cannot determine local dir for %q", pd.Album)
		return ""
	}

	dir := filepath.Join(dlDir, filepath.Base(filepath.FromSlash(pd.Dir)))
	log.Printf("[monitor] local dir reconstructed from SLSKD_DOWNLOAD_DIR: %s", dir)
	return dir
}

// importPendingRelease runs the full import pipeline on a completed download.
// It mirrors RunImporter's per-album logic but uses the MBID for beets tagging.
func importPendingRelease(pd *pendingDownload, localDir string) {
	entry := pd.Entry
	logf := func(msg string) {
		entry.appendLog("[import] " + msg)
		log.Printf("[monitor/import %s] %s", pd.MBID, msg)
	}

	logf(fmt.Sprintf("Starting import from %s", localDir))

	libraryDir := os.Getenv("LIBRARY_DIR")
	if libraryDir == "" {
		entry.finish(fmt.Errorf("LIBRARY_DIR is not set"))
		return
	}

	tracks, err := getAudioFiles(localDir)
	if err != nil {
		entry.finish(fmt.Errorf("scanning audio files: %w", err))
		return
	}
	if len(tracks) == 0 {
		entry.finish(fmt.Errorf("no audio files found in %s", localDir))
		return
	}
	logf(fmt.Sprintf("Found %d tracks", len(tracks)))

	if err := cleanAlbumTags(localDir); err != nil {
		logf(fmt.Sprintf("Clean tags warning: %v", err))
	}

	md, src, err := getAlbumMetadata(localDir, tracks[0], pd.MBID)
	if err != nil {
		entry.finish(fmt.Errorf("metadata failed: %w", err))
		return
	}
	logf(fmt.Sprintf("Tagged via %s: %s — %s", src, md.Artist, md.Album))

	if _, err := DownloadAlbumLyrics(localDir); err != nil {
		logf(fmt.Sprintf("Lyrics warning: %v", err))
	}

	if err := applyReplayGain(localDir); err != nil {
		entry.finish(fmt.Errorf("ReplayGain failed: %w", err))
		return
	}
	logf("ReplayGain applied")

	if _, err := FindCoverImage(localDir); err != nil {
		if err := DownloadCoverArt(localDir, md); err != nil {
			logf(fmt.Sprintf("Cover art download warning: %v", err))
		}
	}

	if err := EmbedAlbumArtIntoFolder(localDir); err != nil {
		entry.finish(fmt.Errorf("cover embed failed: %w", err))
		return
	}
	logf("Cover art embedded")

	var moveErr error
	for _, track := range tracks {
		if err := moveToLibrary(libraryDir, md, track); err != nil {
			logf(fmt.Sprintf("Move warning: %v", err))
			moveErr = err
		}
	}

	lyrics, _ := getLyricFiles(localDir)
	for _, file := range lyrics {
		if err := moveToLibrary(libraryDir, md, file); err != nil {
			logf(fmt.Sprintf("Move lyrics warning: %v", err))
		}
	}

	if coverImg, err := FindCoverImage(localDir); err == nil {
		if err := moveToLibrary(libraryDir, md, coverImg); err != nil {
			logf(fmt.Sprintf("Move cover warning: %v", err))
		}
	}

	os.Remove(localDir)

	if moveErr != nil {
		entry.finish(fmt.Errorf("import completed with move errors: %w", moveErr))
		return
	}

	logf("Import complete")
	entry.finish(nil)
}
