# Plex analyze bypass: media streams from clustarr's probe, skip markers from TheIntroDB

Date: 2026-09-30. Status: approved design. Spans two repos:

- **clustarr** fetches skip segments from TheIntroDB and stores them on the file (`MediaFile.status.markers`).
- **cluster-plex** writes clustarr's probe and those segments into Plex's PostgreSQL library, so Plex has streams and markers without its scanner.

One plan per repo. The cluster-plex plan depends on the clustarr plan's API change (§3.3) being deployed.

## 1. Why

- **Playback is broken:** on kind-cluster-plex, Plex's media analysis has stalled.
  - Only 64 of 834 movies and 453 of 11,152 episodes have `media_streams`.
  - Every other item shows Video/Audio "None" and fails to play with `s1001`.
  - The cause is cluster-plex's shared `.LocalAdminToken`. Only the pod whose Plex started last accepts the scanner's token, so every other scanner run gets 401 and exits 0 having written nothing.
- **Skip markers are missing:** Plex detected credits on 207 items and intros on none.
- **clustarr already has what Plex needs:**
  - It has ffprobed 12,449 of 13,308 files (`MediaFile.status.mediaInfo`).
  - TheIntroDB publishes community intro, recap, credits and preview segments per title and release duration.

The owner wants every file Plex knows playable, and skip buttons where TheIntroDB has segments, without depending on Plex scanning the library. Fixing the token remains worthwhile for Plex's own later analysis. It is **not** part of this design.

## 2. Proven constraints

These were established live on kind-cluster-plex (PMS 1.43.4) on 2026-09-30.

1. **Plex's API cannot write media analysis.** The published OpenAPI (207 paths) has no operation that sets `media_items` or `media_streams` fields:
   - `PUT /library/parts/{id}` only selects the audio/subtitle stream.
   - `PUT /library/streams/{id}.{ext}` only sets an offset.
   - `…/analyze` runs Plex's scanner, which is what this design bypasses.
   - `PUT /library/metadata/{id}` was tried with seven variants on item 1009: plain fields, `Media[0].…`, `media[0].….value`, `Media[0].Part[0].Stream[0].…`, `duration.value`, and two marker forms. Each returned 200 and changed nothing: no rows, no locked fields, `updated_at` unchanged.
2. **Plex's API cannot write skip markers.**
   - `POST /library/metadata/{id}/marker` creates only `type=bookmark` (200).
   - It returns 400 for `intro`, `credits`, `recap`, `preview`, `commercial` and the integers 0–6, including on an analysed movie with client headers.
3. **The Custom Metadata Provider protocol cannot carry media.** Plex's docs: "Metadata support for 'streams' is not yet supported". The `Media` array in a `GET /library/metadata/{id}` response is Plex's report of its own analysis, not an input.
4. **How Plex stores markers:**
   - Each marker is a `taggings` row on the episode or movie. `tag_id` is the single tag with `tag_type = 12`, which has an empty name (id 2215 on this cluster).
   - `text` is `intro`, `credits` or `commercial`.
   - `time_offset` and `end_time_offset` are in milliseconds.
   - `index` orders several markers of one kind.
   - `extra_data` is JSON plus a url-encoded mirror, for example `{"pv:version":"4","pv:final":"1","url":"pv%3Afinal=1&pv%3Aversion=4"}`.
   - Plex has no recap or preview kind.
5. **TheIntroDB v3:**
   - `GET https://api.theintrodb.org/v3/media` takes one of `tmdb_id`, `imdb_id` or `tvdb_id`, plus `season` and `episode` for TV, and optional `duration_ms`.
   - It returns `intro`, `recap`, `credits` and `preview` arrays of `{start_ms, end_ms}`.
     - A null `start_ms` means from 0.
     - A null `end_ms` means to the end.
     - `{null, 0}` means none.
   - A title it doesn't have returns 404 `{"error":"media not found"}`.
   - Anonymous limits, taken from the response headers:
     - `x-ratelimit-limit: 30` per 10 s;
     - `x-usagelimit-limit: 500`;
     - `x-usagelimit-specificmedia-limit: 2000`.
   - Keys are accepted through `Authorization`.
   - Coverage is good for popular titles: Breaking Bad, Game of Thrones, The Office, The Matrix. It returned 404 for Andor and Wolfwalkers.

