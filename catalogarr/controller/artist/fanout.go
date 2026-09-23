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

package artist

import (
	"strings"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// mapPrimaryType folds MusicBrainz's release-group primary type
// (docs/research/metadata.md:253, "Album, Single, EP, Broadcast, Other")
// onto MusicMetadataProfile.PrimaryTypes' CRD enum
// (album;ep;single;broadcast;other). Every MB primary type lower-cases
// directly onto its CRD token, so this needs no explicit table the way
// mapSecondaryType and mapReleaseStatus below do.
func mapPrimaryType(mb string) string {
	return strings.ToLower(mb)
}

// mapSecondaryType folds one MusicBrainz release-group secondary type
// (docs/research/metadata.md:253-254, "Compilation, Soundtrack, Spokenword,
// Interview, Audiobook, Audio drama, Live, Remix, DJ-mix, Mixtape/Street,
// Demo, Field recording") onto MusicMetadataProfile.SecondaryTypes' CRD enum
// (studio;compilation;soundtrack;spokenword;interview;audiobook;live;remix;
// djMix;mixtape;demo;audioDrama). Most fold by a plain case change; three do
// not ("Audio drama"'s space, "DJ-mix"'s hyphen+case, "Mixtape/Street"'s
// slash), so this is an explicit table -- the same style
// catalogarr/metadata/patch.go's mapImageType uses for its own
// provider-to-CRD vocabulary crosswalk -- rather than a strings.ToLower call.
//
// MusicBrainz's "Field recording" has no CRD enum member at all: the CRD's
// own default {studio} names a token with no MusicBrainz equivalent (MB has
// no literal "Studio" secondary type; "studio" is this project's own
// placeholder for "no secondary type set" on a plain studio album, handled
// separately in AlbumAccepted), not the twelfth real MB value substituting
// for it. A release group tagged only "Field recording" therefore has no
// token it could ever match; ok=false reports that rather than silently
// mapping it onto something else or dropping it. Flagged in this task's
// report as a CRD enum gap -- api/catalog/v1alpha1 is out of this task's
// directories, so it is reported, not fixed here.
func mapSecondaryType(mb string) (string, bool) {
	switch mb {
	case "Compilation":
		return "compilation", true
	case "Soundtrack":
		return "soundtrack", true
	case "Spokenword":
		return "spokenword", true
	case "Interview":
		return "interview", true
	case "Audiobook":
		return "audiobook", true
	case "Live":
		return "live", true
	case "Remix":
		return "remix", true
	case "Demo":
		return "demo", true
	case "DJ-mix":
		return "djMix", true
	case "Mixtape/Street":
		return "mixtape", true
	case "Audio drama":
		return "audioDrama", true
	default:
		return "", false
	}
}

// mapReleaseStatus folds one MusicBrainz release status
// (docs/research/metadata.md:255, "official, promotion, bootleg,
// pseudo-release") onto MusicMetadataProfile.ReleaseStatuses' CRD enum
// (official;promotion;bootleg;pseudoRelease). Three already match verbatim;
// only pseudo-release's hyphen needs folding. "withdrawn"/"cancelled" are
// the two statuses docs/research/metadata.md:255 marks unverified and the
// CRD enum deliberately omits, so they report ok=false rather than a
// guessed mapping.
func mapReleaseStatus(mb string) (string, bool) {
	switch mb {
	case "official", "promotion", "bootleg":
		return mb, true
	case "pseudo-release":
		return "pseudoRelease", true
	default:
		return "", false
	}
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// AlbumAccepted decides whether release group alb passes artist's
// MetadataProfile -- the filter that decides WHICH release groups become
// Album objects at all, applied by DesiredAlbums below before any monitored
// decision is made.
//
// PrimaryTypes: alb.PrimaryType must fold (mapPrimaryType) onto a token in
// profile.PrimaryTypes. An album with no primary type at all (should not
// happen for a real release group, but pkg/metadata.Album.PrimaryType is a
// plain string with no non-empty guarantee) is rejected rather than guessed
// into "album".
//
// SecondaryTypes: EVERY one of alb's secondary types must fold
// (mapSecondaryType) onto a token in profile.SecondaryTypes -- Lidarr's own
// MetadataProfileService rejects a release group carrying ANY secondary
// type the profile disallows, not just one carrying none of the allowed
// types, so a "Live"+"Compilation" release group with only "compilation"
// allowed is still rejected. An empty SecondaryTypes list (mapAlbum today:
// a plain MusicBrainz studio album) is folded onto ["studio"], the
// profile's own default acceptance token for that case
// (MusicMetadataProfile.SecondaryTypes' +kubebuilder:default={studio}). A
// secondary type mapSecondaryType cannot fold (see its own doc comment)
// rejects the album outright, the same as failing the allow-list.
//
// ReleaseStatuses: alb must have at least one release whose status folds
// (mapReleaseStatus) onto a token in profile.ReleaseStatuses. alb.Releases
// is populated by ArtistProvider.Album's own release-group lookup (used for
// the gateway's status.metadata), not by ArtistProvider.Albums(mbArtistID)
// (this fan-out's own browse call) -- pkg/metadata/clients/musicbrainz's
// mapAlbum does not currently populate Releases from EITHER call, a Phase B
// client gap flagged in this task's report, not this task's to fix. So in
// practice today alb.Releases is always empty, and an empty Releases list
// passes this dimension rather than blocking every album on data this task
// cannot see.
func AlbumAccepted(profile catalogv1alpha1.MusicMetadataProfile, alb pkgmetadata.Album) bool {
	if !containsFold(profile.PrimaryTypes, mapPrimaryType(alb.PrimaryType)) {
		return false
	}

	secondaryTokens := make([]string, 0, len(alb.SecondaryTypes))
	if len(alb.SecondaryTypes) == 0 {
		secondaryTokens = append(secondaryTokens, "studio")
	} else {
		for _, mb := range alb.SecondaryTypes {
			token, ok := mapSecondaryType(mb)
			if !ok {
				return false
			}
			secondaryTokens = append(secondaryTokens, token)
		}
	}
	for _, token := range secondaryTokens {
		if !containsFold(profile.SecondaryTypes, token) {
			return false
		}
	}

	if len(alb.Releases) > 0 {
		accepted := false
		for _, rel := range alb.Releases {
			token, ok := mapReleaseStatus(rel.Status)
			if ok && containsFold(profile.ReleaseStatuses, token) {
				accepted = true
				break
			}
		}
		if !accepted {
			return false
		}
	}

	return true
}

// AlbumCandidate is the minimal shape InitialAlbumMonitored and its
// firstRelease/latestRelease helpers need to decide one album's initial
// monitored flag against the rest of the artist's known (accepted) albums.
type AlbumCandidate struct {
	ReleaseGroupID string
	ReleaseDate    *time.Time
}

// InitialAlbumMonitored decides alb's monitored flag at add time, per the
// artist's ArtistMonitorMode (api/catalog/v1alpha1/artist_types.go: All,
// Future, Missing, Existing, Latest, First, None -- verified against
// Lidarr's own NewItemMonitorTypes enum, docs/research/quality.md:481).
// Same shape as series.InitialEpisodeMonitored, with ReleaseDate standing in
// for AirDate and "earliest/latest released album" standing in for
// "first/last season".
func InitialAlbumMonitored(
	mode catalogv1alpha1.ArtistMonitorMode,
	alb AlbumCandidate,
	all []AlbumCandidate,
	runStatus catalogv1alpha1.ArtistRunStatus,
	now time.Time,
) bool {
	released := alb.ReleaseDate != nil && !alb.ReleaseDate.After(now)
	switch mode {
	case catalogv1alpha1.ArtistMonitorAll:
		return true
	case catalogv1alpha1.ArtistMonitorFuture:
		return !released && runStatus != catalogv1alpha1.ArtistRunStatusEnded
	case catalogv1alpha1.ArtistMonitorMissing:
		return released
	case catalogv1alpha1.ArtistMonitorExisting:
		return false
	case catalogv1alpha1.ArtistMonitorLatest:
		return alb.ReleaseGroupID != "" && alb.ReleaseGroupID == latestRelease(all)
	case catalogv1alpha1.ArtistMonitorFirst:
		return alb.ReleaseGroupID != "" && alb.ReleaseGroupID == firstRelease(all)
	case catalogv1alpha1.ArtistMonitorNone:
		return false
	default:
		return false
	}
}

// firstRelease returns the release-group id of the earliest-dated album in
// all, or "" when none has a release date. Ties are broken by encounter
// order (all's own order), matching series.firstSeason/lastSeason's own
// tie-break.
func firstRelease(all []AlbumCandidate) string {
	var id string
	var earliest *time.Time
	for _, c := range all {
		if c.ReleaseDate == nil {
			continue
		}
		if earliest == nil || c.ReleaseDate.Before(*earliest) {
			t := *c.ReleaseDate
			earliest = &t
			id = c.ReleaseGroupID
		}
	}
	return id
}

// latestRelease is firstRelease's counterpart: the release-group id of the
// most-recently-dated album in all.
func latestRelease(all []AlbumCandidate) string {
	var id string
	var latest *time.Time
	for _, c := range all {
		if c.ReleaseDate == nil {
			continue
		}
		if latest == nil || c.ReleaseDate.After(*latest) {
			t := *c.ReleaseDate
			latest = &t
			id = c.ReleaseGroupID
		}
	}
	return id
}

// DesiredAlbum is one Album object the Artist reconciler should ensure
// exists, with the monitored decision (if any) to write to its spec at
// creation. Unlike series.DesiredEpisode, it carries no provider-sourced
// status fields at all -- see this package's doc.go for why Album's
// status.metadata is never fanned out.
type DesiredAlbum struct {
	Name           string
	ReleaseGroupID string

	// Monitored is non-nil on the first fan-out (addOptionsApplied == false,
	// decided by InitialAlbumMonitored) and for a brand-new album appearing
	// after add (decided by spec.monitorNewItems). It is nil for an album
	// that already exists as an Album object on a later refresh --
	// ArtistSpec.MonitorNewItems' doc comment says the Artist controller
	// sets spec.monitored only at creation and per monitorNewItems, and
	// after that it belongs to the user, mirroring
	// series.DesiredEpisode.Monitored's own contract.
	Monitored *bool
}

// AlbumName renders "<artist>-<releasegroup-uid8>" per design §4.2.
// k8s.HashSuffixLength (the §5 Download/TranscodeJob-general convention) is
// ten hex characters; Album follows the spec's own literal eight-character
// form instead (matching §4.2's other explicit truncation, TranscodeJob's
// "<mediafile>-<profileHash[:8]>"), so this slices k8s.HashSuffix's ten hex
// characters down to eight rather than adding a second hashing primitive to
// pkg/k8s for one caller.
func AlbumName(artistName, releaseGroupID string) string {
	suffix := k8s.HashSuffix(releaseGroupID)[:8]
	prefix := k8s.NormalizeName(artistName)
	if prefix == "" {
		prefix = "x"
	}
	return prefix + "-" + suffix
}

// DesiredAlbums projects a provider's release-group list into the Album
// objects the reconciler should ensure exist for a, after AlbumAccepted's
// MetadataProfile filter. existingReleaseGroups is the set of
// spec.releaseGroupID values already owned by a (from the reconciler's own
// List call), threaded in explicitly for the same reason
// series.DesiredEpisodes takes existingNames: the "new" vs "already exists"
// distinction cannot be inferred from albums alone.
//
// Albums AlbumAccepted rejects contribute nothing here -- not even to the
// Latest/First candidate pool -- since a release group the profile never
// turns into an Album object has no monitored decision to make in the first
// place.
func DesiredAlbums(
	a *catalogv1alpha1.Artist,
	addOptionsApplied bool,
	existingReleaseGroups map[string]bool,
	albums []pkgmetadata.Album,
	now time.Time,
) []DesiredAlbum {
	var runStatus catalogv1alpha1.ArtistRunStatus
	if a.Status.Metadata != nil {
		runStatus = a.Status.Metadata.Status
	}

	type accepted struct {
		id   string
		date *time.Time
	}
	var kept []accepted
	all := make([]AlbumCandidate, 0, len(albums))
	for _, alb := range albums {
		id := alb.IDs[pkgmetadata.KeyMBReleaseGroup]
		if id == "" {
			continue // cannot name or reference an Album without its release-group id
		}
		if !AlbumAccepted(a.Spec.MetadataProfile, alb) {
			continue
		}
		kept = append(kept, accepted{id: id, date: alb.ReleaseDate})
		all = append(all, AlbumCandidate{ReleaseGroupID: id, ReleaseDate: alb.ReleaseDate})
	}

	out := make([]DesiredAlbum, 0, len(kept))
	for _, alb := range kept {
		name := AlbumName(a.Name, alb.id)

		var monitored *bool
		switch {
		case !addOptionsApplied:
			v := InitialAlbumMonitored(a.Spec.AddOptions.Monitor,
				AlbumCandidate{ReleaseGroupID: alb.id, ReleaseDate: alb.date}, all, runStatus, now)
			monitored = &v
		case !existingReleaseGroups[alb.id] && a.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewItemsAll:
			v := true
			monitored = &v
		case !existingReleaseGroups[alb.id] && a.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewItemsNone:
			v := false
			monitored = &v
		case !existingReleaseGroups[alb.id] && a.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewItemsNew:
			// "new": ruling from this task, mirroring series' own documented
			// MonitorSpecials/UnmonitorSpecials ruling. Lidarr's Artist adds
			// a third MonitorNewItems value beyond Sonarr/Readarr's All/None
			// (docs/research/quality.md:777, "Lidarr adds new") but no
			// source in this repo defines it further, and grepping the tree
			// found no controller reading it before this task. Read here as
			// "monitor a newly-discovered album only if it has not been
			// released yet" -- the same released/not-released test
			// InitialAlbumMonitored's own Future case uses -- rather than
			// "monitor every newly-discovered album regardless of age",
			// which is what MonitorNewItemsAll already means and would make
			// New a synonym for All.
			v := alb.date == nil || alb.date.After(now)
			monitored = &v
		default:
			monitored = nil
		}

		out = append(out, DesiredAlbum{Name: name, ReleaseGroupID: alb.id, Monitored: monitored})
	}
	return out
}
