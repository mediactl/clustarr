# Torrent scratch storage and IndexerProxy settings

Date: 2026-09-30. Approved in conversation.

## 1. Torrent storage: incomplete, then move on finish

qBittorrent's "keep incomplete torrents in": a torrent downloads in a
working area and moves to the shared data volume once complete, then seeds
and is imported from there. It is the usenet engine's placement pattern
(`ScratchSpec`, ADR-0014) applied to torrents.

### API (`download.clustarr.io/v1alpha1`, `DownloadClient.spec.torrent`)

- `scratch` (`*ScratchSpec`, the usenet type as it is): `path`,
  `existingClaim`, `storageClassName`, `volumeName`, `accessModes`,
  `sizeLimit`, with its CEL exclusions.
- `publishDir` (string, `^/`, MaxLength 4096): where finished content lives,
  as `<publishDir>/<category>/<name>`; unset means `/data/torrents`, where
  torrents have always been written. It must be under the data mount (the
  controller refuses one that is not, as for usenet).
- **`scratch` unset keeps today's behaviour exactly**: a torrent downloads
  in `<publishDir>/<category>/<name>` and is never moved.
- CEL: `scratch.volumeName` requires `replicas == 1` (one PV binds one
  claim).

### Workload (the torrent StatefulSet)

| Placement | Torrent engine |
| --- | --- |
| `path` | a directory on `/data`, shared by every replica (names are unique) |
| `storageClassName` | `volumeClaimTemplates`: one claim per replica, `sizeLimit`, `accessModes` |
| `volumeName` | one controller-made claim bound to that PV (replicas 1) |
| `existingClaim` | mounted by every replica; ReadWriteMany when replicas > 1 (documented, not checkable at admission) |
| none of them | emptyDir with `sizeLimit` (lost with the pod) |

The scratch mount path is `/scratch` (`--scratch-dir`, as usenet), or
`scratch.path` itself. The engine config hash covers `scratch` and
`publishDir`, so a change rolls the engine.

### Engine

- A transfer downloads to `<scratch>/<category>/<name>`.
- When every wanted piece is verified, the transfer reports stage
  `publishing` (status Downloading) and the engine publishes it once:
  records the metainfo, drops the torrent, closes its storage, moves the
  directory with `fsops.MoveAtomic` (rename, or copy across volumes) to
  `<publishDir>/<category>/<name>`, and re-adds it with storage there. The
  per-transfer piece-completion database moves with the files, so nothing
  is hashed again. Selection, pause and seed criteria are re-applied, and
  it seeds from `/data`.
- `contentRoot`/`outputPath` then name the published directory, and only
  then does the transfer read Completed -- so the Download reaches
  Completed, and is imported, only after the move.
- Re-attach after a restart looks for `<publishDir>/<category>/<name>`
  first, then the scratch directory.
- A failed move is a local fault: `diskFull` (ENOSPC/EDQUOT) or
  `writeError`, never a blocklist. The engine keeps the scratch copy.
- Removal with data removes whichever directory the transfer is in.
- The re-attach state stays at `/data/torrents/.state`.

### UI

The DownloadClient form's Torrent section gains `torrent.publishDir` and
`torrent.scratch` with usenet's labels.

## 2. IndexerProxy in Settings

IndexerProxy becomes the tenth Settings kind, exactly as the nine
(`docs/superpowers/specs/2026-09-24-settings-crud-design.md`): type
(http / socks4 / socks5 / flaresolverr), host, port and request timeout;
username and password through the write-only Secret path with the key names
`app/indexer/proxy` reads; create, edit and delete; a Settings section
listing name, type, host:port and Ready. The label selector stays
kubectl-only: a proxy is attached through the Indexer form's existing Proxy
dropdown (`spec.proxyRef`). The ui role gains create, patch and delete on
`indexerproxies` through `actions.Grants()`, held to the generated role and
the chart's copy by the existing guards.

## Tests

- API: CEL (volumeName needs replicas 1; the ScratchSpec exclusions under
  torrent), defaults.
- Workload: every placement renders the right volume, claim template or
  claim; the mount and `--scratch-dir`; the hash covers both fields; a
  publishDir off the data mount is refused.
- Engine (real anacrolix, two in-process clients): a torrent with scratch
  completes, moves to publishDir, reports Completed with outputPath there,
  keeps seeding (a second leecher completes from it), and is not
  re-downloaded; re-attach finds a published torrent; a failed move is a
  local fault; scratch unset is unchanged.
- UI: the IndexerProxy form renders, creates with its Secret, and deletes;
  the role guard.
