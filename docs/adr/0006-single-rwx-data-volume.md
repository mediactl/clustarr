# ADR-0006: Storage is one RWX volume at `/data`, TRaSH layout, hardlink-else-copy

**Status:** Accepted, 2026-09-18

## Context

Media files move through several services. grabarr writes a completed download; catalogarr's
importer picks it up, decides the destination path and moves it into a root folder; squasharr
reads the imported file and writes a transcoded replacement; captionarr writes sidecar subtitles
next to it. Every one of those steps runs in a different pod, possibly on a different node.

Two properties of the *arr ecosystem are non-negotiable if the import semantics are going to match
what users expect. First, hardlinking: importing a torrent must not duplicate the bytes, because
the file has to stay in place for seeding while also appearing in the library. Second, atomic
rename: the final move into the library must be atomic, so that a media server scanning
concurrently never sees a partial file. Both properties are filesystem-local — a hardlink cannot
cross a filesystem boundary, and a rename is only atomic within one.

## Decision

A single ReadWriteMany volume mounted at `/data` in every service that touches media, with the
downloads area, the root folders, the transcode scratch area and the recycle bin all inside it.
Layout follows TRaSH Guides conventions. Imports hardlink when source and destination share a
filesystem and fall back to a copy when they do not; the final placement is always an atomic
rename within the destination directory. No object store is used for media.

Root folders declare their own naming scheme, permissions (`chmod`/`chown` semantics) and
recycle-bin retention, and deletions go to the recycle bin rather than to `unlink`.

## Alternatives considered

**Per-service volumes with explicit transfer between them.** Clean isolation, and it works with
RWO storage classes, which are far more widely available. Rejected because it makes hardlinking
impossible by construction — every import becomes a full copy of the file, and a seeding torrent
costs double the space for as long as it seeds.

**S3-compatible object storage for media.** Attractive for durability and for scaling beyond one
filesystem. Rejected: there are no hardlinks, no atomic rename within a prefix, and ffmpeg wants a
seekable file — every transcode would begin with a full download and end with a full upload. The
object store is used, at most, for small artefacts: subtitle blobs, ffprobe JSON.

**Volume per root folder, with the downloads area separate.** A middle position that preserves
hardlinks within a library but not between download and library, which is exactly the boundary
where hardlinking matters most.

## Consequences

RWX is a hard requirement, which constrains storage backends: NFS, CephFS, Longhorn RWX or
similar. The chart warns when the configured storage class is not RWX because the failure mode
otherwise is silent — imports quietly degrade to copies and disk usage doubles.

RWX semantics must actually hold for our two operations on the target storage. Hardlink support
and rename atomicity vary between NFS implementations and versions, so they are validated against
the real backend before a release rather than assumed. The anacrolix torrent engine has known
sharp edges on NFS specifically, which is one of the tracked risks.

One volume also means one failure domain and one capacity pool: no per-service quota, and a
runaway transcode scratch directory can fill the library's space, so scratch paths are per-job and
cleaned on completion.

## Revisit triggers

Chunked transcoding, which needs a dedicated RWX scratch area with its own lifecycle
(`/data/.transcode/<uid>/`) and raises the concurrency on that path considerably; a deployment
target where RWX is genuinely unavailable, which would mean accepting copy-on-import and
documenting the space cost; or media libraries outgrowing a single filesystem, which would force
per-root-folder volumes and the loss of cross-folder hardlinks.
