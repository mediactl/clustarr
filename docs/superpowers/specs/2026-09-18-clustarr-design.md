# Clustarr — Final Design (v1alpha1)

> **Amended.** `2026-09-18-clustarr-design-amendment-1.md` adds the `importarr`
> service, the `pkg/obs` observability stack (slog through context, OpenTelemetry
> spans, a documented Prometheus catalogue) and the `ui` service, and reverses
> this document's "Web UI" non-goal. Read it alongside this one; where they
> disagree, the amendment wins.

Synthesis of the three candidate architectures and three judge verdicts. Base: **Clustarr (CRD-first)** (aggregate winner, 119). Grafted from the streaming edition: KV grab lease, delay profiles via scheduled messages + pending KV, `CLUSTARR_RELEASES` firehose with server-side dedup, `rpc.indexarr.download`, `Clustarr-Schema`/`Clustarr-Trace` headers, `QueueFull` backpressure condition, "transcoding never rewrites release quality", RootFolder naming/permissions/recycle-bin block, `Attempts` backoff, `unmonitoredIssues`. Grafted from the pragmatic edition: SQLite-FTS5 release index, milestone-ordered MVP (Torznab/Newznab first, Cardigann second), typed ImportList union, full Sonarr/Lidarr/Readarr/Mylar add-time fields, GPL-3.0 as a deliberate ADR, `status.import` on Download. Every judge `must_fix` is resolved and cross-referenced in §16.

Where judges disagreed, the decision and the one-line reason:

| Disagreement | Decision | Why |
|---|---|---|
| Transcodes as JetStream work vs batch/v1 Jobs | **batch/v1 Jobs** | Jobs give GPU scheduling, podFailurePolicy, logs and kubectl visibility for free; a 24 h encode under AckWait is a redelivery hazard. |
| Per-file state as `MediaFile` CR vs `status.file` with cross-service SSA | **MediaFile CR, single controller-writer per CR** | All three judges found multi-manager SSA under-specified; one writer per object is provable in envtest, and ~3 small objects per episode is well within etcd for a homelab. |
| Release index: KV+bleve vs per-pod bleve vs SQLite-FTS5 | **SQLite-FTS5 on an RWO PVC, single writer** | Rebuildable cache, cgo-free, Postgres-portable Store interface; both bleve variants were unbounded in RAM. |
| Cardigann engine in MVP vs phase 2 | **In MVP, but milestone M6 after end-to-end via Torznab/Newznab (M3)** | "Indexers borrowed from Prowlarr" is a hard constraint; ordering keeps the first pass shippable. |
| DelayProfile in MVP | **In MVP** | TRaSH assumes usenet-preferred + torrent delay; it costs one scheduled message and one KV record. |
| Chunked transcoding in MVP | **Deferred, shape documented** | Whole-file Jobs plus the NVENC tier cover the homelab; chunking needs RWX scratch and seam verification. |
| Licence | **GPL-3.0** | Verbatim ports of *arr/Bazarr regexes, decision tables and Bazarr scoring are legal only under GPL; recorded as ADR-0002. |

---

## 1. Goals / Non-goals

**Goals.** A Kubernetes-native, event-driven media stack in one Go module that manages a collection end to end: indexers (Prowlarr-compatible Cardigann v11 + Torznab/Newznab), downloads (embedded anacrolix torrent engine + embedded usenet pipeline), inventory for movies, TV, music, books/comics/manga and audiobooks with *arr semantics (releases, quality, monitored, minimum availability, root folders, metadata), import lists, an opinionated TRaSH-only quality model, distributed HEVC 10-bit + AAC transcoding on scheduled workers, and a distributed Bazarr-like subtitle service. Everything a user configures or watches is a CR; `kubectl get movies,downloads,transcodejobs,subtitlerequests -A` is the UI.

**Non-goals (v1).** Postgres/Redis/Kafka; external download clients; admission webhooks (CEL only); multi-tenancy across NATS accounts; Radarr/Sonarr v3 REST facade; HDR10+ preservation; Whisper generation; per-service repos.

## 2. Naming

