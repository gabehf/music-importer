# music-importer

Goes through a folder with a bunch of loose .flac/.mp3 files, or with album folders containing music files, then
fetches metadata with beets/musicbrainz, downloads lyrics via LRClib, embeds discovered cover art, and moves them
into the library with the format {Artist}/[{Year}] {Title} [{Format-Quality}]

this thing is like 95% AI code. use at your own risk

i didn't feel like spending the time to do it all right and i figured its simple enough that chatgpt couldn't possible screw it up *that* bad

## Usage

docker compose

```yaml
services:
  music-importer:
    image: gabehf/music-importer:latest
    container_name: music-importer
    ports:
      - "8080:8080"
    volumes:
      - /my/import/dir:/import
      - /my/library/dir:/library
    environment:
      IMPORT_DIR: /import
      LIBRARY_DIR: /library
      COPYMODE: true # copies files instead of moving. NOT NON-DESTRUCTIVE!!

```

## Quirks

- only works for .flac and .mp3
- not configurable at all, other than dirs
