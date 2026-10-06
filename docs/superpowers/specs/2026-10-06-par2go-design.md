# par2go: PAR2 verify and repair in Go over par2cmdline-turbo's library

Date: 2026-10-06. Status: design approved in conversation; awaiting spec review.

## 1. Purpose and scope

clustarr's usenet engine verifies and repairs downloads by exec'ing the
`par2cmdline-turbo` binary (`pkg/download/usenet/repair.go`, `Par2Runner`).
That path costs a subprocess with its own lifecycle (process-group kill,
`WaitDelay`), regex parsing of par2's text (`par2NeedBlocksRE` and friends),
and an output tail clamped to fit `status.message` -- all three have broken
in production (CLAUDE.md, gotchas on par2).

**par2go** is a new repository, `github.com/mediactl/par2go`, that gives Go
typed verify and repair over par2cmdline-turbo's C++ library, without cgo,
the way `mediactl/ffgo` gives Go FFmpeg: Go loads a shared library with
`purego` and calls a small `extern "C"` shim.

In scope: verify, repair, progress, cancellation, typed results, prebuilt
`linux-amd64` and `linux-arm64` shared libraries.

Out of scope, each on purpose:

- **Creating PAR2 sets.** clustarr never creates them; `ENABLE_CREATOR` stays off.
- **PAR1.** `ENABLE_PAR1` stays off.
- **Windows and macOS.** No consumer.
- **Every clustarr change.** A second spec moves the usenet engine into its
  own binary (as `cmd/squasharr-worker` was split out), because
  `cmd/clustarr` must never link purego (`TestClustarrNeverLinksADynamicLoader`),
  then replaces `Par2Runner`'s exec path and drops the `par2` binary from
  `images/Dockerfile.media`. Isolation from a crash inside the C++ library
  (section 5) belongs there too.

## 2. Upstream: which libpar2

Every libpar2 is C++ with no C ABI, so purego cannot call any of them directly.
The candidates were:

