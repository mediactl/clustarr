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
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// turnPollInterval is how often a paused job looks again (waitWhilePaused).
const turnPollInterval = 250 * time.Millisecond

// turnWaitInterval is how often the slot queue ([Client.queueLoop]) looks
// again when nothing woke it: a grant's status flip can end an outranking
// without any of the changes that kick it. Waiting jobs themselves never
// look; each waits on its own ticket. On talos (2026-10-28) about 720 queued
// jobs each looked under the client lock, walking every job, at every wake
// and every few seconds, and the engine's commands waited behind them for
// minutes. A variable only so a test can take the fallback away and see the
// kick alone.
var turnWaitInterval = 2 * time.Second

// rankMaxAge bounds how stale [Client.outranked]'s view of the highest class
// transferring may be. A change of stage, pause, priority or job list makes
// it stale at once ([Client.rankChanged]); the age covers a grant's status
// flip, which moves none of those.
const rankMaxAge = 200 * time.Millisecond

// rankCache is the highest priority rank among the jobs transferring, as of
// at (unix nanoseconds; 0 means stale). mu lets one caller refresh it while
// the rest read the last value.
type rankCache struct {
	mu   sync.Mutex
	at   atomic.Int64
	high atomic.Int32
}

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
// Every transfer worker asks before each batch, so the answer comes from
// [rankCache], refreshed at most every rankMaxAge by one caller: the job
// list is walked once for all of them, not once per worker per batch. A job
// past its transfer ([job.offSlot]) is never outranked; it holds no slot to
// give back.
//
// Lock order: the client lock, then each job's lock. The caller must hold
// neither.
func (c *Client) outranked(j *job) bool {
	if j.offSlot.Load() {
		return false
	}
	mine := priorityRank(j.getPriority())
	if mine == priorityRank(commonv1.DownloadPriorityHigh) {
		return false
	}
	// j's own class never outranks j: the comparison is strict.
	return int(c.highestTransferring()) > mine
}

// highestTransferring is the highest priority rank among the jobs
// transferring, -1 when none is, from the cache when it is fresh.
func (c *Client) highestTransferring() int32 {
	r := &c.rank
	fresh := func() bool {
		at := r.at.Load()
		return at != 0 && time.Since(time.Unix(0, at)) < rankMaxAge
	}
	if fresh() {
		return r.high.Load()
	}
	// One caller refreshes; the rest go on with the last answer rather than
	// queue behind the client lock the refresh takes.
	if !r.mu.TryLock() {
		return r.high.Load()
	}
	defer r.mu.Unlock()
	if fresh() { // another caller refreshed it meanwhile
		return r.high.Load()
	}
	// Stamped before the walk: a change during it marks the cache stale
	// again (rankChanged) rather than being overwritten by this result.
	at := time.Now().UnixNano()
	r.at.Store(at)
	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, o := range c.jobs {
		jobs = append(jobs, o)
	}
	c.mu.Unlock()
	high := int32(-1)
	for _, o := range jobs {
		if rk := int32(priorityRank(o.getPriority())); rk > high && o.transferring() { //nolint:gosec // 0..2.
			high = rk
		}
	}
	r.high.Store(high)
	if r.at.Load() != at {
		r.at.Store(0)
	}
	return high
}

// rankChanged marks the outranking cache stale, after a change of a job's
// stage, pause, priority or presence.
func (c *Client) rankChanged() { c.rank.at.Store(0) }

// turnChanged kicks the slot queue: a slot or scratch came free, or a job's
// stage, pause, priority or presence changed, any of which may let a
// waiting job run. It also marks the outranking cache stale. It never
// blocks: kicks that arrive while the queue is filling fold into one.
func (c *Client) turnChanged() {
	c.rankChanged()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// closedTicket is the ticket of a job that holds its slot already.
var closedTicket = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// wantSlot puts j in the slot queue, if it is not there already, and
// returns the ticket the queue closes when it gives j a slot. Every worker
// of j waits on the one ticket.
func (c *Client) wantSlot(j *job) <-chan struct{} {
	c.mu.Lock()
	if j.holdsSlot.Load() {
		c.mu.Unlock()
		return closedTicket
	}
	if c.waiting == nil {
		c.waiting = map[*job]chan struct{}{}
	}
	t, ok := c.waiting[j]
	if !ok {
		t = make(chan struct{})
		c.waiting[j] = t
	}
	c.mu.Unlock()
	if !ok {
		c.turnChanged() // a new waiter may take a free slot now
	}
	return t
}

// dropWant takes j out of the slot queue: it stopped waiting.
func (c *Client) dropWant(j *job) {
	c.mu.Lock()
	delete(c.waiting, j)
	c.mu.Unlock()
}

// queueLoop fills free transfer slots ([Client.fillSlots]) at every kick
// ([Client.turnChanged]) and every turnWaitInterval, until stop closes.
func (c *Client) queueLoop(stop <-chan struct{}) {
	defer c.wg.Done()
	t := time.NewTicker(turnWaitInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.kick:
		case <-t.C:
		}
		c.fillSlots()
	}
}

