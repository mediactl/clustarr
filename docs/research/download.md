# Clustarr research note: torrent + Usenet download clients in Go

Date: 2026-09-18. Scope: (1) `github.com/anacrolix/torrent` v1.61.0, (2) Usenet (NNTP, NZB, yEnc, PAR2, RAR, SABnzbd/NZBGet behaviour, Go libraries), (3) the *arr download-client abstraction and a `pkg/download` design for Clustarr, plus a storage recommendation.

Verification method: every Go API below was checked with `go doc` / source reads in a temp module (`scratchpad/dlmod`, Go 1.27) after `go get`; versions via `go list -m -versions` / `go list -m <mod>@latest`; *arr behaviour from Radarr/Sonarr source on GitHub (raw files) and DeepWiki; SABnzbd/NZBGet from their source and `nzbget.conf`; specs from sabnzbd.org (NZB), yEnc 1.3 draft, parchive PAR2 spec, RFC 3977/4642/4643. Items I could not verify are marked "unverified".

---

## 1. Torrent: `github.com/anacrolix/torrent` v1.61.0

Module facts: v1.61.0 is the latest tag. Static, cgo-free build verified: `CGO_ENABLED=0 go build` of a binary importing `torrent`, `torrent/storage`, `nwaples/rardecode/v2`, `javi11/nzbparser`, `Tensai75/nntp`, `akalin/gopar/par2` produced a statically linked ELF. Notable transitive deps: `anacrolix/go-libutp v1.3.2` (uTP; pure-Go fallback used when cgo is off), `pion/webrtc/v4` (WebTorrent; can be disabled with `DisableWebtorrent`), `go.etcd.io/bbolt v1.3.6` (piece completion), `go-llsqlite` (optional sqlite storage).

### 1.1 `ClientConfig` (verified fields; defaults from `NewDefaultClientConfig()`)

```go
type ClientConfig struct {
    ClientTrackerConfig        // DisableTrackers, TrackerDialContext, TrackerListenPacket, LookupTrackerIp (deprecated)
    ClientDhtConfig            // NoDHT, DhtStartingNodes, ConfigureAnacrolixDhtServer, PeriodicallyAnnounceTorrentsToDht (default true), DHTOnQuery
    MetainfoSourcesConfig      // MetainfoSourcesMerger (default merges spec)

    DataDir string             // root for the default "file" storage, unless DefaultStorage set
    ListenHost func(network string) string   // default returns ""
    ListenPort int             // default 42069; TCP+uTP; DHT shares the UDP socket
    NoDefaultPortForwarding bool // default false => UPnP attempted. Set true in k8s.
    UpnpID string
    DisablePEX bool

    NoUpload bool              // never send chunks
    DisableAggressiveUpload bool
    Seed bool                  // default false! Without it the client only uploads to reciprocate; set true for a seedbox
    UploadRateLimiter   *rate.Limiter // default rate.NewLimiter(rate.Inf, 0)
    DownloadRateLimiter *rate.Limiter // default nil = no limiting AND cannot be added later; create one with rate.Inf and call SetLimit at runtime
    MaxUnverifiedBytes int64   // default 64 MiB across all torrents; 0 disables

    PeerID string
    DisableUTP, DisableTCP bool
    DefaultStorage storage.ClientImpl // default: storage.NewFile(DataDir)-like file impl, closed with the Client

    HeaderObfuscationPolicy HeaderObfuscationPolicy // Preferred: true, RequirePreferred: false
    CryptoProvides mse.CryptoMethod; CryptoSelector mse.CryptoSelector
    IPBlocklist iplist.Ranger
    DisableIPv6, DisableIPv4, DisableIPv4Peers bool
    Debug bool; Logger log.Logger; Slogger *slog.Logger   // Slogger recommended

    WebTransport http.RoundTripper; HTTPProxy func(*http.Request) (*url.URL, error)
    HTTPDialContext ...; HTTPUserAgent string; HttpRequestDirector func(*http.Request) error
    WebsocketTrackerHttpHeader func() http.Header
    ExtendedHandshakeClientVersion, Bep20 string

    NominalDialTimeout time.Duration // 20s
    MinDialTimeout time.Duration     // 3s
    EstablishedConnsPerTorrent int   // 50
    HalfOpenConnsPerTorrent int      // 25
    TotalHalfOpenConns int           // 100
    TorrentPeersHighWater int        // 500
    TorrentPeersLowWater int         // 50
    HandshakesTimeout time.Duration  // 4s
    KeepAliveTimeout time.Duration   // 1m
    MaxAllocPeerRequestDataPerConn int // 1 MiB

    PublicIp4, PublicIp6 net.IP
    DisableAcceptRateLimiting bool   // default true
    DropDuplicatePeerIds bool
    DropMutuallyCompletePeers bool   // default true
    DialForPeerConns bool            // default true
    AcceptPeerConnections bool       // default true
    AlwaysWantConns bool
    Extensions, MinPeerExtensions PeerExtensionBits
    DisableWebtorrent, DisableWebseeds bool
    Callbacks Callbacks
    ICEServerList []webrtc.ICEServer; ICEServers []string // deprecated
    DialRateLimiter *rate.Limiter    // default rate.NewLimiter(10, 10)
    PieceHashersPerTorrent int       // default 2
}
func NewDefaultClientConfig() *ClientConfig
func (cfg *ClientConfig) SetListenAddr(addr string) *ClientConfig
```

"Probably not safe to modify this after it's given to a Client" (doc). `NewClient` takes ownership of the config.

`Callbacks` (all synchronous, may hold client locks): `CompletedHandshake func(*PeerConn, InfoHash)`, `ReadMessage`, `ReadExtendedHandshake`, `PeerConnClosed`, `PeerConnReadExtensionMessage []func(...)`, `ReceiveEncryptedHandshakeSkeys`, `ReceivedUsefulData []func(ReceivedUsefulDataEvent)`, `ReceivedRequested`, `DeletedRequest`, `SentRequest`, `PeerClosed`, `NewPeer`, `PeerConnAdded`, `StatusUpdated []func(StatusUpdatedEvent)`. `StatusUpdatedEvent{Event StatusEvent; Error error; PeerId; Url; InfoHash string}` with `StatusEvent` = `peer_connected`, `peer_disconnected`, `tracker_connected`, `tracker_disconnected`, `tracker_announce_successful`, `tracker_announce_error`. Callbacks are for metrics/logging; do not block in them.

### 1.2 Client API (verified)

```go
func NewClient(cfg *ClientConfig) (*Client, error)
func (cl *Client) AddMagnet(uri string) (*Torrent, error)
func (cl *Client) AddTorrent(mi *metainfo.MetaInfo) (*Torrent, error)
func (cl *Client) AddTorrentFromFile(filename string) (*Torrent, error)
func (cl *Client) AddTorrentSpec(spec *TorrentSpec) (t *Torrent, new bool, err error) // add-or-merge
func (cl *Client) AddTorrentOpt(opts AddTorrentOpts) (t *Torrent, new bool)          // by infohash, custom Storage
func (cl *Client) AddTorrentInfoHash(ih metainfo.Hash) (*Torrent, bool)
func (cl *Client) Torrent(ih metainfo.Hash) (*Torrent, bool); Torrents() []*Torrent
func (cl *Client) Stats() ClientStats; ConnStats() ConnStats; WriteStatus(io.Writer)
func (cl *Client) WaitAll() bool; Close() []error; Closed() events.Done
func (cl *Client) ListenAddrs() []net.Addr; LocalPort() int; PublicIPs() []net.IP; PeerID() PeerID
func (cl *Client) AddDhtNodes([]string); AddDhtServer(DhtServer); AddDialer(Dialer); AddListener(Listener)

type TorrentSpec struct {
    AddTorrentOpts
    Trackers [][]string; DisplayName string; Webseeds []string; DhtNodes []string; PeerAddrs []string
    Sources []string          // "xs"/"as" magnet fields (HTTP metainfo sources)
    PieceLayers map[string]string // BEP 52
}
func TorrentSpecFromMagnetUri(uri string) (*TorrentSpec, error)
func TorrentSpecFromMetaInfo(mi *metainfo.MetaInfo) *TorrentSpec
func TorrentSpecFromMetaInfoErr(mi *metainfo.MetaInfo) (*TorrentSpec, error)

type AddTorrentOpts struct {
    InfoHash infohash.T; InfoHashV2 g.Option[infohash_v2.T]
    Storage storage.ClientImpl      // per-torrent storage (this is how to give each download its own directory)
    ChunkSize pp.Integer            // 0 = 16 KiB
    InfoBytes []byte
    DisableInitialPieceCheck bool   // skip hashing if piece completion is missing (huge torrents dropped in place)
    IgnoreUnverifiedPieceCompletion bool
    DisallowDataUpload, DisallowDataDownload bool // "add paused"-ish
}
```

