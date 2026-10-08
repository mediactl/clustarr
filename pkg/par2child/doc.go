/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

// Package par2child runs par2go (github.com/mediactl/par2go, par2cmdline-turbo
// as a library) for the usenet engine, in a short-lived child of this binary
// rather than in the engine's own process (spec 2026-10-06 §8.4, OD21).
//
// Why a child:
//
//   - A crash would become a crash loop. par2's worker threads are not
//     exception-safe (an uncaught exception calls std::terminate, and
//     gf16mul.cpp calls abort()), so in-process a bad set would kill the
//     engine, re-attach would restart the same repair, and the engine would
//     crash again on every start. In a child it costs one repair.
//   - Some phases cannot be cancelled (ComputeRSmatrix, AllocateBuffers,
//     CreateTargetFiles, any read blocked on the NFS working area). A child is
//     SIGKILLed once its grace after the deadline has passed.
//   - Repair buffers live outside the engine's GOMEMLIMIT; in a child a
//     cgroup OOM picks the child (its oom_score_adj is 1000), not the engine.
//   - par2go runs one job at a time per process. Children repair concurrently,
//     as the par2 processes they replace did.
//
// The protocol (§8.4.3): the parent writes a big-endian uint32 length and a
// gob [Request] on the child's stdin and keeps stdin open; the child writes
// one gob [Result] on fd 3 and exits. Closing stdin cancels the run. gob, not
// JSON: NZB and par2 names are arbitrary bytes, and encoding/json rewrites
// invalid UTF-8.
//
// exec.go holds the module's one production exec, and its target is always
// this binary (os.Executable(), "agent par2-repair" in the native image); no
// external program is ever run (ADR-0017). The package imports par2go and the
// standard library only, so any binary can take it or refuse it.
package par2child
