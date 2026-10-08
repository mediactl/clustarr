# ADR-0017: No image ships or runs ffmpeg, ffprobe or par2; probing, decoding, subtitle extraction and par2 run as libraries

**Status:** Accepted, 2026-10-06 (supersedes the "PAR2 is an exec" decision recorded in
`hack/deps/deps.go` and `pkg/download/usenet/repair.go`). Recorded 2026-10-07. The ffmpeg
and ffprobe halves are built; the par2 half waits on a par2go release (see Consequences).

## Context

Until 2026-10-06 four paths ran external programs: ffprobe (two calls per probe,
`pkg/mediainfo`), ffmpeg for segment decoding (`pkg/segments/decode`) and for embedded
subtitle extraction (the caption fetch worker), and `par2 r` for usenet repair. The media
image carried BtbN's unpinned "latest" static ffmpeg and ffprobe and animetosho's par2 zip;
the transcoder image already ran FFmpeg 9 in-process through the ffgo fork (2026-10-01). The
owner asked for markers and transcode on the in-process pattern, for ffprobe to be converted to
ffgo, and for par2 to be par2cmdline-turbo used as a library (`.superpowers/unify/rulings.md`,
R2); converting embedded extraction followed, because the shared native image ships no ffmpeg
(OD8).

## Decision

- **Probe:** `pkg/mediainfo/native`, run by the import agent, which prints what ffprobe
  printed for every stored field, so `mediainfo.ProbeVersion` stays 3. The manager never
  probes: the remediation loop's probe planner queues a probe task
  (`CLUSTARR_WORK_PROBE`) and incorporates the import agent's `clustarr-probes` record.
- **Decode:** `pkg/segments/decode` rebuilds the demux, seek, decode and filter graph
  `ffmpeg(1)` built; the fingerprint cache is versioned (`segments.FingerprintVersion` 2).
- **Embedded subtitles:** `pkg/subtitles/providers/embedded/native`, FFmpeg's decoders and
  srt encoder in-process, run by the caption agent.
- **One runtime gate:** `pkg/ffruntime` loads FFmpeg 9 and the shim once, checks the shim's
  API (`MinShimAPI`), routes FFmpeg's log, and bounds every call by its context plus a grace,
  wedging the process at four abandoned calls (`MaxAbandoned`; the `ffgo` liveness check).
- **par2:** par2go (`github.com/mediactl/par2go`, `libpar2shim.so` through purego), run by the
  usenet engine in a short-lived child of the agent binary (`agent par2-repair`,
  `pkg/par2child`). `pkg/par2child/exec.go` is then the only production code that starts a
  process, and its only target is `os.Executable()`.
- **No test runs ffmpeg or ffprobe either** (owner, 2026-10-06): clips come from
  `internal/testmedia` on ffgo, and parity is judged against `test/data/media-goldens`,
  recorded once from the exec implementations before their deletion. The par2 CLI that
  `test/par2test` makes sets with is the one test-only program `make native-assets` supplies.

## Alternatives considered

- **Keep the CLI execs.** Executables in the images, an unpinned build, and a process per call.
- **par2 in the engine process.** A worker-thread exception or `abort()` in par2-turbo would
  kill the engine and crash-loop it through re-attach; some phases cannot be cancelled; repair
  memory sits outside `GOMEMLIMIT`; par2go runs one job per process (OD21).
- **`akalin/gopar`.** Too slow, and reads a whole set into memory.
- **cgo bindings.** Would tie markers and transcode to FFmpeg's and par2's headers and
  toolchains; purego keeps them `CGO_ENABLED=0`.
- **A separate `cmd/par2` binary.** Another binary and image path for a child the agent can be.

## Consequences

- purego in the agent; the native image carries `.so` files (FFmpeg, the ffgo shim, ONNX
  Runtime, and with par2go `libpar2shim.so`) and their notices (GPL-2.0-or-later for
  par2cmdline-turbo, combined under GPL-3.0, ADR-0002).
- `make native-assets` needs curl, xz, unzip and a C compiler (it compiles `libffshim.so`
  against the pinned FFmpeg); `make test` sets `CLUSTARR_REQUIRE_TOOLS=1`, so a missing native
  library fails rather than skips.
- FFmpeg's and par2's memory is outside `GOMEMLIMIT` (the import, caption and markers limits
  leave room for it: 60% and 50% shares). A stuck FFmpeg call can be abandoned but not killed;
  the `ffgo` liveness check restarts a wedged pod.
- An out-of-memory par2 run or a repair child that died without a verdict is a local fault
  (`writeError`), not a blocklist (OD22); an engine stopped mid-repair no longer blocklists a
  good release.
- Every binary that links ffgo maps FFmpeg at package init, before `main`.
- **Not yet built, 2026-10-07.** The par2 half (split plan Wave 9) waits on a par2go release:
  `v0.1.0`'s module zip is refused by Go's module tooling (a fixture name with an en dash), so
  `go.mod` does not require par2go. Until then the usenet engine still execs `par2`
  (`usenet.Par2Runner`, `pkg/download/usenet/repair.go`, the one production `os/exec`), the
  native image ships no `par2` (`images/distroless/stage.sh` refuses it), and so a transfer
  whose every article verified on receipt skips repair while one with missing articles fails
  as a local fault (`ErrPar2Unavailable`, `writeError`, never blocklisted). The test half
  (`internal/testmedia`, the goldens and the guard that no Go file runs ffmpeg) is in the
  test batch; today's tests still run the CLIs.

## Revisit triggers

- par2go or par2cmdline-turbo becoming exception-safe and cancellable in every phase: the
  child could go.
- ffgo's loader becoming lazy.
- A probe difference the fork cannot remove (OD16), which would force a `ProbeVersion` raise.