`metainfo`: `metainfo.Load(io.Reader)`, `LoadFromFile`, `ParseMagnetUri`, `ParseMagnetV2Uri`; `MetaInfo.HashInfoBytes()` gives the infohash; `Info{PieceLength, Pieces, Name, Length, Files []FileInfo, Private *bool, Source, MetaVersion, FileTree}` with `TotalLength()`, `NumPieces()`, `UpvertedFiles()`, `IsDir()`.

### 1.3 Torrent lifecycle API (verified)

```go
func (t *Torrent) GotInfo() events.Done        // <-chan struct{}; closed once metadata is available (magnets)
func (t *Torrent) Info() *metainfo.Info        // nil before GotInfo
func (t *Torrent) InfoHash() metainfo.Hash; Name() string; Length() int64; NumPieces() int
func (t *Torrent) Metainfo() metainfo.MetaInfo // regenerated .torrent (persist this after GotInfo so restarts don't need DHT)
func (t *Torrent) DownloadAll()                // sets all pieces to PiecePriorityNormal; requires info
func (t *Torrent) DownloadPieces(begin, end int); CancelPieces(begin, end int)
func (t *Torrent) Files() []*File
func (f *File) SetPriority(PiecePriority); Priority(); Download(); Path(); DisplayPath(); Length(); BytesCompleted(); Offset(); BeginPieceIndex(); EndPieceIndex(); State() []FilePieceState
// PiecePriority: None(0, not wanted) < Normal < High < Readahead < Next(deprecated) < Now
func (t *Torrent) BytesCompleted() int64       // completed pieces + dirtied chunks; can go DOWN (hash fails); takes client lock (issue #634)
func (t *Torrent) BytesMissing() int64
func (t *Torrent) Complete() chansync.ReadOnlyFlag // .Bool(), .On() <-chan, .Off() <-chan  -- "all pieces complete"
func (t *Torrent) Stats() TorrentStats
func (t *Torrent) Seeding() bool               // true only if cfg.Seed && !NoUpload && !dataUploadDisallowed && !closed
func (t *Torrent) AllowDataUpload(); DisallowDataUpload(); AllowDataDownload(); DisallowDataDownload()
func (t *Torrent) SetMaxEstablishedConns(max int) int
func (t *Torrent) AddTrackers([][]string); ModifyTrackers; AddWebSeeds(urls, opts...); AddPeers([]PeerInfo); AddSources([]string)
func (t *Torrent) MergeSpec(*TorrentSpec) error
func (t *Torrent) VerifyDataContext(ctx) error // re-hash everything; Piece.VerifyDataContext for one piece
func (t *Torrent) PieceState(i) PieceState; PieceStateRuns(); SubscribePieceStateChanges() *pubsub.Subscription[PieceStateChange]
func (t *Torrent) SetOnWriteChunkError(func(error)) // default: logs critical and DISABLES data download (e.g. disk full)
func (t *Torrent) PeerConns() []*PeerConn; KnownSwarm() []PeerInfo
func (t *Torrent) Drop()                       // remove from client + close storage. Does NOT delete data.
func (t *Torrent) Closed() events.Done
```

`PieceState{storage.Completion{Err, Ok, Complete}; Priority; Hashing; QueuedForHash; Marking; Partial; MissingPieceLayerHash}`.

Stats (verified):

```go
type TorrentStats struct { AllConnStats; TorrentStatCounters; TorrentGauges }
type AllConnStats struct { ConnStats; WebSeeds ConnStats; PeerConns ConnStats }
type ConnStats struct {
    BytesWritten, BytesWrittenData Count          // upload (Data = payload only)
    BytesRead, BytesReadData, BytesReadUsefulData, BytesReadUsefulIntendedData Count
    ChunksWritten, ChunksRead, ChunksReadUseful, ChunksReadWasted, MetadataChunksRead Count
    PiecesDirtiedGood, PiecesDirtiedBad Count
}
type TorrentStatCounters struct { BytesHashed Count }
type TorrentGauges struct { TotalPeers, PendingPeers, ActivePeers, ConnectedSeeders, HalfOpenPeers, PiecesComplete int }
```

