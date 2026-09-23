# Phase E — M4 transcode (squasharr)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** a MediaFile matching a TranscodeProfile is transcoded inside a Kubernetes Job, verified, swapped over the source with the original recycled, and catalogarr re-probes it — with metrics populated and the pipeline page showing the stage.

**Architecture:** squasharr's controller role owns `TranscodeProfile` and `TranscodeJob` and creates `batch/v1` Jobs suspended, admitting them against a slot budget (ADR-0005). The worker role runs *inside* each Job pod: probe, plan, ffmpeg, verify, swap. Almost every library this needs was built in Phase B; this phase is controllers, the worker entrypoint, and wiring.

**Spec:** `docs/superpowers/specs/2026-09-18-clustarr-design.md` §6.4, §12, §16 M4; amendment-1 wins on conflict; ADR-0005 (transcodes as batch Jobs).

**Prior art, verified 2026-09-23 against source.** Where this plan says a thing does not exist, it does not exist.

---

## Global Constraints

- Go 1.27, controller-runtime v0.25.1, k8s.io/* v0.37.0. GPL-3.0 header on every file.
- **All status writes go through `pkg/k8s.PatchStatus`**, and every apply is a **complete declaration** of everything its manager owns. See CLAUDE.md "Gotchas found the hard way" — every variant listed there applies here.
- **An over-claim is silent** (`pkg/k8s` forces ownership). Assert the controller/worker split on `metadata.managedFields`, never on values.
- **Re-`Get` immediately before applying** on any read-slow-work-apply path. The worker's progress loop is exactly that shape.
- `+kubebuilder:rbac` markers must be **package-level comments**. envtest does not enforce RBAC.
- **No `float32`/`float64` in `api/`.** Cap every status list with `MaxItems`.
- **No new Go dependencies** — the survey confirmed none are needed. Never `go get`/`go mod tidy`.
- **No `-race`, no e2e runs, no kind clusters** (user instruction). e2e scenarios are **written, not run**.
- Commit with a pathspec: `git commit -m '…' -- <paths>`. Never `git add -A`/`commit -a`, never `git stash`. **Do not push** — the controller gates and pushes.

## What already exists — consume it, do not rebuild it

- `pkg/transcode`: `Plan(info, profile, caps, meta) (*PlanResult, error)` (`plan.go:190`) decides `skip|remuxOnly|encode|reject` and the tier; `Args(plan)` renders argv (golden-tested); `NewRunner(ffmpeg).Run(ctx, plan, progressFn)` runs it with scaled-int `Progress` and removes its own `.part` on failure but **does not** rename or replace — that is the caller's; `NewVerifier(ffprobe).Verify(ctx, src, dst, exp)`; `FromProbe(mi, raw)`; `ProfileHash(p)`; `ProbeCapabilities`, `SelectTier`, `FallbackTier`.
- `pkg/mediainfo`: `Probe(ctx, path)`, `ClassifyHDR(raw)`, `ProbeHash(path, size, mtime)`.
- `pkg/fsops`: `MoveAtomic`, `AtomicWrite`, `Recycle`, `SweepRecycleBin`, `EnsureFreeSpace`.
- `pkg/pipeline`: `StageTranscoding`/`StageTranscodeDone` and `Related.Jobs` are already derived.
- `pkg/obs/metrics/domain.go:189-224`: `TranscodeJobsActive`, `TranscodeDuration`, `TranscodeSpeedRatio`, `TranscodeSizeRatio` — declared, registered, incremented nowhere.
- **catalogarr's half of the swap is done**: `catalogarr/controller/mediafile/mediafile_controller.go:434` `latestUnincorporatedTranscode` finds the newest `Succeeded` TranscodeJob after `status.probedAt`, re-probes, and takes over `spec.sizeBytes/modTime/original`. It is dead code in production only because nothing creates a TranscodeJob.
- Field managers `ManagerSquasharr` / `ManagerSquasharrWorker` exist (`pkg/k8s/fieldmanager.go:180,183`). `TranscodeJob`'s own doc (`transcodejob_types.go:247-250`) already states the split: controller owns phase/plan/jobRef/attempts/conditions; worker owns progress/result/stderrTail.

## Rulings

**R1 — a `reject` decision lands as phase `Skipped` with a `Failed`-free reason, not as a plan.** `pkg/transcode.Decision` has `reject` (`plan.go:38`); the CRD's `PlanMode` enum (`transcodejob_types.go:45`) allows only `transcode;remuxOnly;skip`. A Dolby Vision `reject` policy or an unavailable encoder is a deliberate *decision not to transcode*, which is what `Skipped` means. Leave `status.plan` unset, set `message` to the reason, and a condition saying why. Do not add `reject` to the enum — that is an API change for a case the existing phase already describes.

**R2 — verification is duration tolerance plus stream layout, as `pkg/transcode` already implements.** The base spec §6.4 mentions `-count_packets` equality; `pkg/transcode/doc.go:18-49` deliberately scopes that out, and the Phase E gate itself asks only for "verified for duration and stream layout". Use the existing `Verifier`. Do not add packet counting.

**R3 — the controller plans from MediaFile's stored probe; the worker re-probes and refuses if the source changed.** The controller role does not mount `/data`. `TranscodeJob.spec.sourceProbeHash` is CEL-immutable for exactly this: the worker computes `mediainfo.ProbeHash` on the live file and exits **3 (invalid source)** on mismatch, rather than transcoding a file that is not the one that was planned.

**R4 — worker exit codes are the contract with `podFailurePolicy`** (§6.4): `0` ok, `2` retriable, `3` invalid source, `4` verification failed. `podFailurePolicy` ignores `DisruptionTarget` and fails the Job on 3 and 4, so a retry happens only for genuinely transient failures. Getting these wrong turns a bad source into `backoffLimit` repeated full transcodes.

**R5 — the swap is: verify, recycle the source, atomic-move the output over the source path.** Never delete the source before the output is verified and in place. "Transcoding replaces the library hardlink only; a seeding copy in `/data/torrents` is untouched" (§6.4) — so replace the path, never touch anything outside the root folder.

**R6 — no progress over NATS.** `ProgressTranscodeSubject` exists and nothing uses it, same as downloads (D3 R7). Progress goes to `status.progress` under `ManagerSquasharrWorker`, throttled. The UI already reads status.

**R7 — the KEDA ScaledJob stays an example.** `config/keda/transcode-scaledjob.yaml` is labelled "OPTIONAL and EXAMPLE ONLY" and mutually exclusive with the slot scheduler. Do not wire it.

---

## Tasks

### E-0 — `squasharr/status` (SERIAL, blocks everything)

**Files:** create `squasharr/status/`. Mirror `grabarr/status` exactly — read it first.

One declaration per manager: `ControllerFields` (TranscodeJob `phase`, `plan`, `jobRef`, `attempts`, `startedAt`, `finishedAt`, `message`, `conditions`, `observedGeneration`) and `WorkerFields` (`progress`, `result`, `stderrTail`), plus TranscodeProfile's status set. A `Patch` that refuses any other manager. Test the split on `metadata.managedFields`.

Also add `+kubebuilder:validation:MaxItems` to `TranscodeProfileStatus.Conditions` (`transcodeprofile_types.go`, currently uncapped — a project invariant) and run `make generate manifests`.

### E-1 — TranscodeProfile controller, and the mapper that creates TranscodeJobs

**Files:** create `squasharr/controller/transcodeprofile/`.

Reconcile each profile: compute `status.hash` with `pkg/transcode.ProfileHash`, validate (`Invalid` condition), and count `matchingFiles`, `pendingJobs`, `runningJobs`.

**The mapper is the part that makes the phase do anything.** For every MediaFile the profile selects (`spec.selector`, or `spec.default` when no other profile selects it), create a TranscodeJob if and only if the file has not already been transcoded to this profile hash — catalogarr writes the `CLUSTARR_PROFILE=<name>@<hash>` tag (`mediafile_controller.go:460`), which is how you tell. **Name the TranscodeJob deterministically** from the MediaFile UID and the profile hash so a re-run creates nothing new. Set `sourcePath` and `sourceProbeHash` from the MediaFile. Owner reference: the MediaFile.

Watch MediaFiles and map them to profiles. Status writes under `ManagerSquasharr` via `squasharr/status.Patch`.

### E-2 — slot scheduler (pure) and TranscodeJob controller

**Files:** create `squasharr/controller/transcodejob/`, including a pure `admit.go`.

`Pending → Planned → Queued → Running → Succeeded|Failed|Skipped`, per §6.4.

- **Planned**: build `pkg/transcode.MediaInfo` from the MediaFile's stored probe and call `Plan`. `skip` → `Skipped`; `reject` → `Skipped` per **R1**; otherwise record `status.plan`.
- **Queued**: create the `batch/v1` Job **suspended**, `restartPolicy: Never`, `backoffLimit: 2`, `podFailurePolicy` ignoring `DisruptionTarget` and failing on exit codes 3 and 4 (**R4**), `activeDeadlineSeconds` and `ttlSecondsAfterFinished` from the profile, resources and GPU from the profile, `/data` mounted, image from a new `--worker-image` (cpu) / `--worker-image-cuda` (nvidia) flag, command `clustarr squasharr --role worker --job <name> --data-dir /data`.
- **Admission** is a pure function over (queued jobs, running jobs, slot budget from `--slots`, per-profile limits, priority) returning which to unsuspend. Table-test it without a cluster — this is `pkg/pipeline.Project`-style logic that CLAUDE.md says should be pure.
- **Running**: unsuspend; mirror Job conditions into phase. `Succeeded`/`Failed` from the Job.

Controller owns `ControllerFields` only. Watch owned Jobs.

### E-3 — the worker (runs inside the Job pod)

**Files:** `squasharr/worker/`; fill `squasharr/run.go`'s `setupWorker`.

`clustarr squasharr --role worker --job <name>`: `Get` the TranscodeJob, `mediainfo.Probe` the live source, compare `ProbeHash` to `spec.sourceProbeHash` (**R3**, exit 3 on mismatch), `ProbeCapabilities`, `Plan`, `EnsureFreeSpace` for scratch, `Runner.Run` with a progress callback that patches `status.progress` under `ManagerSquasharrWorker` **throttled and re-`Get`-before-apply**, `Verifier.Verify` (exit 4 on failure, **R2**), then the swap per **R5** (`Recycle` the source, `MoveAtomic` the output over it), then `status.result`. Populate the four metrics. Exit codes per **R4**.

Tests: a **real** transcode of a small clip generated with ffmpeg's `lavfi` (`testsrc2` + `sine`, a couple of seconds), skipped cleanly when ffmpeg is absent — `pkg/transcode`'s own tests show the skip pattern. Prove the swap leaves the recycled original in the recycle bin and the verified output at the source path, and that a failed verify leaves the source untouched.

### E-4 — wiring, RBAC, readiness (SERIAL, after E-0..E-3)

**Files:** `squasharr/run.go` (`setupControllers`), `cmd/clustarr/`, `config/`, `charts/`.

Register both reconcilers; add squasharr's RBAC markers (TranscodeJob/TranscodeProfile incl. `/status`, `batch/v1` jobs, MediaFiles read) and run `make manifests`, then **sync `charts/clustarr/templates/rbac.yaml` between its BEGIN/END sentinels**. Readiness on every replica. **Add squasharr to `cmd/clustarr/runnable_registration_test.go`'s `runnableServices`** and verify each role reaches `/readyz` in `cmd/clustarr/start_envtest_test.go`, as D2-8 did — in D2 this task found three whole components registered nowhere. Thread `--worker-image`/`--worker-image-cuda` through the manifests and chart, and `TestChartImagesMatchConfig` will hold them to each other.

### E-5 — fixtures and e2e scenario 12 (written, not run)

**Files:** `images/Dockerfile.e2e-fixtures`, `test/e2e/transcode_test.go`.

Generate an **HDR10** clip in the fixture image with ffmpeg (`lavfi` source, `-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc` plus mastering-display metadata). **Dolby Vision cannot be synthesized with ffmpeg alone** — it needs an RPU. If no DV clip is achievable without a new tool, say so, and write the DV scenario to skip with that named reason rather than faking it. Write scenario 12 and extend scenario 1 through the TranscodeJob. **Do not run them.**

### E-6 — gate, CLAUDE.md Status, carried list

Mirror D1-10 (`3038552`). `make generate manifests build lint test` with `KUBEBUILDER_ASSETS` exported. A Phase E paragraph in CLAUDE.md's Status, identifiers grepped before written, stating plainly that scenario 12 is written and **never executed**.

## Waves
0: E-0 · 1: E-1, E-2, E-3 (disjoint packages) · 2: E-4 · 3: E-5 · 4: E-6
