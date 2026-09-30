# Book title case, pipeline trimming and the torrent SOCKS5 proxy

Date: 2026-09-30. Approved in conversation ("Approve all"), after a live probe
of the owner's proxy.

## 1. Book titles in title case

`pkg/textcase.Title(s string) string` applies English title case:

- the first word, the last word and the first word after `:`, `—`, `–` or
  ` - ` are capitalised;
- articles, short conjunctions and short prepositions (a, an, the, and, but,
  or, nor, for, so, yet, as, at, by, in, of, off, on, per, to, via, vs, from,
  into, onto, upon, with, than) are lowercased
  anywhere else, even when the source capitalised them ("The Lock And Key" →
  "The Lock and Key");
- a word with a capital after its first letter (NASA, McCarthy, iPhone) or a
  digit is left exactly as it is;
- each part of a hyphenated word is a word: the first capitalised, the rest
  by the same rules ("Self-Portrait", "Man-of-War", "World-War-II");
- a full stop, semicolon, `?` or `!`, and an opening bracket or quote, start
  a new title too ("… Man. The Meek One", "The Adolescent (A Raw Youth)");
- a title in capitals throughout is cased from lower case ("WAR AND PEACE"
  → "War and Peace"), and an Irish O' capitalises the name ("O'Brien");
- the first letter after an apostrophe is not capitalised ("uncle's" →
  "Uncle's");
- a strict Roman numeral (I–XXXIX, CL…: `^(x{0,3})(ix|iv|v?i{0,3})$`, not
  empty) is uppercased, so "volume ii" → "Volume II" but "mix", "civil" are
  words;
- whitespace and punctuation are kept byte for byte.

The metadata gateway applies it in `buildBookMetadataAC` to `status.metadata.title`
when the book is English or untagged (`Book.Languages` empty or containing
`en`). Book names are the Open Library work id (`author.BookName`), so no
object is renamed. It runs on every render, cached answers included; the 42
Books on kind-cluster-plex are refreshed after the deploy with
`clustarr.io/refresh-metadata`.

## 2. Pipeline trimming

The pipeline page listed one entry per catalog item (≈16,000) ordered by the
item's creation time. It keeps instead:

- every **in-flight** entry, always: MetadataSearching, ReleaseSearching,
  ReleaseSelected, Downloading, Downloaded, Importing, SubtitleSearching,
  SubtitleFound, SubtitleFetching, Transcoding;
- the newest **X settled** entries: Complete, Imported, SubtitleDone,
  TranscodeDone, MetadataFound, MetadataSynced, Failed, Blocked.

Failed and Blocked are settled, not pinned: 1,429 SubtitleRequests on the
owner's cluster are Blocked, which would have pinned ~1,430 rows (a change
from the conversation's "always keep", found in the live data).

A settled entry's `Since` becomes when it settled: the newest of its
Downloads' `importedAt`, `completedAt` and creation, its TranscodeJobs'
`finishedAt`, its SubtitleRequests' items' `downloadedAt`, its MediaFile's
creation and its Search's creation; the item's creation when it has none.
Settled entries are ordered by that, newest first, and in-flight entries
keep their own `Since`. The page shows in-flight entries first, then
settled.

X is `--pipeline-history` on `clustarr ui` (`--ui-pipeline-history` on
`clustarr all`), default **100** (two pages at the default 50 per page);
0 shows in-flight entries only. The chart sets it from `ui.pipelineHistory`.
The full record lives in the history stream; nothing in the cluster is
deleted.

## 3. SOCKS5 proxy for torrent traffic

The owner's proxy is Mullvad's SOCKS-through-WireGuard server at
`10.64.0.1:1080`: no authentication; resolves hostnames; UDP ASSOCIATE works
(relay `10.64.0.1:<port>`, a DNS query relayed to 1.1.1.1 answered); CONNECT
and UDP to `10.64.0.1:53` are refused by its ruleset; pods reach it.

### API

`DownloadClient.spec.torrent.proxy` (optional; absent means no proxy):

