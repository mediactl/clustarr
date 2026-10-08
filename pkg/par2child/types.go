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

package par2child

import (
	"context"
	"errors"
	"fmt"
	"time"

	par2 "github.com/mediactl/par2go"
)

const (
	// ChildCommand is the first argument that makes this binary the repair
	// child: "agent par2-repair".
	ChildCommand = "par2-repair"
	// DefaultGrace is how long a child may take to stop after its deadline
	// before it is killed.
	DefaultGrace = 30 * time.Second
	// reapSlack is how long, after the grace and the kill, the parent still
	// waits for the child to be reaped before it gives up on it.
	reapSlack = 5 * time.Second
	// maxResult caps the framed request the child reads and the result the
	// parent reads.
	maxResult = 1 << 20
)

// ErrChildDied is a child that ended without writing a result: killed, out
// of memory, or crashed inside par2's C++.
var ErrChildDied = errors.New("par2child: repair child exited without a result")

// Op is what a [Request] asks the child to do.
type Op uint8

const (
	// OpRepair verifies the set and repairs it when it can.
	OpRepair Op = 1
	// OpProbe loads the library and reports where it came from.
	OpProbe Op = 2
)

// ErrKind is a par2go sentinel as it crosses gob, which cannot carry an
// error value.
type ErrKind uint8

const (
	ErrKindNone ErrKind = iota
	ErrKindUnavailable
	ErrKindInvalidOptions
	ErrKindInsufficientCriticalData
	ErrKindIO
	ErrKindMemory
	ErrKindRepairFailed
	ErrKindLogic
	ErrKindCancelled
	ErrKindOther
)

// sentinels maps every kind but None and Other onto the error it stands
// for, in a fixed order so kindOf is deterministic.
var sentinels = []struct {
	kind ErrKind
	err  error
}{
	{ErrKindUnavailable, par2.ErrUnavailable},
	{ErrKindInvalidOptions, par2.ErrInvalidOptions},
	{ErrKindInsufficientCriticalData, par2.ErrInsufficientCriticalData},
	{ErrKindIO, par2.ErrIO},
	{ErrKindMemory, par2.ErrMemory},
	{ErrKindRepairFailed, par2.ErrRepairFailed},
	{ErrKindLogic, par2.ErrLogic},
	{ErrKindCancelled, context.Canceled},
}

// sentinelOf is the error kind k stands for, or nil.
func sentinelOf(k ErrKind) error {
	for _, s := range sentinels {
		if s.kind == k {
			return s.err
		}
	}
	return nil
}

// kindOf maps an error onto the kind that crosses gob.
func kindOf(err error) ErrKind {
	if err == nil {
		return ErrKindNone
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrKindCancelled
	}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			return s.kind
		}
	}
	return ErrKindOther
}

// Request is one job for the child.
type Request struct {
	Op Op
	// Index is the absolute path of the index volume.
	Index string
	// Dir is the absolute base directory; targets are written here.
	Dir string
	// Extras are absolute paths scanned as candidate targets, each under Dir.
	Extras []string
	// Threads and FileThreads are par2go's; 0 is its default.
	Threads, FileThreads int
	// MemoryLimit is in bytes; 0 is par2go's default (1/8 of the cgroup).
	MemoryLimit int64
}

// Result is what the child concluded.
type Result struct {
	Status       par2.Status
	BlocksNeeded int
	Headers      par2.Headers
	// Log is par2go's Log, the tail of par2's own output.
	Log string
	// ErrKind is the sentinel, which cannot cross gob as an error.
	ErrKind ErrKind
	// Err is the error's text.
	Err string
	// Library is the path of the libpar2shim.so the child loaded, for logs.
	Library string
}

// Error is nil, or an error wrapping the matching par2go sentinel (or
// context.Canceled), so errors.Is works in the parent.
func (r Result) Error() error {
	if r.ErrKind == ErrKindNone {
		return nil
	}
	if s := sentinelOf(r.ErrKind); s != nil {
		return fmt.Errorf("%w: %s", s, r.Err)
	}
	return fmt.Errorf("par2child: %s", r.Err)
}

// Runner runs a [Request]: [Exec] in a child of this binary, [InProcess] in
// this process.
type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
	Probe(ctx context.Context) error
}
