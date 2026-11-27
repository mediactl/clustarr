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
	"fmt"
	"slices"
	"sort"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// A transfer leaves a primary that does not hold its post (owner,
// 2026-11-27: "what should probably happen is that frugal gets blacklisted
// when it's missing too many articles and then the download has to use
// easynews - reconciling the connections used").
//
// A transfer runs on one primary under a grant of its connections, and what
// that server refuses is chased through the others on small grants of their
// own. That is right for a post the server holds with holes in it. For a
// post it does not hold at all, the transfer asked it for every article
// while its grant stood idle, then asked each other server for every
// article in turn: on talos thirteen such episodes held all eight slots and
// most of three providers' connections, and moved 1.4 MiB/s between them.
//
// Now a primary that refuses more than half of what the transfer's workers
// asked it for, once that is lackSample articles, is left: its grant goes
// back to the ledger at once, no grant is taken on it again, the articles it
// refused are asked of the next primary as the transfer's own work (the
// requeue), and the transfer goes on there with the rest. When the last
// primary is left and the transfer has been refused more than it has
// fetched, the post is on none of them and the download fails as missing --
// in the time three samples take. When it has fetched more than that, the
// post is there with holes: the judgement is forgotten and the failures are
// the chase's and the health floor's, as before.

// lackSample is how many articles a transfer's workers have asked a primary
// for before the share it refused says the post is not there.
const lackSample = backupSample

// lacks reports a primary the transfer has left.
func (j *job) lacks(server string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lacking[server]
}

// lackingServers are the primaries the transfer has left.
func (j *job) lackingServers() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]string, 0, len(j.lacking))
	for name := range j.lacking {
		out = append(out, name)
	}
	return out
}

// holdingPrimaries are the primaries a transfer grant may be taken on: the
// client's, less the ones the transfer has left; all of them again when it
// has left every one (a health pause an operator resumed).
func (j *job) holdingPrimaries() []string {
	all := j.client.primaries()
	j.mu.Lock()
	defer j.mu.Unlock()
	out := slices.DeleteFunc(slices.Clone(all), func(n string) bool { return j.lacking[n] })
	if len(out) == 0 {
		j.lacking, j.askedOn, j.refusedOn = nil, nil, nil
		return all
	}
	return out
}

// noteAnswers counts what one batch asked server for and was refused, and
// reports the answer that shows the server does not hold the post: once,
// when its refusals pass half of at least lackSample articles. Never for a
// job an operator resumed past its health pause, whose health is no longer
// judged.
func (j *job) noteAnswers(server string, asked, refused int) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if asked == 0 || j.healthOverride || j.lacking[server] {
		return false
	}
	if j.askedOn == nil {
		j.askedOn, j.refusedOn = map[string]int{}, map[string]int{}
	}
	j.askedOn[server] += asked
	j.refusedOn[server] += refused
	if j.askedOn[server] < lackSample || 2*j.refusedOn[server] <= j.askedOn[server] {
		return false
	}
	if j.lacking == nil {
		j.lacking = map[string]bool{}
	}
	j.lacking[server] = true
	return true
}

// requeueSeg keeps an article a left server refused for the next primary.
func (j *job) requeueSeg(fi, si int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.requeue = append(j.requeue, seg{fi, si})
	j.lastError = err
}

// takeRequeued is the next round's batches: the requeued articles not
// fetched since, in file order, at most depth to a batch.
func (j *job) takeRequeued(depth int) []batch {
	if depth < 1 {
		depth = 1
	}
	j.mu.Lock()
	segs := j.requeue
	j.requeue = nil
	segs = slices.DeleteFunc(segs, func(sg seg) bool { return j.done[sg.fi].has(sg.si) })
	j.mu.Unlock()
	sort.Slice(segs, func(a, b int) bool {
		if segs[a].fi != segs[b].fi {
			return segs[a].fi < segs[b].fi
		}
		return segs[a].si < segs[b].si
	})
	segs = slices.Compact(segs)
	var out []batch
	for _, sg := range segs {
		if n := len(out); n > 0 && out[n-1].file == sg.fi && len(out[n-1].segs) < depth {
			out[n-1].segs = append(out[n-1].segs, sg.si)
			continue
		}
		out = append(out, batch{file: sg.fi, segs: []int{sg.si}})
	}
	return out
}

// leaveServer is the transfer leaving server, which noteAnswers has just
// found not to hold the post: the grant on it goes back, and what it
// refused waits for the next primary. With no primary left it is the
// verdict on the post, or the judgement forgotten.
func (j *job) leaveServer(ctx context.Context, server string) error {
	log := logging.FromContext(ctx)
	remaining := 0
	for _, name := range j.client.primaries() {
		if !j.lacks(name) {
			remaining++
		}
	}

	j.mu.Lock()
	held := j.grant != nil && j.grant.Server == server
	if j.preferred == server {
		j.preferred = ""
	}
	asked, refused := j.askedOn[server], j.refusedOn[server]
	fetched := 0
	for i := range j.done {
		fetched += j.done[i].count()
	}
	var failed []seg
	for fi, b := range j.failedSegs {
		for si := range j.nzb.Files[fi].Segments {
			if b.has(si) && !j.exhausted[fi].has(si) {
				failed = append(failed, seg{fi, si})
			}
		}
	}
	waiting := len(j.requeue)
	last := j.lastError
	if remaining > 0 {
		// What it refused is the next primary's to be asked for: no failure
		// yet, and nothing for the health floor to count.
		for _, sg := range failed {
			j.failedSegs[sg.fi].clear(sg.si)
		}
		j.requeue = append(j.requeue, failed...)
	}
	j.mu.Unlock()

	log.InfoContext(ctx, "usenet: the server does not hold the post; the transfer leaves it",
		"download", j.id, "server", server, "asked", asked, "refused", refused, "primariesLeft", remaining)
	if held {
		j.giveGrant(ctx)
	}
	if remaining > 0 {
		return nil
	}
	if refusedAll := len(failed) + waiting; refusedAll > fetched {
		return j.breach(ctx, fmt.Sprintf("no primary server holds the post: each refused more than half of what it was asked for (%d of %d on %s, the last), and %d of %d articles were fetched",
			refused, asked, server, fetched, j.nzb.TotalSegments), last)
	}
	// More was fetched than refused: the post is on the primaries, with
	// holes. The judgement is forgotten (holdingPrimaries takes any of them
	// again), and the failures stand for the chase and the health floor.
	j.mu.Lock()
	j.lacking, j.askedOn, j.refusedOn = nil, nil, nil
	j.mu.Unlock()
	return nil
}
