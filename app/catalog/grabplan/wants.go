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
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// StillWants is lifecycle's convergence input (§6.7): whether the owner
// still wants an unclaimed transfer's release. It wants it while it is
// monitored, the release is not blocked for it (a Blocklisted tombstone),
// no other live grab of that purpose is in flight, and some item it covers
// has no file, or has one the release is still an upgrade of -- an older
// restored status must not read a recent upgrade as unwanted (Review Focus
// 3). The seeding obligation of an imported claim is lifecycle's own,
// checked before this (it needs the transfer record).
func StillWants(v View, claim schema.TransferClaim) (bool, string) {
	if !v.Monitored {
		return false, "the owner is not monitored"
	}
	for i := range v.Entries {
		e := &v.Entries[i]
		if e.Phase == commonv1.DownloadPhaseBlocklisted &&
			((claim.InfoHash != "" && strings.EqualFold(e.Release.InfoHash, claim.InfoHash)) ||
				(claim.GUID != "" && e.Release.GUID == claim.GUID && e.Release.IndexerRef == claim.Indexer)) {
			return false, "the release is blocklisted for the owner"
		}
	}
	for i := range v.Entries {
		e := &v.Entries[i]
		if e.Purpose == claim.Purpose && live(e) && e.UID != claim.Entry.UID && !lifecycle.SameSet(e, claim) {
			return false, "the owner has another grab of it in flight"
		}
	}
	if claim.Purpose != "" {
		return true, ""
	}
	cand, ok := claimQuality(v, claim)
	for _, it := range v.Covered {
		if !it.Monitored {
			continue
		}
		if it.Current == nil {
			return true, ""
		}
		if ok && v.Profile != nil && !it.Current.Transcoded {
			cur := quality.Candidate{Quality: it.Current.Quality, Revision: it.Current.Revision, FormatScore: it.Current.FormatScore}
			if v.Profile.UpgradeDecision(cur, cand) == quality.Upgrade {
				return true, "still an upgrade over the current file"
			}
		}
	}
	return false, "the owner has a file at or above the release's quality"
}

// claimQuality is the quality the claim's release title parses to.
func claimQuality(v View, claim schema.TransferClaim) (quality.Candidate, bool) {
	if claim.Title == "" {
		return quality.Candidate{}, false
	}
	kind := v.Kind
	if container(kind) {
		kind = commonv1.MediaKindEpisode
		if v.Kind == commonv1.MediaKindComic {
			kind = commonv1.MediaKindIssue
		}
	}
	parsed, err := release.Parse(claim.Title, release.Options{Kind: kind})
	if err != nil {
		return quality.Candidate{}, false
	}
	var info commonv1.ReleaseInfo
	parsed.ApplyTo(&info)
	return quality.Candidate{Quality: info.Quality, Revision: info.Revision}, true
}
