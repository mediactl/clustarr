# Plex analyze bypass Implementation Plan

> **For agentic workers:** executed natively (superpowers:executing-plans) at the owner's instruction. The owner said to skip long envtest and e2e runs: behaviour is pinned with unit tests on fake clients, and with PostgreSQL tests against a throwaway `postgres:16` container.

**Goal:** Plex shows streams and plays every file clustarr has probed, and shows skip markers wherever TheIntroDB has segments, without Plex's scanner.

**Architecture:**
- **clustarr** fetches TheIntroDB segments per probed movie or episode file into `MediaFile.status.markers`, through a provider, a work subject and a gateway worker.
- **cluster-plex's** Lease holder watches MediaFiles and seeds Plex's PostgreSQL:
  - `media_items`, `media_parts` and `media_streams` for unanalysed files;
  - `taggings` marker rows, reconciled per kind.

**Tech Stack:** Go, controller-runtime, NATS JetStream, pgx v5, templ.

**Spec:** `docs/superpowers/specs/2026-09-30-plex-analyze-bypass-design.md` (clustarr).

## Global Constraints

- GPL-3.0 header on every new clustarr Go file.
- No floats in `api/`; every status list is capped (`segments` MaxItems=20).
- The marker worker writes `status.markers` only, under `catalogarr-markers`, through `pkg/k8s.PatchStatus`, re-reading just before it applies.
- The client:
  - takes an injected limiter;
  - reads bodies through a cap;
  - maps 404 to `metadata.ErrNotFound`;
  - never logs its key, and never puts it in an error.
- cluster-plex imports no clustarr code: it reads unstructured objects.
- The seeder never overwrites a file Plex analysed. It owns a marker kind only where TheIntroDB has segments of that kind, and removes only its own rows otherwise.
- Every seeded row is identifiable: `cp:source` in media `extra_data`, `pv:source` in marker `extra_data`.

## Review Focus

1. **A title TheIntroDB lacks (404):** `NotFound` is recorded, Plex's own markers are untouched, and it is re-checked after 7 days rather than on every reconcile.
2. **A file replaced in place (new probe hash):** markers are re-fetched, and streams are re-seeded only if Plex has none.
3. **An episode of a DVD- or absolute-ordered series:** TheIntroDB numbers seasons in aired order, so no markers are fetched. The file is recorded `NotFound` with a message, never given the wrong segments.
4. **A MediaFile path not yet in Plex:** skipped, and retried on the resync.
5. **Plex's detector rewrites credits after we seeded them:** the next resync re-asserts ours.

---

## clustarr

### Task C1: TheIntroDB client

- Files: `pkg/metadata/clients/theintrodb/theintrodb.go`, `theintrodb_test.go`, fixtures `test/data/metadata/theintrodb/*.json` (recorded live).
- `New(Config{HTTPClient, BaseURL, Limiter, UserAgent, APIKey})`, `Name()`, `Capabilities()`, `Ping(ctx)`.
- `Media(ctx, q Query) (metadata.Segments, error)`, where `Query{TMDB, TVDB, IMDB string; Season, Episode int32; DurationMs int64}`.
- `metadata.Segments{Intro, Recap, Credits, Preview []Segment{StartMs, EndMs int64}}`, and `metadata.MarkersProvider` in `pkg/metadata`.
- Tests:
  - Breaking Bad S01E01: intro, plus credits with a null end resolved to the duration;
  - The Matrix: null start resolved to 0;
  - Friends S01E01: `{null, 0}` dropped;
  - a 404 is `ErrNotFound`;
  - the key is sent as a Bearer token and appears in no error.

### Task C2: API

- `MediaFileStatus.Markers *FileMarkers` (§3.3).
- `MetadataProviderTheIntroDB = "theintrodb"` in the enum.
- `k8s.ManagerCatalogarrMarkers`.
- `make generate manifests`.
- Tests: the CRD cap guard (`pkg/crdcheck`), which runs without envtest.

### Task C3: Provider wiring

- `Registry.Markers`; theintrodb in both registry builders (gateway and controller) and the prober; `KeylessTypes`.
- Tests: the existing registry and supplementary tests extended with the new type.

### Task C4: Deciding and publishing

- Pure `markers.Due(mf, now) (bool, time.Duration)` in `app/catalog/markers`.
- `events.WorkMarkersSubject`, `FilterCatalogMarkers`, `ConsumerCatalogMarkers`.
- The MediaFile reconciler gets an optional `Bus` and publishes when due, with msg id `markers-<probeHash>-<fetchedAt unix>`, and requeues at the next due time.
- Tests: `Due` table-driven; publish through membus with a fake client.

### Task C5: Marker worker

- `app/catalog/markers.Handler` in the metadata gateway's `Setup`:
  1. Read the MediaFile and its item.
  2. A Movie queries by `spec.tmdbID`. An Episode queries by its Series' `spec.tvdbID` plus season and episode, and only when the series' effective order is official.
  3. Call the providers.
  4. Re-`Get`, then apply the complete block.
- Tests use a fake client plus a stub provider:
  - found, not found and error;
  - an order that isn't official;
  - the apply carries only `markers`.

### Task C6: UI

- The detail page's files table gains a "Markers" column, e.g. `intro 0:00–0:23, credits 56:01–end`.
- Test: the detail render.

### Task C7: Docs

- CLAUDE.md and a research note on TheIntroDB.

## cluster-plex

### Task P1: Seed input

- `clustarrwatch.SeedInputOf(u)`: path, probe, probe hash and markers from an unstructured MediaFile.
- `Trim` keeps `status.mediaInfo`, `status.probeHash` and `status.markers`.
- Tests: the unit tests for fields and trim.

### Task P2: Seeder

- Package `pkg/plexseed`:
  - `MediaRows(in) (Media, []Stream)`;
  - `MarkerRows(markers, durationMs) []Marker`;
  - codec and language tables;
  - `Seeder.Seed(ctx, plexPath, in)`: one transaction that finds the part, seeds media if it has no streams, and reconciles markers.
- Tests:
  - pure unit tests;
  - PostgreSQL tests against `hack/plex-postgresql/schema`, gated on `CLUSTERPLEX_TEST_POSTGRES_DSN` and run against a throwaway container.

### Task P3: Wiring

- The watcher adds the seed handler to the MediaFile informer, runs a resync every 6 hours, and restricts seeding to the provisioned clustarr sections.
- The manager opens a pgx pool for the seeder.
- Metrics: `clusterplex_seed_{media,markers,unmatched,errors}_total`.
- Tests: watcher unit tests with a fake seeder.

### Task P4: ADR 0006, CLAUDE.md, deploy and live proof

- Seed Andor S01E02 first, then the whole library.
- Report stream and marker coverage.
