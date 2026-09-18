# Clustarr

Create a golang based project for Kubernetes (controllers + CRDs) that contains the following packages.

The idea of this project is to allow for a highly distributed, event driven media stack that manages a media collection end to end.

This will include:

- Indexers
- Downloaders
- Import lists (desired media)
- Metadata (media metadata)

## Download service (Downloadarr?)

Collection of download clients.

- Usenet downloader
- Torrent download (github.com/anacrolix/torrent)

We will need a distributed task queue of some sort - this should be designed.

- Compare options like Redis, Kafka, etc

## Inventory service (name needed)

stores collections of various media types

- movies
- tv
- music
- books (comics, manga, etc)
- audiobooks

Tracks releases, quality, monitored, minimum availability, root folder, metadata

## Indexer service (name needed)

This service will aggregate various remote indexers into a universal search engine for findign media releases.

It will behave like a hybrid of Prowlarr and Elastic search.

## Transcoding service (name needed)

This service will transcode downloaded media into the desired end state. Ideally, this will be optimized HEVC (ACC audio) 10bit optimized (compressed) format.

This service should schedule workers to distribute the transcoding task to quickly scale and complete all required transcode jobs.

## Subtitle service (name needed)

This service will perform the basic functionality of bazarr, but highly distributed.

### Layout

Each service should be under its own directory (package) with shared code in pkg.

Something like this (names not decided - brainstorm this)

- api
- cmd
- downloadarr
- indexarr
- inventorri
- transcodarr
- pkg

### Metadata

Borrow from the radarr and sonarr projects for populating media metadata. TVDB, OpenSubtitles, etc.

### Quality profiles

Borrow from radarr and sonarr.

We should support *only* an optinionated subset like trash guides.

### Indexers

Borrow from prowlarr.
