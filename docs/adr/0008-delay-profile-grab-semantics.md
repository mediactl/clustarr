# ADR-0008: Grab semantics are Radarr-shaped delay profiles on scheduled messages, with a keep-best pending record and a grab lease

**Status:** Accepted, 2026-09-18

## Context

When a release for a wanted item appears, grabbing it immediately is usually the wrong move. The
TRaSH-recommended posture is usenet-preferred: wait some minutes before taking a usenet release
and longer before falling back to a torrent, because a better release — a higher custom-format
score, a preferred group, a proper — very often shows up inside that window. Radarr and Sonarr
encode this as a delay profile, and users expect its exact semantics.

Two hazards sit underneath it. The first is the double grab: a release can be seen by both an RSS
sync and an interactive search, or a controller can be restarted mid-decision, and the result is
the same item downloaded twice. The second is the losing-the-better-release hazard: if the delay
is implemented by simply dropping candidates and re-searching later, the best release seen during
the window can be gone by the time the window closes.

## Decision

Delay profiles keep the Radarr shape — per-protocol delay, minimum score to bypass, tag-scoped
profile selection — implemented with three mechanisms:

1. **A scheduled JetStream message.** When the first candidate for an item arrives and the profile
   says to wait, we publish a message scheduled for the end of the window rather than holding a
   timer in a controller. Timers do not survive restarts; scheduled messages do.
2. **A pending record in KV, updated keep-best.** Each candidate that arrives during the window is
   compared against the record with a compare-and-swap; the better release replaces it, the worse
   one is discarded. When the scheduled message fires, the record holds the best release seen, not
   the first or the last.
3. **A grab lease in KV.** Before grabbing, the decider takes a short-lived lease keyed by the
   item. Only the lease holder grabs. This is what makes the double grab impossible rather than
   improbable, including across a controller restart and across the RSS/interactive-search race.

Attempt counting and backoff live on the resulting `Download`, so a failed grab retries under the
same accounting rather than re-entering the delay window.

## Alternatives considered

**In-controller timers, or requeue-after.** The natural controller-runtime idiom, but a timer dies
with the process, and `RequeueAfter` re-runs the decision from scratch — discarding the candidates
seen so far unless they were persisted anyway, at which point the KV record is back.

**Deferring delay profiles past the MVP entirely.** Seriously considered — it is one more moving
part in a system that already has many. Rejected because the TRaSH quality model we ship assumes
it: without the usenet-preferred delay, the opinionated profiles produce visibly worse grabs, and
users would attribute that to the quality model rather than to the missing delay. It costs one
scheduled message and one KV record.

## Consequences

Delay state lives in the bus and KV rather than in etcd, which keeps object count down but means
the window is not visible with `kubectl get`; it is observable through the `Search` resource's
status and through bus metrics.

WorkQueue streams must have `AllowMsgSchedules` enabled, and every consumer's `MaxDeliver` must
exceed the length of its `BackOff` array, or a retried grab silently stops. The keep-best CAS loop
is contract-tested against both the NATS and in-memory buses, and because a lease can expire
mid-grab, the grab path is idempotent on the indexer's download identity as a second defence.

## Revisit triggers

Evidence that the KV keep-best record is a contention point under RSS bursts; a need to show
pending delays to users directly, which would argue for surfacing them on the `Search` resource's
status; or delay semantics diverging from upstream Radarr in a way that surprises users, which
would be a reason to re-read upstream and re-port rather than to keep our own variant.
