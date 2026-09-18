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

## Importer service (name needed)

This service will reconcile existing media stored in the root folders and upsert custom resources for each found.

This service will also reconcile import lists from multiple supported sources and reconcile custom resources from the import list items.

The import lists can/should be run on a schedule.

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

## Observability

Include slog logging (dependency injection pattern with context)

Include open telemetry spans

Include prometheus metrics for downloaded files, download speeds, etc.

Discover useful metrics and include them in our docs and code.

Include health check, readiness, etc endpoints for the K8S workloads.

## UI

Build a UI for the application using:

- github.com/a-h/templ (HTML Templating)
- github.com/axadrn/shadcn-templ (UI Components)

The UI should include:

- Media pipeline page (Similar to Radarr/Sonarr activity)
  - Page should contain an element per media item reconciling
  - Each item should show which stage of the process it's in
    - Searching for Metadata -> Metadata Found -> Metadata synced
    - Searching for releases -> Release selected
    - Downloading release -> Download progress (est time) -> Release downloaded
    - Searching for subtitles (if non english) -> Subtitles found
    - Downloading subtitles -> Download progress -> Subtitle downloaded
    - Transcoding file -> Transcode progress (est time) -> Transcoding complete

- Library page
  - Should show all collected and monitored media
    - Status indicator for collection status
  - Each item should include the cover art
    - Clicking an item presents a modal with the item info
  - Toolbar with bulk operation and filtering inputs

- Pages for Downloaders
  - Presents items in the queue
  - Shows download speed per item (downloading)
  - Shows estimated time to completion

- Import lists page

- Settings page