// fillSlots gives free transfer slots to the jobs waiting for one, in
// [jobAhead] order: the higher class first, then the earlier Add. A paused
// job is passed over, and so is an outranked one: a strictly higher class
// is transferring or waiting to. The first job whose scratch reservation
// does not fit the budget (scratch.go) stops the fill, as the one ahead of
// the rest. While every slot is taken it looks at no job at all.
//
// Lock order: the client lock, then each job's lock.
func (c *Client) fillSlots() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.slots) >= c.maxActive || len(c.waiting) == 0 {
		return
	}
	queue := make([]*job, 0, len(c.waiting))
	for j := range c.waiting {
		if c.jobs[j.id] != j { // removed; its worker drops the ticket too
			delete(c.waiting, j)
			continue
		}
		queue = append(queue, j)
	}
	slices.SortFunc(queue, func(a, b *job) int {
		switch {
		case jobAhead(a, b):
			return -1
		case jobAhead(b, a):
			return 1
		}
		return 0
	})
	high := -1
	for _, o := range c.jobs {
		if rk := priorityRank(o.getPriority()); rk > high && o.transferring() {
			high = rk
		}
	}
	for _, j := range queue {
		if len(c.slots) >= c.maxActive {
			return
		}
		c.queueLooks.Add(1)
		rank := priorityRank(j.getPriority())
		if j.paused.Load() || (rank < priorityRank(commonv1.DownloadPriorityHigh) && high > rank) {
			continue
		}
		if !c.reserveScratchLocked(j, false) {
			j.waitingSpace.Store(true)
			return
		}
		j.waitingSpace.Store(false)
		c.giveSlotLocked(j)
	}
}

// giveSlotLocked gives j a transfer slot and closes its ticket, if it has
// one. The caller holds the client lock.
func (c *Client) giveSlotLocked(j *job) {
	c.slots[j] = struct{}{}
	j.holdsSlot.Store(true)
	if t, ok := c.waiting[j]; ok {
		close(t)
		delete(c.waiting, j)
	}
}

// transferring reports whether j is in its article-fetching stage and not
// paused. A job that is itself waiting on a higher class still counts: the
// higher class is running either way, so a lower one is outranked either
// way. A job past its transfer, retrying or chasing its missing articles
// without a slot ([job.offSlot]), does not: it asks for a handful of
// articles, and a lower class's transfer has no reason to stop for it.
func (j *job) transferring() bool {
	if j.paused.Load() || j.offSlot.Load() {
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
func (j *job) waitForTurn(ctx context.Context) error { return j.waitTurn(ctx, true) }

// waitTurn is waitForTurn; withGrant false takes the slot alone, for a
// chase after the transfer gave its grant back.
func (j *job) waitTurn(ctx context.Context, withGrant bool) error {
	waited := false
	for {
		switch {
		case j.paused.Load():
			j.client.releaseSlot(j)
			j.giveGrant(ctx)
		case j.client.outranked(j):
			j.client.releaseSlot(j)
			j.giveGrant(ctx)
			j.waitingSlot.Store(true)
		// A holder answers from its own flag: a transfer worker asks before
		// every batch, and the client lock is the queue's.
		case j.holdsSlot.Load():
			j.waitingSlot.Store(false)
			if waited {
				j.mu.Lock()
				j.lastProgress = time.Now()
				j.mu.Unlock()
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !withGrant {
				return nil
			}
			// The slot is the pod's; the connections are the account's. A
			// pause or an outranking while parked comes back here as
			// errTurnLost, and the next pass gives the slot back.
			err := j.ensureGrant(ctx)
			if !errors.Is(err, errTurnLost) {
				return err
			}
			continue
		default:
			j.waitingSlot.Store(true)
		}
		waited = true
		// The queue (queueLoop) gives the slot and closes the ticket; a
		// paused or outranked job waits on it too, since the queue passes
		// it over until a resume or the end of the outranking kicks it.
		select {
		case <-ctx.Done():
			j.client.dropWant(j)
			return ctx.Err()
		case <-j.client.wantSlot(j):
		}
	}
}

// releaseSlot gives back j's transfer slot, if it holds one, and wakes the
// jobs waiting for one.
func (c *Client) releaseSlot(j *job) {
	j.waitingSlot.Store(false)
	j.waitingSpace.Store(false)
	if !j.holdsSlot.Load() {
		return
	}
	c.mu.Lock()
	delete(c.slots, j)
	j.holdsSlot.Store(false)
	c.mu.Unlock()
	c.turnChanged()
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

// waitToPublish takes one of the client's Config.MaxPublish tokens for
// the copy onto the data volume, waiting Queued in the publishing stage
// while every one is held. The job keeps its post-processing slot
// meanwhile, and the publishing stage's clock starts when the copy does.
func (j *job) waitToPublish(ctx context.Context) (func(), error) {
	release := func() { <-j.client.publish }
	select {
	case j.client.publish <- struct{}{}:
		return release, nil
	default:
	}
	j.mu.Lock()
	j.status = download.StatusQueued
	j.message = "waiting for another download to finish publishing"
	j.mu.Unlock()
	j.waitingPublish.Store(true)
	defer j.waitingPublish.Store(false)
	select {
	case j.client.publish <- struct{}{}:
		j.mu.Lock()
		j.status = download.StatusDownloading
		j.message = ""
		j.stageStarted = time.Now()
		j.lastProgress = j.stageStarted
		j.mu.Unlock()
		return release, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
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