Ratio = `BytesWrittenData / max(BytesReadUsefulData, Length)` (the library itself reports `Uploaded: BytesWrittenData`, `Downloaded: BytesReadUsefulData` to trackers). Counters are process-lifetime only: **persist cumulative uploaded/downloaded/seed-time in the Download status yourself**, adding deltas on each poll, or ratio/seed-time limits reset on every pod restart. `Count` needs 64-bit alignment on some platforms (issues #262/#383): never embed `TorrentStats` inside a struct with odd alignment; copy with `.Copy()`.

Download rate: sample `BytesReadUsefulData` over time (doc explicitly says not to use `BytesCompleted` for rate).

### 1.4 Storage backends (verified)

`storage` package constructors: `NewFile(baseDir)`, `NewFileOpts(NewFileClientOpts)`, `NewFileByInfoHash(baseDir)` (deprecated; `<base>/<infohash-hex>/...`), `NewFileWithCompletion`, `NewFileWithCustomPathMaker(baseDir, TorrentDirFilePathMaker)`, `NewMMap(baseDir)`, `NewMMapWithCompletion`, `NewBoltDB(filePath)` (piece data in bolt; not for media), `storage/sqlite.NewDirectStorage(opts)`, `NewResourcePieces(PieceProvider)`. Piece completion stores: `NewBoltPieceCompletion(dir)` (creates `.torrent.bolt.db` in dir), `NewSqlitePieceCompletion(dir)`, `NewMapPieceCompletion()` (memory), `NewDefaultPieceCompletionForDir(dir)` (bolt by default; sqlite with build tag).

```go
type NewFileClientOpts struct {
    ClientBaseDir   string
    FilePathMaker   FilePathMaker            // func(FilePathMakerOpts{Info, File}) string ; default: <Info.BestName()>/<file path>
    TorrentDirMaker TorrentDirFilePathMaker  // func(baseDir, info, infoHash) string ; default: baseDir itself
    PieceCompletion PieceCompletion
    UsePartFiles    g.Option[bool]           // DEFAULT TRUE
    Logger          *slog.Logger
}
```

Part-file semantics (read from `storage/file-piece.go`, `file-torrent.go`, `file-client.go`):
- With `UsePartFiles` (default on) every incomplete file is written as `<name>.part`. When all pieces of a file are complete it is flushed, renamed to `<name>` ("promotion") and `chmod`'d read-only (`filePerm &^ 0o222`). If a piece later fails verification the file is renamed back to `.part` ("onFileNotComplete").
- With part files on and no `PieceCompletion` given, the default is `NewMapPieceCompletion()` (memory) and on (re)open completion is inferred from file names/sizes (`setCompletionFromPartFiles`): files without `.part` and with the expected size are marked complete without hashing; `.part` files get their pieces marked incomplete (and are re-hashed lazily). This is what makes restarts cheap: **complete files are trusted by name+size, only partial files get hashed.**
- With part files off, completion comes from a persistent bolt/sqlite DB in `ClientBaseDir`.
- **Import consequence**: the importer must ignore `*.part` and must expect read-only files; hardlinking a read-only file is fine, moving requires write permission on the directory only.
- Sparse files: file storage uses `pwrite` at offsets, so downloads are sparse until complete; `du` under-reports.

Per-download directory: pass `AddTorrentOpts.Storage = storage.NewFileOpts(NewFileClientOpts{ClientBaseDir: "<root>/<downloadID>", TorrentDirMaker: nil /* baseDir */})` per torrent (or a shared client impl with `TorrentDirMaker: func(base, info, ih) string { return filepath.Join(base, ih.HexString()) }`). Then "remove with data" = `t.Drop(); <-t.Closed(); os.RemoveAll(dir)`.

Deprecated/low value for Clustarr: mmap storage (do **not** use mmap on NFS/CephFS), bolt data storage, sqlite direct storage (single-node caches).

### 1.5 Seeding, ratio limits, stop/drop

- Seeding requires `ClientConfig.Seed = true`; otherwise uploads only happen while the torrent still needs data ("not altruistic").
- There is **no per-torrent ratio/seed-time limit in the library**. Implement it in the controller: on each poll compute ratio from persisted counters and seed time from `CompletedAt`; when the criteria are met call `t.DisallowDataUpload()` (equivalent of qBittorrent "pausedUP/stoppedUP") and mark `CanMoveFiles=CanBeRemoved=true`; later `Drop()` + delete when the importer confirms it no longer needs the files.
- `NoUpload` (global) and `DisableAggressiveUpload` are global knobs; `DisallowDataUpload/AllowDataUpload` are per torrent.
- Global rate limiting: `UploadRateLimiter`/`DownloadRateLimiter` are `*rate.Limiter` with burst >= 16 KiB chunk (burst 0 lets the lib choose). Because `DownloadRateLimiter == nil` cannot be changed after `NewClient`, always create one (`rate.NewLimiter(rate.Inf, 0)`) and use `SetLimit` at runtime. There is no per-torrent limiter.
- `Drop()` closes storage and connections and is "always safe"; it never deletes files.

### 1.6 Operational gotchas (verified from source/issues)

1. Memory after `Drop()` stays elevated (issue #930, open): heap not returned promptly; plan for a `GOMEMLIMIT` and avoid one giant long-lived client holding thousands of torrents. Roaring bitmaps per torrent (`_completedPieces`, `_pendingPieces`) plus per-peer request state are the main per-torrent memory; ~hundreds of torrents per pod is realistic, tens of thousands are not.
2. Hashing: `PieceHashersPerTorrent` default 2 goroutines per torrent; hashing measured 16-32 MB/s in old issue #184 (SHA-1 in Go; modern CPUs do far better, but the limiter is per torrent). `MaxUnverifiedBytes` (64 MiB) caps unhashed data across all torrents, which throttles fast downloads on slow disks; raise it (e.g. 256-512 MiB) for NVMe-backed workers.
3. `BytesCompleted()` and `Stats()` take the client lock; poll at ~1-5 s, not per request (issue #634 "high CPU").
4. Request stealing can waste 11-13 % bandwidth on high-latency swarms (issue #1094).
5. `SetOnWriteChunkError` default disables downloading on write error (ENOSPC, EIO on NFS) silently other than a log line: install a handler and surface it as `Warning` on the Download.
6. UPnP is on by default (`NoDefaultPortForwarding=false`) and DHT bootstraps to `dht.GlobalBootstrapAddrs`: in Kubernetes set `NoDefaultPortForwarding=true`, use a `hostPort`/NodePort or LoadBalancer for `ListenPort` (TCP+UDP) and set `PublicIp4`.
7. Metadata for magnets requires DHT or trackers in the magnet; Sonarr refuses magnets without `&tr=` when DHT is off. Persist `t.Metainfo()` after `GotInfo()`.
8. Private trackers: `Info.Private` -> the library disables DHT/PEX per torrent automatically per BEP 27 (library behaviour; not re-verified in v1.61.0 source, treat as "expected").
9. Piece-completion DB (`.torrent.bolt.db`) lives in `ClientBaseDir` when part files are disabled: keep it on local disk, not a shared PVC (bolt uses mmap + flock).
10. `ClientConfig.DataDir`/`DefaultStorage` is a single root; multi-tenant categories need per-torrent `Storage`.

---

## 2. Usenet

### 2.1 NNTP (RFC 3977, 4642, 4643) as used by SABnzbd/NZBGet/nntppool

- Transport: TCP 119 plain, TCP 563 "NNTPS" implicit TLS (what every commercial provider uses; RFC 4642 deprecates implicit TLS in favour of `STARTTLS` on 119, but providers still use 563). SABnzbd enforces TLS >= 1.2 and lets you set ciphers/verification level.
- Session: greeting `200` (posting allowed) / `201` (no posting); `CAPABILITIES` -> `101`; `AUTHINFO USER x` -> `381` (password required) or `281` (accepted); `AUTHINFO PASS y` -> `281` ok, `481` rejected, `482` bad sequence, `502` command unavailable/too many connections. SABnzbd treats `480/482/481/502/400` at login as permanent errors and applies a 10-minute penalty; `502` mid-session and `400` ("too many connections", idle timeout, "service discontinued") close the connection. `nntppool` maps `502` and `400` to `ErrMaxConnections`.
- Fetch by message-id (angle brackets required on the wire): `BODY <id>` -> `222` + multi-line body; `ARTICLE <id>` -> `220` head+blank+body; `HEAD <id>` -> `221`; `STAT <id>` -> `223` (exists, no transfer; used for pre-check). Not found: `430` (also `423` by number, `411`, `451` seen by SABnzbd). `500` = command unsupported (SABnzbd then falls back BODY->ARTICLE, STAT->HEAD). `GROUP name` -> `211 count low high name`; modern providers do not need `GROUP` before `BODY <id>` (SABnzbd has a per-server `send_group` option for old servers).
- Multi-line responses end with a line containing a single `.`; lines beginning with `.` are dot-stuffed (`..`). yEnc decoders must un-stuff.
- Pipelining: multiple `BODY` commands may be in flight per connection (SABnzbd `pipelining_requests`, nntppool `Inflight`); STAT can be pipelined much deeper (nntppool `StatInflight` 50-100).
- Provider model (both SABnzbd and NZBGet): priority/level (0 first), optional/backup ("fill") servers only used when all main servers return 430, per-server connection cap (typically 20-50), retention in days (skip articles older than retention: SABnzbd `article.nzf.nzo.avg_stamp < now - retention`), server groups (NZBGet `Server1.Group`: a 430 from one skips the rest of the group because they share a backbone; nntppool `StorageGroup`), penalties/backoff: SABnzbd `_PENALTY_TIMEOUT` 10 min, `_PENALTY_502` 5, `_PENALTY_TOOMANY` 10, `_PENALTY_SHARE` 10, `_PENALTY_PERM` 10, `_PENALTY_UNKNOWN` 3, `_PENALTY_VERYSHORT` 0.1, `_PENALTY_SHORT` 1. NZBGet: `ArticleRetries=3`, `ArticleInterval=10s`, `ArticleTimeout=60s`, and "try all servers of the same level before the next level".
- Keepalive: providers drop idle connections; nntppool sends `DATE` probes (`KeepaliveCommand`).
- Quotas: block accounts have byte quotas (nntppool `QuotaBytes/QuotaPeriod`).

### 2.2 NZB format (sabnzbd.org/wiki/extra/nzb-spec)

```xml
<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head>
    <meta type="title">...</meta>
    <meta type="password">...</meta>   <!-- repeatable -->
    <meta type="tag">h264</meta>
    <meta type="category">TV</meta>
  </head>
  <file poster="joe@bloggs.com (Joe Bloggs)" date="1071674882" subject="Here's your file!  abc-mr2a.r01 (1/2)">
    <groups><group>alt.binaries.newzbin</group></groups>
    <segments>
      <segment bytes="102394" number="1">123456789abcdef@news.newzbin.com</segment>
    </segments>
  </file>
</nzb>
```

`segment bytes` = article payload size (excluding headers), `number` = 1-based part index, body = Message-ID **without** angle brackets (add `<>` on the wire). `file subject` carries `"filename" (n/total)` and often `[x/y]`; parsers derive filename from the quoted string (javi11/nzbparser `ParseSubject` -> `Subject{Filename, Basefilename, File, TotalFiles, Segment, TotalSegments}`). Passwords: `<meta type="password">`, or SABnzbd's filename conventions `Name{{password}}`, `Name / password`, `password=...`, or a password file (NZBGet `UnpackPassFile`). Job total size = sum of segment bytes (yEnc overhead ~2 %, so decoded size is slightly smaller).

### 2.3 yEnc (draft 1.3)

- Encode: `O = (I + 42) mod 256`; critical output chars NUL, LF, CR, `=` are escaped as `=` + `(O + 64) mod 256`; encoders may also escape leading `.` and leading/trailing TAB/SPACE. Decoders must handle `=` escapes and NNTP dot-unstuffing.
- Headers: `=ybegin part=N total=T line=128 size=<full file size> name=<filename>` (name last), for multipart a second line `=ypart begin=B end=E` (1-based inclusive byte range), trailer `=yend size=<part size> part=N pcrc32=XXXXXXXX crc32=XXXXXXXX` (`pcrc32` = CRC32 of this part's decoded bytes, `crc32` = whole file, optional). Validation: `E-B+1` must equal `=yend size`; `pcrc32` must match. Part offset in file = `B-1`. Article ordering comes from the NZB `number`, but the authoritative write offset comes from `=ypart begin` (NZBGet DirectWrite writes each decoded article straight into a preallocated sparse file at that offset).
- File-level CRC without re-reading: SABnzbd combines each article's `pcrc32` with `crc32_combine(crcA, crcB, lenB)` as articles are assembled (`nzf.update_crc32`), giving the file CRC on the fly; NZBGet does the same (`CrcCheck=yes`, used by `ParQuick`).
- uuencode still appears on rare old posts; rapidyenc/nntppool detect `FormatUU`.

### 2.4 PAR2 (parchive spec) and quick verification

Packet header (48 bytes): magic `PAR2\0PKT`, length u64 (multiple of 4, includes header), packet MD5 (from recovery-set-id to end), recovery set id (16 B = MD5 of the Main packet body), type (16 B). Types: `PAR 2.0\0Main\0\0\0\0` (slice size u64, file count u32, recovery file ids[16], non-recovery file ids[16]), `PAR 2.0\0FileDesc` (file id 16, MD5 of whole file 16, MD5 of first 16 KiB 16, length u64, name ASCII/UTF-8 padded to 4), `PAR 2.0\0IFSC\0\0\0\0` (file id, then per slice MD5 16 + CRC32 4), `PAR 2.0\0RecvSlic` (exponent u32 + data), `PAR 2.0\0Creator\0`. File id = MD5(hash16k || length || name). Volume naming: `name.vol000+01.par2` (regex `(.*)\.vol(\d*)[+\-](\d*)\.par2`, blocks = second number); `name.par2` is the index (no recovery slices).

Uses in a downloader:
1. Deobfuscation ("par2 rename", NZBGet `ParRename`/`DirectRename`, SABnzbd `recover_par2_names`): hash the first 16 KiB of each downloaded file and match to `FileDesc.hash16k` -> rename to `FileDesc.name`. Works while downloading once the first article of a file is present (NZBGet downloads first articles of all files first for this).
2. Quick check (SABnzbd `quick_check_set`, NZBGet `ParQuick`): per file, compare on-the-fly CRC32 (from article `pcrc32` combined) and size with the expected file CRC derived from the IFSC slice CRC32s (combine slice CRCs with `crc32_combine`; the last slice is zero-padded to slice size, so un-pad with a "crc32 zero unpad" step — sabctools provides `crc32_combine`, `crc32_multiply`, `crc32_zero_unpad`). If every file matches, skip par2 entirely. This is the single biggest post-processing speed-up and needs no disk reads.
3. Health: NZBGet `health = 1000 - failed_articles/total_articles*1000`; `critical health` = derived from available recovery blocks (par2 sizes) - below it the job cannot be repaired; `HealthCheck=park|pause|delete|none`. SABnzbd equivalent: `check_availability_ratio` vs `req_completion_rate` and `MAX_BAD_ARTICLES`, option "Abort jobs that cannot be completed".
4. Repair: exec `par2 r <index.par2>` (SABnzbd parses "All files are correct", "Repair is required", "You need N more recovery blocks", "Repair is possible", "Repair complete", "Repairing: NN%"; extra flags `-m` memory, `-t` threads, `-T` verify threads, `-N` no data skipping, `-B <basepath>`). If more blocks are needed, fetch additional `.volNNN+MM.par2` files from the NZB (SABnzbd status `Fetching`), then retry.

### 2.5 RAR / 7z

- Obfuscated releases are almost always multi-volume RAR (`name.part01.rar` ... or `name.rar`, `name.r00`, `name.r01` ...), sometimes password-protected or with encrypted headers; SABnzbd/NZBGet exec `unrar x -idp -o+ -p<pw>|-p- -y <first volume> <dest>` and parse "Cannot find volume", "CRC failed", "Incorrect password", "is not RAR archive", "checksum error". Direct Unpack (SABnzbd, auto-enabled when disk > 40 MB/s; NZBGet `DirectUnpack=no` default) starts unrar on the first volume while later volumes download; disabled for encrypted/passworded sets and aborted on bad articles (`MAX_BAD_ARTICLES`).
- Go: `github.com/nwaples/rardecode/v2` v2.4.1 (Aug 2026, BSD-2, pure Go): `OpenReader(name, opts...)` handles multi-volume by standard naming, `NewReader(io.Reader)` single volume, `Password(pw)`, `FileSystem(fs.FS)`, `MaxDictionarySize`, `Reader.Next()` -> `*FileHeader{Name, IsDir, Solid, Encrypted, HeaderEncrypted, UnPackedSize, PackedSize, HostOS, Attributes, ModificationTime, ...}`, `Reader.Read/WriteTo`, `List()`, `OpenFS()`; RAR 1.5/4.x/5.x incl. RAR5 AES; errors `ErrBadPassword`, `ErrCorruptBlockHeader`, `ErrVerMismatch`, `ErrNoSig`. Streaming only (you write the output). Verified `CGO_ENABLED=0` build.
- 7z: `github.com/bodgit/sevenzip` v1.6.5 (`OpenReaderWithPassword`, `NewReaderWithPassword`). zip: stdlib.
- Trade-off: pure-Go rardecode removes the unrar binary/licensing from the image and lets us stream `rar -> dest` inside the worker; unrar (proprietary freeware) is faster on RAR5 solid archives and handles corner cases. Recommendation: rardecode/v2 default, optional `unrar` exec path behind a feature flag for fallback on `ErrUnsupportedDecoder`/corrupt cases.

### 2.6 SABnzbd / NZBGet behaviours to replicate

| Behaviour | SABnzbd | NZBGet | Clustarr decision |
|---|---|---|---|
| Write model | article files in `incomplete/<job>/__ADMIN__`, Assembler writes files incrementally | `DirectWrite=yes`: preallocate sparse file, write each article at yEnc offset; `ArticleCache` MB, `WriteBuffer` KB | DirectWrite-style at `=ypart begin-1` into node-local scratch |
| Pre-check | "Check before download" (STAT/HEAD every article, status `Checking`) | none built-in (has "propagation delay" only) | Optional `STAT` sweep via `nntppool.StatMany` (cheap) before committing disk/quota |
| Propagation delay | minutes; status `Propagating` | `PropagationDelay` minutes (min post age) | Download.spec.usenet.propagationDelay; compare with NZB `file date` |
| Direct unpack | auto (>40 MB/s), not for encrypted | `DirectUnpack=no` default | phase 2; start with post-download unpack |
| Direct rename | via par2 in post-proc + `deobfuscate final filenames` | `DirectRename` (par2 16k hashes during download), `ParRename`, `RarRename` | do 16k-hash rename right after each file's first article |
| Quick check | `Enable Quick Check` (on-the-fly CRC vs par2) | `ParQuick=yes`, `CrcCheck=yes` | yes (section 2.4) |
| Repair | par2cmdline-turbo `r`, `Extra Par2 parameters` | `ParCheck=auto`, `ParRepair=yes`, `ParScan=extended`, `ParThreads`, `ParBuffer=16`, `ParTimeLimit` | exec `par2` (par2cmdline-turbo v1.5.0 static binaries) |
| Hopeless jobs | "Abort jobs that cannot be completed", `MAX_BAD_ARTICLES`, `req_completion_rate` | health/critical health, `HealthCheck=park` | compute health continuously; fail early when `missing > recoverable blocks` |
| Encrypted RAR | "Action when encrypted RAR is downloaded": continue/abort/pause | `UnpackPassFile`; failure `FAILURE/UNPACK` | fail with `IsEncrypted=true` (the *arr "failed download handling" contract) |
| Unwanted ext / samples | pause/abort on unwanted extension; "Ignore samples" | `ExtCleanupDisk=.par2,.sfv`, `UnpackCleanupDisk=yes` | cleanup list + sample/proof deletion at finalize |
| Duplicate detection | "Identical/Smart duplicate detection" | `DupeCheck=yes`, dupe scoring | belongs in Inventory, not the client |
| Layout | `complete_dir/<category>/<job>`; `incomplete_dir/<job>` with `_UNPACK_` prefix during extraction; `__ADMIN__` metadata | `InterDir` -> `DestDir/<category>/<job>` (`AppendCategoryDir=yes`) | `<root>/<category>/<downloadID>` (see section 5) |
| Statuses | Queued, Grabbing, Propagating, Checking, Downloading, Paused, Fetching (extra par2), QuickCheck, Verifying, Repairing, Extracting, Moving, Running (script), Completed, Failed, Deleted | queue statuses + final `SUCCESS/WARNING/FAILURE/DELETED` with detail `FAILURE/PAR`, `FAILURE/UNPACK`, `FAILURE/HEALTH`, `WARNING/HEALTH`, `WARNING/SPACE`, `DELETED/DUPE` | `Status` (6 *arr values) + `Stage` (fine-grained) + `Reason` |
| Post-proc order | par2 (quick check -> verify/repair) -> unpack -> deobfuscate -> cleanup -> move -> script | par-rename -> par-check/repair -> rar-rename -> unpack -> move -> cleanup -> pp-scripts | rename -> quickcheck -> repair -> unpack -> deobfuscate -> cleanup -> publish |
| History retention | keep N days/jobs | `KeepHistory=30` days | Download CR retained by TTL, then GC |

SABnzbd queue/history JSON used by Radarr: queue `nzo_id, filename, cat, mb, mbleft, timeleft, status, priority`; history `nzo_id, name, category, bytes, storage (final path), status, fail_message, stage_log`; `ENCRYPTED /` title prefix -> `IsEncrypted`. Radarr maps queue `Paused`->Paused, `Queued|Grabbing|Propagating`->Queued, everything else->Downloading; history `Completed`->Completed, `Failed`->Failed (or Warning when "Unpacking failed, write error or disk is full?"); `OutputPath = RemapRemoteToLocal(host, storage)`.

### 2.7 Go libraries (versions verified 2026-09-18)

| Module | Version | Purpose | Verdict |
|---|---|---|---|
| `github.com/javi11/nntppool/v4` | v4.23.0 (2026-09-06) | NNTP connection pool: multi-provider, backup providers, 430 failover with parallel STAT probe, pipelining (`Inflight`), 3 dispatch lanes, quotas, keepalive, integrated yEnc decode (`Body`, `BodyStream`, `BodyAsync`, `Stat`, `StatMany`, `Head`, `PostYenc`, `Stats`) | **Use.** Requires cgo (see below). Actively maintained (used by javi11/altmount). |
| `github.com/mnightingale/rapidyenc` | pseudo v0.0.0-20251128204712-7aafef1eaf1c is what nntppool v4.23.0 requires; latest is v0.0.0-20260902082024-527fe101576b | SIMD yEnc decode/encode wrapping animetosho/rapidyenc via cgo with bundled `librapidyenc_{linux_amd64,linux_arm64,darwin,windows_amd64}.a`; `Decoder.Next() -> Response{Metadata ResponseMeta{Meta, CRC, ExpectedCRC, StatusCode, Format,...}, Data}` | **Do not bump independently**: `go get rapidyenc@latest` breaks nntppool v4.23.0 (`DecodeIncremental`, `Format`, `State` removed/changed). Let MVS pick nntppool's pin. |
| `github.com/javi11/rapidyenc` | v0.0.0-20260215144528-f0dac5a39d34 | Fork: pure Go + hand-written amd64/arm64 assembly (`decode_amd64.s` ...), no cgo; `DecodeIncremental`, `NewDecoder(io.Reader)` | Fallback if we must be cgo-free (then NNTP pooling must be our own, e.g. on top of Tensai75/nntp). |
| `github.com/javi11/nzbparser` | v0.5.5 (2026-07) | NZB parse/write; `Nzb{Comment, Meta map[string]string, Files, TotalFiles, Segments, TotalSegments, Bytes}`, `NzbFile{Groups, Segments, Poster, Date, Subject, Bytes, FileHash, Number, Filename, Basefilename, TotalSegments}`, `NzbSegment{Bytes, Number, ID}`, `ParseSubject`, `MakeUnique`, `ParseOptions{RemoveDuplicates}` | **Use** (maintained fork of Tensai75/nzbparser v0.1.0). |
| `github.com/Tensai75/nntp` | v0.1.5 (2026-02) | Plain RFC 3977 client (fork of Go's old nntp): `Dial`, `DialTLS`, `Authenticate`, `Group`, `Article`, `Body`, `Head`, `Stat`, `Post`, `IHave`, `Capabilities`, `Date`, `Quit` | OK for posting/tests or a home-grown pool; no pooling, no yEnc. |
| `github.com/chrisfarms/nntp`, `github.com/chrisfarms/yenc` | pseudo 2015 / 2014 | original nntp/yenc libs | Unmaintained; skip. |
| `github.com/go-newsgroups/nzb` | v0.1.0 | small pure-Go NZB parse + download (`DownloadFile(ctx, ArticleFetcher, File)`), CGO_ENABLED=0 | reference only. |
| `github.com/nwaples/rardecode/v2` | v2.4.1 (2026-08) | RAR reader (RAR5, multi-volume, passwords, streaming) | **Use.** |
| `github.com/bodgit/sevenzip` | v1.6.5 | 7z reader | Use for `.7z`. |
| `github.com/akalin/gopar` | pseudo 2021-05-24 | pure-Go PAR2 `par2.Verify/Repair/Create`, `NewDecoder(delegate, indexFile, numGoroutines)`; reads everything into memory, no SIMD | Only as a last-resort fallback; too slow/memory hungry for 50 GB releases. |
| `github.com/javi11/par2go` (advertised as Tensai75/par2go) | v0.0.14 (2026-09-06) | PAR2 **create** only (ParPar static libs via cgo, ~710 MB/s) | Not for verify/repair. |
| `par2cmdline-turbo` (binary) | v1.5.0 (2026-08-20), static linux amd64/arm64 | verify/repair (`par2 r`, `-t`, `-m`, `-T`) | **Use via exec**; ship in the usenet worker image. |
| `github.com/Tensai75/nzb-monkey-go`, `Tensai75/subjectparser` | v0.3.3 / v0.1.1 | NZB search/indexer helpers, subject parsing | reference for subject regexes. |
| `github.com/go-while/NZBreX`, `github.com/nzbdav/rapidyenc` v1.2.1 | | other Go usenet code / C library fork | reference only. |
| `github.com/ricochet2200/go-disk-usage/du` | pseudo 2021 | free-space check | fine; or `golang.org/x/sys/unix.Statfs`. |

Build verification (temp module): `CGO_ENABLED=1 go build` of a binary importing nntppool/v4 + anacrolix/torrent + rardecode + nzbparser succeeds but links dynamically to `libstdc++.so.6`, `libm`, `libgcc_s`, `libresolv` (base image must be glibc + libstdc++, e.g. `debian:bookworm-slim`/`gcr.io/distroless/cc`; or try `-ldflags '-extldflags "-static"'`). `CGO_ENABLED=0` (with or without `GOEXPERIMENT=simd`) **fails** for nntppool v4.23.0 (`rapidyenc.Format/State/Encoder undefined`), so nntppool is cgo-only today. Without nntppool the rest of the stack (torrent, storage, rardecode, nzbparser, Tensai75/nntp, gopar) builds fully static with `CGO_ENABLED=0`.

---

## 3. The *arr download-client abstraction (what importers expect)

Radarr `IDownloadClient` (NzbDrone.Core.Download): `DownloadProtocol Protocol {get;}`, `Task<string> Download(RemoteMovie, IIndexer)` (returns the client's download id: torrent infohash hex or SAB `nzo_id`), `IEnumerable<DownloadClientItem> GetItems()`, `void RemoveItem(DownloadClientItem item, bool deleteData)`, `DownloadClientInfo GetStatus()`, `void MarkItemAsImported(DownloadClientItem)` (qBittorrent: relabel to the post-import category). `DownloadClientBase.DeleteItemData` removes `OutputPath` from disk when `deleteData` and the client cannot.

`DownloadClientItem` (verbatim fields): `DownloadClientItemClientInfo DownloadClientInfo{Protocol, Type, Id, Name, RemoveCompletedDownloads, HasPostImportCategory}`, `string DownloadId`, `string Category`, `string Title`, `long TotalSize`, `long RemainingSize`, `TimeSpan? RemainingTime`, `double? SeedRatio`, `OsPath OutputPath`, `string Message`, `DownloadItemStatus Status`, `bool IsEncrypted`, `bool CanMoveFiles`, `bool CanBeRemoved`, `bool Removed`.

`DownloadItemStatus`: `Queued=0, Paused=1, Downloading=2, Completed=3, Failed=4, Warning=5`. `TrackedDownloadState`: `Downloading, ImportBlocked, ImportPending, Importing, Imported, FailedPending, Failed, Ignored`; `TrackedDownloadStatus`: `Ok, Warning, Error`. `DownloadProtocol`: `Torrent, Usenet` (Prowlarr ReleaseInfo also carries `DownloadUrl`, `MagnetUrl`, `InfoHash`, `Guid`, `Size`, `Seeders`, `Peers`, `IndexerFlags`).

`DownloadClientInfo`: `IsLocalhost`, `OutputRootFolders []OsPath` (used to validate remote path mappings), `RemovesCompletedDownloads`. `RemotePathMapping{Host, RemotePath, LocalPath}`: `RemapRemoteToLocal(host, path)` = if host matches (case-insensitive) and `RemotePath` is a prefix, `LocalPath + (path - RemotePath)`; validation requires `LocalPath` to exist and not be `/`.

Completed-download handling flow (DownloadMonitoringService -> CompletedDownloadService): poll `GetItems()` every refresh; only `Status == Completed` items in state `Downloading|ImportBlocked` proceed; `OutputPath` empty -> warning "Download doesn't contain intermediate path"; wrong OS path shape -> "remote path mapping" warning; title parse failure -> `ImportBlocked` (manual import); then `ImportPending` -> `Importing` via `ProcessPath(outputPath, ImportMode.Auto, movie, item)` -> `Imported`, or partial-import warnings. Servarr wiki: "Usenet files move immediately upon completion; torrents remain in original locations during seeding and use hardlinks or copies only during library import"; "Use Hardlinks instead of Copy" requires the same filesystem; "Remove Completed" for torrents requires the client to pause on reaching seed goals and keep the category.

Torrent semantics (Sonarr qBittorrent client, verified): `DownloadId = hash.ToUpper()`; states `pausedDL/stoppedDL`->Paused; `queuedDL/checkingDL/checkingUP/checkingResumeData`->Queued; `downloading/forcedDL/moving`->Downloading; `stalledDL`->Warning "stalled"; `metaDL/forcedMetaDL`->Queued (Warning if DHT off); `uploading/stalledUP/pausedUP/stoppedUP/queuedUP/forcedUP`->Completed with `RemainingTime=0`; `error`/`missingFiles`->Warning; `CanMoveFiles = CanBeRemoved = RemoveCompletedDownloads && state in {pausedUP, stoppedUP} && HasReachedSeedLimit` where seed limit = ratio (`ratio_limit>=0` per torrent, `-2` = global), seeding time (minutes), inactive seeding time; `RemainingSize = Size*(1-Progress)`; `OutputPath = ContentPath` (file or folder). Add: magnet or `.torrent` bytes with `category`, `paused`/`stopped` per InitialState, `sequentialDownload`, `firstLastPiecePrio`, `ratioLimit`, `seedingTimeLimit` from the per-indexer `SeedCriteria{SeedRatio, SeedTime, SeasonPackSeedTime}`; then `WaitForTorrent(hash)` up to 10x100 ms. Download URL handling (TorrentClientBase): `magnet:` -> AddFromMagnetLink else HTTP GET; a 301/302/303 whose `Location` is `magnet:` switches to magnet; infohash parsed from the torrent file or `MagnetLink.Parse(...).InfoHashes.V1OrV2.ToHex()`; the client-returned hash is compared with the parsed one.

Usenet semantics: `CanBeRemoved = CanMoveFiles = true` always (SAB); after import Radarr removes from history (`del_files` when deleting data). Failed-download handling (encrypted, unpack failure, missing articles) is usenet-only in *arr: mark Failed with a message, remove, blocklist, re-search.

---

## 4. Clustarr `pkg/download` design

Goals: one interface for both protocols; *arr-compatible status vocabulary (so the Inventory importer logic can be ported from Radarr/Sonarr); rich enough for a CRD status; workers are stateless and re-attachable.

```go
package download // pkg/download

import ("context"; "time")

type Protocol string
const (
    ProtocolTorrent Protocol = "torrent"
    ProtocolUsenet  Protocol = "usenet"
)

// Status is deliberately the 6-value *arr vocabulary; Stage carries detail.
type Status string
const (
    StatusQueued      Status = "Queued"      // accepted, waiting (propagation delay, metadata, pre-check, queue slot)
    StatusPaused      Status = "Paused"
    StatusDownloading Status = "Downloading" // includes usenet post-processing stages (see Stage)
    StatusCompleted   Status = "Completed"   // OutputPath is importable. Torrent may still be seeding (CanMoveFiles=false)
    StatusFailed      Status = "Failed"      // terminal; Reason set
    StatusWarning     Status = "Warning"     // stalled, missing files, disk full, needs operator
)

type Stage string
const (
    StageGrabbing   Stage = "grabbing"    // fetching .torrent/.nzb from indexer URL
    StagePropagating Stage = "propagating"
    StagePrechecking Stage = "prechecking" // STAT sweep
    StageMetadata   Stage = "metadata"    // magnet ut_metadata
    StageChecking   Stage = "checking"    // torrent: hashing existing data
    StageTransfer   Stage = "transfer"
    StageFetchingPar2 Stage = "fetching-par2"
    StageQuickCheck Stage = "quickcheck"
    StageVerifying  Stage = "verifying"
    StageRepairing  Stage = "repairing"
    StageExtracting Stage = "extracting"
    StageMoving     Stage = "moving"
    StageSeeding    Stage = "seeding"     // torrent, Status=Completed
    StageSeedingDone Stage = "seeding-done" // seed criteria met: CanMoveFiles/CanBeRemoved=true
)

type FailureReason string
const (
    ReasonEncrypted        FailureReason = "Encrypted"
    ReasonUnpackFailed     FailureReason = "UnpackFailed"
    ReasonMissingArticles  FailureReason = "MissingArticles"   // health below critical
    ReasonRepairFailed     FailureReason = "RepairFailed"
    ReasonDiskFull         FailureReason = "DiskFull"
    ReasonUnwantedContent  FailureReason = "UnwantedContent"   // unwanted extension / sample only
    ReasonNoMetadata       FailureReason = "NoMetadata"        // magnet timed out
    ReasonGrabFailed       FailureReason = "GrabFailed"
    ReasonRemovedExternally FailureReason = "RemovedExternally"
)

type Source struct {
    // exactly one of
    MagnetURI   string
    TorrentURL  string   // HTTP(S), may redirect to magnet
    TorrentData []byte
    NZBURL      string
    NZBData     []byte
    // hints
    ExpectedInfoHash string // lower-case hex v1 (or v2) infohash from the indexer, compared after add
    Cookies, Headers map[string]string // indexer auth for the grab
}

type SeedCriteria struct {
    Ratio    *float64       // nil = client default
    SeedTime *time.Duration // nil = client default
    // Sonarr: SeasonPackSeedTime; kept generic
    PackSeedTime *time.Duration
}

type AddRequest struct {
    ID          string   // caller-chosen stable id (CR name/UID). Clients map it to their native id.
    Protocol    Protocol
    Title       string   // release title (used for the working dir name and parsing fallback)
    Category    string
    Priority    int32    // 0 normal; positive = higher (SAB "Force/High", qBit top-of-queue)
    Source      Source
    Password    string   // archive password (NZB meta/password conventions already resolved by caller)
    Seed        SeedCriteria
    Paused      bool     // "Initial state: paused"
    Sequential  bool     // torrent: sequential; FirstLast bool below
    FirstLastFirst bool
    WantedFiles []string // optional per-file selection (glob on torrent paths); empty = all
    Labels      map[string]string // indexer id, release guid, tenant, etc.
}

type FileInfo struct {
    Path      string // relative to Item.OutputPath
    Size      int64
    Completed int64
    Wanted    bool
}

type Item struct {
    ID        string   // AddRequest.ID
    NativeID  string   // infohash (lower hex) or usenet job id
    ClientRef string   // which client/worker owns it
    Protocol  Protocol
    Title     string
    Category  string

    Status  Status
    Stage   Stage
    Message string        // human text (Warning/Failed)
    Reason  FailureReason

    TotalBytes, RemainingBytes int64
    DownloadedBytes, UploadedBytes int64 // cumulative, persisted across restarts (torrent)
    DownloadRate, UploadRate int64       // bytes/s, sampled
    ETA *time.Duration

    // torrent
    SeedRatio *float64
    SeedTime  time.Duration
    Peers, Seeders, ConnectedPeers int
    IsPrivate bool

    // usenet
    Health         int32 // 0..1000 (NZBGet semantics)
    CriticalHealth int32
    ArticlesTotal, ArticlesFailed int64
    IsEncrypted bool

    OutputPath string  // path as the client sees it (remote path); "" until content exists
    ContentRoot string // file or dir that should be imported (torrent ContentPath equivalent)
    Files []FileInfo

    CanMoveFiles bool // usenet: true on Completed; torrent: only once seeding is finished
    CanBeRemoved bool
    Removed      bool

    AddedAt, StartedAt, CompletedAt, LastActivityAt time.Time
    Labels map[string]string
}

type ListFilter struct{ Category string; Status []Status; Protocol Protocol; Labels map[string]string }

type RemoveOptions struct{ DeleteData bool }

type ClientInfo struct {
    Name, Type string
    Protocol Protocol
    Host string                // key for remote path mappings
    IsLocal bool
    OutputRootFolders []string // for mapping validation
    RemovesCompletedDownloads bool
    HasPostImportCategory bool
}

type Client interface {
    Info(ctx context.Context) (ClientInfo, error)
    Add(ctx context.Context, req AddRequest) (nativeID string, err error)
    Get(ctx context.Context, id string) (*Item, error)
    List(ctx context.Context, f ListFilter) ([]Item, error)
    Pause(ctx context.Context, id string) error
    Resume(ctx context.Context, id string) error
    SetCategory(ctx context.Context, id, category string) error
    SetSeedCriteria(ctx context.Context, id string, c SeedCriteria) error // torrent; no-op for usenet
    MarkImported(ctx context.Context, id string) error   // post-import category / release hold
    Remove(ctx context.Context, id string, o RemoveOptions) error
    Close() error
}

// Optional: push instead of poll (torrent client emits on piece-state/complete; usenet on stage change).
type EventKind string
const (EventProgress EventKind = "progress"; EventStage = "stage"; EventCompleted = "completed"; EventFailed = "failed"; EventRemoved = "removed")
type Event struct{ Kind EventKind; Item Item; At time.Time }
type Watcher interface{ Watch(ctx context.Context) (<-chan Event, error) }

// Path translation, same semantics as *arr RemotePathMapping.
type PathMapping struct{ Host, RemotePath, LocalPath string }
func RemapRemoteToLocal(host, remote string, maps []PathMapping) string
func RemapLocalToRemote(host, local string, maps []PathMapping) string
```

Invariants (ported from *arr): `Status==Completed` implies `OutputPath != ""`; usenet `CanMoveFiles=CanBeRemoved=true` at Completed; torrent `CanMoveFiles=CanBeRemoved=false` while `Stage==seeding`, `true` at `seeding-done`; `Warning` never terminal; importer treats `Completed && !CanMoveFiles` as "hardlink/copy" and `Completed && CanMoveFiles` as "move". `Remove(DeleteData:true)` on a torrent must Drop before deleting.

### 4.1 CRD sketch (api/download/v1alpha1)

```go
type DownloadSpec struct {
    Protocol   Protocol        `json:"protocol"`
    Title      string          `json:"title"`
    Category   string          `json:"category"`
    Priority   int32           `json:"priority,omitempty"`
    Source     Source          `json:"source"`              // secrets via SecretKeySelector for cookies/headers
    Password   string          `json:"password,omitempty"`
    Seed       SeedCriteria    `json:"seed,omitempty"`
    Paused     bool            `json:"paused,omitempty"`
    WantedFiles []string       `json:"wantedFiles,omitempty"`
    ClientRef  string          `json:"clientRef,omitempty"` // DownloadClient CR name; empty = scheduler picks by protocol/priority/tags
    Usenet     *UsenetOptions  `json:"usenet,omitempty"`    // PropagationDelay, Precheck, Unpack, CleanupExts, DeleteSamples, EncryptedAction
    Torrent    *TorrentOptions `json:"torrent,omitempty"`   // Sequential, FirstLastFirst, MaxConns
    TTLAfterFinished *metav1.Duration `json:"ttlAfterFinished,omitempty"`
}
type DownloadStatus struct { // == download.Item minus Spec-duplicated fields, plus Conditions
    NativeID string; ClientRef string; Status Status; Stage Stage; Reason FailureReason; Message string
    Progress Progress{Total, Remaining, Downloaded, Uploaded int64; DownRate, UpRate int64; ETASeconds *int64}
    Torrent *TorrentStatus{Ratio float64; SeedTimeSeconds int64; Peers, Seeders int; InfoHash string; MetainfoRef string}
    Usenet  *UsenetStatus{Health, CriticalHealth int32; ArticlesTotal, ArticlesFailed int64; Par2Blocks{Needed, Available} }
    OutputPath, ContentRoot string; Files []FileInfo; CanMoveFiles, CanBeRemoved bool
    ObservedGeneration int64; Conditions []metav1.Condition // Accepted, Progressing, Completed, Importable, Failed
}
type DownloadClientSpec struct { // one CR per configured client (torrent engine pod set, usenet engine)
    Protocol Protocol; Priority int32; Categories map[string]CategoryPolicy /* rootPath, postImportCategory */
    Torrent *TorrentClientSpec{ListenPort int32; PublicIP string; Seed bool; MaxUpload, MaxDownload int64; DHT, PEX, UPnP bool; MaxActive int32; DefaultSeed SeedCriteria}
    Usenet  *UsenetClientSpec{Servers []NNTPServerRef /* host, port, tls, connections, priority/level, backup, retention, secretRef, quota */; PropagationDelay; Precheck bool; Par2 Par2Spec; Unpack UnpackSpec}
    PathMappings []PathMapping
    RemoveCompletedDownloads, RemoveFailedDownloads bool
}
```

### 4.2 Torrent implementation plan (`downloadarr/torrent`)

1. One `torrent.Client` per engine pod, config: `Seed=true`, `NoDefaultPortForwarding=true`, `ListenPort` from spec (hostPort or Service), `PublicIp4`, `DisableWebtorrent=true`, `Slogger`, `MaxUnverifiedBytes=256<<20`, `PieceHashersPerTorrent=2..4`, `Upload/DownloadRateLimiter` non-nil, `Callbacks.StatusUpdated` -> metrics, `DefaultStorage` unused.
2. `Add`: resolve `Source` (magnet -> `TorrentSpecFromMagnetUri`; URL -> HTTP GET with *arr redirect-to-magnet rule; bytes -> `metainfo.Load`), verify infohash vs `ExpectedInfoHash`, build `AddTorrentOpts{Storage: storage.NewFileOpts{ClientBaseDir: workdir}, DisallowDataDownload: req.Paused}`, `cl.AddTorrentSpec`, spawn a goroutine: `<-t.GotInfo()` (timeout -> `ReasonNoMetadata`), persist `t.Metainfo()` bytes to the CR/ConfigMap (or `<workdir>/.clustarr/<ih>.torrent`), apply `WantedFiles` via `File.SetPriority` (None for unwanted) else `DownloadAll()`; `t.SetOnWriteChunkError` -> Warning.
3. Poll loop (2 s): `Stats()`, `BytesCompleted()`, `PieceStateRuns()` (checking = any Hashing/QueuedForHash), `Complete().Bool()`; derive `Status/Stage`; accumulate deltas into persisted counters; evaluate `SeedCriteria` -> `DisallowDataUpload()` and `Stage=seeding-done`.
4. Restart/re-attach: on pod start list `Download` CRs bound to this client; re-add from persisted metainfo with the same workdir; part-file inference marks complete files done, partial ones are hashed; if `DisableInitialPieceCheck` is desirable for huge complete torrents use it only when `Status==Completed` was already recorded.
5. `Remove(DeleteData)`: `t.Drop(); <-t.Closed(); os.RemoveAll(workdir)`. Without DeleteData: drop only and keep files (importer already hardlinked).
6. Import handshake: torrent files are read-only + hardlinkable; `ContentRoot = filepath.Join(workdir, info.BestName())`; `Files` from `t.Files()` excluding `PiecePriorityNone`.
7. Scaling: shard torrents across engine pods by consistent hash of infohash; a torrent must live in exactly one pod (single writer). Keep per pod <= ~500 active torrents; `EstablishedConnsPerTorrent` 50 default is fine, lower to 30 on high counts.

### 4.3 Usenet implementation plan (`downloadarr/usenet`)

Pipeline per job (all stages idempotent, state in the Download CR + a small per-job manifest in the workdir):

1. **Grab**: fetch NZB (URL with indexer apikey or inline), parse with `nzbparser.ParseWithOptions{RemoveDuplicates:true}`, resolve password (meta `password`, `{{pw}}` in title), classify files (index par2 / vol par2 by regex, rar volumes by `.partNN.rar|.rNN|.rar`, others), compute totals, apply propagation delay (`min(file date)` vs now), optional pre-check (`StatMany` on the first segment of every file, then all segments if configured).
2. **Transfer**: for each file preallocate a sparse file `<workdir>/<filename>.part` (name from subject; renamed later); dispatch segments to `nntppool.Client.BodyStream(ctx, "<"+id+">", sectionWriter)` where the writer is an `io.NewOffsetWriter(f, meta.PartBegin-1)` set inside the `onMeta` callback (yEnc `=ypart begin`), record per-segment `CRC`/`CRCValid`, retry policy: nntppool already fails over across providers and backup providers; on `ErrArticleNotFound` after all providers mark the segment missing, update `ArticlesFailed`, recompute health/critical health, abort early if `missing blocks > recoverable par2 blocks` (`ReasonMissingArticles`). Download the first segment of every file first (NZBGet's "first articles") to enable early rename and par2 index parsing. Concurrency = sum of provider `Connections`; segments ~700 KB, so in-flight memory ~ connections * 1-2 MB.
3. **Rename**: once the first 16 KiB of a file exists, MD5 it and match `FileDesc.hash16k` from any downloaded `.par2` -> rename. Fallback at the end: SABnzbd heuristics (32-hex names, `[..][..]` + 30 hex, no case/space variety) -> rename largest file to job title + sniffed extension.
4. **Quick check**: combine segment CRCs (`crc32_combine`) per file; compare with IFSC-derived CRC + size; all good -> skip par2.
5. **Repair**: if any missing/bad: ensure enough recovery blocks (fetch more `.volNNN+MM.par2` files, `StageFetchingPar2`), exec `par2 r -t<N> -m<MB> <index>`, parse output; failure -> `ReasonRepairFailed`.
6. **Extract**: `rardecode.OpenReader(firstVolume, Password(pw))` streaming to `<workdir>/_UNPACK_/`; `ErrBadPassword`/`HeaderEncrypted` -> `IsEncrypted=true`, `ReasonEncrypted`; nested archives one level; `.7z` via sevenzip; then delete archives + `.par2` + cleanup list, drop samples/proof if configured.
7. **Publish**: atomic rename `_UNPACK_` -> final `<workdir>/<title>/`, set `OutputPath/ContentRoot`, `Status=Completed`, `CanMoveFiles=true`.

Worker model: transfer is I/O bound (one pod can saturate a 1 Gbit line with 20-40 connections), par2/unrar are CPU bound: split into two job kinds on the queue (`usenet.transfer`, `usenet.postprocess`) so post-processing can run on a different node/pool; both need the same workdir (see section 5). NNTP connection budget is per provider account, so run the transfer stage with a **single pool per provider account** (one pod, or a per-account lease) to avoid `502 too many connections`.

---

## 5. Storage recommendation

Options considered: (A) per-download working dir on a shared RWX PVC (NFS/CephFS/Longhorn-RWX); (B) node-local (RWO/hostPath/emptyDir) working dir + object store (S3/MinIO) hand-off; (C) hybrid: node-local scratch for usenet assembly/post-processing, shared RWX filesystem for finished content and for torrent data.

Facts that decide it:
- Torrents need random `pwrite` while downloading, random reads for the whole seed lifetime, and the importer wants hardlinks (same filesystem as the library) so seeding continues without duplicating data. Object stores cannot serve either (no partial writes, no hardlinks, no `mmap`/`pread` semantics without a FUSE gateway). Copying a finished torrent to S3 and back would double I/O and break seeding.
- Usenet assembly is heavy random writes into sparse files + par2/unrar rereads: NFS latency hurts (SABnzbd only auto-enables Direct Unpack above 40 MB/s disk speed; users report 3x slowdowns on NFS/RWX). Longhorn RWX is an NFSv4 share-manager pod (single throughput point); CephFS is better but still network. Node-local NVMe is the right place for `.part` files, par2 and extraction.
- `*arr` semantics require a single path namespace between downloader and importer (`OutputPath` + remote path mappings). One RWX volume mounted at the same path in downloader, importer and transcoder pods removes path mapping entirely (keep `PathMapping` in the API for external clients).
- Hardlinks work within one filesystem: library and completed downloads must be on the **same** PVC (use `subPath`s of one RWX volume: `/data/downloads/<category>/<id>` and `/data/library/...`), otherwise import degrades to copy.
- Bolt/sqlite piece-completion files and the anacrolix mmap backend must not live on NFS; part-file inference avoids the DB entirely.

Recommendation (C, "local scratch, shared finish"):
1. One RWX `StorageClass` (CephFS via Rook, or a real NFS appliance; Longhorn RWX acceptable for small setups) mounted at `/data` in all media pods. Layout: `/data/downloads/torrent/<category>/<downloadID>/<content>`, `/data/downloads/usenet/<category>/<downloadID>/<title>/`, `/data/library/<type>/...`, `/data/transcode/...`. Hardlink imports; move for usenet.
2. Torrent engine pods write directly to `/data/downloads/torrent/...` (sequential-ish 16 KiB chunk writes are tolerable on CephFS/NFS; throughput ceiling is the network anyway) with `UsePartFiles=true` and no completion DB. Use `EstablishedConnsPerTorrent` <= 30 and `MaxUnverifiedBytes` 128-256 MiB to keep NFS write queues bounded. If CephFS latency proves bad, fall back to a node-local RWO volume per engine pod and `rsync`-free publish by hardlink is impossible across volumes, so that fallback costs one copy per completed torrent (accept only if measured).
3. Usenet transfer + post-processing pods use node-local scratch (`emptyDir` on NVMe or a local-path RWO PVC sized for the largest release, e.g. 100-200 GB) for `.part`/par2/unrar, then **move the finished directory** to `/data/downloads/usenet/...` (single sequential copy, then `os.Rename` is not cross-device: copy to `_UNPACK_` on the RWX volume and rename atomically there). Scheduling: the postprocess job must land on the same node as the transfer job's scratch (node affinity from the transfer job's `status.nodeName`) or the queue payload carries the node name; with a per-job RWO PVC the scheduler enforces it.
4. No object store in the hot path. Optional later: S3 as archival/hand-off for a remote "seedbox" tier (finished, non-seeding usenet content only).
5. Free-space guard: check `Statfs` on both scratch and `/data` before accepting a job (`DiskSpace`/"Minimum Free Space" behaviour), and surface `ReasonDiskFull`.

---

## 6. Sources

- go doc / source of `github.com/anacrolix/torrent@v1.61.0` (config.go, torrent.go, storage/file-*.go), `github.com/javi11/nntppool/v4@v4.23.0`, `github.com/javi11/nzbparser@v0.5.5`, `github.com/mnightingale/rapidyenc` (both pseudo-versions), `github.com/javi11/rapidyenc`, `github.com/nwaples/rardecode/v2@v2.4.1`, `github.com/Tensai75/nntp@v0.1.5`, `github.com/akalin/gopar`, `github.com/bodgit/sevenzip@v1.6.5`, `github.com/go-newsgroups/nzb@v0.1.0`
- https://pkg.go.dev/github.com/javi11/nntppool/v4 , https://github.com/javi11/nntppool , https://pkg.go.dev/github.com/javi11/nzbparser , https://pkg.go.dev/github.com/mnightingale/rapidyenc , https://pkg.go.dev/github.com/nwaples/rardecode/v2 , https://pkg.go.dev/github.com/akalin/gopar , https://pkg.go.dev/github.com/javi11/par2go , https://pkg.go.dev/github.com/Tensai75/nntp
- https://github.com/anacrolix/torrent/issues/930 , /issues/634 , /issues/184 , /issues/1094 , /issues/364
- https://sabnzbd.org/wiki/extra/nzb-spec , https://sabnzbd.org/wiki/configuration/4.5/switches , https://sabnzbd.org/wiki/installation/par2cmdline-turbo
- https://github.com/sabnzbd/sabctools/blob/master/doc/yenc-draft.1.3.txt
- https://parchive.sourceforge.net/docs/specifications/parity-volume-spec/article-spec.html
- https://www.rfc-editor.org/rfc/rfc3977.html , https://www.rfc-editor.org/rfc/rfc4642.html , https://www.rfc-editor.org/rfc/rfc4643.html
- SABnzbd source (develop): sabnzbd/newswrapper.py, downloader.py, newsunpack.py, par2file.py, deobfuscate_filenames.py, directunpacker.py (via raw.githubusercontent.com and DeepWiki)
- NZBGet: https://raw.githubusercontent.com/nzbgetcom/nzbget/develop/nzbget.conf , DeepWiki nzbget/nzbget
- Radarr source (develop): src/NzbDrone.Core/Download/{DownloadClientItem.cs, DownloadItemStatus.cs, CompletedDownloadService.cs, TorrentClientBase.cs, Clients/Sabnzbd/Sabnzbd.cs, TrackedDownloads/TrackedDownload.cs}, src/NzbDrone.Core/RemotePathMappings/RemotePathMappingService.cs; Sonarr src/NzbDrone.Core/Download/Clients/QBittorrent/QBittorrent.cs; DeepWiki Radarr/Radarr, Sonarr/Sonarr, Prowlarr/Prowlarr
- https://raw.githubusercontent.com/Servarr/Wiki/master/radarr/settings.md
- https://github.com/animetosho/par2cmdline-turbo/releases (v1.5.0 static linux amd64/arm64)
- Longhorn RWX / CephFS notes: https://oneuptime.com/blog/post/2026-03-20-longhorn-readwritemany-volumes/view , https://autoize.com/clustered-readwritemany-filesystems-for-kubernetes-persistent-volumes/