| Library | Verdict |
| --- | --- |
| [nzbgetcom/par2cmdline-turbo](https://github.com/nzbgetcom/par2cmdline-turbo) `BUILD_LIB` | **Chosen.** ParPar-accelerated, the same v1.5.0 line the media image runs as a binary; `namespace Par2`, self-contained `include/par2/` headers, C++11 threads (static builds work), progress hooks. |
| [Parchive/libpar2](https://github.com/Parchive/libpar2) | par2cmdline 0.4 plus libsigc++; about 10x slower (the classic par2 note in `Dockerfile.media`). |
| [brenthuisman/libpar2](https://github.com/brenthuisman/libpar2) | Its C API is `par2cmdline(argc, argv)`: still text parsing, so no gain over exec. |

Pin: tag `v1.5.0-20261005`, commit `4aa390514d0236c00810f2a74f4435b6fff5df37`.

Licence: par2cmdline-turbo's sources are GPL-2.0-or-later ("any later
version" in 65 files), so par2go is **GPL-3.0-or-later**, with
`hack/boilerplate.go.txt`'s header on every Go file and the equivalent on the
shim's C++.

The library surface the shim relies on (all `protected` or `public` in
`include/par2/par2repairer.h`, reachable from a subclass):

- `Result Par2Repairer::Process(memorylimit, basepath, nthreads, filethreads,
  parfilename, extrafiles, dorepair, purgefiles, renameonly, skipdata, skipleaway)`
  -- the whole operation in one call.
- `Result` values: `eSuccess`, `eRepairPossible`, `eRepairNotPossible`,
  `eInvalidCommandLineArguments`, `eInsufficientCriticalData`, `eRepairFailed`,
  `eFileIOError`, `eLogicError`, `eMemoryError`.
- Virtual hooks: `SigHeaders(ParHeaders&)`, `SigFilename(std::string)`,
  `SigProgress(int)`, `SigDone(std::string, int available, int total)`.
- `bool cancelled`, checked by the verify and repair loops.
- Counters: `sourceblockcount`, `availableblockcount`, `missingblockcount`,
  `recoverypacketmap.size()`, `completefilecount`, `renamedfilecount`,
  `damagedfilecount`, `missingfilecount`, and `sourcefilemap` for per-file state.
- The CLI's "You need N more recovery blocks" is
  `missingblockcount - recoverypacketmap.size()`
  (`src/par2repairer.cpp:2243`); par2go computes the same number from the
  same members.

## 3. Repository and Go API

```text
par2go/
  par2.go             public API (package par2)
  result.go           Status, File, Headers, sentinel errors
  internal/bindings   purego dlopen and symbol table; library search
  shim/
    par2go.cpp, .h    extern "C" p2_* over a Par2Repairer subclass
    build.sh          fetch par2-turbo at the pin, cmake BUILD_LIB, link libpar2go.so
  testdata/
    make.sh           regenerates every fixture with the real par2 CLI
    ...               the committed fixtures (a few KB)
  .github/workflows/  ci.yml, release.yml
```

Library search, in order: `$PAR2GO_LIB` (a file path), then `libpar2go.so` on
the dynamic loader's standard paths. The module has no cgo.

```go
// Available loads the library once. ErrUnavailable names every path it tried.
func Available() error

func Verify(ctx context.Context, index string, opts Options) (Result, error)
func Repair(ctx context.Context, index string, opts Options) (Result, error)

type Options struct {
	Dir         string         // basepath; defaults to index's directory
	ExtraFiles  []string       // files matched by content (renamed or obfuscated)
	MemoryLimit int64          // bytes; 0 = par2go's default, below
	Threads     int            // 0 = GOMAXPROCS (cgroup-aware)
	FileThreads int            // 0 = _FILE_THREADS (2), the CLI's default
	Purge       bool           // on success, delete .1 backups and the par2 files
	Progress    func(Progress) // optional; called from the polling goroutine
	PollEvery   time.Duration  // default 250ms
}

type Progress struct {
	Phase    Phase  // Loading, Verifying, Repairing
	File     string // file being scanned, if any
	PerMille int    // 0-1000 for the current phase
}

type Result struct {
	Status       Status  // AllCorrect, Repaired, RepairPossible, RepairNotPossible
	BlocksNeeded int     // > 0 only for RepairNotPossible
	Headers      Headers // set id, block size, data and recovery blocks, file counts
	Files        []File  // per file: Name, State, BlocksAvailable, BlocksTotal
	Log          string  // par2's own text, last 4 KiB, for status messages
}
```

`FileState` is `Complete`, `Renamed`, `Damaged` or `Missing`.

Outcomes against errors: "repair possible" and "repair not possible" are
`Status` values, because the caller branches on them, not failures. Library
failures are sentinels for `errors.Is`: `ErrInsufficientCriticalData`,
`ErrIO`, `ErrMemory`, `ErrRepairFailed`, `ErrLogic`, `ErrInvalidOptions`,
`ErrUnavailable`. A failure still returns the `Result` it got so far, `Log`
included. A cancelled `ctx` returns `ctx.Err()`.

`Purge` replaces clustarr's `removePar2Backups` with the library's own
`purgefiles`.

**The memory default is par2go's, not the library's.** `Process` takes
`memorylimit` in bytes and uses it as given (`AllocateBuffers` sizes chunks
from it); the CLI's default -- 1/8 of physical memory, at least 256 MiB --
lives in `CommandLine`, which par2go does not use. It is also the wrong
measure in a pod: `GetTotalPhysicalMemory` reads the host, so on a 64 GiB node
the CLI sizes 8 GiB of buffers inside a container limited to less. par2go's
default is 1/8 of the cgroup v2 limit (`/sys/fs/cgroup/memory.max`), or of
`MemTotal` from `/proc/meminfo` when the limit is `max` or unreadable, with a
floor of 256 MiB; never 0.

**One job at a time per process.** `Process` reads process-wide statics
(`static u32 filethreads`), so a package mutex serialises jobs: a second
`Repair` waits for the first. The usenet engine repairs one transfer at a time
today, so nothing is lost.

## 4. Shim internals and data flow

### 4.1 C ABI

```c
typedef struct p2_job p2_job;

p2_job *p2_new(const p2_options *opts);           /* copies every string */
int     p2_run(p2_job *job, int repair);          /* blocks; returns P2_* */
void    p2_progress(p2_job *job, p2_progress *out);
void    p2_cancel(p2_job *job);                   /* any thread, any time */
int     p2_counts(p2_job *job, p2_counts *out);   /* after p2_run */
int     p2_file(p2_job *job, int i, p2_file_result *out); /* after p2_run */
size_t  p2_log(p2_job *job, char *buf, size_t n); /* after p2_run */
void    p2_free(p2_job *job);
```

Every struct in the ABI is fixed-size: scalars, plus fixed-length `char`
arrays for names, truncation flagged. Nothing is allocated across the boundary
for Go to free. Return codes are the `Result` values, `P2_CANCELLED`, and
`P2_INVALID` for bad options.

### 4.2 The C++ side

`p2_job` owns a `class Job : public Par2::Par2Repairer`:

- **Output.** `sout` and `serr` write to a capped `streambuf` that keeps the last
  4 KiB, which becomes `Result.Log`. The noise level stays `nlNormal`, because
  the repairer advances its progress meter only above `nlQuiet`.
- **Hooks.** `SigProgress` stores the per mille in a `std::atomic<int>`, and the
  phase likewise. `SigHeaders` copies `ParHeaders` and `SigFilename` the current
  name under the job's mutex. `SigDone` adds a file record under the mutex.
  Each hook is a copy and nothing more, so par2's worker threads never wait on
  Go.
- **After `Process` returns,** the shim snapshots the counters (section 2) and
  gives each file record its state from `sourcefilemap`.
- **Cancellation.** `p2_cancel` sets the job's own `std::atomic<bool>
  cancel_requested` and the base class's `cancelled`. A cancel during repair
  makes `ProcessData` return false, and the repairer then calls
  `DeleteIncompleteTargetFiles()` and returns `eFileIOError`. So `p2_run`
  returns `P2_CANCELLED` whenever `cancel_requested` is set, whatever `Process`
  returned.
- **No exception escapes.** Every `p2_*` body is `try`/`catch(...)`:
  `std::bad_alloc` maps to `eMemoryError`, anything else to `eLogicError` with
  `what()` appended to the log. An exception unwinding into purego's frames
  would abort the process.

### 4.3 The Go side of `Repair` (and `Verify`, with `repair = 0`)

1. `Available()`, then validate: the index exists, every extra file is under
   `Dir`, `MemoryLimit >= 0`. Failure is `ErrInvalidOptions`.
2. Take the package mutex. `p2_new(&opts)`.
3. A goroutine calls `runtime.LockOSThread()`, then `p2_run`, which blocks, and
   sends its return code on a channel.
4. The caller loops on `select`:
   - each `PollEvery` tick: `p2_progress`, passed to `opts.Progress`;
   - `ctx.Done()`: `p2_cancel`, then **keep waiting** for the run's channel.
     Go never frees a job C still holds;
   - the run's channel: `p2_counts`, `p2_file` for each file, `p2_log`,
     `p2_free`, then build the `Result`.
5. Map the result:

| Shim result | Go result |
| --- | --- |
| `eSuccess`, nothing damaged or missing | `Status: AllCorrect` |
| `eSuccess` after a repair | `Status: Repaired` |
| `eRepairPossible` (only from `Verify`) | `Status: RepairPossible` |
| `eRepairNotPossible` | `Status: RepairNotPossible`, `BlocksNeeded = max(0, missing - recovery)` |
| `eInsufficientCriticalData`, `eFileIOError`, `eMemoryError`, `eRepairFailed`, `eLogicError` | the matching sentinel, with `Result` (`Log` included) filled in |
| `eInvalidCommandLineArguments`, `P2_INVALID` | `ErrInvalidOptions` |
| `P2_CANCELLED` | `ctx.Err()` |

Completion is the run goroutine's return, never a polled "done" flag: a
polled flag can be read after `p2_free`, and it leaves a window in which the
job is done but not yet known to be.

## 5. Why C never calls Go

The one-way boundary is deliberate, and a later change should not "simplify"
progress into callbacks without answering each point below. These claims were
checked against purego v0.9.1's source on 2026-10-06.

- **Foreign threads.** par2-turbo calls its hooks from its own worker threads.
  purego can take a callback on a thread Go did not create only through
  `internal/fakecgo`, its reimplementation of `runtime/cgo`'s thread hooks
  (`x_cgo_bindm`, `needm`), linked to Go runtime internals. That path does
  exist, so such a callback need not crash, but it is the least-exercised and
  most Go-version-sensitive code in purego. (A commonly repeated claim that the
  Go runtime simply panics on such a callback is wrong for purego with fakecgo.)
- **A fixed, never-freed table.** `NewCallback` hands out entries from a table
  of 2000 trampolines assembled into the binary (`zcallback_amd64.s`,
  `maxCB = 2000`), and "any memory allocated for these callbacks is never
  released". A callback per job would exhaust it in a long-running engine; a
  fixed global set would need a handle registry to find the job. (It generates
  no machine code at runtime, so the W^X objection sometimes raised against
  callback trampolines does not apply to purego.)
- **Narrow signatures.** Every argument must fit in a `uintptr`, and there is at
  most one result: scalars, bools and pointers. A `std::string` cannot cross, so
  every hook would need a C copy and a length, plus unsafe reads on the Go side.
- **Panics and stalls.** A Go panic inside a callback would unwind through
  par2's C++ frames. A slow `Progress` function would stall par2's worker
  threads while they hold the repairer's locks.

Polling has none of these costs. purego's calls into C are cheap, and four
calls a second is nothing beside a repair. So hooks write state that C owns,
and Go reads it.

```go
// Inside Repair, after p2_new; job is the *p2_job, done the run's channel.
tick := time.NewTicker(opts.pollEvery())
defer tick.Stop()
for {
	select {
	case <-tick.C:
		if opts.Progress != nil {
			var p p2Progress
			p2_progress(job, &p)
			opts.Progress(p.toGo())
		}
	case <-ctx.Done():
		p2_cancel(job)
		code := <-done // C still holds job: wait, never free early
		return finish(job, code, ctx.Err())
	case code := <-done:
		return finish(job, code, nil)
	}
}
```

What is **not** handled: a crash inside the C++ library (a SIGSEGV on a hostile
or corrupt set) takes the process down. The only remedy is process isolation,
which is the clustarr integration spec's to decide.

## 6. Build and release

`shim/build.sh`:

1. Clone par2cmdline-turbo at the pinned tag; refuse unless `HEAD` is the pinned
   commit.
2. `cmake -DBUILD_LIB=ON -DENABLE_CREATOR=OFF -DENABLE_PAR1=OFF
   -DCMAKE_POSITION_INDEPENDENT_CODE=ON` and build the static `par2-turbo`.
3. Compile `par2go.cpp` and link `libpar2go.so` against it with
   `-static-libstdc++ -static-libgcc -Wl,--no-undefined`, exporting only the
   `p2_*` symbols (`-fvisibility=hidden`, plus a version script).

Builds run in `debian:bookworm` (glibc 2.36), the base of clustarr's media
image, so the library runs there and on any newer glibc.

Release (`release.yml`, on a `v*` tag): build on `ubuntu-24.04` and
`ubuntu-24.04-arm`, each inside a bookworm container, and attach
`libpar2go-linux-amd64.so`, `libpar2go-linux-arm64.so` and `SHA256SUMS`.
Module tags start at `v0.1.0`. A consumer fetches a pinned asset and checks its
digest, as `Dockerfile.media` fetches par2 today.

## 7. Testing

All tests run with `-race`.

1. **No silent skip.** Without the library, tests skip naming `$PAR2GO_LIB`,
   unless `PAR2GO_REQUIRE=1`, which turns the skip into a failure. CI always
   sets it. (clustarr's `KUBEBUILDER_ASSETS` and `CLUSTARR_PG_ASSETS` show what a
   silent skip costs.)
2. **Fixtures from the real producer.** `testdata/make.sh` builds each set with
   the real `par2` CLI, and its output is committed:
   - intact;
   - damaged and repairable;
   - damaged and unrepairable, needing exactly N more blocks;
   - files on disk under names the set does not record (the obfuscated-name
     case, matched through `ExtraFiles`);
   - volumes named `name.vol-01.par2` with no block counts in the names.
3. **Differential.** With a `par2` binary on `PATH` (CI installs the pinned
   par2cmdline-turbo release), every fixture's `Status` and `BlocksNeeded` must
   equal the CLI's verdict on a fresh copy of the same set. This is the check
   that the members the shim reads mean what par2's text says.
4. **Cancellation.** A set of about 64 MB, generated at test time and not
   committed, is damaged; its repair is cancelled at about 30%. Assert
   `ctx.Err()`, no partial target file left behind, and a second `Repair` of
   the same set succeeding.
5. **Serialisation.** Two concurrent `Repair` calls both complete correctly,
   and the second provably started after the first finished.
6. **Linkage guard.** Through `debug/elf`, the built library's `DT_NEEDED` must
   be a subset of `libc.so.6`, `libm.so.6` and `ld-linux-*`: nothing C++ may be
   dynamic. Its dynamic symbol table exports only `p2_*`.
7. **No cgo.** `go list -deps` of the module includes neither `runtime/cgo` nor
   an import of `C`.
8. **Falsification.** Before a guard is called done, break what it guards
   (drop `-static-libstdc++`; make `p2_run` ignore `cancel_requested`; read
   `availableblockcount` in place of `missingblockcount`) and watch the test
   fail by name.

## 8. Open questions for the integration spec (not this one)

- The usenet engine's own binary and image, and the
  `TestClustarrNeverLinksADynamicLoader` boundary.
- Whether repair runs in a child process of that engine, for crash isolation.
- Mapping `Result` onto `Par2Result` and `status.engineFailureReason`, and
  retiring the exec path, its regexes and the `par2` binary.
