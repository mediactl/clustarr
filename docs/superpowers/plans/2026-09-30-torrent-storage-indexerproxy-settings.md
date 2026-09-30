# Torrent scratch storage and IndexerProxy settings — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**Goal:** torrents download in a usenet-style scratch area and move to `publishDir` on the data volume when complete; IndexerProxy is managed from Settings.

**Architecture:** `TorrentSpec.Scratch`/`PublishDir` reuse `ScratchSpec`; the StatefulSet renders the placement (claim templates per replica); `pkg/download/torrent` publishes a complete transfer once (drop, `fsops.MoveAtomic`, re-add) before it reads Completed; IndexerProxy joins the Settings kinds.

**Tech Stack:** Go, controller-runtime, anacrolix/torrent v1.61, templ.

**Spec:** `docs/superpowers/specs/2026-09-30-torrent-storage-indexerproxy-settings-design.md`

## Global Constraints

- Gate: `env -u KUBEBUILDER_ASSETS -u CLUSTARR_PG_ASSETS go test ./...` and `make lint`; `make generate manifests` after the API change (CRDs applied before the helm upgrade).
- Pathspec commits; never `git stash`; the controlling session pushes.
- `scratch` unset must behave exactly as today (download and seed in `<publishDir>/<category>/<name>`, default `/data/torrents`).
- A failed publish is a local fault (`diskFull`/`writeError`), never a blocklist; the Download reads Completed only after the move.
- Secret keys for IndexerProxy credentials: `username`, `password` (`app/indexer/proxy/resolve.go`).

## Review Focus

1. A torrent that completes while paused, or whose selection excludes files: publish moves the whole directory, the re-add keeps the selection and pause.
2. Engine restart mid-move (scratch copy partly moved): re-attach must find a whole copy, never a half one.
3. Removal with data after publish removes the published directory, not the (gone) scratch one.
4. `replicas > 1` with `storageClassName`: one claim per replica, not one shared RWO claim.
5. The piece-completion store: the re-added torrent is complete without re-downloading (no peers in the test after the move).

### Task 1: API
`api/download/v1alpha1/downloadclient_types.go`: `TorrentSpec.Scratch *ScratchSpec`, `TorrentSpec.PublishDir string` (`^/`, MaxLength 4096), `TorrentSpec.PublishDirOrDefault()` (`/data/torrents`); DownloadClientSpec CEL: torrent `scratch.volumeName` requires `replicas == 1`. `make generate manifests`. Tests: accessor; `pkg/crdcheck` CEL cases.

### Task 2: workload
`app/grab/controller/downloadclient/workload.go`: `scratchVolume`/`needsScratchClaim`/`buildScratchPVC` read the client's ScratchSpec for either protocol (`scratchSpec(dc)`); the StatefulSet gets `volumeClaimTemplates` for `storageClassName`, a claim for `volumeName`, a volume for `existingClaim`/emptyDir; the torrent container mounts it at the scratch dir and passes `--scratch-dir`; `validateEngineDirs` covers torrent `scratch.path` and `publishDir`; the config hash covers both. Tests in `workload_test.go` per placement.

### Task 3: engine
`pkg/download/torrent`: `Config.ScratchDir` (empty = no scratch) and `Config.DataDir` as the publish root; `Add` places a new transfer in scratch unless its published dir exists; `itemFromTorrent` reports Downloading/`publishing` for a complete, unpublished transfer and starts `publish` once; `publish` drops, closes storage, `MoveAtomic`, re-adds, re-applies selection/pause/seed, sets `contentRoot`; failures set a local failure reason. `Remove` removes the current `contentRoot`. `app/grab/run.go`: torrent engine takes `ScratchDir` from `--scratch-dir` or `spec.torrent.scratch.path`, `DataDir` from `PublishDirOrDefault()`, state dir fixed at `/data/torrents/.state`. Tests with two in-process clients.

### Task 4: UI
`ui/forms/kinds.go`: torrent `publishDir`/`scratch` paths + labels; IndexerProxy kind (Groups, Labels, Secrets `secretRef` username/password, Hidden `selector`). `ui/actions/config.go` configKinds + grants; `ui/settings_forms.go` constructor; Settings section in `ui/settings.go`/`views/settings.templ`; regenerate ui role (`make manifests`), chart copy. Tests: form renders, create with Secret, delete, role guard.

### Task 5: docs
CLAUDE.md: torrent scratch in the storage gotcha; Settings kinds list (ten).
