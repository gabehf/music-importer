# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build -o importer .

# Build with version baked in
go build -ldflags="-X main.version=v1.0.0" -o importer .

# Run locally (requires IMPORT_DIR and LIBRARY_DIR env vars)
IMPORT_DIR=/path/to/import LIBRARY_DIR=/path/to/library ./importer

# Build Docker image
docker build -t music-importer .

# Build Docker image with version
docker build --build-arg VERSION=v1.0.0 -t music-importer .
```

There are no tests in this codebase.

## Architecture

This is a single-package Go web app (`package main`) that runs as a web server on port 8080. Users trigger an import via the web UI, which runs the import pipeline in a background goroutine.

**Pipeline flow** (`importer.go: RunImporter`):
1. **Cluster** — loose audio files at the top of `IMPORT_DIR` are grouped into subdirectories by album tag (`files.go: cluster`)
2. For each album directory:
   - **Clean tags** — removes COMMENT/DESCRIPTION tags via `metaflac` (`audio.go`)
   - **Tag metadata** — tries `beets` first; falls back to reading existing file tags, then MusicBrainz API (`metadata.go: getAlbumMetadata`)
   - **Lyrics** — fetches synced LRC lyrics from LRClib API; falls back to plain lyrics formatted as LRC (`lrc.go`)
   - **ReplayGain** — runs `rsgain easy` on the directory (`audio.go`)
   - **Cover art** — looks for existing image files, downloads from Cover Art Archive via MusicBrainz if missing, then embeds into tracks (`media.go`)
   - **Move** — moves tracks, .lrc files, and cover image into `LIBRARY_DIR/{Artist}/[{Date}] {Album} [{Quality}]/` (`files.go: moveToLibrary`)

**Key types** (`importer.go`):
- `AlbumResult` — tracks per-step success/failure/skip for one album
- `ImportSession` — holds all `AlbumResult`s for one run; stored in `lastSession` global
- `MusicMetadata` — artist/album/title/date/quality used throughout the pipeline

**Web layer** (`main.go`):
- `GET /` — renders `index.html.tmpl` with the last session's results
- `POST /run` — starts `RunImporter()` in a goroutine; prevents concurrent runs via `importerMu` mutex

**External tool dependencies** (must be present in PATH at runtime):
- `ffprobe` — reads audio tags and stream info
- `beet` — metadata tagging via MusicBrainz (primary metadata source)
- `rsgain` — ReplayGain calculation
- `metaflac` — FLAC tag manipulation and cover embedding
- `curl` — MusicBrainz API fallback queries

**Environment variables**:
- `IMPORT_DIR` — source directory scanned for albums
- `LIBRARY_DIR` — destination library root
- `COPYMODE=true` — copies files instead of moving (still destructive on the destination)
- `SLSKD_URL` — base URL of the slskd instance (e.g. `http://localhost:5030`)
- `SLSKD_API_KEY` — slskd API key (sent as `X-API-Key` header)

**Releases**: Docker image `gabehf/music-importer` is built and pushed to Docker Hub via GitHub Actions on `v*` tags.
