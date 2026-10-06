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
`purego` (v0.11.1) and calls a small `extern "C"` shim.

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

**Patched (as built, 2026-10-06).** The nzbgetcom fork differs from
animetosho's v1.5.0 on one point that matters here: it converts every
stored file name from Latin-1 to UTF-8 (`descriptionpacket.cpp`), so a
name already written as UTF-8 -- what par2cmdline, ParPar and MultiPar
write -- is encoded twice, the file reads as missing, and repair writes a
garbled duplicate. `shim/patches/0001-keep-utf8-names.patch` keeps valid
UTF-8 as is; `build.sh` applies it on top of the verified pin.
`0002-scan-progress.patch` fixes verification reporting no progress:
`VerifyDataFile` scanned through a throwaway meter (the `ScanDataFile`
overload that builds `dummy_progress`), and the scan's meter takes a
callback upstream never set; it passes the meter through and adds the
`SigScanProgress` hook.

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
    par2shim.cpp, .h  extern "C" p2_* over a Par2Repairer subclass
    build.sh          fetch par2-turbo at the pin, cmake BUILD_LIB, link libpar2shim.so
  testdata/
    make.sh           regenerates every fixture with the real par2 CLI
    ...               the committed fixtures (a few KB)
  .github/workflows/  ci.yml, release.yml
```

Library search, in order: `$PAR2GO_LIB` (a file path), then `libpar2shim.so` on
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

### 4.1 C ABI (as built: ABI version 2, `shim/par2shim.h`)

```c
typedef struct p2_job p2_job;

int32_t  p2_abi_version(void);                     /* 2; Go refuses any other */
uint64_t p2_sizeof(int32_t which);                 /* struct sizes, checked at load */
p2_job  *p2_new(const char *index, const char *basepath, int64_t memory_limit,
                int32_t threads, int32_t file_threads, int32_t purge);
