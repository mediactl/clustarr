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
package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// PartSetGUIDPrefix starts a part set's GUID, which no indexer issues.
const PartSetGUIDPrefix = "parts:"

// PartSet is a movie released in parts, each part posted as a release of
// its own (56.Up.2012.Part1/2/3.1080p.BluRay.x264-USURY): its members'
// decisions, part 1 to N in order.
type PartSet struct {
	Members []Decision
	base    string
}

// PartSets finds the part sets among a movie search's decisions: releases
// of one indexer and protocol whose title carries a part token
// (release.PartOf), agreeing on the parsed title -- which holds a token
// that comes before the year, "Che.Part.1.2008" naming a film --, the year, the quality, the release group (case folded)
// and the edition, that number 1 to N with no part missing, 2 <= N <=
// common.MaxReleaseParts. Of a part posted more than once the post with
// the fewest rejections besides its size is taken, then the newest. A
// release for another title is never a member, and the parts of an encode
// the indexer also posts whole (at a film's size) are no set: the one
// download is the better grab.
func PartSets(ds []Decision) []PartSet {
	type member struct {
		d    Decision
		base string
		part int
	}
	groups := map[string][]member{}
	whole := map[string]bool{}
	var order []string
	for _, d := range ds {
		if d.Parsed == nil || BandOf(d.Approved, d.Rejections) == BandOtherTitle {
			continue
		}
		// The parsed title is in the key: a part token before the year is
		// the title's ("Che.Part.1.2008"), so its two films never meet.
		key := strings.Join([]string{
			d.Release.IndexerRef, string(d.Release.Protocol), release.TitleNorm(d.Parsed.Title), strconv.Itoa(d.Parsed.Year),
			d.Parsed.Quality.Name, strings.ToLower(d.Parsed.Group), strings.ToLower(d.Parsed.Edition),
		}, "\x00")
		base, n, ok := release.PartOf(d.Release.Title)
		if !ok {
			// The encode posted whole as well, at a film's size: one
			// download, never its parts.
			if len(memberRejections(d)) == len(d.Rejections) {
				whole[key] = true
			}
			continue
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], member{d: d, base: base, part: n})
	}
	var out []PartSet
	for _, key := range order {
		if whole[key] {
			continue
		}
		best := map[int]member{}
		top := 0
		for _, m := range groups[key] {
			if cur, ok := best[m.part]; !ok || betterPost(m.d, cur.d) {
				best[m.part] = m
			}
			top = max(top, m.part)
		}
		if top < 2 || top > common.MaxReleaseParts || len(best) != top {
			continue
		}
		s := PartSet{base: best[1].base}
		for n := 1; n <= top; n++ {
			s.Members = append(s.Members, best[n].d)
		}
		out = append(out, s)
	}
	return out
}

// betterPost reports a the better post of one part than b: fewer
// rejections besides its size, then newer.
func betterPost(a, b Decision) bool {
	if ra, rb := len(memberRejections(a)), len(memberRejections(b)); ra != rb {
		return ra < rb
	}
	pa, pb := a.Release.PublishedAt, b.Release.PublishedAt
	switch {
	case pa == nil:
		return false
	case pb == nil:
		return true
	}
	return pa.After(pb.Time)
}

// memberRejections are d's rejections a set of which it is a part inherits:
// all but its size's, which the set's own size replaces.
func memberRejections(d Decision) []common.Rejection {
	var out []common.Rejection
	for _, rej := range d.Rejections {
		if ReasonBelowMinimumSize.Of(rej) || ReasonAboveMaximumSize.Of(rej) {
			continue
		}
		out = append(out, rej)
	}
	return out
}

// GUID is the set's GUID: PartSetGUIDPrefix and 24 hex digits of its
// members' GUIDs' digest, the same set the same GUID in every search.
func (s PartSet) GUID() string {
	h := sha256.New()
	for _, m := range s.Members {
		h.Write([]byte(m.Release.GUID))
		h.Write([]byte{0})
	}
	return PartSetGUIDPrefix + hex.EncodeToString(h.Sum(nil))[:24]
}

// Release is the set as one release to judge and grab: titled as its
// members are without the part, the size of all of them, published when
// its oldest part was, and listing each part (ReleaseInfo.Parts).
func (s PartSet) Release() common.ReleaseInfo {
	lead := s.Members[0].Release
	r := common.ReleaseInfo{
		GUID: s.GUID(), IndexerRef: lead.IndexerRef, IndexerName: lead.IndexerName, Title: s.base,
		Protocol: lead.Protocol, Categories: append([]int32(nil), lead.Categories...), IDs: lead.IDs,
	}
	for i, m := range s.Members {
		p := m.Release
		r.SizeBytes += p.SizeBytes
		if p.PublishedAt != nil && (r.PublishedAt == nil || p.PublishedAt.Before(r.PublishedAt)) {
			at := *p.PublishedAt
			r.PublishedAt = &at
		}
		r.Parts = append(r.Parts, common.ReleasePart{
			Part: int32(i + 1), GUID: p.GUID, Title: p.Title, SizeBytes: p.SizeBytes, //nolint:gosec // at most MaxReleaseParts
			DownloadURL: p.DownloadURL, MagnetURL: p.MagnetURL, InfoHash: p.InfoHash, InfoURL: p.InfoURL,
			PublishedAt: p.PublishedAt,
		})
	}
	return r
}

// Judge is d -- the set's Release, evaluated -- with every member's
// rejection besides its size's (memberRejections), each reason once.
func (s PartSet) Judge(d Decision) Decision {
	have := map[string]bool{}
	for _, rej := range d.Rejections {
		have[rejectionCode(rej)] = true
	}
	for _, m := range s.Members {
		for _, rej := range memberRejections(m) {
			if code := rejectionCode(rej); !have[code] {
				have[code] = true
				d.Rejections = append(d.Rejections, rej)
			}
		}
	}
	d.Approved = len(d.Rejections) == 0
	d.TemporarilyRejected = false
	if !d.Approved {
		d.TemporarilyRejected = true
		for _, rej := range d.Rejections {
			if rej.Type != common.RejectionTemporary {
				d.TemporarilyRejected = false
			}
		}
	}
	return d
}

// rejectionCode is a rejection's Reason code, the text before its ": ".
func rejectionCode(rej common.Rejection) string {
	code, _, _ := strings.Cut(rej.Reason, ": ")
	return code
}
