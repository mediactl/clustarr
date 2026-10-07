# ADR-0019: The manager is the control plane; agents execute tasks and never write the Kubernetes API; the Download kind is removed

**Status:** Accepted, 2026-10-07 (the owner's decisions; the design,
`docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`, is under the owner's
review before any implementation)

## Context

ADR-0016 made per-file work MediaFile status written by one remediation loop, and kept
`Download` a resource "because it targets an item rather than a file, exists before any file,
may become many files or none, keeps seeding after import, and gates data removal on its
engine's finalizer" (`docs/adr/0016-per-file-work-is-mediafile-status.md:89-92`). The
manager/agent split (`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`) put
every reconciler in one `cmd/manager` process and every worker in an `agent --domain <d>`
process, and kept each worker's Kubernetes writes, and each worker's decisions, where they
were (split §5.2, "After the split" column; loop spec §4.14 keeps agent-metadata's item
status, the grab worker's item status and the engines' Download status).

On 2026-10-07 the owner made four decisions (`.superpowers/unify/rulings.md`, "Agents never
write Kubernetes"):

1. A Download "updated directly from a worker" is an anti-pattern; the Download kind is
   removed, not kept with the manager as its only writer.
2. No code in `cmd/agent` (any domain), `cmd/markers` or `cmd/transcode` writes the
   Kubernetes API.
3. The manager receives the workers' acknowledgements and updates from the streams and is the
   only writer of every CR.
4. "The manager should primarily function as a reconciliation loop/state machine. It will
   function as a proper control plane - determining which agent to queue for which task."

An audit of the branch at `530b5dfb` (design §3) found 44 Kubernetes write paths in agent code
(4 `catalog`, 5 `events`, 5 `metadata`, 16 `import`, 5 `index`, 1 `caption`, 4 in each engine
domain) under eleven field managers, four of which are written from more than one process;
four of the paths are already removed by ADR-0016's fold. Among the rest are a forced status
apply with no compare-and-swap (`Download.status.import`), MediaFile and item deletes with no
precondition,
two Secrets written by both the manager and an agent, and engine finalizers that make data
removal depend on a Kubernetes round trip from the engine. The same audit found that agents
also make the policy decisions behind those writes: which release to grab and when, which
file belongs to which item, which items an import list adds or removes, when to retry, hold,
fail or blocklist, when an indexer is disabled, when a torrent has seeded enough.

Three premises have changed since ADR-0016 kept Download:

- **A worker can report without a status write.** `pkg/records` and its waker
  (`pkg/records/recordsource`) exist on the branch: a worker writes a compare-and-swap record
  in a Durable KV bucket, and a leader-only source in the manager only enqueues.
- **The manager is one always-running process with one lease** (split §5.8), so leader-only
  consumers of results, intake streams and advisories, and every state machine, have a home
  that does not scale to zero.
- **A grab already has an owner.** Every Download is owned by the item it targets, or by the
  Series for a season pack (`app/catalog/worker/grab/perform.go:276,318-319`), and the item
  already mirrors it (`activeDownloadRef`, the Downloading overlay, the donor state).

## Decision

**The manager is the control plane.** Every resource's lifecycle (an item, a MediaFile, a
grab and its transfer, a library scan, an import list sync, a search, a metadata refresh, an
indexer's health, a DownloadClient's engines) is a state machine in the manager's reconcile
loop, persisted in that resource's status. A task is an effect of a state transition: the
manager decides that work is due, picks the task kind and its destination (agent domain and
queue, hardware class, DownloadClient engine instance, node-bound pool), admits it against the
destination's known capacity, publishes it, and advances the state machine on the
acknowledgements and updates that come back. A task whose destination has no capacity, or no
agent at all, stays pending in the state machine and visible on the CR; it is never dropped.

**Agents are executors.** An agent takes a task, does it and reports. It holds no policy.
Pure computation stays in agents because it is heavy, needs libraries the manager must not
link, or needs in-process state: parsing, probing, scoring and ranking a candidate list,
fingerprinting, rendering, provider and indexer I/O with their limiters and sessions, moving
bytes on disk. Any choice that changes desired state is the manager's: grab this release,
attribute, create or delete this file or item, blocklist, retry, hold or fail, which client,
pool or agent, and when to refresh, rescan, sync or search. Where the design keeps a choice in
an agent, it says why.

