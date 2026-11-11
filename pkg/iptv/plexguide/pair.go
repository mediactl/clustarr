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

package plexguide

import (
	"slices"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// MissingGrace is how long a paired station may be absent from the lineup's
// fetches before its channel is paired again: Plex lists at most 1,000
// stations and which are left out can change between fetches, and a
// channel leaving Plex loses its recording rules.
const MissingGrace = 7 * 24 * time.Hour

// maxCandidates bounds an Ambiguous pairing's candidates, as the
// IPTVChannel status does.
const maxCandidates = 5

// Match is how a channel was paired, or why it is off Plex; the same
// strings as the API's PlexMatch.
type Match string

const (
	Manual      Match = "Manual"
	Automatic   Match = "Automatic"
	Ambiguous   Match = "Ambiguous"
	Taken       Match = "Taken"
	Timeshifted Match = "Timeshifted"
	NotInLineup Match = "NotInLineup"
	Off         Match = "Off"
	NoLineup    Match = "NoLineup"
)

// OnPlex is whether a channel so paired reaches Plex.
func (m Match) OnPlex() bool { return m == Manual || m == Automatic }

// Prev is a channel's last pairing on Plex, as its status records it: how
// it was paired, the station it was on (whole, so a station absent from a
// fetch is still served) and since when that station has been absent.
type Prev struct {
	Match        Match
	Station      Station
	MissingSince time.Time
}

// Channel is one active channel of the lineup to pair.
type Channel struct {
	// Object is the IPTVChannel's name, the key of Pair's answer.
	Object, Number, Name string
	// ChannelID is the channel's XMLTV guide id, which may name its
	// station's call sign ("ABCEast_WABC.us").
	ChannelID string
	// Station is spec.epg.plex.station, a gridKey chosen by hand; Off is
	// spec.epg.plex.off.
	Station     string
	Off         bool
	Timeshifted bool
	Prev        Prev
}

// Pairing is one channel's answer.
type Pairing struct {
	Match Match
	// Station is the station paired, or for Taken the one another channel
	// holds.
	Station Station
	// TakenBy is the Object holding the station, for Taken.
	TakenBy string
	// Candidates are an Ambiguous pairing's stations, by number, at most 5.
	Candidates []Station
	// MissingSince is set while the paired station is absent.
	MissingSince time.Time
	// Lineup is the lineup the pairing was judged against, the first of a
	// provider's lineups to pair the channel (the custom-guides design
	// §5.3); Pair leaves it to its caller, which knows the order. Empty
	// before any lineup is held.
	Lineup string
}

// group is one station under its base call sign (WEAR and WEARDT), served
// as its HD channel, else its lowest number.
type group struct {
	best Station
	// broadcast is a local station (K or W and three letters): one station
	// under its call sign, never one network's feed with another station
	// its network titles alike (WEAR and WABC, both "ABC" in PMS's lineup).
	broadcast bool
}

type index struct {
	byGridKey  map[string]Station
	byCallSign map[string]*group
	byName     map[string][]*group
}

func newIndex(stations []Station) index {
	ix := index{byGridKey: map[string]Station{}, byCallSign: map[string]*group{}, byName: map[string][]*group{}}
	for _, s := range stations {
		ix.byGridKey[s.GridKey] = s
		base := baseCallSign(s.CallSign)
		g := ix.byCallSign[base]
		if g == nil {
			// A local call sign is K or W and three letters, never an HD
			// feed's (WEHD is WE tv's), or a station the lineup names a
			// network affiliate of.
			local := broadcastCallSign.MatchString(base) && !strings.HasSuffix(strings.ToUpper(s.CallSign), "HD")
			g = &group{best: s, broadcast: local || s.AffiliateCallSign != ""}
			ix.byCallSign[base] = g
		} else if better(s, g.best) {
			g.best = s
		}
	}
	for base, g := range ix.byCallSign {
		for _, k := range []string{Normalize(base), Normalize(g.best.Title), Normalize(g.best.ShortTitle)} {
			if k != "" && !slices.Contains(ix.byName[k], g) {
				ix.byName[k] = append(ix.byName[k], g)
			}
		}
	}
	// The names of every station of a group, not only its best's.
	for _, s := range stations {
		g := ix.byCallSign[baseCallSign(s.CallSign)]
		for _, k := range []string{Normalize(s.Title), Normalize(s.ShortTitle)} {
			if k != "" && !slices.Contains(ix.byName[k], g) {
				ix.byName[k] = append(ix.byName[k], g)
			}
		}
	}
	return ix
}

// better is whether a serves its station over b: HD first, then the lower
// number, then the id, so the choice never depends on the list's order.
func better(a, b Station) bool {
	if a.HD != b.HD {
		return a.HD
	}
	if c := xmltv.CompareNumbers(a.VCN, b.VCN); c != 0 {
		return c < 0
	}
	return a.ID < b.ID
}

// Pair pairs each channel with a station of l (the design's §5.3), keyed by
// Object. Channels are taken by number, so the answer never depends on
// their order.
func Pair(l Lineup, chans []Channel, now time.Time) map[string]Pairing {
	out := make(map[string]Pairing, len(chans))
	sorted := slices.Clone(chans)
	slices.SortStableFunc(sorted, func(a, b Channel) int {
		if c := xmltv.CompareNumbers(a.Number, b.Number); c != 0 {
			return c
		}
		return strings.Compare(a.Object, b.Object)
	})
	if len(l.Stations) == 0 {
		for _, c := range sorted {
			out[c.Object] = Pairing{Match: NoLineup}
		}
		return out
	}
	ix := newIndex(l.Stations)
	holder := map[string]Channel{} // gridKey -> the channel holding it
	for _, c := range sorted {
		p := ix.pair(c, now)
		out[c.Object] = p
		if !p.Match.OnPlex() {
			continue
		}
		key := p.Station.GridKey
		h, held := holder[key]
		switch {
		case !held:
			holder[key] = c
		case p.Match == Manual && out[h.Object].Match == Automatic:
			// A pairing chosen by hand takes the station; the lower number
			// keeps it otherwise (sorted lists it first).
			out[h.Object] = Pairing{Match: Taken, Station: out[h.Object].Station, TakenBy: c.Object}
			holder[key] = c
		default:
			out[c.Object] = Pairing{Match: Taken, Station: p.Station, TakenBy: h.Object}
		}
	}
	// A channel taken from the holder it lost to is Taken by whoever holds
	// the station now.
	for obj, p := range out {
		if p.Match == Taken {
			p.TakenBy = holder[p.Station.GridKey].Object
			out[obj] = p
		}
	}
	return out
}

// pair is one channel's pairing before the one-channel-per-station rule.
func (ix index) pair(c Channel, now time.Time) Pairing {
	switch {
	case c.Off:
		return Pairing{Match: Off}
	case c.Timeshifted:
		return Pairing{Match: Timeshifted}
	}
	if c.Station != "" {
		if s, ok := ix.byGridKey[c.Station]; ok {
			return Pairing{Match: Manual, Station: s}
		}
		if p, ok := kept(c.Prev, c.Station, now); ok {
			p.Match = Manual
			return p
		}
		return Pairing{Match: NotInLineup}
	}
	// Only an automatic pairing sticks: a hand pick set back to automatic is
	// paired by the rules again.
	if prev := c.Prev.Station.GridKey; prev != "" && c.Prev.Match == Automatic {
		if s, ok := ix.byGridKey[prev]; ok {
			return Pairing{Match: Automatic, Station: s}
		}
		if p, ok := kept(c.Prev, prev, now); ok {
			p.Match = Automatic
			return p
		}
	}
	var ambiguous []*group
	for _, found := range ix.rules(c) {
		found = oneNetwork(found)
		switch {
		case len(found) == 1:
			return Pairing{Match: Automatic, Station: ix.feed(found[0]).best}
		case len(found) > 1 && ambiguous == nil:
			ambiguous = found
		}
	}
	if ambiguous != nil {
		cands := make([]Station, 0, len(ambiguous))
		for _, g := range ambiguous {
			cands = append(cands, g.best)
		}
		slices.SortFunc(cands, func(a, b Station) int { return xmltv.CompareNumbers(a.VCN, b.VCN) })
		return Pairing{Match: Ambiguous, Candidates: cands[:min(len(cands), maxCandidates)]}
	}
	return Pairing{Match: NotInLineup}
}

// oneNetwork is found as one group when every group in it is one network
// under its feeds' own call signs (FX and FXHD, STARZ and STZHD, titled
// alike once Normalize cuts the HD or SD): its best feed serves it.
func oneNetwork(found []*group) []*group {
	if len(found) < 2 {
		return found
	}
	title := Normalize(found[0].best.Title)
	best := found[0]
	for _, g := range found {
		if title == "" || g.broadcast || Normalize(g.best.Title) != title {
			return found
		}
		if better(g.best, best.best) {
			best = g
		}
	}
	return []*group{best}
}

// feed is g's network's best feed: a group titled as g is once Normalize
// cuts the HD or SD (OXYGEN's "Oxygen True Crime" and OXYGNHD's "Oxygen
// True Crime HD") serves it when it is better.
func (ix index) feed(g *group) *group {
	title := Normalize(g.best.Title)
	if title == "" || g.broadcast {
		return g
	}
	best := g
	for _, o := range ix.byName[title] {
		if !o.broadcast && Normalize(o.best.Title) == title && better(o.best, best.best) {
			best = o
		}
	}
	return best
}

// kept is a pairing whose station (gridKey) is absent from the lineup,
// standing while it has been absent for less than MissingGrace.
func kept(prev Prev, gridKey string, now time.Time) (Pairing, bool) {
	if prev.Station.GridKey != gridKey || prev.Station.ID == "" {
		return Pairing{}, false
	}
	since := prev.MissingSince
	if since.IsZero() {
		since = now
	}
	if now.Sub(since) >= MissingGrace {
		return Pairing{}, false
	}
	return Pairing{Station: prev.Station, MissingSince: since}, true
}

// rules are the automatic rules' answers in order of evidence: the call
// signs the guide id names, then the name and the guide id as Normalize
// reads them, then the same through the aliases.
func (ix index) rules(c Channel) [][]*group {
	var out [][]*group
	var bySign []*group
	for _, cs := range callSignsIn(c.ChannelID) {
		if g := ix.byCallSign[cs]; g != nil && !slices.Contains(bySign, g) {
			bySign = append(bySign, g)
		}
	}
	out = append(out, bySign)
	names := []string{Normalize(c.Name), Normalize(c.ChannelID)}
	for _, n := range names {
		out = append(out, ix.byName[n])
	}
	for _, n := range names {
		if a, ok := aliases[n]; ok {
			out = append(out, ix.byName[a])
		}
	}
	return out
}
