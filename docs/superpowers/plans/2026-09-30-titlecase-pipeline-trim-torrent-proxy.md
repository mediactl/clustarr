# Title case, pipeline trimming and torrent SOCKS5 proxy — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** English book titles in title case; the pipeline page keeps in-flight entries plus the last 100 results; a proxied torrent engine sends every byte and every public DNS query through a SOCKS5 proxy, UDP included.

**Architecture:** a pure `pkg/textcase` applied at the gateway's Book render; `pipeline.Trim` plus a settled time in `pipeline.Project`, applied by the ui projection; `pkg/socks5` (CONNECT dialer, UDP ASSOCIATE PacketConn, name-routing DNS resolver) wired into `pkg/download/torrent.New` from a new `TorrentSpec.Proxy`.

**Tech Stack:** Go, controller-runtime, anacrolix/torrent v1.61.0 + anacrolix/utp v0.1.0 + anacrolix/dht/v2, golang.org/x/net/proxy and dns/dnsmessage, templ.

**Spec:** `docs/superpowers/specs/2026-09-30-titlecase-pipeline-trim-torrent-proxy-design.md`

## Global Constraints

- GPL header (`hack/boilerplate.go.txt`) on every new Go file; testify; table-driven.
- Gate: `env -u KUBEBUILDER_ASSETS -u CLUSTARR_PG_ASSETS go test ./...` and `make lint` (envtest skipped by the owner's instruction).
- Commit with a pathspec (`git commit -m ... -- paths`); never `git stash`; never push from a task.
- `make generate manifests` after any `api/` change; chart RBAC/CRDs follow their existing copy rules.
- Pointer + accessor for every defaulted bool (typed-client gotcha).
- Never touch the `frugal` DownloadClient on the cluster.

## Review Focus

1. A public name reaching cluster DNS (search-domain expansion, trailing dot, upper case) — must go through the proxy.
2. A UDP datagram from a source other than the relay, or with FRAG≠0 — dropped, never delivered.
3. The proxy dropping the UDP control connection — association re-established, no panic, no direct fallback.
4. A settled entry older than X but newer by settle time than by creation — ordering by settle time, not creation.
5. A title with an acronym, a hyphen, an apostrophe and a colon — each rule at once.

---

### Task 1: `pkg/textcase` and Book titles

**Files:** Create `pkg/textcase/title.go`, `pkg/textcase/title_test.go`. Modify `app/catalog/metadata/patch.go` (`buildBookMetadataAC`), `app/catalog/metadata/patch_test.go`.

**Produces:** `textcase.Title(s string) string`.

- [ ] Test `TestTitle` table: "the brothers karamazov"→"The Brothers Karamazov"; "The Lock And Key Library"→"The Lock and Key Library"; "notes from the underground"→"Notes from the Underground"; "what we talk about: a life"→"What We Talk About: A Life"; "my uncle's dream"→"My Uncle's Dream"; "self-portrait in a convex mirror"→"Self-Portrait in a Convex Mirror"; "NASA and the iPhone"→"NASA and the iPhone"; "letters vol ii"→"Letters Vol II"; "the civil mix"→"The Civil Mix"; "1984"→"1984"; "war and peace – part one"→"War and Peace – Part One"; "  of mice  and men "→"  Of Mice  and Men " (whitespace kept); "Beyaz Geceler"→"Beyaz Geceler"; "" → "".
- [ ] Run, watch fail (package missing). Implement: tokenize on whitespace keeping separators; per word strip leading/trailing punctuation; rules per spec §1. Run, pass.
- [ ] Test `TestBookMetadataTitleCase`: `buildBookMetadataAC(&Book{Title:"the idiot"})` → "The Idiot"; `Languages:["en","fr"]` → cased; `Languages:["tr"]` with "beyaz geceler" → unchanged. Fail, implement `bookTitle(b)` helper in patch.go, pass.
- [ ] Commit `feat(catalog): English book titles in title case`.

### Task 2: pipeline trimming

**Files:** Modify `pkg/pipeline/project.go`, `pkg/pipeline/stage.go`, create `pkg/pipeline/trim.go` + `trim_test.go`; modify `pkg/pipeline/project_test.go`, `ui/projection/projection.go` (+test), `cmd/clustarr/services.go`, `cmd/clustarr/all.go`, `charts/clustarr/values.yaml`, `charts/clustarr/templates/deployments.yaml`, `config/manager/ui.yaml`.

**Produces:** `func (s Stage) Settled() bool`; `func Trim(entries []Entry, keep int) []Entry` (in-flight first in input order, then settled newest-`Since` first, at most `keep`); `projection.New(r, interval, opts ...Option)`, `projection.WithPipelineHistory(n int) Option`, `projection.DefaultPipelineHistory = 100`; `buildUIProjection(ctx, reader, history int)`.

- [ ] `TestStageSettled` (every Stage constant classified per spec §2). `TestTrimKeepsInFlightAndNewestSettled` (5 in-flight + 150 settled, keep 100 → 105, settled sorted by Since desc, in-flight all present), `TestTrimZeroKeepsOnlyInFlight`. Fail, implement, pass.
- [ ] `TestProjectSettledSinceIsLastActivity`: an item created 2020 with a Download imported yesterday and a MediaFile created yesterday reads Complete/Imported with Since = yesterday's newest; an idle item keeps its creation. Fail; implement by wrapping Project's body (`project` unexported) and setting `Since = settledAt(item, related)` when `entry.Stage.Settled()`. Pass.
- [ ] `TestProjectionTrimsPipeline` in ui/projection with a fake reader of 3 settled items and history 2 → 2 entries. Implement option; `project()` calls `pipeline.Trim(entries, p.history)` after the sort.
- [ ] Flags: `--pipeline-history` (ui, default `projection.DefaultPipelineHistory`), `--ui-pipeline-history` (all); chart `ui.pipelineHistory: 100` → `--pipeline-history=%d`; `config/manager/ui.yaml` args gain `--pipeline-history=100`. Run `go test ./cmd/clustarr/ ./ui/...` (parity and wiring tests).
- [ ] Commit `feat(ui): the pipeline keeps in-flight entries and the last 100 results`.

### Task 3: `pkg/socks5` — CONNECT dialer and UDP ASSOCIATE

**Files:** Create `pkg/socks5/socks5.go`, `udp.go`, `socks5_test.go`, `udp_test.go`, `server_test.go` (a minimal RFC 1928 server used only by tests: no-auth + user/pass, CONNECT, UDP ASSOCIATE with a real relay socket).

**Produces:**
```go
type Proxy struct{ Addr, Username, Password string; Forward proxy.ContextDialer }
func (p Proxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) // CONNECT, hostnames unresolved
func (p Proxy) URL() *url.URL // socks5://[user:pass@]addr
func (p Proxy) ListenPacket(ctx context.Context) (*PacketConn, error) // UDP ASSOCIATE; ErrUDPUnsupported when refused
type PacketConn // net.PacketConn; re-associates when the control connection drops
var ErrUDPUnsupported = errors.New("socks5: proxy refused UDP ASSOCIATE")
```

- [ ] Tests against the in-test server (fixture built from RFC 1928, not from the client): `TestDialContextSendsHostnameUnresolved`, `TestDialContextWithUsernamePassword`, `TestPacketConnRoundTrip` (echo UDP server behind relay), `TestPacketConnDropsForeignSourceAndFragments`, `TestPacketConnUnspecifiedRelayUsesProxyHost`, `TestListenPacketRefusedIsErrUDPUnsupported`, `TestPacketConnReassociatesAfterControlDrop`. Fail, implement, pass. TCP uses `golang.org/x/net/proxy.SOCKS5`.
- [ ] Commit `feat(socks5): CONNECT dialer and UDP ASSOCIATE packet conn`.

### Task 4: name-routing DNS resolver

**Files:** Create `pkg/socks5/resolver.go`, `resolver_test.go`.

**Produces:** `func (p Proxy) Resolver(dnsServer string, cluster func(ctx context.Context, network, addr string) (net.Conn, error)) *net.Resolver`; `func IsClusterName(name string) bool`.

- [ ] `TestIsClusterName` (nats, x.svc, a.b.svc.cluster.local., FOO.CLUSTER.LOCAL, tracker.org, tracker.org.appkins.io, x.local). `TestResolverRoutesByName`: fake cluster DNS and fake public DNS (behind the test SOCKS server) each answering a distinct A record; `LookupHost("nats")` → cluster; `LookupHost("tracker.example.org")` → public, via CONNECT to dnsServer. `TestResolverCachesByTTL`: two lookups, one query upstream. Fail, implement (net.Pipe server speaking DNS-over-TCP framing; `dnsmessage` parse), pass.
- [ ] Commit `feat(socks5): resolver that sends public names through the proxy`.

### Task 5: API, engine wiring, UI

**Files:** Modify `api/download/v1alpha1/downloadclient_types.go` (+ generated), `config/crd`, `pkg/download/torrent/client.go` (+test), `app/grab/run.go`, `app/grab/engine/torrent/reconciler.go` (http client), `app/grab/controller/downloadclient/workload.go` (start hash includes proxy + Secret data), `ui/forms/kinds.go`, `ui/views/downloads.templ`, `CLAUDE.md`, `go.mod` (anacrolix/utp direct).

**Produces:** `TorrentProxy` type + accessors; `dltorrent.Config.Proxy *ProxyConfig{Proxy socks5.Proxy; PeerConnections, UDP bool; Events func(reason, msg string)}`.

- [ ] API: fields per spec table; `make generate manifests`; `TestTorrentProxyDefaults` for accessors. `go test ./pkg/crdcheck/`.
- [ ] `pkg/download/torrent`: `TestNewWithProxyOpensNoLocalSocket` (listeners none except announce-only; config flags), `TestProxiedClientDialsPeersThroughProxy` (two anacrolix clients: a seeder direct, the leecher through the in-test SOCKS server; leecher completes a small torrent, server saw the CONNECT), `TestProxiedClientWithoutUDPSupportReportsEvent`. Fail, implement, pass.
- [ ] grabarr: build `ProxyConfig` from spec + Secret (read by name via direct client), install `net.DefaultResolver` when `hostnameLookup`, engine readyz includes a proxy greeting check, `.torrent` fetch http client uses `Proxy.URL()`; workload start hash covers proxy and its Secret data (`TestEngineTemplateHashCoversProxy`).
- [ ] UI: Proxy group + secret in kinds.go (`TestDownloadClientFormHasProxy` in ui/forms), downloads client row "via SOCKS5 host:port (UDP)" (view test). `make css` only if classes change.
- [ ] CLAUDE.md paragraph (UI section / Gotchas). Commit `feat(grab): SOCKS5 proxy for the torrent engine, UDP and DNS included`.