| Thing | Name |
|---|---|
| Project / domain | `clustarr` / `clustarr.io` (register clustarr.io; .com is taken) |
| Go module | `github.com/mediactl/clustarr` (Go 1.27) |
| API groups | `catalog.clustarr.io`, `index.clustarr.io`, `download.clustarr.io`, `transcode.clustarr.io`, `subtitle.clustarr.io`, all `v1alpha1`; `api/common/v1alpha1` = shared Go types, no CRDs |
| Services (dirs = binaries' subcommands) | `catalogarr/` (inventory + metadata + release decisions), `importarr/` (library rescan + import lists + completed-download import), `indexarr/`, `grabarr/`, `squasharr/`, `captionarr/`, `ui/` (server-rendered web UI) |
| Binary | one cobra binary `clustarr`: `clustarr <service> --role <role>`; `clustarr all` for kind/dev |
| Images | `ghcr.io/mediactl/clustarr` (distroless static; indexarr), `ghcr.io/mediactl/clustarr-media` (debian-slim: ffmpeg 9 + libx265 + nvenc headers, ffprobe, par2cmdline-turbo v1.5.0, CGO build for nntppool; catalogarr, grabarr, squasharr, captionarr), `ghcr.io/mediactl/clustarr-media-cuda` (squasharr GPU workers). `importarr` uses the media image (it hardlinks, probes and moves files); `ui` uses the distroless static image. |
| LeaderElectionID | `<service>.clustarr.io` |
| Field managers (SSA) | `catalogarr`, `catalogarr-series`, `catalogarr-worker`, `catalogarr-metadata`, `catalogarr-grab`, `importarr`, `importarr-worker`, `indexarr`, `grabarr`, `grabarr-engine`, `squasharr`, `squasharr-worker`, `captionarr`, `captionarr-worker`. `ui` never writes status and owns no field manager. Three of these were added in Phase C, all for the same reason -- server-side apply replaces a manager's whole ownership set per apply rather than merging it, so two writers sharing one name silently release each other's fields. `catalogarr-series`: the Series reconciler writes provider-derived fields onto the Episodes it owns, and sharing `catalogarr` with the Episode reconciler made each release the other's. `catalogarr-metadata` (gateway; owns `status.metadata` on Movie and Series) and `catalogarr-grab` (grab worker, RSS matcher and search sink; own `status.activeDownloadRef`, `status.pendingGrab`, `status.lastSearchedAt`, `status.searchAttempts` on Movie and Episode, never `status.phase`) were split out of `catalogarr-worker`, where each one's apply deleted the other's fields in BOTH directions: a grab dropped a movie's cached metadata to `Phase=Pending` and forced a provider refetch, and a metadata refresh dropped `activeDownloadRef`/`pendingGrab`, taking a delayed item out of `Delayed` back to `Wanted` where the wanted cron re-searched an item that already had a grab scheduled. `catalogarr-worker` keeps the consumers with no such overlap: the interactive search worker's `Search.status` results, import and importlist. |
| Finalizers | `<group>/<kind-lowercase>` e.g. `download.clustarr.io/download` |
| Helm chart | `charts/clustarr` (umbrella: nats 2.14.6, nack 0.35.0 optional, keda optional) |
| NATS streams | `CLUSTARR_EVENTS`, `CLUSTARR_RELEASES`, `CLUSTARR_WORK_CATALOGARR`, `CLUSTARR_WORK_IMPORTARR`, `CLUSTARR_WORK_INDEXARR`, `CLUSTARR_WORK_CAPTIONARR`, `CLUSTARR_DLQ` |

## 3. Architecture

Five controller-runtime v0.25.1 managers, one per service, each its own Deployment/RBAC/leader election. Two coupling mechanisms, in priority order:

1. **Kubernetes watches (default).** `MediaFile` is the pivot: catalogarr creates it on import; squasharr's `TranscodeProfile` reconciler and captionarr's `SubtitleProfile` reconciler watch MediaFiles and create `TranscodeJob` / `SubtitleRequest`; catalogarr's importer watches `Download` (phase `Completed`); catalogarr's MediaFile reconciler watches `TranscodeJob` (Succeeded → path/size follow-up) and `SubtitleRequest` (sidecar rescan). Cross-service watches use a predicate on `metadata.generation` or `status.probeHash` only (§10) and RBAC is get/list/watch on other groups' kinds; the only cross-group status write is catalogarr → `Download.status.import` (SSA, manager `catalogarr`).
2. **NATS JetStream (exception).** Rate-limited or long-running worker dispatch (search, grab, import, metadata refresh, import-list sync, RSS polls, subtitle fetch), scatter/gather RPC (indexer search, indexer download resolution, metadata lookup), the release firehose, the history log, and KV for leases/pending grabs/throttles/caches/progress.

**Single-writer rule.** Every CR has exactly one controller owner (its service) that writes `status.phase` and `status.conditions`; a worker/engine of the same service may SSA a disjoint, enumerated set of status fields under `<service>-worker`/`grabarr-engine` (§5 per CRD). `Status().Update()` is forbidden by lint (`forbidigo`) outside `pkg/k8s`; all status writes go through `k8s.PatchStatus` (SSA apply configurations, `+kubebuilder:ac:generate=true`, `SchemeGroupVersion` defined in every group).

**Process topology.**

| Deployment | Roles | Replicas |
|---|---|---|
| `catalogarr` | controllers (leader-elected) + queue workers search/grab/import/importlist/rss-matcher (every replica) + history sink + DLQ projector (leader) | 1 (scale-out allowed; workers scale, controllers leader-only) |
| `catalogarr-metadata` | metadata gateway: consumes `work.catalogarr.metadata.>`, serves `rpc.catalogarr.metadata.*`, owns all outbound metadata clients, rate limiters, otter L1 + KV L2 | **exactly 1** (chart-enforced; limiter windows are in-process) |
| `indexarr` | Indexer/IndexerDefinition/IndexerProxy controllers, search RPC, RSS worker, SQLite release index, Torznab facade | **exactly 1**, strategy `Recreate`, RWO PVC `clustarr-index` |
| `grabarr` | DownloadClient/Download controllers | 1 |
| `<downloadclient>-engine` | StatefulSet owned by each `DownloadClient` (torrent) or Deployment (usenet) | `DownloadClient.spec.replicas` (default 1) |
| `squasharr` | TranscodeProfile/TranscodeJob controllers + slot scheduler | 1 |
| batch/v1 Jobs | `clustarr squasharr worker` per TranscodeJob | on demand |
| `captionarr` | controllers + cron republishers | 1 |
| `captionarr-worker` | fetch consumers | 2 fixed (KEDA optional) |
| `nats` | JetStream R3 (R1 on kind), file store 20Gi | 3 |

## 4. API groups & CRDs

Legend for the field listings: `req` = `+required`; `opt` = `+optional`; `imm` = `+kubebuilder:validation:XValidation:rule="self == oldSelf"`; `=v` default; `enum{...}` = `+kubebuilder:validation:Enum`; `map[key]` = `+listType=map,+listMapKey=key`; `≤N` = `+kubebuilder:validation:MaxItems=N`. All kinds have `metadata`, `status.observedGeneration int64`, `status.conditions []metav1.Condition` (`map[type]`), printer columns for phase/ready. Status budget: **no object > 256 KiB, no status list > 200 entries** unless stated.

### 4.1 `api/common/v1alpha1` (Go only)

```go
type MediaKind string // movie series episode artist album author book audiobook comic issue
type MediaRef struct {
    Kind MediaKind `json:"kind"` // req
    Name string    `json:"name"` // req, same namespace
    Keys []string  `json:"keys,omitempty"` // Episode/Issue names for packs, ≤200
}
type Source string   // enum unknown cam telesync telecine workprint dvd tv webdl webrip bluray
type Modifier string // enum none regional screener rawhd brdisk remux
type Quality struct {
    Name       string   `json:"name"`                 // canonical definition name (video: derived from tuple; music/book/audiobook/comic: table name e.g. FLAC, MP3-320, EPUB, M4B, CBZ)
    Source     Source   `json:"source,omitempty"`     // video only
    Resolution int32    `json:"resolution,omitempty"` // enum 0 360 480 540 576 720 1080 2160
    Modifier   Modifier `json:"modifier,omitempty"`
}
type Revision struct { Version int32 `json:"version"`; Real int32 `json:"real"`; Repack bool `json:"repack"` } // =1,=0,=false
type ReleaseType string // single multi seasonPack album book issue
type ReleaseInfo struct { // snapshot stored on Download.spec.release and Search results
    GUID, IndexerRef, IndexerName, Title string
    Protocol     string  // enum torrent usenet
    SizeBytes    int64
    PublishedAt  metav1.Time
    DownloadURL, MagnetURL, InfoHash, InfoURL string
    Seeders, Leechers *int32
    IndexerFlags []string // freeleech halfleech neutralleech doubleupload internal exclusive scene
    Categories   []int32
    Quality      Quality; Revision Revision; ReleaseGroup, Edition string; Languages []string
    ReleaseType  ReleaseType; FormatScore int32; MatchedFormats []string
    IDs          map[string]string // tmdb imdb tvdb ...
}
type HdrFormat string // none pq10 hdr10 hdr10plus hlg10 dolbyVision dolbyVisionHdr10 dolbyVisionSdr dolbyVisionHlg dolbyVisionHdr10Plus
type AudioStream struct { Index int32; Codec, Profile, Language, Title string; Channels int32; BitrateKbps int32; Default, Commentary bool }
type SubtitleStream struct { Index int32; Codec, Language, Title string; Forced, HearingImpaired, Bitmap bool }
type MediaInfo struct { // *arr MediaInfoModel vocabulary
    Container, VideoCodec, VideoProfile, PixelFormat string; VideoBitDepth, Width, Height int32; Fps float64 `json:"fps"`
    VideoBitrateKbps int32; Hdr HdrFormat; DoviProfile, DoviBLCompatID *int32; RuntimeSeconds float64
    Audio []AudioStream `json:"audio"` /* ≤64 */; Subtitles []SubtitleStream /* ≤64 */; Attachments int32; Chapters int32
}
type SeedCriteria struct { Ratio *float64; SeedTime, PackSeedTime, InactiveTime *metav1.Duration }
type RejectionType string // Permanent Temporary
type Rejection struct { Reason string; Type RejectionType }
type Attempts struct { Initial, Latest *metav1.Time; Count int32 }
type AddSource struct { ImportListRef string }
```

### 4.2 `catalog.clustarr.io` (owner: catalogarr)

**RootFolder** (Namespaced)
```go
type RootFolderSpec struct {
    Path string      // req, imm, CEL rule="self.startsWith('/data/media/')"
    Kind string      // req, enum movie series music book audiobook comic
    Defaults RootDefaults // opt
    Naming   NamingSpec    // opt
    RecycleBin RecycleBin  // {Path string ="/data/.recycle"; CleanupDays int32 =7}
    MinFreeBytes int64     // =0
    Permissions  Perms     // {FileMode string ="0664"; DirMode string ="0775"; Group *int64}
}
type RootDefaults struct { QualityProfileRef, TranscodeProfileRef, SubtitleProfileRef, DelayProfileRef string; Monitored bool =true; MonitorNewItems string enum{all,none,new} ="all"; SearchOnAdd bool =true; MinimumAvailability string ="released"; SeriesType string ="standard"; SeasonFolder bool =true; Tags []string }
type NamingSpec struct { Dialect string enum{jellyfin,plex,emby,kodi} ="jellyfin"; ColonReplacement string enum{delete,dash,spaceDash,spaceDashSpace,smart} ="smart"; MultiEpisodeStyle string enum{extend,duplicate,repeat,scene,range,prefixedRange} ="prefixedRange"; Overrides map[string]string /* token keys: movieFolder movieFile seriesFolder seasonFolder episodeFile animeFile dailyFile artistFolder albumFolder trackFile authorFolder bookFile audiobookFolder audiobookFile comicFolder issueFile */ }
type RootFolderStatus struct { Accessible bool; FreeBytes, TotalBytes int64; UnmappedFolders []string /* ≤200 */; ItemCount int32; LastScannedAt *metav1.Time } // conditions Ready, DiskSpaceOK
```

**QualityProfile** (Cluster)
```go
type QualityProfileSpec struct {
    MediaKind string       // req, imm, enum video music book audiobook comic
    BuiltIn bool           // imm; type-level CEL: rule="!oldSelf.builtIn || self == oldSelf" (built-ins immutable; users copy)
    Tiers []Tier           // req, ≤40, best first (TRaSH order). Tier{Name string; Qualities []string /* canonical names or aliases, ≤8 */}
    Cutoff string          // req; CEL: must equal a tier name
    UpgradeAllowed bool =true
    MinFormatScore int32 =0; CutoffFormatScore int32 =10000; MinUpgradeFormatScore int32 =1
    ScoreSet string enum{default,anime-radarr,anime-sonarr} ="default"
    EnabledFormatGroups []string // optional TRaSH families: audio movieVersions streamingBoost seasonPack unwantedOptional
    FormatScores []FormatScore   // map[format]; overrides: {Format string (catalogue slug); Score int32}; unknown slug → Invalid condition
    Language string ="original"  // original | any | BCP-47
    ProperPolicy string enum{preferAndUpgrade,doNotUpgrade,doNotPrefer} ="preferAndUpgrade"
    SizeTable string enum{movie,series,anime,none} ="movie"
    SizeLimits []SizeLimit       // map[quality]; {Quality string; MinMBPerMinute, PreferredMBPerMinute, MaxMBPerMinute float64}; overrides the table
    PreferredProtocol string enum{usenet,torrent,any} ="any" // ranking key only; delays live in DelayProfile
}
type QualityProfileStatus struct { CatalogueVersion string; ResolvedFormats int32; QualityOrder []string; Hash string } // conditions Ready, Invalid
```

**DelayProfile** (Namespaced) — Radarr shape.
```go
type DelayProfileSpec struct {
    EnableUsenet bool =true; EnableTorrent bool =true
    PreferredProtocol string enum{usenet,torrent} ="usenet"
    UsenetDelayMinutes, TorrentDelayMinutes int32 =0
    BypassIfHighestQuality bool =true
    BypassIfAboveFormatScore bool =false; MinimumFormatScore int32 =0
    Order int32 =100          // lowest wins; the chart installs `default` with order 1000 and no tags
    Tags []string             // matches item spec.tags; empty = catch-all
}
type DelayProfileStatus struct{ PendingCount int32 }
```

**Movie** (Namespaced)
```go
type MovieSpec struct {
    TmdbID int64            // req, imm, +kubebuilder:selectablefield
    Monitored bool =true
    MinimumAvailability string enum{tba,announced,inCinemas,released} ="released"
    AvailabilityDelayDays int32 =0
    QualityProfileRef, RootFolderRef string // req
    DelayProfileRef, TranscodeProfileRef, SubtitleProfileRef *string // nil → tags/RootFolder defaults/selectors
    Folder *string
    Region string ="US"     // release-date region
    AddOptions MovieAddOptions // {Monitor string enum{movieOnly,movieAndCollection,none} ="movieOnly"; SearchForMovie bool =true; AddMethod enum{manual,list,collection} ="manual"} applied once (status.addOptionsApplied)
    Collection *CollectionOpts // {Monitor bool; QualityProfileRef, RootFolderRef string; MinimumAvailability string; SearchOnAdd bool} defaults for movies added from the collection
    Tags []string; Source *common.AddSource
}
type MovieStatus struct {
    Phase string enum{Pending,Unavailable,Wanted,Delayed,Downloading,Imported,CutoffUnmet,Unmonitored}
    Metadata *MovieMetadata // {Title, OriginalTitle, SortTitle, OriginalLanguage, Overview, Certification string; Year, RuntimeMinutes int32; Genres []string; Status enum{tba,announced,inCinemas,released}; InCinemas, DigitalRelease, PhysicalRelease *metav1.Time; ReleaseDates []ReleaseDate{Country string; Type int32 /*1-6*/; Date metav1.Time} ≤60; Collection *{TmdbID int64; Name string}; ExternalIDs map[string]string; Images []Image{Type enum{poster,fanart,logo}; URL string}; AlternateTitles []string ≤50; RefreshedAt metav1.Time}
    AddOptionsApplied bool; Available bool; AvailableAt *metav1.Time; Path string
    HasFile bool; FileRef *string; FileQuality *common.Quality; FileFormatScore int32; CutoffMet bool
    ActiveDownloadRef *string; PendingGrab *PendingGrab // {ReleaseTitle string; Protocol string; GrabAt metav1.Time}
    LastSearchedAt *metav1.Time; SearchAttempts common.Attempts
    Labels mirrored by controller: catalog.clustarr.io/resolution, /source, /modifier, /video-codec, /hdr (from the MediaFile)
} // conditions Ready, MetadataReady, Available, HasFile, CutoffMet, QueueFull
```

**Series** (Namespaced)
```go
type SeriesSpec struct {
    TvdbID int64 // req, imm, selectable
    SeriesType string enum{standard,daily,anime} ="standard"
    Monitored bool =true; MonitorNewItems string enum{all,none} ="all"
    Seasons []SeasonSpec // map[number]; {Number int32; Monitored bool}
    SeasonFolder bool =true; EpisodeOrder string enum{official,dvd,absolute} ="official" // absolute forced when anime
    QualityProfileRef, RootFolderRef string // req
    DelayProfileRef, TranscodeProfileRef, SubtitleProfileRef *string; Folder *string
    AddOptions SeriesAddOptions // {Monitor enum{all,future,missing,existing,firstSeason,lastSeason,pilot,recent,monitorSpecials,unmonitorSpecials,none,skip} ="all"; IgnoreEpisodesWithFiles, IgnoreEpisodesWithoutFiles bool; SearchForMissing bool =true; SearchForCutoffUnmet bool =false} applied once
    Tags []string; Source *common.AddSource
}
type SeriesStatus struct {
    Phase string enum{Pending,Ready,Unmonitored}
    Metadata *SeriesMetadata // {Title, SortTitle, Network, AirTime, Overview, Certification, OriginalLanguage string; Year, RuntimeMinutes int32; Status enum{continuing,ended,upcoming}; Genres []string; ExternalIDs map[string]string (tvdb tmdb imdb tvmaze anidb anilist mal kitsu); Images []Image; AlternateTitles []AltTitle{Title string; SceneSeason *int32} ≤100; RefreshedAt}
    AddOptionsApplied bool; Path string
    Seasons []SeasonStatus // map[number]; {Number int32; Monitored bool; EpisodeCount, EpisodeFileCount int32; SizeBytes int64; NextAiring *metav1.Time}
    EpisodeCount, EpisodeFileCount int32; NextAiring, PreviousAiring *metav1.Time; LastSearchedAt *metav1.Time
} // conditions Ready, MetadataReady, EpisodesSynced
```

**Episode** (Namespaced; created/owned by Series; name `<series>-s<NN>e<NN>`, daily `<series>-<yyyy-mm-dd>`)
```go
type EpisodeSpec struct { SeriesRef string /* req imm */; SeasonNumber, EpisodeNumber int32 /* imm */; Monitored bool /* user-editable; Series controller sets it only at creation and per MonitorNewItems */ }
type EpisodeStatus struct {
    TvdbID int64; Title, Overview string; AirDate *metav1.Time; RuntimeMinutes int32; AbsoluteNumber *int32
    SceneNumbering *SceneNumbering // {Season, Episode, Absolute *int32; Unverified bool}
    FinaleType string; Phase string enum{Unaired,Wanted,Delayed,Downloading,Imported,CutoffUnmet,Unmonitored}
    HasFile bool; FileRef *string; FileQuality *common.Quality; FileFormatScore int32; CutoffMet bool
    ActiveDownloadRef *string; PendingGrab *PendingGrab; LastSearchedAt *metav1.Time; SearchAttempts common.Attempts
} // conditions Aired, HasFile, CutoffMet
```

**Artist** (Namespaced)
```go
type ArtistSpec struct {
    MusicBrainzID string // req imm
    Monitored bool =true; MonitorNewItems string enum{all,none,new} ="all"
    MetadataProfile MusicMetadataProfile // {PrimaryTypes []string enum{album,ep,single,broadcast,other} =[album,ep]; SecondaryTypes []string enum{studio,compilation,soundtrack,spokenword,interview,audiobook,live,remix,djMix,mixtape,demo,audioDrama} =[studio]; ReleaseStatuses []string enum{official,promotion,bootleg,pseudoRelease} =[official]}
    AddOptions ArtistAddOptions // {Monitor enum{all,future,missing,existing,latest,first,none} ="all"; SearchForMissing bool}
    QualityProfileRef, RootFolderRef string; DelayProfileRef *string; Folder *string; Tags []string; Source *common.AddSource
}
type ArtistStatus struct { Metadata *ArtistMetadata /* Name, SortName, Disambiguation, Type, Overview string; Status enum{continuing,ended}; Genres; ExternalIDs; Images; RefreshedAt */; Path string; AlbumCount, AlbumFileCount int32; AddOptionsApplied bool } // conditions Ready, MetadataReady, AlbumsSynced
```

**Album** (Namespaced; owned by Artist, name `<artist>-<releasegroup-uid8>`)
```go
type AlbumSpec struct { ArtistRef string /* req imm */; ReleaseGroupID string /* req imm (MB release-group) */; Monitored bool; AnyReleaseOk bool =true; ReleaseID *string /* pinned MB release */; QualityProfileRef *string /* inherits Artist */ }
type AlbumStatus struct {
    Metadata *AlbumMetadata // {Title, Disambiguation, Overview string; AlbumType string; SecondaryTypes []string; ReleaseDate *metav1.Time; Releases []ReleaseSummary{ID, Status, Country, Label string; TrackCount int32; Media []Medium{Number int32; Format string}} ≤50; SelectedReleaseID string; Images}
    Tracks []Track // map[recordingID]; ≤200 (double albums fit; >200 → Invalid condition); {RecordingID string; Medium, Number, AbsoluteNumber int32; Title string; DurationMs int32; Explicit bool; FileRef *string}
    Phase string enum{Wanted,Delayed,Downloading,Imported,CutoffUnmet,Unmonitored}; Path string; TrackFileCount int32
    Quality *common.Quality /* min over tracks */; FormatScore int32; CutoffMet bool; ActiveDownloadRef *string; PendingGrab *PendingGrab; LastSearchedAt *metav1.Time; SearchAttempts common.Attempts
}
```

**Author** (Namespaced)
```go
type AuthorSpec struct {
    OpenLibraryID string // req imm ("OL…A"); HardcoverID *string
    Monitored bool =true; MonitorNewItems string enum{all,none} ="all"
    MetadataProfile BookMetadataProfile // {MinPopularity float64; SkipMissingDate, SkipMissingISBN, SkipPartsAndSets, SkipSeriesSecondary bool; AllowedLanguages []string; MinPages int32}
    AddOptions AuthorAddOptions // {Monitor enum{all,future,missing,existing,none}; SearchForMissing bool}
    QualityProfileRef, RootFolderRef string; DelayProfileRef *string; Folder *string; Tags []string; Source *common.AddSource
}
type AuthorStatus struct { Metadata *AuthorMetadata; Path string; BookCount, BookFileCount int32; AddOptionsApplied bool }
```

**Book** (Namespaced; owned by Author or standalone)
```go
type BookSpec struct {
    AuthorRef *string; WorkID string // req imm (Open Library work "OL…W")
    Monitored bool =true; AnyEditionOk bool =true; EditionID *string // pinned edition (OL edition id)
    Editions []EditionSpec // map[id]; ≤100; {ID string; Monitored bool} overrides
    QualityProfileRef, RootFolderRef *string // inherit Author
}
type BookStatus struct {
    Metadata *BookMetadata // {Title, Subtitle, Overview string; SeriesLinks []SeriesLink{Series string; Position string; Primary bool} ≤10; ReleaseDate *metav1.Time; Genres; Editions []Edition{ID, ISBN13, ASIN, Title, Language, Format, Publisher string; IsEbook bool; PageCount int32; ReleaseDate *metav1.Time} ≤100; ExternalIDs; Images}
    Phase string; Path string; HasFile bool; FileRef *string; FileFormat string; CutoffMet bool; ActiveDownloadRef *string; PendingGrab *PendingGrab; LastSearchedAt *metav1.Time; SearchAttempts common.Attempts
}
```

**Audiobook** (Namespaced)
```go
type AudiobookSpec struct { ASIN string /* req imm */; Region string enum{us,uk,ca,au,de,fr,es,in,it,jp} ="us"; BookRef *string; Monitored bool =true; QualityProfileRef, RootFolderRef string /* req; profile mediaKind audiobook */; DelayProfileRef *string; Folder *string; Tags []string; Source *common.AddSource }
type AudiobookStatus struct {
    Metadata *AudiobookMetadata // {Title, Subtitle string; Authors []NamedRef{Name, ASIN string}; Narrators []string; Series *SeriesLink; Publisher string; ReleaseDate *metav1.Time; RuntimeMinutes int32; Abridged bool; Language, ISBN string; Overview; Genres; Chapters []Chapter{Title string; StartMs int64} ≤200; ExternalIDs; Images}
    Phase string; Path string; HasFile bool; FileRefs []string /* ≤200 audio parts */; Quality *common.Quality; CutoffMet bool; ActiveDownloadRef *string; PendingGrab *PendingGrab; LastSearchedAt *metav1.Time; SearchAttempts common.Attempts
}
```

**Comic** (Namespaced) and **Issue** (owned by Comic, name `<comic>-<calculatedNumber padded 5.1>`)
```go
type ComicSpec struct {
    Source string enum{comicvine,mangadex} // req imm
    SourceID string // req imm (ComicVine volume id / MangaDex manga uuid)
    Kind string enum{comic,manga} ="comic"
    Monitored bool =true; MonitorNewIssues bool =true
    SpecialVersion string enum{normal,tpb,oneShot,hardCover,omnibus,volumeAsIssue} ="normal"
    UnmonitoredIssues []string // issue numbers the user unmonitored (kept even if Issue objects are recreated)
    QualityProfileRef, RootFolderRef string /* req; profile mediaKind comic */; DelayProfileRef *string; Folder *string; Tags []string; Source *common.AddSource
}
type ComicStatus struct { Metadata *ComicMetadata /* Title, Publisher, Overview string; Year, VolumeNumber, IssueCount int32; Manga enum{unknown,no,yes,yesAndRightToLeft}; AgeRating string; ExternalIDs (comicvine metron mangadex anilist mal); Images */; Path string; IssueFileCount int32; NextPullDate *metav1.Time } // conditions Ready, MetadataReady, IssuesSynced
type IssueSpec struct { ComicRef string /* req imm */; Number string /* imm */; CalculatedNumber float64 /* imm */; Monitored bool }
type IssueStatus struct { SourceID string; Title string; Date *metav1.Time; State string enum{wanted,skipped,snatched,downloaded,archived,ignored,failed}; HasFile bool; FileRef *string; FileQuality *common.Quality; ActiveDownloadRef *string; LastSearchedAt *metav1.Time; SearchAttempts common.Attempts }
```

**MediaFile** (Namespaced; ownerReference to the media CR; written only by catalogarr)
```go
type MediaFileSpec struct {
    MediaRef common.MediaRef // req imm
    Path string              // req; mutable only by catalogarr (transcode replacement / rename)
    SizeBytes int64; ModTime metav1.Time
    Quality common.Quality; Revision common.Revision // frozen at import; NEVER rewritten by transcoding
    ReleaseType common.ReleaseType; ReleaseGroup, Edition string; Languages []string
    FormatScore int32; MatchedFormats []string /* slugs */; ProfileHash string /* QualityProfile.status.hash at import */
    ImportedFrom *ImportSource // {DownloadRef, ReleaseTitle, IndexerName, Protocol string; ImportedAt metav1.Time; Manual bool}
    Original bool =true        // false once replaced by a transcode
}
type MediaFileStatus struct {
    ProbeHash string          // sha1(path|size|mtime); bumped ⇒ downstream services replan
    ProbedAt *metav1.Time; MediaInfo *common.MediaInfo
    Sidecars []Sidecar        // map[path]; ≤50; {Path, Language string; Forced, HI bool}
    Transcode *TranscodeState // {Compliant bool; ProfileTag string; JobRef *string; LastResult enum{none,succeeded,failed,skipped}}
    // controller-mirrored labels: catalog.clustarr.io/kind, /resolution, /source, /modifier, /video-codec, /hdr, /original
} // conditions Probed, Ready
```

**MetadataProvider** (Namespaced)
```go
type MetadataProviderSpec struct { Type string enum{tmdb,tvdb,musicbrainz,coverart,fanart,openlibrary,hardcover,audnexus,comicvine,metron,mangadex,anilist,kitsu,animelists} /* req imm */; Enabled bool =true; Priority int32 =50; SecretRef *corev1.LocalObjectReference /* keys apiKey pin bearer */; BaseURL *string; Region string ="US"; Language string ="en"; RateLimit *RateLimit /* {RequestsPerSecond float64; Burst int32; PerDay *int32} */; ContactUserAgent string /* req for musicbrainz/openlibrary/anidb */; CacheTTL *CacheTTL /* {Announced, Released, Ended, Search, Crosswalk metav1.Duration} */ }
type MetadataProviderStatus struct { QuotaRemaining *int32; QuotaResetAt, TokenExpiresAt, ThrottledUntil, LastSuccessAt *metav1.Time; LastError string } // conditions Ready, Authenticated, Throttled
```

**ImportList** (Namespaced)
```go
type ImportListSpec struct {
    Kinds []string enum{movie,series,album,book,audiobook,comic} // req
    Enabled bool =true; AutomaticAdd bool =true
    RefreshInterval metav1.Duration // clamped up to provider minimum: trakt/tmdb/mdblist 12h, plex 6h, arr 15m, stevenlu 24h, custom 6h
    SecretRef *corev1.LocalObjectReference
    // ExactlyOneOf(trakt,plex,tmdb,mdblist,stevenlu,imdbCSV,custom,arr)
    Trakt *TraktList   // {ListType enum{watchlist,list,collection,popular,trending}; Username, ListSlug string; Limit int32}
    Plex *PlexWatchlist // {}
    Tmdb *TmdbList     // {ListID *string; Discover map[string]string}
    Mdblist *MdbList   // {URL string}
    StevenLu *StevenLu // {}
    ImdbCSV *CSVList   // {ConfigMapRef corev1.LocalObjectReference}
    Custom *CustomList // {URL string; Format enum{json,rss}}
    Arr *ArrList       // {BaseURL string; Kind enum{radarr,sonarr,lidarr,readarr,clustarr}}
    Defaults ListDefaults // {QualityProfileRef, RootFolderRef string (req); DelayProfileRef *string; Monitored bool; MonitorNewItems, MinimumAvailability, SeriesType string; SeasonFolder, SearchOnAdd bool; Tags []string}
    SyncLevel string enum{disabled,logOnly,keepAndUnmonitor,removeAndKeep,removeAndDelete} ="logOnly"
}
type ImportListStatus struct { LastSyncAt, NextSyncAt *metav1.Time; ItemCount, AddedCount, ExcludedCount, RemovedCount int32; Auth *DeviceAuth /* {State enum{none,pending,authorized,expired}; UserCode, VerificationURL string; ExpiresAt, TokenExpiresAt *metav1.Time} */; LastError string } // items live in KV clustarr-importlist, not status
```

**ImportExclusion** (Namespaced): `spec{Kind string enum; ExternalIDs map[string]string (≥1 of tmdb tvdb imdb musicbrainz openlibrary asin comicvine mangadex, CEL size(self.externalIDs)>0); Title string; Year int32; Reason string}`.

**Search** (Namespaced; interactive search, TTL-deleted)
```go
type SearchSpec struct {
    Query *string; MediaRef *common.MediaRef // ExactlyOneOf
    Kinds []string; IndexerRefs []string; Categories []int32; Limit int32 =100 (≤200)
    Grab []string   // GUIDs the user wants grabbed; controller creates Downloads
    Override bool   // allow grabbing Permanently rejected results (= *arr shouldOverride)
    TTL metav1.Duration ="1h"
}
type SearchStatus struct {
    Phase string enum{Pending,Running,Completed,Failed}; StartedAt, FinishedAt *metav1.Time
    IndexerOutcomes []IndexerOutcome // map[name]; {Name string; State enum{ok,timeout,error,skipped}; Count int32; DurationMs int32; Error string}
    Results []ReleaseDecision       // ≤200; {common.ReleaseInfo; Approved, TemporarilyRejected bool; Rejections []common.Rejection; Rank int32}
    Grabbed []GrabResult            // {GUID, DownloadRef, Error string}
}
```

### 4.3 `index.clustarr.io` (owner: indexarr)

**IndexerDefinition** (Cluster; only custom/overriding definitions — the bundled 547 ship in the image)
`spec{YAML string (req, ≤1 MiB, validated against bundled schema.json v11); Replaces *string}` · `status{ID, Name, Language string; Type enum{public,semiPrivate,private}; Protocol enum{torrent,usenet}; Sha256 string; Caps CapsSummary{Modes map[string][]string; Categories []int32}}` conditions Valid.

**Indexer** (Namespaced)
```go
type IndexerSpec struct {
    // ExactlyOneOf(definition, definitionRef, generic)
    Definition *string            // bundled Cardigann id, e.g. "1337x"
    DefinitionRef *string         // IndexerDefinition name
    Generic *GenericNewznab       // {Protocol enum{torrent,usenet}; APIPath string ="/api"} for Newznab/Torznab upstreams incl. Prowlarr/Jackett
    BaseURL string                // req
    Enabled bool =true; Priority int32 =25 (1..50)
    Settings map[string]string    // non-secret Cardigann settings
    SecretRef *corev1.LocalObjectReference // keys apikey username password cookie passkey rss_key
    EnableRss, EnableAutomaticSearch, EnableInteractiveSearch bool =true
    RssInterval metav1.Duration ="15m"
    Limits *Limits                // {QueryLimit, GrabLimit *int32; Unit enum{day,hour} ="day"}
    RequestDelay metav1.Duration  // ≥ definition requestDelay, default 2s
    Timeout metav1.Duration ="30s"
    ProxyRef *string; Categories []int32; AnimeCategories []int32; AnimeStandardFormatSearch bool
    MinimumSeeders int32 =1; SeedCriteria *common.SeedCriteria; DownloadClientRef *string; Tags []string
}
type IndexerStatus struct {
    Protocol, Privacy string; Caps *Caps // {Modes map[string][]string; Categories []Category{ID int32; Name string; Sub []Category}; LimitsMax, LimitsDefault int32; SupportsRawSearch bool}
    EscalationLevel int32; DisabledUntil, InitialFailureAt, LastFailureAt *metav1.Time; LastFailure string
    QueriesInWindow, GrabsInWindow int32; LastRssAt *metav1.Time; LastRssNewCount int32; IndexedReleases int64; SessionSecretRef string
} // conditions Ready, Authenticated, Healthy, RateLimited
```

**IndexerProxy** (Namespaced): `spec{Type enum{flaresolverr,http,socks4,socks5} (req); Host string (req); Port int32; SecretRef *LocalObjectReference; RequestTimeout metav1.Duration ="60s"; Selector metav1.LabelSelector (Indexer labels; at most one FlareSolverr matches, applied last)}` · `status{LastCheckedAt *metav1.Time; Version string}` conditions Ready.

### 4.4 `download.clustarr.io` (owner: grabarr)

**DownloadClient** (Namespaced)
```go
type DownloadClientSpec struct {
    Protocol string enum{torrent,usenet} // req imm
    Enabled bool =true; Priority int32 =1
    Replicas int32 =1            // torrent engine shards (StatefulSet); CEL: protocol=='usenet' ⇒ replicas==1 in v1alpha1
    Categories map[string]string // mediaKind → subdir; default "<kind>"
    Torrent *TorrentSpec  // req iff protocol==torrent (CEL). {ListenPort int32 =42069; PublicIP *string; EnableDHT, EnablePEX bool =true; MaxActive int32 =200; DownloadLimitBps, UploadLimitBps int64; MaxUnverifiedBytes int64 =134217728; Seed common.SeedCriteria ={Ratio 1.0, SeedTime 168h, PackSeedTime 336h, InactiveTime 24h}; RemoveCompleted bool =true}
    Usenet *UsenetSpec    // req iff protocol==usenet. {Providers []NNTPProvider{Name, Host string; Port int32 =563; TLS bool =true; Connections int32; SecretRef corev1.LocalObjectReference; Backup bool; Priority int32; QuotaBytes *int64} (≥1); PostProcess{Par2 bool =true; Unpack bool =true; DeleteArchives bool =true; CleanupPatterns []string}; PropagationDelay metav1.Duration; PreCheck bool; AbortHealthPercent int32 =90; HealthAction enum{pause,delete} ="pause"; Scratch{SizeLimit resource.Quantity ="50Gi"; StorageClassName *string /* nil = emptyDir */}}
    Resources corev1.ResourceRequirements; NodeSelector map[string]string; Tolerations []corev1.Toleration
}
type DownloadClientStatus struct { Engine *EngineStatus /* {WorkloadRef string; Replicas, ReadyReplicas int32} */; Active, Queued, Seeding int32; DownloadRateBps, UploadRateBps int64; FreeBytes int64 } // conditions Ready, EngineReady, DiskSpaceOK
```

**Download** (Namespaced; ownerReference to the target media CR; name `<target>-<sha1(guid)[:10]>`)
```go
type DownloadSpec struct {
    Protocol string enum{torrent,usenet} // req imm
    ClientRef string        // resolved once by grabarr at admission if empty; imm afterwards (CEL: oldSelf.clientRef=='' || self==oldSelf)
    Source DownloadSource   // imm; ExactlyOneOf(magnetURL,torrentURL,nzbURL,indexerDownload). {MagnetURL, TorrentURL, NZBURL *string; IndexerDownload *IndexerDownload{IndexerRef, GUID, URL string}; ExpectedInfoHash *string}
    Release common.ReleaseInfo // imm
    Target common.MediaRef     // imm
    QualityProfileRef string; Priority string enum{high,normal,low} ="normal"; Paused bool
    SeedCriteria *common.SeedCriteria; RemoveOnImport bool =true; RemoveDataOnDelete bool =true
    GrabbedBy string enum{rss,search,interactive,push,redownload}; Manual bool // Manual ⇒ importer skips monitored/availability checks
}
type DownloadStatus struct {
    // grabarr (controller): phase, conditions, clientRef/engine, failureReason, blocklist, timestamps
    Phase string enum{Pending,Assigned,Queued,Downloading,Paused,Completed,Seeding,Imported,Failed,Blocklisted,Removing}
    Engine string           // "<client>-<ordinal>", set once; label download.clustarr.io/engine mirrors it
    FailureReason string enum{none,missingArticles,diskFull,encrypted,stalled,writeError,timeout,importRejected,manual}
    BlocklistedUntil *metav1.Time; StartedAt, CompletedAt, SeedGoalMetAt *metav1.Time
    // grabarr-engine (SSA, disjoint): telemetry
    Stage string enum{fetchingMetadata,transferring,verifying,repairing,extracting,publishing,seeding,done}
    DownloadID string; OutputPath, ContentRoot string; Files []DownloadFile /* ≤200; {Path string; SizeBytes int64; Skipped bool} */
    TotalBytes, RemainingBytes, DownloadedBytes, UploadedBytes int64; DownloadRateBps, UploadRateBps int64; ETASeconds *int32; Progress float64
    Seeders, Peers int32; Ratio float64; SeedTimeSeconds int64; Health *UsenetHealth /* {Health, CriticalHealth int32; FailedArticles, TotalArticles int32} */
    IsEncrypted, CanMoveFiles, CanBeRemoved bool; Message string; LastProgressAt *metav1.Time
    // catalogarr (SSA, disjoint): import outcome
    Import *ImportState // {State enum{pending,blocked,importing,imported,ignored}; Message string; Imported []ImportedFile{SourcePath, DestPath, MediaFileRef string} ≤200; Rejections []string; ImportedAt *metav1.Time}
} // conditions Assigned, Downloaded, SeedGoalMet, Imported, Failed
```
Blocklist = Downloads with label `download.clustarr.io/blocklisted=true` and `status.blocklistedUntil` (default now+90d), swept by grabarr. The decision engine reads them through a catalogarr informer keyed by infohash and normalized title. There is no other blocklist.

### 4.5 `transcode.clustarr.io` (owner: squasharr)

**TranscodeProfile** (Cluster)
```go
type TranscodeProfileSpec struct {
    Default bool                 // exactly one default (controller sets Invalid on the newer one)
    Selector *metav1.LabelSelector // over MediaFile labels; video kinds only (kind ∈ movie,episode enforced by controller)
    Container string enum{mkv,mp4} ="mkv"
    Hardware string enum{cpu,nvidia,intel} ="cpu"
    Video VideoSpec   // {Codec ="hevc"; PixelFormat ="yuv420p10le"; Profile ="main10"; CRF CRFTable{SD int32 =21; HD int32 =22; UHD int32 =23; HDROffset int32 =-1}; Preset string ="slow"; Tune *string; KeyintFactor int32 =10; BFrames int32 =8; Refs int32 =4; RCLookahead int32 =40; AQMode int32 =3; MaxRateKbps, BufSizeKbps *int32 (required for DV; validated by planner); ExtraX265Params map[string]string; NVENC{Preset ="p6"; Tune ="hq"; CQ int32 =24; Multipass ="fullres"; BRefMode ="middle"}; QSV{GlobalQuality int32 =22; Preset ="veryslow"; LookAheadDepth int32 =40}}
    Audio AudioSpec   // {Codec ="aac"; BitratePerChannelKbps int32 =64; KeepOriginal enum{never,lossless,atmos,always} ="atmos"; Languages []string; DropCommentary bool =true; StereoCompatTrack bool =false}
    Subtitles SubSpec // {CopyText bool =true; CopyBitmap bool =true; CopyAttachments bool =true}
    HDR HDRSpec       // {HDR10Plus enum{drop} ="drop"; DolbyVision enum{passthrough,downgradeToHDR10,reject} ="passthrough"}
    Policy PolicySpec // {SkipIfCompliant bool =true; RemuxOnlyWhenVideoCompliant bool =true; NeverTranscodeModifiers []string =[remux,brdisk]; MinDuration metav1.Duration ="1m"; MaxOutputToSourceRatio float64 =1.0; ReplaceSource bool =true; RecycleBin bool =true}
    Verify VerifySpec // {PacketCount bool =true; FullDecode bool =false; VMAFMin *float64}
    Resources corev1.ResourceRequirements // default limits cpu 8, memory 4Gi (1080p) — CPU limit fed to x265 pools
    GPU *GPUSpec      // {Count int32 =1; RuntimeClassName string ="nvidia"; NodeSelector map[string]string; Tolerations []corev1.Toleration}
    Scratch resource.Quantity ="20Gi" // emptyDir sizeLimit
    Priority int32 =50; ActiveDeadline metav1.Duration ="48h"; TTLSecondsAfterFinished int32 =86400
    Chunking *ChunkSpec // deferred; accepted but Enabled must be false in v1alpha1 (CEL)
}
type TranscodeProfileStatus struct { Hash string /* sha256 of render-relevant spec → CLUSTARR_PROFILE=<name>@<hash> */; MatchingFiles, PendingJobs, RunningJobs int32 } // conditions Ready, Invalid
```

**TranscodeJob** (Namespaced; ownerReference to MediaFile; name `<mediafile>-<profileHash[:8]>`)
```go
type TranscodeJobSpec struct { MediaFileRef string /* req imm */; ProfileRef string /* req imm */; SourcePath string /* req imm */; SourceProbeHash string /* imm */; OutputPath *string /* default <stem>.mkv beside source */; Priority int32; Hardware *string /* override */; Suspend *bool /* user pause */ }
type TranscodeJobStatus struct {
    // squasharr: phase, plan, jobRef, conditions, attempts
    Phase string enum{Pending,Planned,Queued,Running,Verifying,Succeeded,Failed,Skipped}
    Plan *Plan // {Encoder string; Mode enum{transcode,remuxOnly,skip}; SkipReason string; HDRMode string; VideoArgs []string; AudioTracks []AudioPlan{SourceIndex int32; Action enum{encode,copy,drop}; Codec string; BitrateKbps int32; Default bool}; SubtitleTracks []int32; ArgsHash string}
    JobRef *string; Attempts int32; StartedAt, FinishedAt *metav1.Time; Message string
    // squasharr-worker (SSA, disjoint): progress, result
    Progress *Progress // {Percent float64; Frame int64; FPS, Speed float64; OutTimeSeconds float64; BitrateKbps float64; UpdatedAt metav1.Time} — patched ≤ every 10 s
    Result *Result     // {OutputPath string; OutputSizeBytes int64; Ratio float64; VMAF *float64; MediaInfo *common.MediaInfo}
    StderrTail string  // ≤4 KiB
} // conditions Planned, JobCreated, Verified, Succeeded, Failed
```

### 4.6 `subtitle.clustarr.io` (owner: captionarr)

**SubtitleProfile** (Cluster)
```go
type SubtitleProfileSpec struct {
    Default bool; Selector *metav1.LabelSelector // MediaFile labels
    Languages []LanguageItem // req ≤20; map[key]; {Key string /* "en", "en:forced", "pt-BR:hi" computed by controller if empty */; Language string (BCP-47); Forced bool; HI enum{required,prefer,excluded} ="prefer"; AudioExclude, AudioOnlyInclude bool}
    Cutoff *string          // langKey; nil = any
    MustContain, MustNotContain []string // regex on release_info
    OriginalFormat bool =false; HIExtension enum{sdh,hi,cc} ="sdh"; LanguageEquals []string // "pt-BR:pt"
    MinScorePercent ScorePct // {Episode int32 =90; Movie int32 =70}
    Embedded EmbeddedSpec    // {Extract bool =true; IgnorePGS, IgnoreVobSub, IgnoreASS bool; SkipCommentary bool =true}
    Search SearchSpec        // {Interval metav1.Duration ="6h"; AdaptiveDelay ="504h"; AdaptiveDelta ="168h"}
    Upgrade UpgradeSpec      // {Enabled bool =true; Interval ="12h"; LookbackDays int32 =7; MinDeltaPoints int32 =3}
    Sync *SyncSpec           // deferred; {Enabled bool =false; Tool enum{ffsubsync,alass}; ThresholdPercent ScorePct; MaxOffsetSeconds int32 =60; GSS bool =true; NoFixFramerate bool =true}
    Whisper *WhisperSpec     // deferred; {Enabled bool =false; ProviderRef string}
    Mods []string enum{removeHI,removeTags,ocrFixes,common,fixUppercase,reverseRTL,color}
    Providers []string       // ordered SubtitleProvider names; empty = all enabled by priority
}
type SubtitleProfileStatus struct { WantedKeys []string; MatchingFiles int32 } // conditions Ready, Invalid
```

**SubtitleProvider** (Namespaced): `spec{Type enum{opensubtitlescom,gestdown,subdl,subsource,embedded,whisper} (req imm); Enabled bool =true; Priority int32 =50; SecretRef *LocalObjectReference (apiKey username password); Endpoint *string; Options map[string]string (aiTranslated=exclude, trustedSources, vip=true); Languages []string; RequestsPerSecond float64 =5}` · `status{ThrottledUntil *metav1.Time; ThrottleReason string; Quota *{Remaining int32; ResetAt metav1.Time}; TokenExpiresAt, LastSuccessAt *metav1.Time; ErrorsLast120s int32; HIVerifiable bool}` conditions Ready, Authenticated, Throttled.

**SubtitleRequest** (Namespaced; one per video MediaFile; ownerReference to MediaFile; name = MediaFile name)
```go
type SubtitleRequestSpec struct { MediaFileRef string /* req imm */; ProfileRef string; Languages []string /* override langKeys */; MinScoreOverride *int32; ForceSearch bool /* one-shot; controller resets */ }
type SubtitleRequestStatus struct {
    // captionarr: phase, planning, conditions
    Phase string enum{Satisfied,Wanted,Searching,Blocked}; ProfileGeneration int64; ProbeHash string
    Existing []ExistingSub // ≤64; {LangKey string; Source enum{embedded,sidecar}; Path string; StreamIndex *int32}
    // captionarr-worker (SSA, disjoint): items[langKey] except nextSearchAt/attempts which the controller owns
    Items []Item // map[langKey]; ≤20; {LangKey string; State enum{pending,searching,downloaded,upgradable,unavailable,failed}; Score, ScoreOutOf int32; Provider, SubtitleID, Path string; Attempts common.Attempts; NextSearchAt *metav1.Time; LastError string; DownloadedAt *metav1.Time}
} // conditions Planned, Satisfied, CutoffMet
```

## 5. Events & work queue

**Choice: NATS JetStream** (nats-server v2.15.0, nats.go v1.53.1, nats Helm chart 2.14.6; NACK v0.24.0 optional with `--control-loop`). ADR-0001 (§17) compares alternatives.

**Message headers (every message):** `Nats-Msg-Id` (CR-driven: `<uid>:<generation>:<task>`; RSS: `sha1(<indexerName>:<guid>)`; subtitle: `<request-uid>/<langKey>/<probeHash>`), `Clustarr-Type`, `Clustarr-Schema` (e.g. `catalog.SearchTask.v1`), `Clustarr-Source` (`<service>@<version>`), `Clustarr-Key`, `Clustarr-Time` (RFC 3339), `Clustarr-Trace` (W3C traceparent), `Content-Type: application/json`. Payload schemas are Go structs in `pkg/events/schema` versioned by the header, never by subject.

**Streams**

| Stream | Subjects | Retention | MaxAge | MaxBytes | Duplicates | Extras |
|---|---|---|---|---|---|---|
| `CLUSTARR_EVENTS` | `clustarr.evt.>` | Limits | 168h | 2 GiB | 10m | File, R3, DenyDelete, S2 compression |
| `CLUSTARR_RELEASES` | `clustarr.rel.>` | Limits | 72h | 4 GiB | 2h | File, R3 |
| `CLUSTARR_WORK_CATALOGARR` | `clustarr.work.catalogarr.>` | WorkQueue | – | 1 GiB | 1h | DiscardNew, **AllowMsgSchedules** |
| `CLUSTARR_WORK_INDEXARR` | `clustarr.work.indexarr.>` | WorkQueue | – | 256 MiB | 1h | DiscardNew, **AllowMsgSchedules** |
| `CLUSTARR_WORK_CAPTIONARR` | `clustarr.work.captionarr.>` | WorkQueue | – | 256 MiB | 1h | DiscardNew, **AllowMsgSchedules** |
| `CLUSTARR_DLQ` | `clustarr.dlq.>` | Limits | 720h | 1 GiB | 10m | File, R3 |

WorkQueue retention is immutable: `natsbus.Ensure` refuses to start if an existing stream's retention differs (operator must migrate). grabarr and squasharr have no work stream.

**Subjects**

| Subject | Payload schema | Producer → consumer |
|---|---|---|
| `clustarr.evt.catalog.<kind>.added\|updated\|deleted.<uid>` | `catalog.ItemEvent.v1` | catalogarr → history |
| `clustarr.evt.catalog.release.grabbed\|rejected.<target-uid>` | `catalog.ReleaseEvent.v1` (guid, indexer, releaseGroup, downloadRef, rejections) | catalogarr → history, indexarr (grab accounting) |
| `clustarr.evt.catalog.mediafile.imported\|replaced\|deleted.<uid>` | `catalog.MediaFileEvent.v1` (droppedPath, importedPath, reason manual\|missingFromDisk\|upgrade) | catalogarr → history |
| `clustarr.evt.catalog.importlist.synced.<uid>` | `catalog.ImportListSynced.v1` | catalogarr → history |
| `clustarr.rel.<protocol>.<indexerName>.<newznabTop>` | `index.Release.v1` (common.ReleaseInfo + parsed) | indexarr → catalogarr-rss-matcher |
| `clustarr.evt.index.indexer.disabled\|recovered\|limited.<uid>` | `index.IndexerEvent.v1` | indexarr → history |
| `clustarr.evt.download.download.queued\|started\|completed\|seedGoalMet\|imported\|failed\|blocklisted\|removed.<uid>` | `download.DownloadEvent.v1` | grabarr → history |
| `clustarr.evt.transcode.job.queued\|started\|succeeded\|failed\|skipped.<uid>` | `transcode.JobEvent.v1` | squasharr → history |
| `clustarr.evt.subtitle.subtitle.downloaded\|upgraded\|failed.<request-uid>` | `subtitle.SubtitleEvent.v1` | captionarr → history |
| `clustarr.work.catalogarr.search.<high\|normal\|low>.<mediaKey>` | `catalog.SearchTask.v1` {mediaRef, keys, reason add\|missing\|cutoffUnmet\|interactive\|redownload, searchRef, userInvoked} | catalogarr ctrl → search workers |
| `clustarr.work.catalogarr.grab.normal.<mediaKey>` (scheduled) | `catalog.GrabTask.v1` {mediaRef, keys} | search worker/matcher → grab worker |
| `clustarr.work.catalogarr.import.normal.<download-uid>` | `catalog.ImportTask.v1` {downloadRef} | importer ctrl → import workers |
| `clustarr.work.catalogarr.metadata.<high\|normal>.<mediaKey>` | `catalog.MetadataTask.v1` {mediaRef, refreshEpoch} | ctrl → metadata gateway |
| `clustarr.work.catalogarr.importlist.normal.<uid>` | `catalog.ImportListTask.v1` | ctrl → importlist worker |
| `clustarr.work.catalogarr.wantedscan.low.<namespace>` | `catalog.WantedScan.v1` | cron → search workers |
| `clustarr.work.indexarr.rss.normal.<indexer-uid>` (scheduled) | `index.RssTask.v1` | indexarr ctrl → rss worker |
| `clustarr.work.indexarr.definitions.normal.sync` | `index.DefinitionsSync.v1` | ctrl → worker |
| `clustarr.work.captionarr.fetch.<high\|normal\|low>.<request-uid>.<langKey>` | `subtitle.FetchTask.v1` {requestRef, langKey, minScore, upgrade bool, probeHash} | captionarr ctrl → fetch workers |
| `clustarr.work.captionarr.sync\|generate.normal.<request-uid>.<langKey>` | deferred | |
| `clustarr.rpc.indexarr.search` | `index.SearchRequest.v1` → `index.SearchResponse.v1` {releases ≤500, outcomes[]} | catalogarr → indexarr (micro, queue group `indexarr`, **single reply** at min(deadline, 45s); still-running indexers reported `timeout`) |
| `clustarr.rpc.indexarr.download` | {indexerRef, guid, url} → {bytes\|magnetURL\|redirectURL, contentType} | grabarr → indexarr (uses session cookies/passkeys, counts GrabLimit) |
| `clustarr.rpc.indexarr.query` | {text, filters} → releases | Search CR (query mode), Torznab facade → indexarr SQLite index |
| `clustarr.rpc.catalogarr.metadata.lookup\|search\|resolve` | metadata request/response | catalogarr workers, import lists → metadata gateway |
| `clustarr.progress.download.<uid>`, `clustarr.progress.transcode.<uid>` | core NATS, 1 Hz, not persisted | engines/workers → optional UIs |
| `clustarr.dlq.<service>.<task>.<id>` | original payload + headers `Clustarr-DLQ-Reason`, `-Attempts`, `-Consumer`, `-Subject` | pkg/events → DLQ projector |

**Durable pull consumers** (AckExplicit; every entry satisfies MaxDeliver > len(BackOff); BackOff covers AckWait expiry only, so `pkg/events` translates handler errors into `NakWithDelay(backoff[attempt])`):

| Consumer | Stream | FilterSubjects | AckWait | MaxDeliver | BackOff | MaxAckPending | Heartbeat |
|---|---|---|---|---|---|---|---|
| `catalogarr-rss-matcher` | RELEASES | `clustarr.rel.>` | 30s | 6 | 1s,5s,30s,2m,10m | 256 | – |
| `catalogarr-search-high` | WORK_CATALOGARR | `…search.high.>` | 120s | 5 | 30s,2m,10m | 8 | – |
| `catalogarr-search-normal` | WORK_CATALOGARR | `…search.normal.>`, `…search.low.>`, `…wantedscan.>` | 120s | 5 | 30s,2m,10m,1h | 8 | – |
| `catalogarr-grab` | WORK_CATALOGARR | `…grab.>` | 60s | 5 | 10s,1m,5m | 16 | – |
| `catalogarr-import` | WORK_CATALOGARR | `…import.>` | 300s | 5 | 30s,2m,10m | 4 | 60s |
| `catalogarr-metadata` | WORK_CATALOGARR | `…metadata.>` | 60s | 8 | 30s,2m,10m,1h,6h | 32 | – |
| `catalogarr-importlist` | WORK_CATALOGARR | `…importlist.>` | 300s | 4 | 5m,30m,2h | 2 | 60s |
| `catalogarr-history` | EVENTS | `clustarr.evt.>` | 30s | 3 | 5s,30s | 512 | – |
| `indexarr-rss` | WORK_INDEXARR | `…rss.>` | 120s | 4 | 1m,5m,15m | 4 | – |
| `indexarr-definitions` | WORK_INDEXARR | `…definitions.>` | 300s | 3 | 5m,30m | 1 | – |
| `captionarr-fetch-high` | WORK_CAPTIONARR | `…fetch.high.>` | 90s | 8 | 30s,2m,10m,1h,6h | 16 | – |
| `captionarr-fetch-normal` | WORK_CAPTIONARR | `…fetch.normal.>`, `…fetch.low.>` | 90s | 8 | 30s,2m,10m,1h,6h | 16 | – |
| `clustarr-dlq-projector` | DLQ | `clustarr.dlq.>` | 30s | 3 | 5s,30s | 64 | – |

DLQ path: handler returns `events.Discard` → copy to `clustarr.dlq.*` → `TermWithReason`; a core-NATS subscription on `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.>` (leader in catalogarr) does `GetMsg → publish to DLQ → DeleteMsg`. The DLQ projector sets condition `Failed` (reason `DeadLettered`) on the CR named by `Clustarr-Key` and emits a Kubernetes Event; `kubectl annotate <cr> clustarr.io/replay=<dlq-seq>` republishes with a fresh Msg-Id.

**KV buckets** (names cannot contain dots)

| Bucket | Keys | TTL | Use |
|---|---|---|---|
| `clustarr-leases` | `grab.<mediaKey>` → Download name | none (deleted by Download controller on terminal phase; 10-min sweeper deletes keys whose Download no longer exists) | double-grab guard (Create-fails-if-exists) |
| `clustarr-pending` | `grab.<mediaKey>` → best candidate ReleaseInfo (CAS keep-best) | 7d | delay profiles |
| `clustarr-indexer-sessions` | `<indexer-uid>` cookies/JWT | 30d | Cardigann logins (mirrored into owned Secret) |
| `clustarr-indexer-limits` | `<indexer-uid>.query`, `.grab` timestamp rings (CAS) | 2d | QueryLimit/GrabLimit windows |
| `clustarr-provider-throttle` | `<provider-uid>` {until, reason, quota} | 24h | Bazarr throttle table |
| `clustarr-metadata-cache` | `<provider>.<kind>.<id>` JSON with `expiresAt` | 30d bucket, per-entry expiry checked by gateway | L2 metadata cache |
| `clustarr-search-cache` | `<indexer-uid>.<sha256(request)>` | 35m | Jackett-style raw-result cache |
| `clustarr-progress` | `download.<uid>`, `transcode.<uid>` | 10m | 1 Hz telemetry; controllers throttle into status ≤ every 10 s |
| `clustarr-importlist` | `<list-uid>` item list | 7d | keeps items out of status |
| `clustarr-dedup` | import fingerprints `sha1(path\|size\|mtime)` | 24h | re-import no-op |

## 6. Services

### 6.1 catalogarr (`catalogarr/`, `clustarr catalogarr --role controller|worker|metadata|history|all`)
Owns catalog.clustarr.io. **Controllers:** `movie`, `series` (creates/owns Episodes), `episode`, `artist` (owns Albums), `album`, `author` (owns Books), `book`, `audiobook`, `comic` (owns Issues), `issue`, `mediafile` (probe, labels, sidecar scan, transcode follow-up), `rootfolder`, `qualityprofile` (installs 12 built-ins, validates slugs/conflicts), `delayprofile`, `metadataprovider`, `importlist`, `importexclusion`, `search`, `importer` (Watches Downloads: `Completed` → import task; `Failed` → redownload search), `wantedcron` (12h missing/cutoff-unmet scan with per-item ≥6h gap and `Attempts` backoff 6h·2^n capped 7d). **Workers (every replica):** search (RPC + decide + lease + pending/grab), grab (scheduled), rss-matcher (informer-backed in-memory map tmdb/tvdb/imdb/mbid/normalizedTitle+year → monitored items), import (CompletedDownloadService port), importlist (trakt, plex, tmdb, mdblist, stevenlu, imdbCSV, custom, arr). **Metadata gateway (1 replica):** `pkg/metadata` registry with all clients, `rate.Limiter` per provider, otter L1 + KV L2, TTL by entity state (Radarr/Sonarr refresh heuristics), serves `rpc.catalogarr.metadata.*`. **History:** EVENTS → events.k8s.io Events on the owning CR; DLQ sweeper + projector.

### 6.2 indexarr (`indexarr/`, `clustarr indexarr --role all`, exactly one replica)
**Controllers:** `indexer` (validate definition/generic, probe caps, login test, owned session Secret, schedule RSS via `WithScheduleAt` on `work.indexarr.rss`, mirror KV health/limits into status with Prowlarr's escalation table `[0,60,300,900,1800,3600,10800,21600,43200,86400]s` + 15-min startup grace), `indexerdefinition` (schema validation, sha256), `indexerproxy`. **Search service:** NATS micro on `rpc.indexarr.search`: select Indexers (enabled, `EnableAutomaticSearch` or interactive per request, caps support the mode/categories, not `DisabledUntil`, under QueryLimit), errgroup with per-indexer `context.WithTimeout(spec.timeout)`, Cardigann engine or generic Torznab/Newznab client (id params gated by caps; fallback `t=search&q=`), parse titles with `pkg/release`, upsert every hit into SQLite, dedup by infohash then (indexer, guid) keeping best (priority, seeders) with `alsoOn` provenance, cap 500, record outcomes and `RecordSuccess/RecordFailure`. `rpc.indexarr.download` resolves links with the indexer session. **RSS worker:** `t=search` empty q per Indexer, insert new rows (UNIQUE(indexer, guid)), publish each new row to `CLUSTARR_RELEASES`. **Release index:** `indexarr/releaseindex.Store` interface; SQLite (modernc.org/sqlite, WAL, FTS5 on `title_norm, grp`) on PVC `clustarr-index` (RWO, 5Gi), columns from `pkg/release` (source, resolution, modifier, codec, hdr, audio, languages, group, year, season, episode, ids), `expires_at` sweep every 10 min (72h), readiness tied to the DB open; Postgres FTS is a Store swap. **Facade:** `/{indexer}/api`, `/{indexer}/download`, `/search/api` (aggregate Torznab, Jackett filter grammar deferred).

### 6.3 grabarr (`grabarr/`, `clustarr grabarr --role controller|torrent-engine|usenet-engine`)
**Controllers:** `downloadclient` → owned StatefulSet (torrent; `spec.replicas` ordinals, `/data` mounted, hostPort/Service for ListenPort) or Deployment (usenet; scratch volume); `DiskSpaceOK` via statfs; blocklist sweeper. `download`: pick `ClientRef` (enabled, protocol, lowest priority number) once; wait `EngineReady`; **engine = `<client>-<hash(infoHash|guid) mod spec.replicas>`** computed from spec.replicas (not ready count), written once to `status.engine` + label; reassign only when the ordinal ≥ current `spec.replicas` and the old pod is gone; `Phase=Assigned`; `evt.download.queued`; delete the grab lease on terminal phase; finalizer (`Drop`, `<-Closed()`, `os.RemoveAll` when `RemoveDataOnDelete`). **Torrent engine pod:** informer filtered by its engine label; on start re-attaches every labelled Download from persisted metainfo (`/data/torrents/.state/<infohash>.torrent`) and `.part` files **before** accepting new adds (readiness gate); `AddTorrentOpt` with `storage.NewFileOpts{ClientBaseDir: /data/torrents/<category>/<download>}`; `Seed=true`, `NoDefaultPortForwarding`, non-nil rate limiters, `SetOnWriteChunkError` → `writeError`; polls `Stats()` every 5 s, SSA telemetry every ≤10 s; `Complete()` → `Completed` (`CanMoveFiles=false`); seed criteria on persisted cumulative counters → `DisallowDataUpload()`, `CanBeRemoved=true`, `SeedGoalMet`. **Usenet engine pod:** `nzbparser` → `/scratch/<download>/` sparse `.part` files written at yEnc offsets via `nntppool/v4` (one pool per provider, `Backup` fills), PAR2 16k-hash renames, crc32_combine quick-check, `par2` exec repair only when needed, `rardecode/v2` + `sevenzip` extract, cleanup, copy into `/data/usenet/<category>/_UNPACK_<download>` then atomic rename → `Completed` (`CanMoveFiles=true`); NZBGet-style health; `missingArticles`/`encrypted`/`diskFull` failures.

### 6.4 squasharr (`squasharr/`, `clustarr squasharr --role controller|worker`)
`transcodeprofile`: `Watches(&MediaFile{})` mapped to profiles whose selector matches labels (default when none), predicate = `status.probeHash` changed; compliance = hevc + main10 + yuv420p10le + AAC-only audio + container + `CLUSTARR_PROFILE=<name>@<hash>` tag; creates `TranscodeJob` (create-if-absent by deterministic name). `transcodejob`: Pending → Planned (`pkg/transcode.Planner`) → Queued (Job created **suspended**) → admission by priority against slot budgets `--slots cpu=2,nvidia=1,intel=1` and per-profile counts → Running (Job unsuspended) → mirror Job conditions (Owns) → Succeeded/Failed; TTL cleanup. Job spec: `restartPolicy Never`, `backoffLimit 2`, `podFailurePolicy` (Ignore on DisruptionTarget; FailJob on exit codes 3,4), `podReplacementPolicy Failed`, `activeDeadlineSeconds`, `ttlSecondsAfterFinished`, `/data` RWX + emptyDir scratch, Downward API `limits.cpu` → `CLUSTARR_CPU_LIMIT`, GPU: `nvidia.com/gpu`, `runtimeClassName`, `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`, affinity `nvidia.com/gpu.present=true`; Intel: `gpu.intel.com/i915|xe`, affinity `intel.feature.node.kubernetes.io/gpu=true`, supplementalGroups render. **Worker:** probe (reuse MediaFile info + own ffprobe for DOVI/frame side data), `ffmpeg -progress pipe:1 -stats_period 1 -nostats -loglevel error`, `pools=<cpu limit>`, writes `<stem>.part.mkv`, verifies (`-count_packets` equality, ±1 frame video / ±100 ms audio, optional full decode / libvmaf), tags `CLUSTARR_PROFILE`, atomic rename over the source path (source → recycle bin), exit codes 0 ok / 2 retriable / 3 invalid source / 4 verification failed. Transcoding replaces the library hardlink only; a seeding copy in `/data/torrents` is untouched.

### 6.5 captionarr (`captionarr/`, `clustarr captionarr --role controller|worker`)
`subtitleprofile` watches MediaFiles (video kinds) → ensures one `SubtitleRequest` per file. `subtitlerequest`: replan when `status.profileGeneration`/`status.probeHash` stale: existing = embedded text streams (skip bitmap per profile and commentary) + sidecars parsed right-to-left; wanted = Bazarr planner; for items `nextSearchAt ≤ now` publish fetch task (`Msg-Id <uid>/<langKey>/<probeHash>`), state `searching`; adaptive gate (`initial+3w > now` full cadence, else `latest+1w ≤ now`), 12h upgrade pass (`score < outOf−3`, `minScore=score+1`). `subtitleprovider` mirrors KV throttle/quota into status. **Workers:** load MediaFile + owner metadata (ids, title, year, season/episode), verify path/size/mtime == probeHash (else `Retry(5m)`), moviehash if ≥128 KiB, iterate providers by priority skipping throttled; each provider is a gateway-less in-process client with a **shared KV token bucket** (`clustarr-provider-throttle` holds JWT + `remaining/reset`) so N workers never exceed 5 req/s; score with Bazarr weights; filter must/mustNot; download with fall-through; post-process (BOM → language encodings → chardet → UTF-8; mods via regexp2; go-astisub → SRT unless originalFormat); write `<stem>.<lang>[.forced|.sdh].srt` via temp+rename; SSA `items[langKey]`; publish `subtitle.downloaded`. Errors map to Bazarr durations (TooManyRequests 1h [OS.com 1m], DownloadLimitExceeded 3h [OS.com 6h], ServiceUnavailable 20m, APIThrottled 10m, Parse 6h, Timeout 1h, Auth/Config 12h) + 5-errors-in-120s.

## 7. Shared packages (`pkg/`)

```go
// pkg/events
type Envelope struct { ID, Type, Schema, Source, Key string; Time time.Time; Headers map[string]string; Data []byte }
type Message interface { Envelope() *Envelope; Subject() string; Attempt() uint64; Ack(ctx) error; Nak(ctx, delay time.Duration) error; Term(ctx, reason string) error; Heartbeat(ctx) error }
type Handler func(ctx context.Context, m Message) error
func Retry(after time.Duration, err error) error; func Discard(reason string, err error) error
type PublishOption func(*publishOpts) // WithMsgID, WithScheduleAt(t), WithExpectStream(s)
type Publisher interface { Publish(ctx, subject string, e *Envelope, opts ...PublishOption) (Receipt, error) } // Receipt{Stream string; Seq uint64; Duplicate bool}; ErrQueueFull on DiscardNew
type Subscription struct { Stream, Durable string; Filters []string; AckWait time.Duration; MaxDeliver int; Backoff []time.Duration; MaxInFlight int; Heartbeat time.Duration }
type Subscriber interface { Subscribe(ctx, Subscription, Handler) (Stop func(), err error) }
type Requester interface { Request(ctx, subject string, in, out any) error; Serve(subject, queue string, h func(ctx, []byte) ([]byte, error)) error }
type KV interface { Get(ctx, key) (Entry, error); Create(ctx, key, val []byte, opts ...KVOption) (uint64, error); Update(ctx, key, val []byte, rev uint64) (uint64, error); Put(ctx, key, val) (uint64, error); Delete(ctx, key) error; Watch(ctx, pattern string) (<-chan Entry, error) }
type Bus interface { Publisher; Subscriber; Requester; KV(bucket string) KV; Ensure(ctx, Topology) error; Close() error }
// natsbus.New(nc *nats.Conn, opts) Bus; membus.New(clock clockwork.Clock) Bus; contracttest.Run(t, func() Bus)
// k8sbridge.Source[T client.Object](sub Subscriber, s Subscription, key func(*Envelope) types.NamespacedName) source.Source
// k8sbridge.PublishFromReconcile(ctx, bus, obj client.Object, task string, payload any) error // Msg-Id <uid>:<generation>:<task>

// pkg/quality
type Definition struct { Quality common.Quality; Name string; Aliases []string; Weight int; MinMBPerMin, PrefMBPerMin, MaxMBPerMin float64 }
func Lookup(kind, name string) (Definition, bool)               // accepts Radarr and Sonarr names
type Profile struct { Tiers [][]Definition; CutoffIndex int; UpgradeAllowed bool; MinFormatScore, CutoffFormatScore, MinUpgradeFormatScore int; Scores map[string]int; Language string; ProperPolicy string; Sizes map[string]SizeLimit; Hash string }
func FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (Profile, []error)
func (p Profile) Index(q common.Quality) (idx int, ok bool)
func SizeLimits(p Profile, q common.Quality, runtimeMin int) (min, max int64)
// pkg/quality/catalogue (generated by hack/gen-catalogue from testdata/trash)
type CondKind string // ReleaseTitle ReleaseGroup Source Resolution Modifier Language IndexerFlag ReleaseType (Edition Size Year = no-op)
type Condition struct { Kind CondKind; Name string; Negate, Required bool; Pattern *regexp2.Regexp; Source common.Source; Resolution int32; Modifier common.Modifier; Language string; ExceptLanguage bool; Flag string; ReleaseType common.ReleaseType }
type Format struct { Slug, Name string; TrashIDs map[string]string; Scores map[string]int /* default anime-radarr anime-sonarr */; Group string; Conditions []Condition }
type Catalogue struct { Version string; Formats map[string]*Format; Conflicts [][2]string }
type ItemContext struct { OriginalLanguage string; IndexerFlags []string; ReleaseType common.ReleaseType }
func (c *Catalogue) Match(r *release.ParsedRelease, ic ItemContext) []string // exact *arr group semantics
func (c *Catalogue) Score(p *Profile, r *release.ParsedRelease, ic ItemContext) (score int, matched []string)

// pkg/release
type ParsedRelease struct { Title string; Titles []string; Year int; Quality common.Quality; Revision common.Revision; Languages []string; Group, Hash, Edition string; Seasons, Episodes, Absolute []int; AirDate *time.Time; FullSeason, Partial, MultiSeason, Special bool; ReleaseType common.ReleaseType; Music *MusicInfo; Book *BookInfo; Comic *ComicInfo; Hints Hints /* Codec, HDR, Audio, Channels, Streaming, Container []string */; IDs map[string]string }
type Options struct { Kind common.MediaKind; SeriesType string }
func Parse(title string, o Options) (*ParsedRelease, error); func ParsePath(path string, o Options) (*ParsedRelease, error)
func ClassifyKind(title string) common.MediaKind
func MatchTitle(p *ParsedRelease, cands []TitleCandidate) (best int, score float64) // fuzzysearch + year

// pkg/decision
type Target struct { Kind common.MediaKind; Key string; Monitored, Available bool; RuntimeMinutes int; EpisodeRuntimes []int; OriginalLanguage string; Current *Current; Queue []Queued; Blocklist func(infohash, title string) bool; FreeBytes int64 }
type Current struct { Quality common.Quality; Revision common.Revision; FormatScore int; Formats []string }
type Options struct { UserInvoked bool; ProtocolsEnabled map[string]bool; IndexerPriority map[string]int; PreferredProtocol string }
type Decision struct { Release common.ReleaseInfo; Parsed *release.ParsedRelease; Approved, TemporarilyRejected bool; Rejections []common.Rejection; Score int; Matched []string; Rank RankKey }
func Evaluate(ctx, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision
func Rank(ds []Decision, o Options) []Decision // quality idx+revision → CF score → protocol pref → episode count → indexer priority → flags → seeders/age → |size−preferred|
func Upgradable(p quality.Profile, cur *Current, cand Current, proper string) (ok bool, reason string) // verbatim UpgradableSpecification table

// pkg/download
type Status string // Queued Paused Downloading Completed Failed Warning
type Item struct { ID string; Status Status; Stage, FailureReason string; Progress float64; TotalBytes, RemainingBytes, UploadedBytes, DownloadedBytes int64; DownRate, UpRate int64; ETA *time.Duration; Ratio float64; SeedTime time.Duration; Seeders, Peers int; Health *int; OutputPath, ContentRoot string; Files []File; CanMoveFiles, CanBeRemoved, IsEncrypted bool; Message string }
type Client interface { Info(ctx) (Info, error); Add(ctx, AddRequest) (string, error); Get(ctx, id string) (Item, error); List(ctx) ([]Item, error); Pause(ctx, id) error; Resume(ctx, id) error; SetSeedCriteria(ctx, id, common.SeedCriteria) error; MarkImported(ctx, id) error; Remove(ctx, id string, deleteData bool) error; Close() error }
func ApplyStatus(item Item) *downloadac.DownloadStatusApplyConfiguration // grabarr-engine fields only

// pkg/mediainfo
func Probe(ctx, path string) (*common.MediaInfo, *Raw, error) // 2 ffprobe calls; Raw keeps DOVI record + MDCV/CLL
func ClassifyHDR(raw *Raw) common.HdrFormat; func ProbeHash(path string, size int64, mtime time.Time) string; func MovieHash(path string) (string, error)

// pkg/transcode
type Plan struct { Mode string; SkipReason string; Encoder string; Input, Output string; VideoArgs, Filters, Maps []string; Audio []AudioPlan; Subtitles []int; Attachments bool; HDR HDRParams; Verify VerifySpec }
type Planner struct { Profile transcodev1alpha1.TranscodeProfileSpec }
func (p Planner) Plan(mi *common.MediaInfo, raw *mediainfo.Raw, hw string) (Plan, error)
type Runner struct { FFmpeg string }; func (r Runner) Run(ctx, Plan, progress func(Progress)) error
type Verifier struct { FFprobe, FFmpeg string }; func (v Verifier) Verify(ctx, src, out string, spec VerifySpec) (Report, error)
func X265Params(p Plan, cpuLimit int) string // golden-tested
func ProfileHash(spec transcodev1alpha1.TranscodeProfileSpec) string

// pkg/subtitles
type LangKey string // "en" "en:forced" "en:hi"
func Plan(profile subtitlev1alpha1.SubtitleProfileSpec, audioLangs []string, existing []Existing) (wanted []LangKey, cutoffMet bool)
type Query struct { Kind common.MediaKind; Title string; Year int; IDs map[string]string; Season, Episode int; Hash string; SizeBytes int64; Release *release.ParsedRelease; Languages []LangKey }
type Candidate struct { Provider, ID, Language string; HI, Forced bool; ReleaseInfo string; Matches map[string]bool; Score, ScoreWithoutHash int }
type Provider interface { Name() string; Search(ctx, Query) ([]Candidate, error); Download(ctx, Candidate) ([]byte, string, error); HIVerifiable() bool }
type ProviderError struct { Kind string; RetryAfter time.Duration } // TooManyRequests DownloadLimit ServiceUnavailable APIThrottled Parse Timeout Auth Config
func Score(kind common.MediaKind, matches map[string]bool) (score, without int); var MaxScore = map[common.MediaKind]int{"episode": 360, "movie": 180}
func PostProcess(raw []byte, lang string, mods []string, toSRT bool) ([]byte, error)
func SidecarName(videoPath string, key LangKey, hiExt string) string; func ParseSidecar(videoStem, name string) (LangKey, bool)
func ThrottleFor(provider string, err error) (reason string, d time.Duration)

// pkg/metadata
type ExternalIDs map[string]string; func (e ExternalIDs) Merge(o ExternalIDs) ExternalIDs
type MovieProvider interface { Movie(ctx, tmdbID string, region string) (*Movie, error); FindMovie(ctx, ExternalIDs) (*Movie, error); SearchMovies(ctx, q string, year int) ([]MovieHit, error) }
type SeriesProvider interface { Series(ctx, tvdbID string) (*Series, error); Episodes(ctx, tvdbID, order string) ([]Episode, error); Updates(ctx, since time.Time) ([]string, error) }
type ArtistProvider, BookProvider, AudiobookProvider, ComicProvider, ArtworkProvider, IDResolver interface{ /* per note */ }
type Registry struct{ /* providers by kind+priority, first-wins merge */ }; func (r *Registry) Lookup(ctx, kind common.MediaKind, ids ExternalIDs) (any, error)
type Cache interface { Get(ctx, key string, out any) (bool, error); Set(ctx, key string, v any, ttl time.Duration) error }
func RefreshTTL(kind common.MediaKind, state string, lastRefreshed time.Time) time.Duration // ported *arr heuristics

// pkg/naming
type Engine struct { Dialect string; Colon string; MultiEpisode string; Overrides map[string]string }
func (e Engine) Render(template string, ctx Context) (string, error) // {Movie CleanTitle} {season:00} {absolute:000} {[Quality Full]} {-Release Group} {MediaInfo VideoDynamicRangeType} ...
func (e Engine) MovieFolder/MovieFile/SeriesFolder/SeasonFolder/EpisodeFile/TrackFile/BookFile/AudiobookFile/IssueFile(ctx Context) (string, error)

// pkg/fsops
func HardlinkOrCopy(src, dst string) (linked bool, err error); func MoveAtomic(src, dst string) error; func AtomicWrite(path string, r io.Reader, mode os.FileMode) error
func Recycle(root, path string) (string, error); func FreeBytes(path string) (int64, error); func IsPart/IsSample/IsExtra(...) bool

// pkg/k8s
func PatchStatus(ctx, c client.Client, obj client.Object, apply any, manager string) error // SSA, force=true, subresource status
func SetCondition(conds *[]metav1.Condition, t string, s metav1.ConditionStatus, reason, msg string, gen int64)
func EnsureFinalizer(ctx, c, obj, name) (added bool, err error) // caller must not return early after add
func TTL(finishedAt *metav1.Time, ttl time.Duration) (expired bool, requeue ctrl.Result)
func MapRef(kind schema.GroupVersionKind, field string) handler.MapFunc; func ProbeHashChanged() predicate.Predicate; func GenerationOrFinalizer() predicate.Predicate
```
Also: `pkg/cardigann` (Definition, Load, Validate, Engine{Caps, Login, Search, Download}, 25 filters, .NET→Go date translator, GetBytes), `pkg/torznab` (Query, Caps, Item, Client, WriteCaps/WriteResults/WriteError, codes 100–910/410/429), `pkg/newznab` (category table, `Expand`, `Parent`, `Custom(trackerID)`, `ByKind`: movie 2000*, tv 5000*, anime 5070, music 3000*, audiobook 3030, book 7020, comic/manga 7030), `pkg/importlist` (List, Item, DeviceAuth, Dedupe tmdb→imdb→tvdb→title+year, ApplySyncLevel), `pkg/ratelimit` (per-provider limiter table + KV token bucket).

## 8. Data flows

**8.1 Want.** Movie created (user/GitOps/ImportList). Movie reconciler: finalizer (no early return) → apply `addOptions` once (`status.addOptionsApplied`) → if metadata missing/stale per `RefreshTTL` publish `work.catalogarr.metadata.normal.<mediaKey>` (Msg-Id `<uid>:<refreshEpoch>:metadata`), `MetadataReady=False`; gateway patches `status.metadata` (SSA `catalogarr-metadata`, only `status.metadata`) → reconcile computes `Available` with Radarr `IsAvailable(minimumAvailability, delay)`, `Path` via `pkg/naming`; `Phase=Unavailable` + `RequeueAfter(availableAt)` or `Wanted`. Series additionally creates/owns Episodes from `/series/{id}/episodes/{order}` (absolute for anime) and applies the monitor mode once. Publish failure with `ErrQueueFull` → condition `QueueFull=True`, requeue 1m.

**8.2 Search → decide → delay → grab.** Trigger: `searchOnAdd`, Available transition, annotation `catalog.clustarr.io/search=now`, wanted cron, redownload, or a `Search` CR. Controller publishes `work.catalogarr.search.<tier>.<mediaKey>` (high = interactive, normal = add/redownload, low = cron). Search worker: snapshot item + profile + current MediaFile + Downloads (queue + blocklist informers) → `rpc.indexarr.search` (ids tmdb/imdb or tvdb+season+ep/absolute; categories by kind; deadline 45 s) → `decision.Evaluate` (protocol enabled, availability unless `UserInvoked`, size MB/min×runtime with 110/45-min fallbacks and summed episode runtimes for packs, quality in profile, MinFormatScore, language, sample, blocklist, already imported by hash/name, queue higher/equal preference, `Upgradable` table) → `Rank`. Search CR: write ≤200 results, `Completed`. Automatic: best approved → **DelayProfile** resolution (item ref → tag match → lowest order): if `delay(protocol) > 0` and no bypass (`BypassIfHighestQuality` and quality index == top tier; or `BypassIfAboveFormatScore` and score ≥ min): CAS keep-best into `clustarr-pending grab.<mediaKey>`, publish `work.catalogarr.grab.normal.<mediaKey>` with `WithScheduleAt(firstSeen+delay)` (Msg-Id `<mediaKey>:<firstSeen>`), set `PendingGrab`, `Phase=Delayed`; else grab now. **Grab (worker or scheduled consumer):** take lease `Create("grab.<mediaKey>" → downloadName)` for every key (episodes of a pack; all-or-nothing, release taken ones on failure); on exists → ack and stop; re-read item `status.activeDownloadRef` under optimistic lock; create `Download{name <target>-<sha1(guid)[:10]>, source (indexerDownload when the indexer is authenticated), release, target, qualityProfileRef, seedCriteria from Indexer, grabbedBy}` with ownerRef; set `activeDownloadRef`; publish `release.grabbed`. Search-CR grabs: user sets `spec.grab=[guid]`; controller creates Downloads (`Override` required for Permanent rejections), fills `status.grabbed`.

**8.3 Download.** As §6.3. `grabarr` records the grab against `clustarr-indexer-limits` via `evt.catalog.release.grabbed` consumed by indexarr. `Failed` → `Blocklisted` (label + `blocklistedUntil`), `evt.download.failed`; catalogarr importer watch clears `activeDownloadRef`, deletes the lease, publishes a `redownload` search.

**8.4 Import.** Importer controller watches Downloads (`Completed|Seeding`, `status.import.state != imported`) → publishes `work.catalogarr.import.normal.<uid>`. Import worker: lease `clustarr-dedup import.<uid>`; verify `ContentRoot` exists; enumerate ignoring `*.part`/samples/extras; `release.ParsePath` per file (fallback release title), map to Movie/Episodes (scene numbering honoured); import specs (target match, not sample, free space ≥ RootFolder.minFreeBytes, `Upgradable` vs current unless `Manual`); `pkg/mediainfo.Probe`; destination via naming preset; `HardlinkOrCopy` when `!CanMoveFiles` else `MoveAtomic`; old file → recycle bin, old MediaFile deleted; create `MediaFile` (quality/revision/formatScore/matchedFormats/releaseType frozen); MediaFile reconciler probes, sets labels, `probeHash`, `Probed`; item `HasFile`, `CutoffMet`, `Phase=Imported|CutoffUnmet`, `activeDownloadRef=nil`; SSA `Download.status.import` (manager `catalogarr`); grabarr sees `imported` → `MarkImported`, deletes on `SeedGoalMet` when `RemoveOnImport`. Blocked imports: `status.import.state=blocked` + message; override via Download annotations `catalog.clustarr.io/import-target=<kind>/<name>[/<key>]` and `catalog.clustarr.io/import-override=true`.

**8.5 Transcode.** Ordering is strict: MediaFile `probeHash` set → squasharr creates TranscodeJob → Job → worker swaps file at the **same path** → `Succeeded` → catalogarr MediaFile reconciler (Watches TranscodeJob mapped by `spec.mediaFileRef`) sets `spec.sizeBytes/modTime`, `Original=false`, re-probes, bumps `probeHash`, `status.transcode.compliant=true` → profile reconciler sees compliance; captionarr replans on the new `probeHash`. Between swap and re-probe captionarr workers detect the mtime mismatch and `Retry`. Quality/formatScore never change (release-time values), so `x265 (HD)` -10000 cannot cause a spurious upgrade.

**8.6 Subtitles.** As §6.5. MediaFile reconciler Watches SubtitleRequests (mapped by `spec.mediaFileRef`) and rescans sidecars into `status.sidecars` — this is the sidecar feedback path.

**8.7 RSS, lists, upgrades.** indexarr schedules `work.indexarr.rss` per Indexer; new rows → `CLUSTARR_RELEASES` (Msg-Id `sha1(indexer:guid)`, 2h Duplicates). rss-matcher maps each release to monitored items (ids or normalized title+year via informer index) and runs the same `Evaluate` for the single release; approved → the same delay/lease/grab path (pending CAS keep-best updates a delayed grab). Wanted cron republishes missing/cutoff-unmet searches at low tier. ImportList controller schedules sync tasks; worker fetches (Trakt device flow: `status.auth.userCode`, token pair rotated atomically in the owned Secret; Plex Discover; TMDB; MDBList; StevenLu; CSV; custom; arr), dedupes, drops `ImportExclusion`s, resolves ids via the gateway, creates items with `Source.ImportListRef` and list defaults (no ownerRef), applies `SyncLevel` to items absent from every enabled list.

**8.8 Failure handling.** Controllers: `RequeueAfter` only, `reconcile.TerminalError` for invalid specs, `RecoverPanic`, `ReconciliationTimeout 5m`, conditions with `observedGeneration`, events via `mgr.GetEventRecorder`. Workers: `Retry(after)` → `NakWithDelay`; generic error → `NakWithDelay(backoff[attempt])`; `Discard` → DLQ; long tasks heartbeat. NATS outage: readiness fails (JetStream ping), controllers keep reconciling status-only paths, publishes requeue (dedup absorbs replays). Disk: statfs guards before assignment, import and Job creation (`DiskSpaceOK`). Engine restart: re-attach gate. Poison: DLQ projector → `Failed` condition + Event.

## 9. Quality model

Identity = `common.Quality` tuple for video with the canonical Radarr definition table (weights Unknown=1 … Remux-2160p=24, BR-DISK=25, Raw-HD=26) and Sonarr aliases (`Bluray-1080p Remux` ⇔ `Remux-1080p`); *arr numeric ids never persisted. Non-video tables: music = Lidarr 38 values in tiers (Trash/Poor/Low/Mid/High lossy, Lossless, 24-bit, WAV); book = PDF < MOBI < EPUB < AZW3; audiobook = Unknown Audio < MP3 < M4B < FLAC; comic = PDF < CBR < CBZ. Codec/HDR/audio are custom-format concerns, never quality.

Built-in immutable `QualityProfile`s installed by catalogarr at startup and re-seeded on version bump: `hd-bluray-web` (Bluray-1080p > WEB 1080p{WEBRip-1080p,WEBDL-1080p} > Bluray-720p; cutoff Bluray-1080p), `uhd-bluray-web`, `remux-web-1080p`, `remux-web-2160p`, `web-1080p`, `web-2160p`, `anime-remux-1080p` (minFormatScore 100, scoreSet anime-radarr; `anime-web-1080p` uses anime-sonarr), `music-lossless` (cutoff FLAC), `music-standard` (cutoff MP3-192), `ebook` (cutoff MOBI), `audiobook` (cutoff MP3), `comic` (cutoff CBZ). Video built-ins: upgradeAllowed, minFormatScore 0, cutoffFormatScore 10000, minUpgradeFormatScore 1, language original, properPolicy preferAndUpgrade, sizeTable movie/series/anime from TRaSH `quality-size` JSON. Users copy (`kubectl get qualityprofile hd-bluray-web -o yaml`, rename, set `builtIn: false`) and edit tiers/cutoff/`formatScores`/`enabledFormatGroups`; unknown slugs → `Invalid`; `conflicts.json` pairs rejected.

Custom formats are **not a CRD**: `pkg/quality/catalogue/catalogue_gen.go` is generated from `testdata/trash/docs/json/{radarr,sonarr}/cf` with stable slugs (unwanted, repack/proper, HD/UHD/Remux/WEB tiers 01–03, WEB Scene, HDR set, streaming services, audio, movie versions, language, anime tiers, season pack, optional unwanted), `trash_id` per app, `trash_scores` score sets (`default`, `anime-radarr`, `anime-sonarr`). Only ReleaseTitle, ReleaseGroup, Source, Resolution, Modifier, Language(+Except; `original` resolved from the item), IndexerFlag, ReleaseType are evaluated; Edition/Size/Year are accepted no-ops. Match rule verbatim: Negate per condition; group by kind; group ok iff no Required failed and ≥1 matched; format matches iff every group matches; score = Σ. Regexes via `dlclark/regexp2` (IgnoreCase, MatchTimeout 50 ms; timeout = no match + log), compiled at load, cheap kinds evaluated first. CI job diffs upstream trash_ids/scores/regexes.

`pkg/release` ports the *arr regex families (GPL): Source/Resolution/Remux/BRDISK/Proper/Repack/Version/REAL(case-sensitive)/Edition/ReleaseGroup (+ `[Group]` anime prefix, exceptions, InvalidReleaseGroupRegex)/LanguageParser and Sonarr's ordered episode families with SeriesType-aware selection; `moistari/rls` supplies hint tags and music/book/comic classification; dedicated regexes for music codec+bitrate → Lidarr enum, book/comic format by extension, audiobook `{Narrator}`/`[ASIN]` tokens.

`pkg/decision` ports the specification list and `UpgradableSpecification` verbatim (quality better & cutoff unmet → upgrade; worse → reject; revision better → upgrade unless doNotPrefer, only at equal quality; !upgradeAllowed → reject; revision worse → reject; cutoff met → reject; score ≤ current → reject; current ≥ cutoffFormatScore → reject; score < current+minUpgrade → reject), size checks, ranking. Every MediaFile records quality/revision/releaseType/score/matched formats/profile hash, so decisions never re-parse.

**Non-video pipelines (contract level, implemented after M6):** *Music* — Album search by artist+album (`t=music`, cat 3000*); release selection: parse track count/format from release, choose MB release matching `AnyReleaseOk` or pinned `ReleaseID` by track count and medium format; import matches tracks by number+duration (±5 s) via id3v2/dhowden tags, writes `{Artist}/{Album (Year)}/{Artist} - {Album} - {track:00} - {Title}` and tags files. *Books* — Book search by author+title (`t=book`, cat 7020) or ISBN; edition selection: `AnyEditionOk` accepts any edition whose format is in the profile, else pinned `EditionID`; `{Author}/{Title}/{Author} - {Title}.epub`. *Audiobooks* — search cat 3030 by title+author/narrator; N audio parts become N MediaFiles under `{Author}/{Series}/{Title} [ASIN]/`; Audiobookshelf layout, chapters from Audnexus. *Comics/manga* — Issue search cat 7030 by series+issue number (`v` volume tokens), CBZ preferred; importer writes `ComicInfo.xml` v2.1 (Series, Number, Volume, Year/Month/Day, Writer…, Manga=YesAndRightToLeft for manga, AgeRating) into the CBZ; weekly pull list = ComicVine `date_added` poll marks new Issues `wanted` when `MonitorNewIssues`.

## 10. Cross-service contract

| Watcher | Watches | Predicate | RBAC |
|---|---|---|---|
| squasharr `transcodeprofile` | catalog MediaFile | `status.probeHash` changed or created | get/list/watch mediafiles; create/update/delete/patch transcodejobs(+status) |
| captionarr `subtitleprofile`/`subtitlerequest` | catalog MediaFile, Movie/Episode (metadata for queries, read-only) | `status.probeHash` changed | get/list/watch catalog kinds; full on subtitlerequests |
| catalogarr `mediafile` | transcode TranscodeJob, subtitle SubtitleRequest | `status.phase==Succeeded` / `status.items` changed | get/list/watch those kinds |
| catalogarr `importer` | download Download | `status.phase ∈ {Completed,Seeding,Failed}` | get/list/watch downloads; patch downloads/status (SSA `status.import` only) |
| grabarr `download` | — | — | get/list/watch catalog kinds (owner resolution only) |

Each service gets its own ClusterRole generated by `controller-gen rbac` markers next to each reconciler; envtest proves the three managers on `Download.status` (`grabarr`, `grabarr-engine`, `catalogarr`) never clobber each other.

## 11. Storage

One RWX PVC `clustarr-data` (CephFS preferred; NFS/Longhorn RWX acceptable; never exFAT/SMB; chart validates access mode) mounted at `/data` in every media-touching pod with `fsGroup` + `fsGroupChangePolicy: OnRootMismatch`, UMASK 002:

```
/data/torrents/<category>/<download>/         anacrolix per-download storage, .part files, seeds in place
/data/torrents/.state/<infohash>.torrent      persisted metainfo
/data/usenet/<category>/<download>/           atomically published (_UNPACK_<download> staging)
/data/media/{movies,tv,music,books,audiobooks,comics}/   RootFolder paths (CEL-enforced prefix)
/data/.recycle/<yyyy-mm-dd>/                  recycle bin; catalogarr sweeps per RootFolder.cleanupDays
```
Node-local scratch: usenet engine `/scratch` (emptyDir or NVMe StorageClass ephemeral volume), transcode Jobs emptyDir (`profile.scratch`). Never put anacrolix completion DBs/mmap on NFS (defaults avoid them). Other stores: etcd (CRs), JetStream file store PVC 20Gi R3, SQLite PVC `clustarr-index` 5Gi RWO, Secrets (provider keys, indexer credentials/sessions, Trakt tokens). No Postgres, Redis or object store.

## 12. Scheduling / scaling

Controllers 1 replica each, leader-elected (catalogarr workers scale horizontally; `catalogarr-metadata` and `indexarr` pinned to 1). Torrent engines: StatefulSet ordinals = `DownloadClient.spec.replicas`, ≤500 torrents/pod, `GOMEMLIMIT` = 80 % of limit. Transcode: Jobs gated by suspend + slot budgets (`--slots cpu=2,nvidia=1,intel=1`), sizes 8 CPU/4Gi (1080p) and 8 CPU/8Gi (2160p), NVIDIA time-slicing replicas ≤8 (default 4). Subtitle workers fixed 2 replicas. **KEDA is opt-in** (`keda.enabled=false` default): when enabled, ScaledObjects on `captionarr-worker` and catalogarr search workers use the `prometheus` trigger on `jetstream_consumer_num_pending + jetstream_consumer_num_ack_pending` from `prometheus-nats-exporter` until kedacore/keda#8166 ships, `cooldownPeriod ≥ AckWait`, `terminationGracePeriodSeconds` = AckWait, workers drain on SIGTERM. Homelab: NATS 3×(100m/256Mi req, 1 CPU/1Gi lim), fileStore 20Gi; total messaging overhead < 1 GiB.

## 13. Observability

controller-runtime metrics + `metrics.RegisterRESTClientMetrics()` on `:8443` with auth filters and a ServiceMonitor; custom collectors: `clustarr_queue_pending{consumer}`, `clustarr_search_indexer_duration_seconds{indexer,outcome}`, `clustarr_indexer_escalation_level`, `clustarr_download_rate_bytes{client,direction}`, `clustarr_transcode_fps{job}`, `clustarr_transcode_slots{hardware,state}`, `clustarr_subtitle_provider_throttled{provider}`, `clustarr_metadata_cache_hits_total{tier}`. Readiness: JetStream ping (all), SQLite open (indexarr), engine re-attach complete (grabarr engines). Structured zap logs with `trace` from `Clustarr-Trace`. Kubernetes Events (events.k8s.io) on owning CRs: Grabbed, Imported, ImportBlocked, Failed, Transcoded, SubtitleDownloaded, DeadLettered. History = `CLUSTARR_EVENTS` (7d) + Events.

## 14. Testing

- **Unit/golden:** pkg/quality (all 2791 TRaSH regexes compile; score sets; conflicts; profile compile), pkg/decision (Radarr UpgradableSpecification and CustomFormatCalculation fixtures as tables; size/availability), pkg/release (~2k title corpus incl. anime brackets, daily, packs, music/book/comic/audiobook), pkg/naming (dialect goldens for TRaSH formats), pkg/transcode (argv goldens per encoder × SDR/HDR10/DV5/DV7/DV8 × remux; progress parser; verifier), pkg/mediainfo (recorded ffprobe JSON; moviehash `1606fd38140b6f23`), pkg/subtitles (planner cases, `hash == Σ−1`, sidecar parse/write, HI mods), pkg/cardigann (whole bundled corpus parses in CI; 1337x HTML + UNIT3D JSON fixtures vs cardigann-go/Prowlarr oracle; 25 filters; date translator), pkg/torznab (XML round-trips, error codes), pkg/metadata + importlist (httpmock recorded JSON incl. Trakt device flow, OpenSubtitles 406/429).
- **pkg/events contract suite** on membus (fake clock) and natsbus (embedded nats-server/v2): ack/nak-delay/term, AckWait redelivery, MaxDeliver→DLQ via sweeper, dedup window, WorkQueue delete-on-ack + non-overlapping filters, scheduled delivery, heartbeat, drain, KV Create/Update CAS/Watch, `ErrQueueFull`.
- **envtest (kube-apiserver 1.37):** per controller package with membus and fake providers: defaults/CEL (immutability, ExactlyOneOf, built-in protection, RootFolder prefix), finalizer without early return, Series→Episodes, Comic→Issues, MediaFile labels/probeHash, cross-kind watches and predicates (metadata refresh must not trigger transcode reconcile), TranscodeJob→Job (GPU placement, Downward API), Job completion mirroring (startTime → SuccessCriteriaMet → Complete), TTL, three-manager SSA no-clobber on Download, delay-profile scheduling with fake clock, grab lease race (two workers, one Download).
- **Engine integration (build-tagged):** two in-process anacrolix clients (per-download dir, `.part` re-attach, seed enforcement, remove-with-data); usenet pipeline against an in-process NNTP stub with yEnc articles, missing-article health, par2 repair, multi-volume RAR; transcode worker on lavfi-generated 10 s 1080p10 SDR + HDR10 clips (Main 10, MDCV/CLL preserved, packet counts, atomic swap); SQLite store (dedup, FTS, TTL sweep).
- **E2E (kind, `make test-e2e`):** chart with NATS R1, hostPath RWX, fixture Torznab server + seeder pod (30 s H.264 clip with embedded English subtitle), mock OpenSubtitles, stub TMDB; apply providers/Indexer/DownloadClient/RootFolder/Movie; assert ≤10 min: Download Imported, Jellyfin-dialect path, MediaFile labels, TranscodeJob Succeeded with hevc/main10, SubtitleRequest Satisfied with `<stem>.en.sdh.srt`, DLQ empty, expected EVENTS subjects. Scenarios 2–4: RSS upgrade (old file in `.recycle`), failed download → blocklisted → redownload, idempotency rerun (no duplicate Downloads/Jobs).
- **CI gates:** controller-gen drift, golangci-lint v2.13 (forbidigo on `Status().Update`), race detector on pkg/events and workers, TRaSH upstream diff, Cardigann corpus sync + schema validation, Helm lint + kind smoke, image matrix (static, cgo media, cuda).

## 15. Repo layout

```
go.mod  PROJECT  Makefile  LICENSE(GPL-3.0)  hack/{boilerplate.go.txt,gen-catalogue,sync-indexers,kind.sh,fixtures.sh}
cmd/clustarr/main.go                      cobra root: catalogarr|indexarr|grabarr|squasharr|captionarr|all|version
api/common/v1alpha1/                      shared Go types (no CRDs)
api/catalog/v1alpha1/  api/index/v1alpha1/  api/download/v1alpha1/  api/transcode/v1alpha1/  api/subtitle/v1alpha1/
                                          *_types.go, groupversion_info.go (GroupVersion + SchemeGroupVersion), zz_generated.deepcopy.go, applyconfiguration/
catalogarr/  run.go (manager wiring, roles)
  controller/{movie,series,episode,artist,album,author,book,audiobook,comic,issue,mediafile,rootfolder,qualityprofile,delayprofile,metadataprovider,importlist,importexclusion,search,importer,wantedcron}/
  worker/{search,grab,rssmatcher,importer,importlist}/   metadata/ (gateway: registry, cache, limiter, rpc)   history/ (event sink, dlq sweeper+projector)   builtin/ (profiles YAML)
indexarr/    run.go  controller/{indexer,indexerdefinition,indexerproxy}/  search/ (micro rpc, fanout, dedup, download)  rss/  releaseindex/ (Store, sqlite/)  facade/  health/  defs/ (embedded v11 corpus + schema.json)
grabarr/     run.go  controller/{downloadclient,download,blocklist}/  engine/torrent/  engine/usenet/  assign/
squasharr/   run.go  controller/{transcodeprofile,transcodejob,slots}/  jobs/ (batch/v1 builders, GPU placement)  worker/
captionarr/  run.go  controller/{subtitleprofile,subtitleprovider,subtitlerequest,cron}/  worker/  providers/{opensubtitlescom,gestdown,subdl,subsource,embedded,whisper(stub)}/  throttle/
pkg/events/{natsbus,membus,contracttest,k8sbridge,schema}  pkg/quality/{catalogue}  pkg/release  pkg/decision  pkg/naming  pkg/mediainfo  pkg/transcode  pkg/subtitles  pkg/metadata/clients/{tmdb,tvdb,musicbrainz,coverart,fanart,openlibrary,hardcover,audnexus,comicvine,metron,mangadex,anilist,kitsu,animelists}  pkg/importlist/{trakt,plex,tmdb,mdblist,stevenlu,imdbcsv,custom,arr}  pkg/cardigann  pkg/torznab  pkg/newznab  pkg/download  pkg/fsops  pkg/k8s  pkg/ratelimit  pkg/version
config/{crd/bases,rbac,manager,nats,keda,prometheus,samples,default}   charts/clustarr/
images/{Dockerfile.controller,Dockerfile.media,Dockerfile.media-cuda}
test/{e2e,fixtures}   testdata/{trash,cardigann,releases,ffprobe,subtitles,nzb}   docs/adr/
```

## 16. MVP scope (ordered milestones; each ends green in CI)

- **M0 Scaffold:** kubebuilder v4.16 multigroup bumped to controller-runtime v0.25.1 / k8s v0.37; all CRDs in §4 generated (types compile, CRDs install, CEL tested); cobra binary; two images; kustomize + Helm umbrella with NATS and the `/data` PVC; `pkg/events` natsbus+membus+contract suite+topology+DLQ+k8sbridge; `pkg/k8s`.
- **M1 Catalog core:** pkg/quality (catalogue, 13 built-ins), pkg/release (movie+TV incl. anime/daily/packs), pkg/decision; catalogarr Movie/Series/Episode/MediaFile/RootFolder/QualityProfile/DelayProfile/MetadataProvider(tmdb, tvdb)/ImportExclusion/Search controllers; metadata gateway; search/grab/rss-matcher workers with lease + delay profiles; wanted cron.
- **M2 Indexers (generic):** pkg/torznab + pkg/newznab; Indexer controller (generic Newznab/Torznab incl. Prowlarr/Jackett), caps/health/backoff/limits, `rpc.indexarr.search|download|query`, RSS worker, SQLite FTS5 store, `CLUSTARR_RELEASES` publishing.
- **M3 Downloads + import (end-to-end):** grabarr DownloadClient (torrent StatefulSet, usenet Deployment), Download lifecycle, engines, blocklist; catalogarr importer + naming (Jellyfin/Plex/Emby presets) + MediaFile probe/labels. **E2E scenario 1 passes here.**
- **M4 Transcode:** squasharr profiles/jobs/slots/worker, libx265 CPU + hevc_nvenc GPU, HDR10 explicit params, DV passthrough/downgrade/reject, HDR10+ drop, remux-only, verification, replace + recycle, MediaFile follow-up.
- **M5 Subtitles:** captionarr profiles/providers/requests, planner, fetch workers, opensubtitlescom + embedded + gestdown providers, Bazarr scoring/post-processing, throttles, adaptive + upgrade crons.
- **M6 Prowlarr parity + lists + non-video inventory:** pkg/cardigann (HTML/JSON/XML, all 25 filters, form/cookie/post/get login) with bundled corpus + IndexerDefinition + IndexerProxy (http/socks; FlareSolverr client) + Torznab facade; ImportList Trakt (device flow) + Plex Discover; history sink + DLQ projector; Artist/Album/Author/Book/Audiobook/Comic/Issue controllers with metadata (MusicBrainz+CAA, Open Library, Audnexus, ComicVine, MangaDex) and **manual import** (Download with `Manual=true` + `import-target` annotation) so every kind stores files end-to-end; automatic search/grab for these kinds follows the §9 contracts.

Must-fix resolution map: status ownership §3/§4/§10; double-grab §8.2; bounded lists §4 legend; no inline bytes §4.4; indexarr single writer §3/§6.2; grabarr placement/restart §6.3; CEL §4; watch predicates/RBAC §10; KEDA optional §12; Search CR + annotations in MVP §4.2/§8.2/§8.4; Comic issues as Issue Kind §4.2; DelayProfile in MVP; ScoreSet split; non-video contracts §9; catalogarr topology §3; blocklist source of truth §4.4; TranscodeProfile video-only; add-time fields on Movie/Series/Artist/Author/Album; rpc.indexarr.download; single-reply RPC; sidecar feedback §8.6; TranscodeJob name includes profile hash; AllowMsgSchedules on all WORK streams; MaxDeliver > len(BackOff) in every row; catalogarr uses SSA everywhere.

## 17. Deferred

Chunked transcoding (Indexed Jobs: `completions=chunks`, `backoffLimitPerIndex=2`, `maxFailedIndexes=0`, scene-aligned keyframe splits, concat/verify Job with `successPolicy`, RWX scratch `/data/.transcode/<uid>/`), HDR10+ preserve, dovi_tool profile-7, QSV/VAAPI hardening, Kueue; external download clients + RemotePathMapping; usenet transfer/post-process split; subtitle sync (ffsubsync/alass), Whisper, SubDL/SubSource/scraper plugins; import lists TMDB/MDBList/StevenLu/CSV/custom/arr/Simkl/AniList; Radarr/Sonarr v3 REST facade and `/release/push`; Jackett filter grammar; Postgres FTS Store; NACK-managed topology as default; TVDB `/updates` + TMDB `/changes` invalidation; anime crosswalks (Fribb/Kometa), XEM; NFO/artwork writing, media-server refresh notifications; rename-on-mediainfo-change; `kubectl-clustarr` plugin; multi-tenancy; web UI.

## 18. Risks

- **Object count:** ~3 CRs per episode (Episode, MediaFile, SubtitleRequest) + TTL'd TranscodeJobs; 5k episodes ≈ 15k objects. Mitigation: no episodes/issues in parent status, status budget, `EnableWarmup`, `GroupKindConcurrency`. Revisit trigger > 50k media objects: merge SubtitleRequest into `MediaFile.status.subtitles` under a `captionarr` field manager (the only planned multi-service status).
- **regexp2 backtracking** on RSS volume: pre-filters + 50 ms timeout; benchmark 10k titles × 242 CFs before GA.
- **anacrolix gotchas** (memory after Drop, client lock on Stats, NFS): GOMEMLIMIT, 5 s polling, no completion DB, seed counters persisted by us.
- **cgo usenet image** (nntppool → rapidyenc pin `v0.0.0-20251128204712-7aafef1eaf1c`); pure-Go fallback documented, not built.
- **Provider ToS/quotas** (TVDB PIN/licence, ComicVine non-commercial, MusicBrainz 1 rps, OpenSubtitles 20/day): single gateway is a deliberate bottleneck; KV token buckets for subtitle providers.
- **Single writers** (indexarr, metadata gateway, grabarr engine per ordinal): search unavailable during indexarr restart (catalogarr `Retry`s), documented upgrade paths (Postgres Store; KV limiter windows).
- **RWX semantics**: hardlinks/atomic renames must hold across pods; validate on target storage before GA; chart warns on non-RWX.
- **x265 throughput** (2–4 fps at 2160p): GPU tier and chunking are the levers.
- **GPL-3.0** constrains downstream embedding; accepted.

## 19. ADR summaries

- **ADR-0001 Task queue / event bus = NATS JetStream.** Compared: *Redis Streams* (go-redis v9.22 / asynq v0.26): no server-side AckWait/MaxDeliver/BackOff/DLQ, hand-rolled XAUTOCLAIM reclaimers, Redis 8 tri-licence → Valkey drift; *Kafka/Redpanda* (franz-go v1.22): ≥2 GiB/core, Go clients lack share groups (per-message ack), no work-queue semantics; *RabbitMQ*: quorum queues fine but replayable pub/sub needs Streams + second client, Erlang, cert-manager; *Postgres/River* v0.47: best Go job queue but not a bus, and etcd is the system of record so transactional insert buys nothing; *K8s-native (CRDs as queue)*: no ordering/ack/backoff, etcd churn for sub-minute tasks — used only for human-visible jobs. JetStream alone gives Ack/Nak(delay)/Term/InProgress, MaxDeliver+BackOff, Nats-Msg-Id dedup, WorkQueue retention with non-overlapping consumers, scheduled messages, KV CAS/TTL/Watch, micro request/reply, Helm chart, NACK operator, KEDA scaler in ~100 MiB. Revisit: Go Kafka share groups GA; KEDA #8166; a service needing relational history (River + CNPG for that service).
- **ADR-0002 Licence = GPL-3.0.** Enables verbatim ports of *arr parsers, decision tables, naming grammar and Bazarr scoring/HI regexes; TRaSH data is MIT.
- **ADR-0003 Release index = SQLite FTS5 (modernc) on RWO PVC, single writer, Store interface.** Rebuildable cache; Postgres FTS is the multi-replica path.
- **ADR-0004 One CR per human-visible unit, one controller-writer per CR.** Download, TranscodeJob, SubtitleRequest, Search, MediaFile, Episode, Issue are CRs; sub-minute tasks are messages; no cross-service status managers except `Download.status.import`.
- **ADR-0005 Transcodes are batch/v1 Jobs gated by suspend + slot budgets**, not queue messages; chunking = Indexed Jobs later.
- **ADR-0006 Storage = one RWX volume at `/data`, TRaSH layout, hardlink-else-copy / atomic move; no object store.**
- **ADR-0007 Metadata gateway is a single replica** owning all outbound metadata clients and limiters; other services use RPC/work queue.
- **ADR-0008 Grab semantics = DelayProfile (Radarr shape) via scheduled JetStream messages + pending KV keep-best + grab lease.**
