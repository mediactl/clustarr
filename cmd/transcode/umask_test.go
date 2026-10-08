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

package main

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/app/transcode/jobspec"
)

// A graft pod writes into /data, so UMASK is applied before --graft-task
// runs (§11: every media-touching pod creates files with UMASK 002), and a
// UMASK it cannot read is a misconfigured pod, not a graft that fails.
func TestUMASKIsAppliedBeforeAGraft(t *testing.T) {
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })
	t.Setenv("UMASK", "002")
	assert.Equal(t, 1, run([]string{"--graft-task", "not json"}, env(nil)), "a task the loop did not issue")
	assert.Equal(t, 0o002, syscall.Umask(0o022), "the process umask after run")
	t.Setenv("UMASK", "nonsense")
	assert.Equal(t, jobspec.WorkerExitMisconfigured, run([]string{"--graft-task", "{}"}, env(nil)))
}