| Field | Type | Default | qBittorrent |
| --- | --- | --- | --- |
| `type` | enum `socks5` | `socks5` | Type |
| `host` | string, required, MinLength 1 | | Host |
| `port` | int32 1–65535, required | | Port |
| `secretRef` | LocalObjectReference, optional; keys `username`, `password` | | Authentication |
| `hostnameLookup` | *bool | true | Perform hostname lookup via proxy |
| `dnsServer` | string `host:port` | `1.1.1.1:53` | — |
| `peerConnections` | *bool | true | Use proxy for peer connections |
| `udp` | *bool | true | — (libtorrent's UDP ASSOCIATE) |

Pointer booleans with accessors (`TorrentProxy.HostnameLookupOrDefault` …),
per the typed-client default gotcha.

### Engine behaviour with a proxy

- No local listener: anacrolix's `DisableTCP`, `DisableUTP` and `NoDHT` are
  set, so it opens no socket; `AcceptPeerConnections` is false. A
  never-accepting listener carrying `listenPort` is added so announces do
  not send port 0.
- WebTorrent is disabled (WebRTC's STUN/ICE cannot be proxied).
- HTTP(S) trackers, webseeds, metainfo sources and the engine's own
  `.torrent` fetches use a `socks5://` proxy URL, which sends hostnames to
  the proxy unresolved.
- `peerConnections`: a TCP peer dialer over SOCKS5 CONNECT is added. When
  false, peers dial directly (TCP and uTP sockets opened as without a proxy,
  incoming still off).
- `udp` (with `peerConnections`): `pkg/socks5` opens UDP associations; one
  carries a DHT server (`Client.NewAnacrolixDhtServer` + `AddDhtServer`),
  one an outgoing uTP socket (`anacrolix/utp.NewSocketFromPacketConn`,
  added as a dialer of network `udp`), and `TrackerListenPacket` opens one
  per UDP tracker client. When the proxy refuses UDP ASSOCIATE, the three
  stay off and the engine emits a Warning Event `ProxyUDPUnavailable` on the
  DownloadClient. Nothing falls back to a direct socket.
- A lost association's control connection re-associates in the background;
  datagrams written meanwhile are dropped (UDP semantics).

### DNS

With `hostnameLookup`, the engine process installs a resolver
(`net.DefaultResolver`, Go resolver) that routes each query by the name in
its question:

- a cluster name — no dot, or ending in `cluster.local`, `.svc` or `.local`
  — goes to the nameserver the resolver was asked to dial (cluster DNS);
- every other name goes over TCP, through a SOCKS5 CONNECT, to `dnsServer`.

Public answers are cached for their TTL (at least 30 s, at most 1 h;
negative answers 30 s), since anacrolix resolves a UDP tracker's host on
every packet. Search-domain expansions (`…appkins.io`) are public names and
go through the proxy too. The resolver exists only in a proxied torrent
engine's process.

### Reporting

The engine's readiness requires a SOCKS5 greeting to succeed at start and
fails while the proxy is unreachable, so `EngineReady` on the DownloadClient
reflects it; UDP refusal is the `ProxyUDPUnavailable` Event. (The
conversation named a `ProxyReady` condition: DownloadClient status has one
writer, the grabarr controller, and the engine cannot see the proxy from
there, so readiness plus an Event replace it.)

A change to `spec.torrent.proxy` or its Secret's data rolls the engine pods,
as start-time settings already do.

### UI

The download client form gains a Proxy group in the torrent section with
qBittorrent's labels; username and password go through the existing Secret
path (`torrent.proxy.secretRef`, both optional). The downloads page's client
section shows "via SOCKS5 host:port" and "(UDP)" when `udp` is on.

### Out of scope

Indexer traffic (indexarr's `.torrent` fetches through an Indexer) keeps the
Indexer's own IndexerProxy. Incoming connections through SOCKS5 do not
exist (BIND is one connection; Mullvad has no port forwarding). HTTP and
SOCKS4 proxy types.