**Consequence:** cluster-plex writes Plex's database for media and markers. This is an exception to cluster-plex's invariant "Plex's configuration is provisioned through its API, never its database". It is recorded in an ADR (§4.6), with this section as its evidence. Configuration (providers, agents, libraries, preferences) stays API-only.

## 3. clustarr: TheIntroDB segments on the file

### 3.1 Why the file

- TheIntroDB identifies a release by `duration_ms`, and only the probed file knows its duration.
- cluster-plex already watches MediaFiles and maps their paths onto Plex's files.
- So segments live on `MediaFile.status`, next to the probe they were fetched for.

### 3.2 Client: `pkg/metadata/clients/theintrodb`

- `Media(ctx, ids metadata.ExternalIDs, season, episode int32, durationMs int64) (Segments, error)`.
  - It uses tmdb, else tvdb, else imdb.
  - It sends `season` and `episode` only for an episode, and `duration_ms` when it is known (> 0).
- `Segments` has `Intro`, `Recap`, `Credits` and `Preview`, each a `[]Segment{StartMs, EndMs int64}`.
  - A null start becomes 0.
  - A null end becomes `durationMs`. If the duration is unknown, the segment is dropped.
  - A segment with `EndMs <= StartMs` is dropped, which covers `{null, 0}`.
- House rules apply:
  - The limiter is injected (`Config.Limiter`), never defaulted on.
  - The body is read through a cap (`metadata.ReadBody`).
  - A 404 maps to `metadata.ErrNotFound`.
  - A key is sent as `Authorization: Bearer <key>` when configured. It is never logged and never put in an error string.
- The client returns the response's `x-ratelimit-*` and `x-usagelimit-*` values (`Limits{Remaining, Reset}`) so the caller can pace itself.
- Tests run against responses recorded from the live API, through `hack/record-metadata-fixtures`:
  - Breaking Bad S01E01 (`tmdb_id=1396`): intro plus credits with a null end;
  - The Matrix (`tmdb_id=603`): intro with a null start;
  - Friends S01E01 (`tmdb_id=1668`): `{null, 0}` intro;
  - one 404.

### 3.3 API: `MediaFileStatus.markers`

```go
// Markers are the skip segments TheIntroDB publishes for this file.
// +optional
Markers *FileMarkers `json:"markers,omitempty"`

type FileMarkers struct {
	// Result is Found, NotFound or Error.
	Result MarkersResult `json:"result"`
	// FetchedAt is when TheIntroDB was last asked.
	FetchedAt metav1.Time `json:"fetchedAt"`
	// ForProbeHash is status.probeHash when these were fetched; a new file re-fetches.
	ForProbeHash string `json:"forProbeHash,omitempty"`
	// DurationMs is the duration_ms sent, the probe's runtime.
	DurationMs int64 `json:"durationMs,omitempty"`
	// Segments, ordered by start. +kubebuilder:validation:MaxItems=20
	Segments []MarkerSegment `json:"segments,omitempty"`
	// Message says why an Error happened. +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`
}