int32_t  p2_add_extra(p2_job *job, const char *path);
int32_t  p2_start(p2_job *job, int32_t repair);     /* returns at once */
void     p2_progress_read(p2_job *job, p2_progress *out); /* done, result, phase, per mille, file */
void     p2_cancel(p2_job *job);                   /* any thread, any time */
int32_t  p2_counts_read(p2_job *job, p2_counts *out);
int32_t  p2_file_read(p2_job *job, int32_t i, p2_file_result *out);
uint64_t p2_log_read(p2_job *job, char *buf, uint64_t n);
int32_t  p2_active_jobs(void);                     /* started jobs not yet finished */
void     p2_free(p2_job *job);                     /* cancels, joins, frees: safe any time */
```

Every struct in the ABI is fixed-size: 8-byte scalars, then fixed-length
`char` arrays for names, truncation flagged. Nothing is allocated across the
boundary for Go to free. Results are the `Result` values, `P2_CANCELLED`, and
`P2_INVALID` for bad options or a second start.

### 4.2 The C++ side

`p2_job` owns a `class Job : public Par2::Par2Repairer` and the thread it runs on:

- **Its own thread (2026-10-06, owner's decision).** `p2_start` runs the job
  on a `std::thread` the job owns and returns at once; no Go thread waits in C
  for the length of a job. A process-wide mutex in the shim serialises jobs,
  because `Process` reads statics. `p2_free` cancels a running job, joins its
  thread and only then frees it, so freeing is safe whatever the caller is
  doing. Done and the result are atomics `p2_progress_read` reports.
- **Output.** `sout` and `serr` write to a capped `streambuf` that keeps the last
  4 KiB, which becomes `Result.Log`; a `\r` rewinds to the start of its line, as
  a terminal would. The noise level stays `nlNormal`, because the repairer
  advances its progress meters only above `nlQuiet`.
- **Phases and progress.** Loading (par2 files; `SigProgress` per file),
  Verifying (data scan; `SigScanProgress`, added by patch 0002, of the whole
  scan) and Repairing (`BeginRepair`; `SigProgress` of the whole repair). Each
  hook is a copy into atomics or under the job's mutex, so par2's worker
  threads never wait on Go.
- **After verification,** the shim snapshots the counters (section 2) and each
  file's state from `sourcefiles`, as `UpdateVerificationResults` does.
- **Cancellation.** `p2_cancel` sets the job's own `std::atomic<bool>
  cancel_requested` and, with an atomic store, the base class's `cancelled`.
  A cancel during repair makes `ProcessData` return false, and the repairer
  then deletes the partial rebuild and returns `eFileIOError`; the job reports
  `P2_CANCELLED` whenever a cancel was requested and the repair did not
  finish. A repair that finished is reported as finished.
- **Restoring originals.** Repair moves each damaged file to `<name>.1` before
  rebuilding. When a repair stops (cancel or error), the shim moves each one
  back wherever its name is free, so the set is as repairable as before.
- **No exception escapes.** The job's thread and every `p2_*` body catch
  everything: `std::bad_alloc` maps to `eMemoryError`, anything else to
  `eLogicError` with `what()` in the log. par2's own worker threads are outside
  this: a crash or uncaught exception there ends the process (section 5).

### 4.3 The Go side of `Repair` (and `Verify`, with `repair = 0`)

1. `Available()`, then validate: the index exists, every extra file is under
   `Dir`, `MemoryLimit >= 0`. Failure is `ErrInvalidOptions`.
2. Take the package mutex (queueing, and "cancelled while waiting" returns
   `ctx.Err()` without starting). `p2_new`, `p2_add_extra` per extra file,
   `p2_start`; `defer p2_free`, which cancels and joins in C, so a return, a
   panic in `Progress` or a `runtime.Goexit` there all leave safely.
3. Poll: every 5 ms read `p2_progress_read`; return when it reports done;
   call `opts.Progress` every `PollEvery`; on `ctx.Done()`, `p2_cancel` and keep
   polling until the job has stopped.
4. Read `p2_counts_read`, `p2_file_read` per file and `p2_log_read`, and map the result:

| Shim result | Go result |
| --- | --- |
| `eSuccess`, nothing damaged or missing | `Status: AllCorrect` |
| `eSuccess` after a repair | `Status: Repaired` |
| `eRepairPossible` (only from `Verify`) | `Status: RepairPossible` |
| `eRepairNotPossible` | `Status: RepairNotPossible`, `BlocksNeeded = max(0, missing - recovery)` |
| `eInsufficientCriticalData`, `eFileIOError`, `eMemoryError`, `eRepairFailed`, `eLogicError` | the matching sentinel, with `Result` (`Log` included) filled in |
| `eInvalidCommandLineArguments`, `P2_INVALID` | `ErrInvalidOptions` |
| `P2_CANCELLED` | `ctx.Err()` |

A polled done flag is safe here, unlike a design where Go frees the job:
only `p2_free` frees, and it joins first.

## 5. Why C never calls Go

The one-way boundary is deliberate, and a later change should not "simplify"
progress into callbacks without answering each point below. These claims were
checked against purego v0.11.1's source (`syscall_unix.go`, `zcallback_*.s`, `internal/fakecgo`) on 2026-10-06; par2go pins v0.11.1, the version ffgo uses.

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

**An async C thread, as built (2026-10-06).** The first cut ran `p2_run` on
a Go goroutine locked to its OS thread, and an async shim was at first
declined: a blocking purego call holds one thread, as a blocking syscall
does, and par2go runs one job at a time, so it costs one thread either way.
The owner chose the async design anyway, and it is what ships (section 4):
the job runs on a thread the shim owns, Go only polls, and the shim -- not
Go -- decides when job memory may go. The one-way rule above is unchanged:
C still never calls Go.

What is **not** handled: a crash inside the C++ library (a SIGSEGV on a hostile
or corrupt set) takes the process down. The only remedy is process isolation,
which is the clustarr integration spec's to decide.

## 6. Build and release

`shim/build.sh`:

1. Clone par2cmdline-turbo at the pinned tag; refuse unless `HEAD` is the pinned
   commit.
2. `cmake -DBUILD_LIB=ON -DENABLE_CREATOR=OFF -DENABLE_PAR1=OFF
   -DCMAKE_POSITION_INDEPENDENT_CODE=ON` and build the static `par2-turbo`.
3. Compile `par2shim.cpp` and link `libpar2shim.so` against it with
   `-static-libstdc++ -static-libgcc -Wl,--no-undefined`, exporting only the
   `p2_*` symbols (`-fvisibility=hidden`, plus a version script).

Builds run in `debian:bookworm` (glibc 2.36), the base of clustarr's media
image, so the library runs there and on any newer glibc.

Release (`release.yml`, on a `v*` tag): build on `ubuntu-24.04` and
`ubuntu-24.04-arm`, each inside a bookworm container, and attach
`libpar2shim-linux-amd64.so`, `libpar2shim-linux-arm64.so` and `SHA256SUMS`.
Module tags start at `v0.1.0`.

**No `libpar2.so` (2026-10-06).** `v0.1.0` briefly also published the
patched par2cmdline-turbo as a shared C++ library; the owner withdrew it
from that release and from the build, since `libpar2shim.so` already
contains par2 and no headers were published for C++ users. A consumer fetches a pinned asset and checks its
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
   (drop `-static-libstdc++`; make the job ignore `cancel_requested`; read
   `availableblockcount` in place of `missingblockcount`) and watch the test
   fail by name.

## 8. Open questions for the integration spec (not this one)

- The usenet engine's own binary and image, and the
  `TestClustarrNeverLinksADynamicLoader` boundary.
- Whether repair runs in a child process of that engine, for crash isolation.
- Mapping `Result` onto `Par2Result` and `status.engineFailureReason`, and
  retiring the exec path, its regexes and the `par2` binary.