**No agent writes the Kubernetes API.** Code linked into `cmd/agent`, `cmd/markers` or
`cmd/transcode` makes no create, update, patch, delete, status, finalizer, Secret or Event
call. Agent ServiceAccounts hold `get`, `list` and `watch` only (and `get` on the Secrets they
read), or no binding at all. The agent's controller-runtime client is wrapped read-only, and
an AST guard and a role guard hold the rule.

**Agents report over NATS, in two shapes.** State the manager reads while running a state
machine (a transfer, an import's result, a search's scored candidates, a metadata document,
an indexer's outcomes) is a **record**: a compare-and-swap value in a Durable KV bucket with
one writer per key. A report the manager did not ask for (a release the RSS feed matched to a
wanted item, a file a scan found or lost, the items an import list holds) is an **intake
message** on a Durable work stream, `CLUSTARR_INTAKE`, consumed only by the manager, which
decides what it implies and acks it only after that decision has landed.

**The manager writes every CR and every Secret clustarr writes.** Field manager names stay as
they are (split ruling R7), so `managedFields` carry over; each is now written only from
`cmd/manager`. The ui's user-intent writes (`ui/actions`) stay: the ui is not an agent.

**The Download kind is removed.** A grab is an entry in its owning item's status,
`status.downloads[]`, written by the remediation loop's item key under `catalogarr`. A Movie,
Album, Book or Audiobook owns its own grabs; a Series owns every grab of its episodes, single
episodes included; a Comic owns every grab of its issues. The entry holds the intent (release,
source, client, engine instance, purpose, seed criteria, removal policy) in etcd, so a NATS
loss loses no intent. Engines take whole desired-state commands, fenced by sequence, from a
work stream only the manager publishes to, and report transfer records. An engine never
removes a transfer or its data because something is absent; only an explicit command
removes. Deleting an item publishes the removals and waits for them behind an item finalizer,
with R-6's ten minutes when the engine is gone. The blocklist is a capped list in the item's
status.

**The manager consumes acknowledgements.** Besides records and intake acks, the manager
consumes JetStream's `MSG_NAKED`, `MSG_TERMINATED` and `MAX_DELIVERIES` advisories for the
work it dispatches, and shows a task's retries and dead letter on the CR's dispatch block.
The DLQ projector and the history sink move into the manager.

This ADR supersedes, in ADR-0016, the sentence "These stay resources: Download, …" and the
alternative "Fold Download"; ADR-0016's Download reasoning now places the grab on the item,
not on the MediaFile. It supersedes, in the split design that ADR-0018 will adopt, every agent
row of §5.2's "After the split" column, §5.3.1, the agent-side Secret writes of §5.3.2,
§5.3.4, the agent write verbs of §4.6 and §10.2.4, and the engine, DLQ-projector and
history-sink rows of §3.5.3; and loop spec §4.14's agent table. It refines ADR-0008: the delay
profile and the keep-best pending grab become the item's state machine in the manager, and
the KV grab lease, which existed to serialise agents, retires.

## Alternatives considered

**Keep Download, with the manager its only writer.** Engines and fileimport report records;
the manager writes Download status. It keeps `kubectl get downloads` and changes the fewest
readers. Declined by the owner. It also keeps a second object per grab with its own
lifecycle (two finalizers, owner references, a 90-day blocklist kept as labelled objects that
a sweeper deletes, `app/grab/controller/downloadclient/blocklist.go:96-128`), mirrored back
into the item by six reconcilers: the mirroring ADR-0016 removed for per-file work.

**The manager writes, agents keep deciding.** Agents send the manager finished decisions
("grab R for X", "create MediaFile M", "disable indexer I") and the manager only applies them.
It removes the Kubernetes writes with the least code moved, but leaves policy spread over
eight processes, each deciding on a cache that lags the manager's, and the manager a relay
that cannot refuse a decision it cannot see the reasons for. Declined by owner decision 4.

**The manager does everything, scoring included.** Rejected on cost: TRaSH custom-format
scoring is regexp2 over 2,791 patterns per release, and the RSS firehose delivers every
indexer's feed; running it on the single leader would put the heaviest CPU path of the system
on the one process that must never stall, and pull indexer, provider and media libraries into
a binary that split ruling R6 keeps free of them. Agents score; the manager chooses.

**A new manager-written kind for transfers.** Keep Download renamed; the same costs.

**Results only in KV, nothing in status.** ADR-0016 rejected it because `kubectl` is the
interface. Nothing about that has changed, and it is still rejected: intent, phase, the
import verdict's summary, the pending candidate and the blocklist are in status. What has
changed is that records now exist for the parts a status cannot carry well: per-second
progress (already in `clustarr-progress`), a transfer's file list, an import's per-file
detail, a search's candidate list, and the engine's own state, which the engine rebuilds after
a NATS loss.

**Engines read desired state from a KV bucket instead of a command stream.** Level-triggered
and simpler to resync, but it gives the manager no per-command delivery signal (no nak, no
`MAX_DELIVERIES`, no dead letter), which the owner's third decision asks for. The design keeps
level semantics on the stream instead: every command is the whole desired state at a
sequence, and the engine applies only the newest.

## Consequences

**Gains:**

- One process decides and writes. Agent roles are read-only, `TestEveryFieldManagerHasItsHome`
  has a single home for every name but `clustarr-ui`, and the cross-process lost-update and
  co-ownership cases of split §5.3 and §5.5 disappear.
- Every decision is made on the manager's cache, under its lease, recorded in a status the
  user can read, and taken by one serialised reconcile per object: the grab lease and the
  live-read double-grab guards that serialised agents retire.
- The unfenced writes the audit found are fenced on the way: MediaFile and item deletes carry
  UID preconditions, the import verdict is compare-and-swap, the two dual-written Secrets have
  one writer.
- A grab's whole life is on the item it is for. The Downloading overlay, `activeDownloadRef`,
  the donor state and the blocklist read the item's own status instead of a second kind.
- Engines need no Kubernetes write and no catalog read: their command carries the file
  selection, and their own journal (`<data>/torrents/.state`, the usenet manifest) carries
  re-attach.
- Work waits visibly. A task with no agent to take it is a pending state on its CR, and the
  queue the HPA scales on is the manager's admitted backlog, not a stream that may discard.

**Costs:**

- Latency: a report crosses NATS and a manager reconcile before anything acts on it, typically
  well under a second, longer under a backlog. An automatic grab now takes two hops (scored
  candidates in, desired state out) where it took one.
- The manager carries more: the grab, import, list and indexer-health policy, the downloads
  planner, the intake appliers, the routing and admission tables and the advisory intake. It
  links `pkg/decision`'s item-state rules but not the scoring path's heavy inputs; it is one
  blast radius, bounded by planner isolation and per-intake consumers.
- Imports become two phases (inspect, then execute the approved plan), so an import needs two
  task round trips.
- More NATS state: six Durable records buckets, a Durable intake stream and a Durable engine
  command stream, about 1.3 GiB of file-store reservations, and one small memory stream for
  task advisories.
- Item status grows: a Series holds up to 24 grab entries and 64 blocklist entries, each
  string capped; a budget test holds the worst case under etcd's limit.
- Series and Comic become loop item keys, so their reconcilers move into the loop as Movie's
  and Episode's did.
- `kubectl get downloads` goes. The queue is `status.downloadPhase`, a print column and
  selectable field on every item kind, and the ui's Downloads page reads item status and
  `clustarr-progress`.
- Migration needs two releases: release N adopts every live Download into item status, strips
  the Download finalizers and keeps the frozen objects for rollback; release N+1 removes the
  CRD.

## Revisit triggers

- The manager's decisions or Kubernetes writes become the bottleneck (reconcile queue depth,
  apiserver or etcd latency), or the manager runs more than one active leader.
- A decision needs a latency the manager's round trip cannot meet.
- A record or intake message is lost in a way the owed-effect rules cannot repair (a NATS data
  loss that also loses intent the status should have held).
- An item's status outgrows its size budget, or a Series routinely holds more grabs than its
  cap.
