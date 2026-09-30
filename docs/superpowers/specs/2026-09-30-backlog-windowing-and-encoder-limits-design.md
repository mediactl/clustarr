# Backlog windowing and encoder device limits

Date: 2026-09-30. Approved in conversation ("Do all recommended including
the device-limits design").

## Why

On the owner's library the work backlogs were materialised one object per
file: 13,308 SubtitleRequests (one per MediaFile, 11,600 of them Satisfied
with no items -- nothing was ever wanted) and a TranscodeJob per matching
file per profile hash (10-20k objects; every profile edit created a new set
and left the old one). Re-rolls meant creating and deleting tens of
thousands of objects at 30-145 a second, and every list of them was tens of
MiB. The catalog kinds (Movie, Series, Episode, MediaFile) stay CRDs: they
are the library, they grow with it rather than with churn, and the design
of record makes custom resources the interface. What changes is that a
backlog is no longer an object per item.

Separately, NVENC refused the profile's `bFrames: 8` on the owner's RTX
2070 Max-Q ("Max B-frames 8 exceed 5") and clipped `rcLookahead` (64 to 54).
The fix is not a limit hard-coded in the planner (rejected, 2026-09-30) but
the device's own limits, measured on the pod that encodes.

## 1. TranscodeJob: a window, not a backlog

The TranscodeProfile controller still computes every matching file
(`status.matchingFiles`), but creates TranscodeJobs only while the
profile's **non-terminal** jobs (no phase, Pending, Planned, Queued,
Running) number fewer than its window: `--transcode-job-window` (default
32). New jobs are taken in MediaFile name order, so passes are
deterministic. A file whose job already exists is not re-applied (its spec
is immutable; the old code re-applied every job on every pass).

Terminal jobs stay as they were: Failed and Blocked for the operator,
Skipped as the record that stops the file being planned again. A Succeeded
job is deleted once it is older than `--transcode-job-retention` (default
24h) **and** its MediaFile has been probed since it finished -- catalogarr
incorporates a swap by reading the Succeeded job
(`latestUnincorporatedTranscode`), so it must outlive that. Its output is
tagged `CLUSTARR_PROFILE`, so no job is created for the file again, and the
history sink already holds its events.

Status: `matchingFiles` as before; the Ready message says how many files
are waiting outside the window.

## 2. SubtitleRequest: only where a subtitle may be wanted

The SubtitleProfile controller no longer ensures a request for every
matching file. It skips a file

- not probed yet (no `status.mediaInfo`) -- the probe watch wakes it; and
- whose probe proves nothing can be wanted: `subtitlerequest.MayWant` runs
  the request controller's own `plannerProfile` and `subtitles.Plan` with
  the file's **tagged** audio languages and no existing subtitles at all. If
  that wants nothing, no existing sidecar or embedded track could change
  it. Untagged audio (the original-language fallback needs the item) or any
  wanted key means a request, and the request controller decides exactly as
  before.

A request it would now skip is deleted only when it has no `status.items`
and no user override in spec (`languages`, `minScoreOverride`,
`forceSearch`).

## 3. Encoder device limits

- **Measure.** The pool worker measures its tier's limits once per process
  and per requested value, by trial encodes (a 256x256 synthetic clip, 10
  frames, `-f null`): for NVENC, `-bf <profile>` (a refusal names the
  maximum: "Max B-frames N exceed M") and `-rc-lookahead <profile>` (a clip
  names the depth: "Clipping lookahead depth to M"). libx265 has no device
  limits; QSV and VAAPI report none until their messages are measured on
  hardware (the owner's Intel iGPU is not exposed to the cluster).
- **Apply.** `transcode.Capabilities` carries `Limits[tier]`; Plan renders
  `min(profile, limit)` and appends each clamp to the plan's reason
  (`bFrames 8 -> 5 (device limit)`), which is the job's Planned message.
  The profile hash is untouched: a new GPU or driver re-rolls nothing.
- **Publish.** The worker merges its node's limits into the
  `clustarr-progress` key `encoder-limits.<class>` (a node -> limits map with
  measurement times, compare-and-swap; entries older than 10 minutes are
  dropped, and the bucket's TTL expires a class nobody refreshes). The
  TranscodeJob controller plans with the minimum over fresh entries, so its
  recorded argv matches the worker's; with no entry it plans with the
  profile's values and the worker's own plan (which always has its limits)
  runs.
- **Show.** TranscodeProfile `status.encoderLimits` (per class, capped)
  lists the limits in force.
