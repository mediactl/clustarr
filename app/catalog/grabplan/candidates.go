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

package grabplan

import (
	"context"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/uuid"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabsource"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Caps of an entry's release (ADR-0019 §6.3): a title is clamped on a rune
// boundary, a GUID over maxGUID refuses the candidate, never clamped.
const (
	maxTitle   = 512
	maxGUID    = 1024
	maxRef     = 253
	maxText    = 256
	maxLangs   = 16
	maxLang    = 35
	maxCovered = 200
)

// ctxBackground carries pkg/decision's log lines out of a pure planner.
var ctxBackground = context.Background()

// newUID is the uid the manager issues a grab when it admits it.
var newUID = func() string { return string(uuid.NewUUID()) }

// FromRecord turns an item's search answer into candidates (§6.6 step 1):
// each approved release of the record, GrabbedBy search (or redownload
// when the stage says the search was one, R25), covering target -- the item
// the record answers, empty for a single-item owner. The record is grab
// input while it answers the item's current searchDispatch.seq and is
// younger than CandidateWindow (R7); the caller checks the seq.
func FromRecord(rec *schema.SearchRecord, target *schema.ItemRef, grabbedBy commonv1.GrabSource) []Candidate {
	if rec == nil {
		return nil
	}
	purpose := commonv1.DownloadPurpose("")
	if rec.Purpose == schema.SearchPurposeAudioDonor {
		purpose = commonv1.DownloadPurposeAudioDonor
	}
	if grabbedBy == "" {
		grabbedBy = commonv1.GrabSourceSearch
	}
	var out []Candidate
	for _, sr := range rec.Candidates {
		if !sr.Decision.Approved {
			continue
		}
		c := Candidate{
			Release: sr, GrabbedBy: grabbedBy, Purpose: purpose, At: rec.FinishedAt, pending: -1,
		}
		c.Source, _ = grabsource.ResolveSource(sr.Decision.ReleaseInfo)
		if target != nil {
			c.Targets = []schema.ItemRef{*target}
		}
		out = append(out, c)
	}
	return out
}

// FromParked turns an intake candidate into one: an RSS match, or a
// Search's pick (Manual) or grabBest, which grab at once.
func FromParked(p intake.Parked) Candidate {
	c := p.Candidate
	out := Candidate{
		MsgID: p.MsgID, Release: c.Release, GrabbedBy: c.GrabbedBy, Purpose: c.Purpose,
		Manual: c.Manual, Override: c.Override, Targets: c.Targets, At: c.At, pending: -1,
		NoDelay: c.SearchRef != nil,
	}
	if c.Season != nil {
		// A season search's pick (Wave S): its coverage resolves it.
		n := *c.Season
		out.Season = &n
	}
	if out.At.IsZero() {
		out.At = p.At
	}
	out.Source, _ = grabsource.ResolveSource(c.Release.Decision.ReleaseInfo)
	return out
}

// fromPending is a delayed grab come due, made from status alone: its
// stored candidate, covering the items it names.
func fromPending(pg catalogv1alpha1.PendingGrab, covered []Covered, i int) (Candidate, bool) {
	gc := pg.Candidate
	if gc == nil {
		return Candidate{}, false
	}
	info := ReleaseInfo(gc.Release)
	c := Candidate{
		Release: schema.ScoredRelease{
			Decision: commonv1.ReleaseDecision{ReleaseInfo: info, Approved: true},
			Score:    gc.Score,
		},
		Source: *gc.Source.DeepCopy(), GrabbedBy: gc.GrabbedBy, Purpose: gc.Purpose, Manual: gc.Manual,
		NoDelay: true, At: pg.GrabAt.UTC(), pending: i,
	}
	for _, it := range covered {
		for _, n := range pg.Episodes {
			if strings.EqualFold(it.Ref.Kind, "Episode") && it.Season == n.Season && it.Number == n.Number {
				c.Targets = append(c.Targets, it.Ref)
			}
		}
		for _, iss := range pg.Issues {
			if it.Issue != "" && it.Issue == iss {
				c.Targets = append(c.Targets, it.Ref)
			}
		}
	}
	return c, true
}

// DownloadRelease is the capped subset of rel an entry keeps (§6.3): the
// title clamped on a rune boundary, every other string clamped to its
// cap, the info hash normalised (or dropped when it is not one), at most
// 16 languages.
func DownloadRelease(rel commonv1.ReleaseInfo, score int32) catalogv1alpha1.DownloadRelease {
	if score == 0 {
		score = rel.FormatScore
	}
	out := catalogv1alpha1.DownloadRelease{
		Title:        clamp(rel.Title, maxTitle),
		GUID:         rel.GUID,
		IndexerRef:   clamp(rel.IndexerRef, maxRef),
		IndexerName:  clamp(rel.IndexerName, maxRef),
		InfoHash:     normalizeInfoHash(rel.InfoHash),
		Protocol:     rel.Protocol,
		SizeBytes:    rel.SizeBytes,
		Quality:      rel.Quality,
		Revision:     rel.Revision,
		FormatScore:  score,
		ReleaseGroup: clamp(rel.ReleaseGroup, maxText),
	}
	out.Edition = clamp(rel.Edition, maxText)
	for _, l := range rel.Languages {
		if len(out.Languages) == maxLangs {
			break
		}
		out.Languages = append(out.Languages, clamp(l, maxLang))
	}
	return out
}

// ReleaseInfo is r as the ReleaseInfo pkg/decision reads.
func ReleaseInfo(r catalogv1alpha1.DownloadRelease) commonv1.ReleaseInfo {
	info := commonv1.ReleaseInfo{
		Title: r.Title, GUID: r.GUID, IndexerRef: r.IndexerRef, IndexerName: r.IndexerName,
		InfoHash: r.InfoHash, Protocol: r.Protocol, SizeBytes: r.SizeBytes,
		Quality: r.Quality, Revision: r.Revision, FormatScore: r.FormatScore,
		Languages: append([]string(nil), r.Languages...), ReleaseGroup: r.ReleaseGroup, Edition: r.Edition,
	}
	return info
}

// normalizeInfoHash is h in the entry's pattern (lower-case hex, 40 or 64),
// "" when it is not a hex info hash.
func normalizeInfoHash(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) != 40 && len(h) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(h); err != nil {
		return ""
	}
	return h
}

// sourceEmpty reports a source naming nothing to fetch.
func sourceEmpty(s commonv1.DownloadSource) bool {
	return s.MagnetURL == nil && s.TorrentURL == nil && s.NZBURL == nil && s.IndexerDownload == nil
}

// clamp cuts s to at most n bytes on a rune boundary.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
