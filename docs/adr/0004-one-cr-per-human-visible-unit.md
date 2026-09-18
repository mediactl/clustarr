# ADR-0004: One custom resource per human-visible unit, one controller-writer per resource

**Status:** Accepted, 2026-09-18

## Context

Clustarr has no web UI, by design. `kubectl get movies,downloads,transcodejobs,subtitlerequests -A`
is the interface. That commits us to two related questions: which units of work deserve to be
objects in etcd, and who is allowed to write to each object's status.

The first question is a budget question. A custom resource costs an etcd write, a watch event to
every informer that cares, and a row in someone's `kubectl get` output. Sub-minute internal steps
— "query indexer 7", "refresh metadata for this series", "probe this file" — happen thousands of
times a day and are not things a user ever wants to see. Making them objects is how you turn a
homelab apiserver into a pager.

The second question is where Kubernetes conventions get hard. Server-side apply allows multiple
field managers on one object, so it is tempting to let each service own a slice of another
service's status. All three architecture reviews found multi-manager ownership under-specified in
practice: conflicts surface as apply errors at the worst moment, ownership is invisible without
`--show-managed-fields`, and the failure modes are hard to reproduce in tests.

## Decision

A unit becomes a custom resource when a human would reasonably want to see it, cancel it or
retry it, or when it outlives a single reconcile by minutes or more. That gives `Download`,
`TranscodeJob`, `SubtitleRequest`, `Search`, `MediaFile`, `Episode` and `Issue`, alongside the
configuration kinds. Everything shorter-lived is a message on the bus.

Each custom resource has exactly one controller that writes its status, identified by a fixed
field manager name (`catalogarr`, `indexarr`, `grabarr`, `squasharr`, `captionarr`, and their
`-worker` variants). Cross-service status writing is prohibited, with one deliberate exception:
`Download.status.import`, written by catalogarr's importer, because the import outcome genuinely
belongs to the download's lifecycle and is the one place the alternative was worse. The rule is
enforced mechanically — `Status().Update()` and `Status().Patch()` are forbidden outside `pkg/k8s`
by a forbidigo lint rule — rather than by review.

## Alternatives considered

**Per-file state as `MediaFile.status` fields on the parent, written by several services via
SSA.** Fewer objects, and SSA nominally supports it. Rejected: no reviewer could describe the
conflict semantics confidently, and a single-writer design is provable in envtest, which a
multi-manager one is not.

**Everything as a custom resource, including short tasks.** Uniform and debuggable with kubectl
alone. Rejected on etcd churn: sub-minute tasks at our volume produce write rates the apiserver
should not absorb, and they have no ack, ordering or backoff semantics anyway (ADR-0001).

**Nothing as a custom resource except configuration; all state in the bus and KV.** Cheapest, and
completely opaque — no `kubectl get downloads`, no cancel, no events, no RBAC story. The premise
of the project is the opposite.

**Episodes and issues embedded in the parent's status.** Rejected on bounded-list grounds: a
300-episode series would carry a 300-element array rewritten on every episode change, against a
hard object-size ceiling and a noisy watch stream.

## Consequences

Object count is the accepted cost: roughly three objects per episode (Episode, MediaFile,
SubtitleRequest) plus TTL'd TranscodeJobs, so a 5,000-episode library is around 15,000 objects.
Mitigations are deliberate: no episode or issue lists in parent status, a status size budget,
informer warmup, and per-GroupKind concurrency limits. Single ownership makes every status field
traceable to one controller, makes envtest assertions straightforward, and makes conflicts
impossible rather than rare.

## Revisit triggers

More than about 50,000 media objects in a single cluster. The planned response is to merge
`SubtitleRequest` into `MediaFile.status.subtitles` under a `captionarr` field manager — the only
multi-service status write we are prepared to introduce, and only with the conflict semantics
written down and tested first.
