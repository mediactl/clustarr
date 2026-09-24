# ADR-0013: Controllers stay one Deployment per service until the system is production ready; then they unify into one manager

**Status:** Accepted, 2026-09-24 (the unified topology is designed and deferred)

## Context

Every service runs its reconcilers in its own Deployment with its own
ServiceAccount, generated ClusterRole and leader-election lease: ten
Deployments before download engines and transcode pools, six leases, about
1.5Gi of requested memory for controllers that are thin by design. The
question was raised whether a Loki-style hash ring should spread controller
load across replicas, and whether the controller pods are too sparse.

Neither holds. Leader election means one active controller replica whatever
the topology, so controller replicas add no throughput; JetStream pull
consumers already spread queue work across worker replicas; and a ring
solves ownership of keyed state, which only the download engines have, and
there the unit is a DownloadClient that cannot be split. The real
inefficiency is pod count, memory and duplicated informer caches, and the
remedy for that is one manager hosting every reconciler.

Tinkerbell's unified binary was examined as prior art. It runs every service
as an errgroup goroutine in one Deployment but keeps one controller-runtime
manager and one lease per controller service; `clustarr all` already has
that shape. A single manager with one cache and one lease is the version
worth adopting.

## Decision

Keep the per-service controller Deployments for now. During active
development, one pod per component is what makes a crash loop, a resource
limit or a log stream attributable to one service without extra tooling,
and that is worth more than three pods and a gigabyte of requests.

Adopt the unified topology in
`docs/superpowers/specs/2026-09-24-unified-manager-design.md` once the
system is production ready and battle tested: Phase H green on kind and a
period of real use without a fault that needed per-pod isolation to
diagnose. The owner decides when that bar is met. Workers keep their own
ServiceAccounts in both shapes.

## Alternatives considered

**Unify now.** Fewer pods immediately, but every controller fault during
development becomes "the manager restarted" instead of "squasharr
restarted", and the parity, RBAC and registration guards would be rewritten
while the components they guard are still moving.

**Ring scheduling across controller replicas.** Adds membership, rebalancing
and a membership store to solve an ownership problem this system does not
have; rebalancing would drop live download sessions where it does.

**Tinkerbell's N managers in one process.** One pod, but six leases, six
caches and six metrics endpoints; it forfeits the two concrete gains of
unifying. Kept only as `clustarr all`'s dev shape.

## Consequences

Nothing changes today. The design and the migration steps are recorded so
the adoption is a plan to execute, not a design to rediscover. Until then,
every new controller keeps landing in its service's Deployment, and every
guard that holds the installers to the split shape stays.

## Revisit triggers

Phase H complete; or a homelab node where pod count or requested memory
blocks deployment; or a second real user asking for fewer moving parts.
