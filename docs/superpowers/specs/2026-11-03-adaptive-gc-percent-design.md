# Adaptive GC percent for the manager

**Date:** 2026-11-03
**Status:** proposed
**Owner's ask:** "Would it make any sense to dynamically adjust the GOGC
within the manager code based on the business of the loop?" then "Design a
solution for this pattern."

## 1. Problem

Go's collector trades memory for CPU through one static number. GOGC is the
heap headroom as a share of the live heap, and the headroom is what sets how
often the collector runs: cycles per second = allocation rate / headroom.
Measured on talos on 2026-11-03 across every five-minute sample of the day
(revs 197 to 200 and the GOGC 20 deploy at 23:11Z), that relation held to
within 20%, and each cycle cost 3.5 to 4 ms of CPU per MiB of live heap: 0.9
to 1.0 CPU-seconds at the manager's 240 to 320 MiB.

The manager's allocation rate is not one number. Settled it allocates 4 to
6 MB/s, almost all of it row decodes (about 9 KB each, 400 to 700 a second).
Under a backlog, the ten to thirty minutes after a start while the
remediation loop drains its initial list, or a day-time re-plan, it allocates
35 to 60 MB/s. So no static GOGC fits both:

| | GOGC 100 (rev 200) | GOGC 20 (pod kxmnt) |
|---|---|---|
| Headroom | 180 to 250 MiB | 50 MiB |
| RSS | 620 to 635 MiB | 490 to 500 MiB |
| GC cycles, settled | 1.3 a minute | 6 a minute (forecast) |
| GC cycles, backlog | 8 to 12 a minute | 30 to 40 a minute |
| GC CPU, backlog | 0.13 to 0.19 cores | 0.45 cores |
| Process CPU at 60 passes/s | 0.17 to 0.27 cores | 0.36 to 0.49 cores |
| CFS throttling (500m limit) | not measured | 66% of periods since start |
| Episode backlog drained | by 30 min | still 34 to 80/s at 40 min |

Pass latency was the same under both (p50 9 to 14 ms, p90 93 to 194 ms), so
GOGC 20 is "functional" and costs a quarter of a core and a slower start.
GOGC 1, deployed for eight minutes the same evening, ran the collector at
170 to 560 cycles a minute, at its one-core cap, the process pinned at its
limit: the floor.

The memory limit does not help here. GOMEMLIMIT is memory-aware, not
allocation-aware: it hands the pacer every byte up to the limit at any
allocation rate, so GOGC off with a limit is the largest RSS at idle, the
opposite of the aim. What is wanted is headroom proportional to the
allocation rate, which is a constant interval between cycles: the
collector's CPU bounded while allocation is high, the heap small while it is
low.

## 2. Design

`pkg/gctune` holds the controller; the manager runs it. It sets GOGC from
the runtime's own allocation rate, between a floor and a ceiling, so the
time between collections stays near a target.

**The signal is the runtime's, not the loop's.** The allocation rate is the
quantity the collector's cost is made of, and `runtime/metrics` reports it
directly (`/gc/heap/allocs:bytes`, cumulative). It also covers every
allocator the loop does not see: a Live TV guide render, the artwork index
listing 53,000 objects at start, an apiserver relist. The loop's pass rate
or queue depth would be a proxy, and the controller would then know about
the loop, which it has no reason to.

**The knob is GOGC, not the limit.** `debug.SetGCPercent` takes the heap
lock briefly (no stop-the-world since Go 1.19) and the pacer applies it at
the next trigger check. GOMEMLIMIT stays exactly what automemlimit set: the
pacer takes the lower of the two goals, so the limit remains the ceiling on
memory, the OOM protection and the collector's 50% CPU cap are untouched,
and raising GOGC while the limit binds is a harmless no-op. GOGC off with a
dynamically computed limit would make a wrong computation an OOM.

**The controller**, once per tick (5 s):

1. Read, in one `metrics.Read`: `/gc/heap/allocs:bytes`,
   `/gc/heap/live:bytes` (the live heap after the last cycle),
   `/gc/cycles/total:gc-cycles`, `/gc/gogc:percent`.
2. rate = Δallocs / Δt. The estimate attacks at once and decays slowly:
   est = max(rate, est × 0.8). A burst raises it in one tick; after the
   burst it falls by a fifth a tick, 40 MB/s to 5 MB/s in about 50 s.
3. headroom = est × Interval (10 s by default). percent = headroom / live ×
   100, clamped to [Floor, Ceiling]. Before the first cycle (live 0) nothing
   changes.
4. If percent differs from the current GOGC by more than 5 points, or
   reaches a clamp the current value is not at, `debug.SetGCPercent`.
   Gauges `clustarr_gc_percent` (what was set) and
   `clustarr_gc_alloc_bytes_per_second` (the estimate); a Debug log line
   per change.

With the day's numbers and a 10 s interval:

- Settled, 5 MB/s, live 250 MiB: 50 MiB of headroom, 20%, the floor. Six
  cycles a minute, 0.09 cores, RSS about 490.
- A re-plan at 20 MB/s: 200 MiB, 80%. Six cycles a minute, 0.1 cores, where
  GOGC 20 runs 24 a minute at 0.35. RSS rises to about 600 for the
  backlog's duration and the scavenger returns it within a minute or two of
  the estimate decaying.
