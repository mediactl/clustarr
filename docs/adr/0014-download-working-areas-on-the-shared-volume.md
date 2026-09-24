# ADR-0014: Download working areas live on the shared data volume, not on node-local scratch

**Status:** Accepted, 2026-09-24

## Context

Both download engines need somewhere to write while a transfer runs and
somewhere to put the result the importer reads. The design (§6.3, §12) put
the torrent engine's transfers on the shared RWX data volume from the start
and gave the usenet engine a node-local scratch area (an emptyDir, or a
node StorageClass), after the research note's finding that yEnc assembly
is heavy random I/O and NFS latency triples it.

The first real usenet grab on the owner's single-node kind cluster
(2026-09-24) showed what the emptyDir costs:

- **It does not survive the pod.** A deploy replaced the engine pod while a
  finished 9.4 GB transfer sat on `/scratch` waiting for a fix; the new pod
  found nothing and downloaded the release again. The engine checkpoints
  its article bitsets every two seconds precisely to resume, and had
  nowhere to resume from.
- **It shares the node disk with etcd.** On kind, etcd's data directory,
  containerd and every emptyDir sit on one filesystem. Writing 10 GB at
  50 MB/s and then reading it back for par2 pushed etcd's WAL fsyncs past
  a second; the grabarr controller lost its leader lease and exited, and
  on the second run the kube-apiserver failed its liveness probe and was
  killed. A control plane that shares a disk with bulk I/O is not a
  production shape, but a homelab node is exactly where clustarr runs.

The owner's library is one NFS export (`/volume2/data`) already laid out
the TRaSH way: `media/`, `torrents/` and `usenet/{incomplete,complete}` on
one filesystem.

## Decision

The usenet engine's working area is placed by `spec.usenet.scratch`, and
finished content by `spec.usenet.publishDir`, and the recommended
placement is a directory on the shared data volume:
`scratch.path: /data/usenet/incomplete`, `publishDir: /data/usenet/complete`.
The engine then mounts no scratch volume at all; a transfer, its manifest
and its checkpoints survive a pod restart; publishing is one `rename(2)`
on the same filesystem; and the node disk carries none of it.

The other placements stay for clusters that have a fast local volume to
give: `existingClaim` (a claim the operator made, RWX or RWO),
`storageClassName` (a claim the controller makes), `volumeName` (that
claim bound statically to an existing PersistentVolume, with the
`accessModes` asked, ReadWriteMany for NFS). The emptyDir remains the
default only because a nil spec must mean something; every installer
example sets a path.

The single shared volume stays the pattern for both engines:

- **Torrents need it.** A seeding torrent must keep its files where the
  engine put them while the importer hard-links them into the library;
  a hard link needs one filesystem, and a copy doubles the space for as
  long as the torrent seeds. The torrent engine already writes
  `<data>/torrents/<category>/<download>` and keeps its re-attach state
  under `<data>/torrents/.state`; nothing changes there.
- **Usenet does not need it for linking** -- a usenet download is moved,
  not seeded -- but it needs it for resume and for keeping bulk I/O off
  the node, and the atomic rename into `complete/` and the importer's
  hard link into `media/` both want the same filesystem.
- **Assembly over NFS is slower** than on NVMe, as the research note
  measured. That is the price. A cluster with a real local volume (and a
  control plane on its own disk) can hand the engine one through
  `storageClassName` or `existingClaim` and take the copy into `complete/`
  at publish time instead.

Caveats that come with NFS: every clustarr pod and the NAS must agree on
the uid/gid or on a `squash` export option (the owner's library is
1024:100 while clustarr runs as 1000:1000); hard links work only inside
one export, which is why `media/` and the download directories must be
under the same one; and anacrolix's piece-completion database stays off
NFS (§12), as it always has.

## Alternatives considered

- **Keep the emptyDir and copy into `/data` at publish time (the design's
  original shape).** Fastest assembly, but no resume, and on a
  single-node cluster it is the etcd-starving shape above.
- **A node-local PersistentVolume for scratch (local-path on kind).**
  Resume works, assembly stays fast, but the disk is still etcd's on kind,
  and the publish is a cross-filesystem copy of the whole release.
  Available through `storageClassName` for clusters where it fits.
- **Separate volumes for `incomplete/` and `complete/`.** Two claims and a
  copy between them; nothing gained over one directory tree on one
  filesystem, and hard links across them are impossible.
