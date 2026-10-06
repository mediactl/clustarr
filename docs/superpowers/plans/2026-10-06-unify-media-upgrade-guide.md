# Upgrade guide: bringing the 2026-10-06 media-runtime decisions into `unify-manager-agent`

Date: 2026-10-06. For: whoever executes
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md` on the
`unify-manager-agent` branch (worktree `/home/appkins/src/mediactl/clustarr-unify`,
spec at `4b103429`).

## Why this exists

A session on `main` was asked by the owner to remove ffmpeg, ffprobe and par2
from clustarr: `Dockerfile.media` should carry only the libraries ffgo and
par2go need, and **all** ffmpeg/ffprobe usage must move to ffgo. Exploring
that request showed the branch's spec already designs nearly all of it (R2, R3,
§6 to §8, §10.1.3). The owner then ruled that the binary topology waits on this
branch, and asked for this guide instead of a separate plan on `main`.

So this is a delta. Section 1 lists what the branch already gets right and
needs no change. Section 2 lists what must change, each with the owner
decision or new fact behind it and the spec sections it touches. Section 3 is
the order of work, section 4 a checklist.

Sources: the owner's answers in that session (2026-10-06); par2go at
`github.com/mediactl/par2go` (main `bc4c4f2`, tag `v0.1.0`); the branch's spec.

## 1. Already aligned (no change)

- **Topology.** The owner deferred the binary split to this branch. Its R1
  (static `clustarr` image for manager and ui; FROM-scratch `native` image for
  agent, markers and transcode; `Dockerfile.media` and `cmd/clustarr` retired)
  answers the question the `main` session left open. Nothing on `main` builds
  a competing split.
- **No external programs at run time (R2), probe placement (R3), markers
  decode (§7.2), embedded subtitles (§7.3), `ffruntime` (§7.4), par2 as a
  library in a repair child (§8, `pkg/par2child`).** The `main` session's
  design reached the same shapes: a seam per call site, ffgo implementations
  outside the static binary, SRT written in Go, bitmap subtitles unsupported,
  `AnalyzerVersion` raised if decode output changes, `ProbeVersion` raised and
  `probeHash` untouched if a probe field cannot match.
- **par2go basis.** The spec's basis `1bd94eb` already has everything the
  library does: ABI 2 (each job on the shim's own thread; `p2_free` cancels and
  joins), Loading/Verifying/Repairing phases with whole-scan verify progress
  (patch 0002), UTF-8 names kept (patch 0001), damaged originals restored after
  a stopped repair, a finished repair reported as finished despite a late
  cancel, a cgroup-aware memory default, and `libpar2shim.so` exporting only
  `p2_*`.

## 2. Changes

### U1. par2go is published: pin the release, drop the local replace (resolves OD19)

**Fact.** `github.com/mediactl/par2go` is public. Tag **`v0.1.0`** (commit
`159eebd`) has a GitHub release whose assets are
`libpar2shim-linux-amd64.so`, `libpar2shim-linux-arm64.so` and `SHA256SUMS`:

```
39364599a57f6b7bb36b4532200ab5741213fbab0476c2399d0a3e091a38de2c  libpar2shim-linux-amd64.so
22f65e291e3dca59b70906194996af658abf41a53626d05b34bba35104589389  libpar2shim-linux-arm64.so
```

Both were built in the digest-pinned `debian:bookworm` image with apt from
snapshot.debian.org, tested on their own architecture before upload, and
re-verified after publishing.

**`v0.1.0` against the basis `1bd94eb`.** Same Go API, same ABI 2, same
patches 0001 and 0002. The tag's tree also briefly carried a `libpar2.so` C++
library target and a header patch 0003. The owner withdrew both: the
`libpar2.so` assets were deleted from the release, and `main` (`bc4c4f2`)
removed the target and the patch. 0003 only reorders includes in
`libpar2.h`, so the shim assets built with it behave identically. If an exact
tree-to-asset match matters, ask the owner for `v0.1.1` at `bc4c4f2` (OD19
follow-up); otherwise pin `v0.1.0`.

**Edits.**
- **go.mod.** `require github.com/mediactl/par2go v0.1.0`. Delete the
  `replace … => ../par2go` (R12, §7.6 step list) and its `ci.yml` local-replace
  failure for par2go (§10.1.7). ffgo keeps the R12 treatment until OD18.
- **§10.1.3 `par2` stage.** Replace the cmake/g++ build of `shim/build.sh` with
  a download of `libpar2shim-linux-${TARGETARCH}.so` from the `v0.1.0` release,
  checked against the two digests above. Install it at `/out/usr/lib/libpar2shim.so`.
  Drop the `par2go` named context (§10.1.5) and the `git archive` export.
- **§10.1.6 `make native-assets`.** Download the same asset and check it,
  instead of exporting `$(PAR2GO_REF)` and building. The script comment at
  spec line 4312 already anticipates this.
- **§13.** Mark OD19 resolved (published 2026-10-06, `v0.1.0`).

### U2. No ffmpeg or ffprobe in tests either (owner decision, 2026-10-06)

**Decision.** Asked whether "all usage of ffmpeg or ffprobe" includes the test
suites and the e2e fixtures image, the owner chose **production and tests**: no
test may run the ffmpeg or ffprobe CLI, fixture clips are generated with ffgo
or committed as small files, and CI drops its ffmpeg install.

The spec currently keeps a real ffmpeg for tests:
- §4.5.6's guard scans only non-test files and leaves `hack/` and `test/` out
  ("which is why the parity oracles live under `test/`");
- §7.2.10's decode parity "runs only when `ffmpeg` is on PATH", with
  `make native-assets` supplying one (spec line 3427);
- §10.1.7 turns CI's "Install ffmpeg and par2" into `make native-assets`;
- §10.1.7/§10.1.8 leave `e2e-fixtures` "unchanged", and it apt-installs ffmpeg.

**Edits.**

1. **`internal/testmedia`**, a Go package on ffgo that writes deterministic
   clips:
   - H.264 and HEVC video, 8- or 10-bit, SDR or HDR10 with mastering-display
     and content-light side data and colour tags;
   - AAC and AC-3 audio as sine tones, with language tags;
   - SubRip and ASS text subtitle streams with languages;
   - chapters and format tags (`CLUSTARR_PROFILE`);
   - truncated files for the "incomplete" tests.

   Dolby Vision cannot be synthesised; those tests keep their named skip. Every
   test that runs `ffmpeg`/`ffprobe` today moves to it:
   `pkg/mediainfo/{hdr10_fixture,incomplete,profiletag_ffmpeg}_test.go`,
   `pkg/segments/decode/decode_test.go`,
   `pkg/subtitles/providers/embedded/provider_test.go`,
   `pkg/transcode/{engine/helpers,standard/parity,selfcheck}_test.go`,
   `app/squash/worker/{inprocess/probe,graft/graft,worker_envtest}_test.go`
   (`graft_test.go:204` runs ffprobe),
   `app/squash/controller/transcodejob/parity_envtest_test.go` (hard-codes
   `/usr/bin/ffmpeg`), `app/import/worker/{fileimport/probe_envtest,rescan/folder_envtest}_test.go`,
   `app/catalog/controller/mediafile/mediafile_envtest_test.go` and
   `test/parity/parity_test.go:228`. Tests needing ffgo skip without FFmpeg's
   shared libraries unless `CLUSTARR_FFGO_REQUIRE=1`, which CI sets (par2go's
   `PAR2GO_REQUIRE` pattern), so a missing library can never pass silently.

2. **ffgo `.12` (§7.5) gains what `testmedia` needs**, if ffgo lacks it:
   - muxing pre-made text subtitle packets (ffgo has no subtitle encoder);
   - attaching HDR10 side data to encoded output;
   - writing chapters and format tags.

   Same rules as the other additions: one commit each, with tests.

3. **Parity oracles become committed goldens.** Before each R2 step deletes an
   exec implementation, record its output once on `testmedia` clips with a
   program under `hack/` that calls clustarr's existing functions, never the
   CLIs. Store the results in `test/data/media-goldens/`:
   - `mediainfo.Probe` → `MediaInfo` JSON;
   - the embedded extractor → SRT bytes;
   - `segments/decode` → PCM and frame hashes;
   - `Par2Runner` → verdicts.

   §6.7 and §7.2.6/§7.2.10 then compare the ffgo and par2go implementations
   with those files instead of a live ffmpeg. Delete the recorder with the last
   exec implementation. Recording falls naturally before R2's first step, while
   the exec code still exists on the branch.

4. **§4.5.6 widens.** `TestNoProductionCodeStartsAProcess` stays as written.
   Add `TestNoGoFileRunsFFmpeg` over **every** `.go` file, `_test.go`, `test/`
   and `hack/` included. It fails on an `exec.Command`/`CommandContext`/`LookPath`
   naming `ffmpeg` or `ffprobe`, and on the strings `/usr/bin/ffmpeg` and
   `/usr/bin/ffprobe`. While the goldens recorder exists it is the only
   allow-listed file, and that entry is deleted with the recorder.

5. **§10.1.6 `make native-assets`** ships no ffmpeg executable. It provides
   only the pinned BtbN shared libraries, `libffshim.so`, `libpar2shim.so` (U1)
   and ORT. The par2 CLI it installs "for creating test sets only" is outside
   the owner's ffmpeg/ffprobe rule and may stay. The cleaner option is to
   commit the few par2 sets the usenet tests create, as par2go's `testdata/`
   does, and drop the CLI too. Ask the owner if unsure.

6. **§10.1.7 CI.** The test job no longer installs ffmpeg/ffprobe. It sets
   `CLUSTARR_FFGO_REQUIRE=1` and the `native-assets` environment.

7. **`images/Dockerfile.e2e-fixtures` changes** (it is "unchanged" in
   §10.1.7/§10.1.8 today). It stops apt-installing ffmpeg and bakes its clips
   with a small Go program, `test/fixtures/mkclips` on `testmedia`, in a stage
   built on the same pinned shared FFmpeg as `native`.

### U3. Only the FFmpeg libraries ffgo loads (owner request)

**Request.** The media runtime should "take only the required libs for ffmpeg".
§10.1.3's `ffmpeg` stage is "unchanged" from the transcoder: it copies
`/opt/ffmpeg/lib/lib*.so*` wholesale.

**Edits.**
- The `ffmpeg` stage copies exactly the seven libraries ffgo and its shim load,
  with their soname symlinks: `libavutil`, `libavcodec`, `libavformat`,
  `libavfilter`, `libavdevice` (`libffshim.so` links `-lavdevice`),
  `libswscale` and `libswresample`. Plus `libffshim.so`; no `bin/`.
- `hack/image-checks.sh` (§10.1.4) fails if the staged `/usr/lib` holds any
  other `libav*`, `libsw*` or `libpostproc*` file, in addition to the existing
  refusal of `ffmpeg`, `ffprobe` and `par2` executables. `stage.sh`'s `trace`
  still pulls each library's own dependencies, so nothing needed is lost; the
  check only stops whole-tree copies coming back.

### U4. Nothing to execute on `main`

The `main` session's draft plan had a "Phase 1": seams and ffgo/par2go
implementations under its own package names, `segmentarr-worker` on ffgo, an
additive `Dockerfile.media`. This branch moves and renames the same packages
(`pkg/mediainfo/native`, `…/embedded/execextract`, `pkg/segments/decode`,
`cmd/markers`, `Dockerfile.media` deleted), so landing that phase on `main`
first would only create rebase conflicts in R14. **It was not started.** All
of the work in this guide belongs on this branch.

## 3. Order of work on the branch

1. **U1** first: go.mod, then the `par2` image stage and `native-assets`.
   It is mechanical and unblocks every par2 test.
2. **U2.2** next, the `testmedia` needs, appended to the ffgo `.12` commits
   (§7.5) before the tag.
3. **`internal/testmedia`** (U2.1), and move every test listed there off the
   CLI.
4. **Record goldens** (U2.3) while every exec implementation still exists,
   i.e. before §6.9's and §7's first R2 deletion.
5. Each R2 step as the spec orders it, with its parity test reading goldens.
6. **U2.4** guard, **U2.5/U2.6** native-assets and CI, **U2.7** e2e fixtures,
   **U3** image libraries and checks, once the last exec implementation and
   the recorder are gone.

## 4. Checklist

- [ ] `go.mod` requires `github.com/mediactl/par2go v0.1.0`, with no par2go replace; OD19 marked resolved.
- [ ] The `native` `par2` stage and `native-assets` download `libpar2shim-linux-<arch>.so` and check it against the published digests.
- [ ] ffgo `.12` includes `testmedia`'s needs (text subtitle muxing, HDR10 side data on encode, chapters and tags).
- [ ] `internal/testmedia` exists; no test runs `ffmpeg`/`ffprobe` or names `/usr/bin/ff*`.
- [ ] `test/data/media-goldens/` recorded before the first exec deletion; parity tests read it.
- [ ] `TestNoGoFileRunsFFmpeg` covers every `.go` file, `_test.go`, `test/` and `hack/` included.
- [ ] `native-assets` and CI ship no ffmpeg/ffprobe; CI sets `CLUSTARR_FFGO_REQUIRE=1`.
- [ ] `Dockerfile.e2e-fixtures` installs no ffmpeg; `test/fixtures/mkclips` bakes its clips.
- [ ] The `native` image stages exactly seven FFmpeg libraries plus `libffshim.so`; `image-checks.sh` refuses any other `libav*`/`libsw*`/`libpostproc*`.
