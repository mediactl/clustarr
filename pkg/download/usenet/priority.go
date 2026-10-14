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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"time"
)

// turnPollInterval is how often a job waiting its turn -- paused, or
// outranked by a higher-priority transfer -- looks again. It is the same
// cadence the pause wait has always used.
const turnPollInterval = 250 * time.Millisecond

// priorityRank orders the three spec.priority classes. An empty value is the
// CRD default, normal.
func priorityRank(p commonv1.DownloadPriority) int {
	switch p {
	case commonv1.DownloadPriorityHigh:
		return 2
	case commonv1.DownloadPriorityLow:
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
	mine := priorityRank(j.getPriority())
	if mine == priorityRank(commonv1.DownloadPriorityHigh) {
		return false
	}

	c.mu.Lock()
	others := make([]*job, 0, len(c.jobs))
	for _, o := range c.jobs {
		if o != j && priorityRank(o.getPriority()) > mine {
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
	return j.stage == commonv1.DownloadStageTransferring && j.status == download.StatusDownloading
}

// getPriority is j's spec.priority class ("" reads as normal wherever it is
// ranked).
func (j *job) getPriority() commonv1.DownloadPriority {
	p, _ := j.prio.Load().(commonv1.DownloadPriority)
	return p
}

// setPriority changes j's class and reports whether it changed; "" and
// normal are the same class.
func (j *job) setPriority(p commonv1.DownloadPriority) bool {
	if priorityRank(j.getPriority()) == priorityRank(p) {
		return false
	}
	j.prio.Store(p)
	return true
}

// waitWhilePaused blocks while j is paused -- by a caller or by the health
// action -- for the stages the priority gate does not cover.
func (j *job) waitWhilePaused(ctx context.Context) error {
	for j.paused.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(turnPollInterval):
		}
	}
	return ctx.Err()
}

// waitForTurn blocks while j is paused or outranked, or until it holds one
// of the client's Config.MaxActive transfer slots. Partial files stay on
// disk, which is what makes waiting cheap: resuming re-fetches only the
// articles the bitsets still show missing.
//
// A paused or outranked job gives its slot back, so a health-paused job or
// a normal one behind a high one never holds a slot it is not using. A job
// that waited counts its stall time from the moment it got its turn, as the
// engine's stall watcher already does for a pause: the wait is the queue's
// time, not the release's.
func (j *job) waitForTurn(ctx context.Context) error {
	waited := false
	for {
		switch {
		case j.paused.Load():
			j.client.releaseSlot(j)
		case j.client.outranked(j):
			j.client.releaseSlot(j)
			j.waitingSlot.Store(true)
		case j.client.admit(j):
			j.waitingSlot.Store(false)
			if waited {
				j.mu.Lock()
				j.lastProgress = time.Now()
				j.mu.Unlock()
			}
			return ctx.Err()
		default:
			j.waitingSlot.Store(true)
		}
		waited = true
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(turnPollInterval):
		}
	}
}

// admit gives j a transfer slot if it holds one already, or if one is free
// and no job ahead of it is waiting for one: the higher class first, then
// the earlier Add (jobAhead). A higher-class job waiting for a slot also
// outranks every lower-class holder (outranked counts it as transferring),
// so they give theirs back at their next batch.
//
// Lock order: the client lock alone; other jobs are read through their
// atomics.
func (c *Client) admit(j *job) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.slots[j]; ok {
		return true
	}
	if len(c.slots) >= c.maxActive {
		return false
	}
	for _, o := range c.jobs {
		if _, holds := c.slots[o]; holds || o == j || !o.waitingSlot.Load() || o.paused.Load() {
			continue
		}
		if jobAhead(o, j) {
			return false
		}
	}
	c.slots[j] = struct{}{}
	return true
}

// releaseSlot gives back j's transfer slot, if it holds one.
func (c *Client) releaseSlot(j *job) {
	j.waitingSlot.Store(false)
	c.mu.Lock()
	delete(c.slots, j)
	c.mu.Unlock()
}

// jobAhead reports whether a takes a free transfer slot before b: the
// higher priority class, then the earlier Add, then the id, so the order
// is total.
func jobAhead(a, b *job) bool {
	ra, rb := priorityRank(a.getPriority()), priorityRank(b.getPriority())
	if ra != rb {
		return ra > rb
	}
	if !a.addedAt.Equal(b.addedAt) {
		return a.addedAt.Before(b.addedAt)
	}
	return a.id < b.id
}

// waitToPostProcess takes one of the client's Config.MaxPostProcess
// tokens, waiting Queued in the verifying stage while every one is taken,
// and returns the func that gives it back. Waiting there rather than in
// the transferring stage keeps a finished high-class job from outranking
// lower-class transfers while it waits.
func (j *job) waitToPostProcess(ctx context.Context) (func(), error) {
	release := func() { <-j.client.post }
	select {
	case j.client.post <- struct{}{}:
		return release, nil
	default:
	}
	j.setStage(commonv1.DownloadStageVerifying, download.StatusQueued)
	j.mu.Lock()
	j.message = "waiting for another download to finish repairing and unpacking"
	j.mu.Unlock()
	j.waitingPost.Store(true)
	defer j.waitingPost.Store(false)
	select {
	case j.client.post <- struct{}{}:
		j.mu.Lock()
		j.message = ""
		j.lastProgress = time.Now()
		j.mu.Unlock()
		return release, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}
