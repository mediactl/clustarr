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

// Package gomemlimit sets a binary's Go memory limit to a share of its
// container's memory limit, read from the cgroup
// (github.com/KimMachineGun/automemlimit; owner, 2026-11-03). Every
// binary calls [Set] first thing in main.
//
// The share is $AUTOMEMLIMIT, a ratio in (0, 1], which every installer and
// every pod the manager builds sets ([Env], [Share]): 0.80 for a Go-heavy
// process, 0.60 for one whose FFmpeg or par2 allocates outside the Go heap
// (agent-import, agent-caption, the transcode pools), 0.50 for markers,
// whose ONNX Runtime does too (spec 2026-10-06 §10.2.1, OD42). Until then
// the installers baked the share into $GOMEMLIMIT in bytes, which a memory
// limit edit had to recompute; an explicit $GOMEMLIMIT (the chart's
// goMemLimit) still wins, and a process whose cgroup sets no memory limit
// runs without one.
//
// GOMAXPROCS needs no such package: since Go 1.25 the runtime sets it from
// the cgroup's CPU limit itself, rounded up and at least 2, and follows a
// change. go.uber.org/automaxprocs would round it down to 1 on the 500m and
// 1-CPU pods and stop the runtime's re-check (owner, 2026-11-03: not used).
package gomemlimit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
)

const (
	// Env names the Go memory limit's share of the container's memory
	// limit, a ratio in (0, 1].
	Env = "AUTOMEMLIMIT"

	// DefaultPercent is the share when $AUTOMEMLIMIT names none: design
	// §12's 80% of the limit.
	DefaultPercent = 80

	// refresh is how often the limit is read again, so a pod resized in
	// place takes its new limit without a restart.
	refresh = time.Minute
)

// Share is $AUTOMEMLIMIT's value for percent of the memory limit, as the
// chart's clustarr.automemlimit helper renders it: 80 is "0.80".
func Share(percent int) string {
	return strconv.FormatFloat(float64(percent)/100, 'f', 2, 64)
}

// Set sets the Go memory limit from the cgroup's memory limit and
// $AUTOMEMLIMIT, and reads the limit again every minute. It runs before
// logging exists, so a failure is said on stderr and leaves the limit as
// the environment set it.
func Set() {
	_, err := memlimit.Set(
		memlimit.WithRatio(float64(DefaultPercent)/100),
		memlimit.WithRefreshInterval(context.Background(), refresh),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gomemlimit:", err)
	}
}
