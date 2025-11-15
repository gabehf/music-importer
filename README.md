# music-importer

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

```

## Quirks

- only works for .flac, .mp3, and .m4a
- not configurable at all, other than dirs
