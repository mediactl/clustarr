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

package usenet

import "fmt"

// The scratch budget ([Config.ScratchBudget]). A working area of a fixed
// size -- a node-local volume, which no provisioner enforces, or an emptyDir
// the kubelet evicts the pod past -- must never be promised to more releases
// than it holds. A job reserves its peak ([job.scratchNeed]) when it is first
// admitted to a transfer slot, and only while the reservation fits beside
// every started job's; it holds it while paused or outranked, since its
// partial files stay, until its files leave ScratchDir: published, or
// removed. A job waiting for space waits its turn as one waiting for a slot
// does, so a large release is never starved by smaller ones behind it.

// scratchNeed is j's peak in the working area: its articles, the unpacked
// copy beside them, and headroom for par2's rebuilt files -- the free-space
// pre-flight's own reckoning ([job.checkDiskSpace]).
func (j *job) scratchNeed() int64 { return 2*j.nzb.TotalBytes + diskHeadroomBytes }

// reserveScratchLocked gives j its reservation unless it holds one already;
// past the budget it refuses, unless force: a re-attached job's files are
// already on disk. The caller holds c.mu.
func (c *Client) reserveScratchLocked(j *job, force bool) bool {
	if c.cfg.ScratchBudget <= 0 {
		return true
	}
	if _, ok := c.scratch[j]; ok {
		return true
	}
	need := j.scratchNeed()
	if !force && c.scratchHeld.Load()+need > c.cfg.ScratchBudget {
		return false
	}
	c.scratch[j] = need
	c.scratchHeld.Add(need)
	return true
}

// releaseScratchLocked gives back j's reservation, if it holds one. The
// caller holds c.mu.
func (c *Client) releaseScratchLocked(j *job) {
	if need, ok := c.scratch[j]; ok {
		delete(c.scratch, j)
		c.scratchHeld.Add(-need)
	}
}

// releaseScratch gives back j's reservation once its files have left
// ScratchDir.
func (c *Client) releaseScratch(j *job) {
	c.mu.Lock()
	c.releaseScratchLocked(j)
	c.mu.Unlock()
	c.turnChanged() // a job waiting for space may fit now
}

// scratchWaitMessage is a job's message while it waits for space. It reads
// the held total lock-free: item holds j.mu, and nothing takes the client
// lock while holding a job's.
func (c *Client) scratchWaitMessage(j *job) string {
	return fmt.Sprintf("waiting for scratch space: needs %s, %s of the %s budget held by started jobs",
		gib(j.scratchNeed()), gib(c.scratchHeld.Load()), gib(c.cfg.ScratchBudget))
}

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }
