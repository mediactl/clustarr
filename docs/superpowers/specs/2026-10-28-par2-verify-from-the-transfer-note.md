# par2 verify from what the transfer already proved (note, not built)

Owner, 2026-10-28: keep this idea on file. It is worth building only if par2
latency matters again: scratch back on NFS, or sets much larger than today's.

## What runs now

The usenet engine skips par2 entirely when every article's yEnc pcrc32
verified on receipt (`job.repair`'s quick check). That is about 88% of jobs:
in the week to 2026-10-28, 177 skipped and 25 repaired.

A job with failed articles runs par2go's whole `Repair`, which reads the set
twice:

1. **Verify.** Every byte of every file is hashed (MD5 and CRC32 per block,
   MD5 per file) to learn which blocks are intact. It is MD5-bound,
   `FileThreads` files at a time, one core each.
2. **Repair.** Every intact source block is read again as an input to the
   Reed-Solomon multiply, and the rebuilt blocks are written.

On NFS scratch the two reads made a 27.6 GB repair take 498 s. On talos-88's
NVMe, with `FileThreads` 8 (2026-10-28), verify is roughly a quarter of what
it was at par2go's default of 2.

## The idea

The engine already knows which byte ranges are intact. Each article covers a
known range of a known file, and it either arrived with a valid CRC or it did
not. With the slice size from the Main packet (`par2Set.SliceSize`):

- a block is intact when every article overlapping it verified;
- a file is intact when all of its articles verified, and its renamed name
  matches its FileDesc (`renameObfuscated`).

Verify could therefore be cut down to the files that hold a failed article.
For an intact file, the engine would supply the block map instead of par2
hashing it. Two ways to do that:

- **Narrow:** par2go's `Verify` takes a list of intact files and the shim
  marks them complete, with every block available at its own offset. par2
  still hashes the damaged files, so a hole is found as it is today. Repair
  is unchanged.
- **Wide:** also feed the repair's inputs from the transfer. That is the
  single-pass shape ParPar uses for creation. It needs par2cmdline-turbo's
  `Par2Repairer` to take an external availability map, which is a much
  larger patch.

## Why it is only a note

- Repair has to read every intact block in any case, so the narrow form
  saves at most one of the two passes, on the ~12% of jobs that need par2.
- On NVMe, with 8 file threads, that pass is seconds to tens of seconds per
  repair, against publishes to the NAS that take minutes.
- Trusting the transfer's CRCs over par2's MD5s gives up par2's check of
  bytes changed on disk after the CRC (a bad write, or a reader racing a
  writer). `verifyContent`'s size check covers only part of that.
- Both forms need a patch to par2cmdline-turbo and a shim ABI change in
  par2go.

Related: `docs/adr/0014-download-working-areas-on-the-shared-volume.md`
(as-built note, 2026-10-28), `pkg/download/usenet/rename.go`,
`app/grab/engine/usenet/repairer.go` (`par2FileThreads`).