- The start flood, 40 to 60 MB/s while the live heap climbs from 30 to
  390 MiB: the ceiling, 100, at once; above about 330 MiB live the memory
  limit binds and the pacer behaves exactly as rev 200's did. The flood is
  the case GOGC 20 paid most for, and the case the controller must already
  be running for.

**Floor and ceiling.** The floor is the GOGC the process started with
(`/gc/gogc:percent` at the first tick; the chart's `manager.gogc`, 20 on
talos), so the operator's memory choice stays theirs. The ceiling is
`--gc-percent-max` (chart `manager.gcPercentMax`), 0 by default, which turns
the controller off: a manager without the flag runs at its GOGC as today. A
floor at or above the ceiling logs once and does nothing.
`--gc-cycle-interval` (chart `manager.gcCycleInterval`) is the target, 10 s.

**Where it runs.** A goroutine `gctune.Run(ctx, opts)` started in
`internal/cli/manager.Run` right after observability (step 2) and before
`ctrl.NewManager`: the flood it exists for is the cache sync, which
controller-runtime finishes before it starts any runnable, so a manager
runnable would begin after the phase that matters. It runs on every replica
and stops with the root context. Its only dependencies are `runtime`,
`runtime/debug`, `runtime/metrics` and the metrics package; it is a
candidate for any binary, but only the manager is wired, since the agents'
heaps are small and agent-import's and agent-caption's memory is FFmpeg's.

**Seams.** `Options{Floor, Ceiling, Interval, Tick, Decay, Hysteresis,
sample func() Sample, set func(int) int}`; `sample` and `set` default to the
runtime and are the test's hooks, since GOGC is process-global and a test
must never move it.

## 3. What it cannot do

- It is the better of GOGC 20 and GOGC 100 at every moment, never a third
  thing: it cannot beat 100 when busy or 20 when idle.
- The memory limit is the ceiling. The start peak (live about 390 MiB plus
  about 140 MiB of span slack, stacks and metadata under the 614 MiB limit)
  is limit-bound whatever GOGC says, so a start runs as at GOGC 100 and no
  faster. Only the per-pass memo lowers that peak.
- The estimate is a 5 s average with a one-tick attack: a burst shorter than
  a tick is absorbed by the current headroom, which is what headroom is for.
- It changes GOMEMLIMIT never, reads the cgroup never, and the pod's CPU
  limit still bounds everything; it only stops the collector from spending
  that limit.
- A live heap that grows while allocation stays low (a leak) is the limit's
  to catch, as today.

## 4. Alternatives considered

- **A timer**: GOGC 100 for the first N minutes after a start, then 20.
  Covers the start flood and misses every day-time backlog (a profile edit
  re-planning thousands of files, a season pack import, a list sync).
- **The loop's busyness as the signal**: a proxy for the allocation rate,
  blind to the other allocators, and a coupling between the loop and the
  runtime.
- **GOGC off with a dynamic memory limit**: the same controller on the other
  knob; it gives up the OOM guard the static limit is.
- **Maximise GOGC within the memory budget** (Uber's tuner, the ballast
  trick): memory-aware, and what GOMEMLIMIT does natively since Go 1.19; it
  maximises RSS at idle, the opposite of the aim.
- **Only the per-pass memo**: cuts the busy allocation three to four times,
  which alone takes GOGC 20's backlog cost from 0.45 to about 0.15 cores.
  Independent of this design and worth more; both together leave the
  controller worth about a tenth of a core during starts. The order is the
  owner's.

## 5. Tests

`pkg/gctune`, no runtime calls:

- `decide` is pure, table-driven: idle gives the floor; a flood gives the
  ceiling; a rate between gives the proportional percent; live 0 keeps the
  current; floor at or above ceiling is off.
- The estimator attacks in one tick and decays by the factor each tick.
- The runner with a fake sampler, a fake setter and a manual clock: `set`
  is called only for a change past the hysteresis or onto a clamp, the
  gauges follow, and the loop ends with its context.
- `pkg/obs/metrics`' unbounded-dimension guard: both gauges are label-free.
- The chart: `manager.gcPercentMax` and `manager.gcCycleInterval` render the
  two flags and nothing else changes (`test/guards`, as `pprofBindAddress`).

## 6. Rollout and verification

Flags `--gc-percent-max` (default 0, off) and `--gc-cycle-interval` (default
10s) on the manager; chart values `manager.gcPercentMax` (integer) and
`manager.gcCycleInterval` (duration), rendered into the manager's args
beside `--pprof-bind-address`, documented beside `gogc` in values.yaml and
the schema. On talos: `gogc: 20` stays, `gcPercentMax: 100`.

Verify over one start and one settled hour, from Mimir:

- `rate(go_gc_cycles_total_gc_cycles_total[5m]) * 60` near 6 whenever
  neither clamp binds, against 30 to 40 under GOGC 20's backlog;
- `clustarr_gc_percent` at 20 settled, rising with
  `clustarr_gc_alloc_bytes_per_second` and falling within a minute of it;
- `process_resident_memory_bytes` near 490 MiB settled, as under GOGC 20;
- the container's CFS throttling from the node's cAdvisor endpoint
  (`kubectl get --raw /api/v1/nodes/<node>/proxy/metrics/cadvisor`), against
  the 66% the GOGC 20 pod showed; and the Episode backlog drained by 30
  minutes, as rev 200's was.