type MarkerSegment struct {
	// +kubebuilder:validation:Enum=intro;recap;credits;preview
	Kind    MarkerKind `json:"kind"`
	StartMs int64      `json:"startMs"`
	EndMs   int64      `json:"endMs"`
}
```

No floats and a capped list, per the invariants. `Message` is clamped at the boundary, as `pkg/download.clampMessage` does.

### 3.4 Provider and worker

- **Provider:**
  - It is a new MetadataProvider type, `theintrodb`, with an optional `secretRef` (`apiKey`).
  - `SeedDefaults` seeds it, since it needs no key.
  - While the usage limit is exhausted, the provider's `status` reports `Ready=False, reason UsageLimited` until the reset.
  - The registry gains `Markers []MarkersProvider`.
- **Worker:**
  - It is a new marker worker in catalogarr's metadata role. It is the only writer of `status.markers`, under a new field manager, `k8s.ManagerCatalogarrMarkers` (`catalogarr-markers`).
  - MediaFile status keeps one writing service (catalogarr) with disjoint managers, like artwork's.
- **What needs fetching:** a movie or episode MediaFile with `status.mediaInfo` and one of:
  - no `markers`;
  - `markers.forProbeHash != status.probeHash`;
  - `Found` older than 30 days;
  - `NotFound` older than 7 days;
  - `Error` older than 1 day.
- **Triggering:**
  - The MediaFile reconciler publishes a markers task on a new work subject when the file needs fetching.
  - The message id is `events.MsgIDForObject(uid, generation, "markers-"+probeHash)`.
  - The consumer is rate-limited per host through the `clustarr-provider-throttle` pattern: one limiter per provider, shared by replicas through KV.
- **Handling a task:**
  1. Read the MediaFile.
  2. Read the owning item through `spec.mediaRef`: the Movie's `status.metadata` ids, or the Episode's ids plus season and episode. The MediaFile's own reference gives it, so no Episode index is needed.
  3. Call the provider with `durationMs = mediaInfo.runtimeMillis`.
  4. Re-`Get` the MediaFile immediately before applying (the lost-update rule).
  5. Apply the complete `markers` block through `pkg/k8s.PatchStatus`.
- **Results:**
  - A usage-limited or failed call records `Error` with the reason and is retried after 1 day, or after the limit resets.
  - A 404 records `NotFound`.
- **UI:** the item page lists the file's segments, read-only.

## 4. cluster-plex: the seeder

### 4.1 Where

- A seeder in the Lease holder, beside the clustarr watcher and fed by it.
- The watcher's `Trim` additionally keeps each MediaFile's:
  - `status.mediaInfo`;
  - `status.probeHash`;
  - `status.markers`.
- It still reads clustarr as unstructured objects and imports none of clustarr's code.
- The seeder runs:
  - on each MediaFile add or update whose probe or markers changed;
  - on a full resync every 6 hours;
  - once when the Lease is gained.
- Work is debounced per file with bounded concurrency (4), and every write for a file happens in one transaction.

### 4.2 Mapping a file to Plex

- The watcher's existing path mapper turns `spec.path` into Plex's path (`/data/media` → `/library`).
- That path gives `media_parts.file` → `media_parts.media_item_id` → `media_items.metadata_item_id`.
- It only acts inside the sections cluster-plex provisioned for clustarr ("Clustarr Movies" and "Clustarr TV", read from its own provisioning config).
- A file that isn't in Plex yet is skipped and retried on the next event or resync.

### 4.3 Seeding media

It seeds only when the `media_items` row has no `media_streams`. Plex's own analysis is never overwritten.

- `media_items`:
  - `container`, `video_codec`, `audio_codec` (from the first default audio track);
  - `width`, `height`, `duration` (`runtimeMillis`);
  - `bitrate` (video plus audio kbps × 1000, when known), `frames_per_second` (`fpsMilli`/1000);
  - `display_aspect_ratio` (width/height), `audio_channels`.
- `media_parts`: `duration`, and `size` from `spec.sizeBytes`.
- `media_streams`:
  - Video: one row (`stream_type_id` 1), with `ma:bitDepth` and `ma:profile` in `extra_data`.
  - Audio: one row per track (2), with `channels`, `bitrate`, `default`, and `ma:audioChannelLayout` and `ma:profile` in `extra_data`.
  - Subtitles: one row per track (3), with `forced` and hearing-impaired carried in `extra_data`.
  - Each row carries `index` (the probe's stream index), `codec`, `language` and `media_part_id`.
- Name translation lives in a table in code:
  - codecs: `subrip`→`srt`, `hdmv_pgs_subtitle`→`pgs`, `dvd_subtitle`→`vobsub`, `mov_text`→`mov_text`, and anything else passes through;
  - languages: ISO 639-2 → Plex's two-letter form (`eng`→`en`).
- `media_analysis_version` is left untouched (0). If Plex's scanner works again it re-analyses the file and replaces these rows, which is intended.
- Every seeded row's `extra_data` carries `cp:source=clustarr` so it can be told apart.

### 4.4 Reconciling markers

Mapping from TheIntroDB kinds to Plex:

| TheIntroDB | Plex `text` | Notes |
|---|---|---|
| intro | `intro` | |
| recap | `intro` | an extra intro, so clients offer "Skip Intro" over it |
| credits | `credits` | the one ending at the file's end gets `pv:final=1` |
| preview | `credits` | never final, so "Skip Credits" jumps over it without ending the episode |

For each file's episode or movie, and each Plex kind (`intro`, `credits`), the desired rows come from `status.markers` when `result=Found`:

- **TheIntroDB has segments of that kind:**
  - The seeder owns every row of that kind on the item.
  - It deletes Plex-detected rows of that kind, and any of its own that no longer match.
  - It inserts the missing ones, with `index` in start order.
  - `extra_data` is `{"pv:version":"4","pv:source":"theintrodb"[, "pv:final":"1"]}` plus its url mirror.
  - If Plex's detector later rewrites them, the next event or resync re-asserts them.
- **TheIntroDB has none of that kind** (including `NotFound`): the seeder deletes only its own rows (`pv:source=theintrodb`) and leaves Plex-detected rows alone.
- **`Error`:** changes nothing.
- The marker tag is the existing `tags` row with `tag_type = 12`. If none exists, the seeder creates one exactly as Plex's is: an empty tag name, `tag_type` 12.

### 4.5 Failure handling

| Case | Behaviour |
|---|---|
| Database unreachable | the seeder retries with backoff and reports `clusterplex_seed_errors_total` |
| Path not in Plex | skipped; `clusterplex_seed_unmatched_total` counts it; retried on the next resync |
| Plex already analysed the file | media untouched |
| Lease lost mid-run | the transaction for that file rolls back; the new holder resyncs |
| Unexpected schema (a missing column) | refuse to seed and report it; never guess |

Everything is idempotent: a replay writes nothing new.

### 4.6 ADR

cluster-plex ADR `0006-seed-analysis-and-markers-into-the-database`:

- the invariant exception;
- §2 as its evidence;
- the rule that configuration stays API-only;
- the rule that seeded rows are identifiable and yield to Plex's own analysis;
- CLAUDE.md updated to match.

## 5. Testing

- **clustarr:**
  - Recorded-fixture client tests (§3.2), covering the null-start, null-end and `{null, 0}` cases and the limit headers.
  - An envtest for the worker, run against an object already in steady state, covering:
    - it fetches only when needed;
    - a new probe hash re-fetches;
    - the re-`Get` closes the lost-update window when a second writer interleaves;
    - `managedFields` show `catalogarr-markers` owning only `status.markers`.
  - `pkg/crdcheck` guards the new list cap.
- **cluster-plex:**
  - A real-PostgreSQL test of the seeder's SQL against `hack/plex-postgresql/schema/plex_schema.sql`, gated on the DSN variable the bootstrap tests use. It covers:
    - media seeding of an unanalysed file, from a probe recorded from this cluster (Andor S01E02);
    - an analysed file left untouched;
    - marker reconciliation with Plex-detected rows present: TheIntroDB wins its kinds, and Plex keeps the others;
    - NotFound removes only our rows;
    - idempotence.
  - Unit tests of the codec and language table and the kind mapping.
- **Live proof on kind-cluster-plex, in order:**
  1. Seed one episode, Andor S01E02 (Plex item 1009). Plex must show Video HEVC and Audio EAC3 5.1, it must play, and it must offer "Skip Intro" (Skip Intro is checked on the first library item whose `markers.result` is `Found`, since TheIntroDB has no Andor).
  2. Roll out to the whole library and report `media_streams` coverage and marker counts before and after.

## 6. Out of scope

- The `.LocalAdminToken` fix and remote transcoding: tracked separately.
- Chapters, loudness, BIF thumbnails and intro fingerprinting: Plex-scanner features nobody asked for.
- Contributing segments back to TheIntroDB.
- Music, books and other non-video libraries: Plex's custom providers serve movie and TV libraries only.
