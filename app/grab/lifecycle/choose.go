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

package lifecycle

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
)

// ChooseClient picks the DownloadClient for a grab (§5.1): today's
// pickClient -- enabled, the release's protocol, the lowest priority, then
// the name -- except that the release Indexer's spec.downloadClientRef wins
// when it names an enabled client of the protocol (Sonarr's per-indexer
// client).
func ChooseClient(clients []Client, protocol commonv1.Protocol, indexerClientRef string) (Client, bool) {
	if indexerClientRef != "" {
		for _, c := range clients {
			if c.Name == indexerClientRef && c.Enabled && c.Protocol == protocol {
				return c, true
			}
		}
	}
	var (
		best Client
		ok   bool
	)
	for _, c := range clients {
		if !c.Enabled || c.Protocol != protocol {
			continue
		}
		if !ok || c.Priority < best.Priority || (c.Priority == best.Priority && c.Name < best.Name) {
			best, ok = c, true
		}
	}
	return best, ok
}

// EngineName is an engine instance's name, "<client>-<ordinal>".
func EngineName(client string, ordinal int32) string {
	return client + "-" + itoa(ordinal)
}

// ChooseOrdinal picks the engine instance of c for a grab (§5.1): among the
// Ready instances with a fresh record, the fewest active + queued, ties
// broken by names.HashOrdinal(replicas, infoHash, guid). A usenet client
// has ordinal 0. false when no instance is Ready.
func ChooseOrdinal(c Client, engines map[string]Engine, infoHash, guid string, now time.Time) (int32, bool) {
	replicas := max(c.Replicas, 1)
	if c.Protocol == commonv1.ProtocolUsenet {
		replicas = 1
	}
	hashed := names.HashOrdinal(replicas, infoHash, guid)
	var (
		best     int32 = -1
		bestLoad int32
	)
	for o := range replicas {
		e, ok := engines[EngineName(c.Name, o)]
		if !ok || !engineReady(e, now) {
			continue
		}
		load := e.Active + e.Queued
		switch {
		case best < 0, load < bestLoad:
			best, bestLoad = o, load
		case load == bestLoad && o == hashed:
			best = o
		}
	}
	return best, best >= 0
}

// engineReady reports an engine Ready on a fresh record.
func engineReady(e Engine, now time.Time) bool {
	return e.Ready && !e.At.IsZero() && now.Sub(e.At) <= EngineRecordFresh
}

// EntryID is a grab entry's stable id in R4's argument order:
// names.ChildName(target, guid), or names.ChildName(target, purpose, guid)
// for a donor, so an adopted entry keeps its Download's name.
func EntryID(target string, purpose commonv1.DownloadPurpose, guid string) string {
	if purpose != "" {
		return names.ChildName(target, string(purpose), guid)
	}
	return names.ChildName(target, guid)
}

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
