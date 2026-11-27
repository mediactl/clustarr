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

package torrent

import (
	"cmp"
	"slices"
	"time"

	anatorrent "github.com/anacrolix/torrent"
)

// seeder is one completed torrent still on the swarm: how long it has
// seeded in all, restarts included, and when this process saw it complete.
type seeder struct {
	id          string
	seeded      time.Duration
	completedAt time.Time
}

// overSeedCap is which of the seeding torrents give up their place under a
// cap of limit: all but the limit that have seeded least, the later
// completed first among equals. So the place a newly completed torrent
// takes comes from the one that has had the most of its turn, and the
// choice is the same after a restart, which keeps every torrent's seed time
// ([download.AddRequest.SeedHistory]) and nothing else about the order
// they finished in. A limit of 0 or less is no cap.
func overSeedCap(seeding []seeder, limit int) []string {
	if limit <= 0 || len(seeding) <= limit {
		return nil
	}
	s := slices.Clone(seeding)
	slices.SortFunc(s, func(a, b seeder) int {
		return cmp.Or(
			cmp.Compare(a.seeded, b.seeded),
			b.completedAt.Compare(a.completedAt),
			cmp.Compare(a.id, b.id),
		)
	})
	out := make([]string, 0, len(s)-limit)
	for _, x := range s[limit:] {
		out = append(out, x.id)
	}
	return out
}

// capSeeding holds the client to [Config.MaxSeeding] seeding torrents
// (DownloadClient.spec.torrent.maxSeeding; owner, 2026-11-27: "We should
// only allow a max of 3 seeding torrents at a time"). A torrent over the
// cap has its seed goal counted as met, exactly as one that reached its
// ratio or its time: it stops uploading, the goal stays met across a
// restart (the engine persists it), and the caller's rules for a met goal
// do the rest -- one already imported can be removed with its data, one not
// yet imported keeps its files where the importer reads them and is removed
// after it. Nothing is deleted or moved here.
//
// A torrent is seeding once a poll has seen it complete, until its goal is
// met; one still in scratch counts, since it uploads from there while it is
// published. A paused or failed one holds no place and loses nothing. With
// no cap this does nothing.
func (c *Client) capSeeding(ts []*anatorrent.Torrent, now time.Time) {
	if c.cfg.MaxSeeding <= 0 {
		return
	}
	byID := make(map[string]*anatorrent.Torrent, len(ts))
	var seeding []seeder
	for _, t := range ts {
		id := idFromInfoHash(t.InfoHash())
		sess := c.sessionFor(id)
		sess.mu.Lock()
		if !sess.completedAt.IsZero() && !sess.seedGoalMet && !sess.failed && !sess.removed && !sess.paused {
			seeding = append(seeding, seeder{id: id, seeded: sess.seedTimeLocked(now), completedAt: sess.completedAt})
			byID[id] = t
		}
		sess.mu.Unlock()
	}
	for _, id := range overSeedCap(seeding, c.cfg.MaxSeeding) {
		sess := c.sessionFor(id)
		sess.mu.Lock()
		stopped := !sess.seedGoalMet
		if stopped {
			sess.seedGoalMet = true
			sess.seedGoalMetAt = now
			byID[id].DisallowDataUpload()
		}
		seeded := sess.seedTimeLocked(now)
		sess.mu.Unlock()
		if stopped && c.cfg.Logger != nil {
			c.cfg.Logger.Info("torrent: seeding stopped, more torrents are seeding than the client allows at once",
				"id", id, "name", byID[id].Name(), "maxSeeding", c.cfg.MaxSeeding, "seeded", seeded.Round(time.Second).String())
		}
	}
}
