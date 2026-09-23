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

import (
	"context"
	"time"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// turnPollInterval is how often a job waiting its turn -- paused, or
// outranked by a higher-priority transfer -- looks again. It is the same
// cadence the pause wait has always used.
const turnPollInterval = 250 * time.Millisecond

// priorityRank orders the three spec.priority classes. An empty value is the
// CRD default, normal.
func priorityRank(p downloadv1alpha1.DownloadPriority) int {
	switch p {
	case downloadv1alpha1.DownloadPriorityHigh:
		return 2
	case downloadv1alpha1.DownloadPriorityLow:
		return 0
	default:
		return 1
	}
}

// outranked reports whether some other job of a strictly higher priority
// class is transferring right now, in which case j must not fetch articles.
//
// This is how this client honours Download.spec.priority (gap-fix ruling
// R-12). The design spec gives the field only its enum; the API type's doc
// says what it means -- "high jumps the queue", "low runs only when the
// engine is otherwise idle" -- and that is strict class ordering, which is
// what SABnzbd and NZBGet do with their own queue priorities. Every job
// here draws on one shared pool of provider connections, so without it a
// low-priority season pack added first would take the connections a
// high-priority grab needs. Before this, AddRequest.Priority was accepted
// and never read.
//
// Only the transfer is gated. Propagation delay, pre-check, repair, unpack
// and publish are not provider-bound, so a lower-class job may do them
// alongside a higher-class transfer. Jobs of the same class share the pool
// as they always have.
//
// Lock order: the client lock, then each other job's lock. The caller must
// hold neither.
func (c *Client) outranked(j *job) bool {
	mine := priorityRank(j.priority)
	if mine == priorityRank(downloadv1alpha1.DownloadPriorityHigh) {
		return false
	}

	c.mu.Lock()
	others := make([]*job, 0, len(c.jobs))
	for _, o := range c.jobs {
		if o != j && priorityRank(o.priority) > mine {
			others = append(others, o)
		}
	}
	c.mu.Unlock()

	for _, o := range others {
		if o.transferring() {
			return true
		}
	}
	return false
}

// transferring reports whether j is in its article-fetching stage and not
// paused. A job that is itself waiting on a higher class still counts: the
// higher class is running either way, so a lower one is outranked either
// way.
func (j *job) transferring() bool {
	if j.paused.Load() {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stage == downloadv1alpha1.DownloadStageTransferring && j.status == download.StatusDownloading
}

// waitForTurn blocks while j is paused or outranked. Partial files stay on
// disk, which is what makes waiting cheap: resuming re-fetches only the
// articles the bitsets still show missing.
func (j *job) waitForTurn(ctx context.Context) error {
	for j.paused.Load() || j.client.outranked(j) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(turnPollInterval):
		}
	}
	return ctx.Err()
}
