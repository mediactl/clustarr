# One manager, domain workers: the unified controller topology

**Status:** Designed and deferred, 2026-09-24. Not implemented. The owner's
decision (ADR-0013) is to keep the per-service controller Deployments while
the system is under active development, because one pod per component makes
debugging and isolation cheap, and to adopt this topology once clustarr is
production ready and battle tested. This document is the design to adopt
then; it does not change anything that runs today.

**Prior art examined:** Tinkerbell's unified binary
(`cmd/tinkerbell/cmd.go` in `tinkerbell-community/tinkerbell`): one binary,
one Deployment, one replica; every service an errgroup goroutine behind its
own enable flag; each controller service (tink-controller, rufio) builds its
own controller-runtime manager with its own `LeaderElectionID`; smee runs a
third, hand-rolled Lease election for DHCP; `retry.Do` restart loops wrap only
the embedded etcd and apiserver, not the controllers, and a controller whose
`Start` fails cancels the errgroup and exits the process. `clustarr all`
already has exactly that shape: six `Run` functions, six `ctrl.NewManager`
calls, six `<service>.clustarr.io` leases, leader election forced off.

## 0. Why unify at all, and why not yet

| | Today: per-service controllers | Unified |
| --- | --- | --- |
| Base pods | 10 Deployments (6 controllers, 2 workers, metadata, ui) before engines and pools | 7 (manager, 4 domain workers, metadata, ui) |
| Leases | 6 | 2 (manager; indexarr's sweep under Postgres) |
| RBAC identities | 7 generated roles, least privilege per pod | manager holds the union of the six controller roles; workers, `grabarr-engine` and `ui` unchanged |
| Requested memory for controllers | about 1.5Gi across six pods | about 512Mi in one pod |
| Failure blast radius | one service's reconcilers | every reconciler, for the restart window; workers keep draining |
| Informer caches | six, shared kinds watched up to six times | one |
| Logs | one pod per service | one pod, slog component fields |
| Dev parity | `clustarr all` differs from production | `clustarr all` is the manager plus workers in-process |
| Test surface | start envtest per identity; parity per component | fewer identities and render paths |

The per-component shape is worth keeping during development for exactly the
reasons in the left column: `kubectl logs deploy/clustarr-squasharr` is one
service's story, a crash-looping reconciler names its service in the pod
list, and a resource limit belongs to one component. The unified shape wins
on pod count, memory, lease churn and cache duplication, none of which bite
on a development cluster.

## 1. Processes and roles

- **`clustarr manager`**, a new subcommand. One controller-runtime manager,
  one lease `manager.clustarr.io`, one cache. It calls an exported
  `RegisterControllers(mgr, bus, options)` from each of the six services
  (today's unexported `setupControllers` functions already take a manager,
  so the export is mechanical and no reconciler moves files). What lives
  there is everything that is a reconciler or a leader-only runnable: every
  CRD controller, the OverlayProfile controller, the transcode slot
  scheduler, the RootFolder schedule, the wanted cron, the artwork reaper,
  the QualityProfile bootstrap and the DownloadClient engine builder. Per
  service: an enable flag and a log-level override, borrowed from
  Tinkerbell's composition; one errgroup; a failed start exits the process.
- **Domain workers**, one Deployment each, own ServiceAccount, scaled by
  queue lag: `clustarr-catalogarr` (search, grab, RSS matcher, artwork
  render, history sink, DLQ projector); `clustarr-importarr` (scan, list
  sync, file import); `clustarr-indexarr` (release index, RSS poll, search
  RPC, Torznab facade; one replica under SQLite, free under Postgres with
  its own lease for the sweep); `clustarr-captionarr` (subtitle fetch).
  `clustarr <service>` keeps `--role`; the installers pass the worker roles
  explicitly.
- **Unchanged.** `clustarr-metadata` stays one replica (ADR-0007). grabarr
  and squasharr have no worker Deployment; engines and pools stay. The ui
  is unchanged. `clustarr all` remains the dev shape.

## 2. Identity and RBAC

- `RBAC_ROLES` gains `manager`, generated as the union of the six controller
  marker sets; the six controller roles and ServiceAccounts are deleted.
  Worker roles, `grabarr-engine` and `ui` stay. The start envtest runs the
  manager and each worker under only its own role.
- Field managers do not change; the one-writer invariants hold per writer,
  not per pod.

## 3. Installers

- Chart and kustomize ship exactly: manager, four workers, metadata, ui.
  KEDA ScaledObjects retarget the worker Deployments. The chart-versus-
  kustomize parity test, the NATS_URL guard and the render-role guard move
  to the new names; a new guard asserts no Deployment other than the manager
  runs a controller role.
- The rename is breaking for existing installs: `helm upgrade` replaces the
  Deployments; kustomize users delete the old ones. The README says so.

## 4. Docs on adoption

ADR-0013 flips from "deferred" to "adopted"; design of record §3 and §6 and
CLAUDE.md's services table and invariants are rewritten to match.

## 5. Execution when the time comes

Straight to the single manager, no N-manager intermediate (that intermediate
is `clustarr all` with leader election on, and it forfeits the shared cache
and single lease that justify the change). Tasks, each keeping `make test`
and `make lint` green: the export refactor per service; the manager command;
RBAC generation and the start envtest; installers and guards; `clustarr all`
and KEDA; docs.

## 6. Adoption trigger

Phase H green on kind for every scenario, plus a period of real use without
a controller crash loop that needed per-pod isolation to diagnose. The owner
decides when that bar is met.
