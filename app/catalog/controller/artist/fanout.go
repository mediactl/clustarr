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
// (https://musicbrainz.org/doc/Release_Group/Type: "Compilation",
// "Soundtrack", "Spokenword", "Interview", "Audiobook", "Audio drama",
// "Live", "Remix", "DJ-mix", "Mixtape/Street", "Demo", "Field recording")
// onto MusicMetadataProfile.SecondaryTypes' CRD enum
// (studio;compilation;soundtrack;spokenword;interview;audiobook;live;remix;
// djMix;mixtape;demo;audioDrama;fieldRecording). Most fold by a plain case
// change; four do not ("Audio drama"'s and "Field recording"'s spaces,
// "DJ-mix"'s hyphen+case, "Mixtape/Street"'s slash), so this is an explicit
// table -- the same style catalogarr/metadata/patch.go's mapImageType uses
// for its own provider-to-CRD vocabulary crosswalk -- rather than a
// strings.ToLower call.
//
// "studio" has no MusicBrainz counterpart: MB has no literal "Studio"
// secondary type, and the token is this project's own for "no secondary
// type set" on a plain studio album, handled separately in AlbumAccepted. A
// spelling outside the table (MusicBrainz adding a thirteenth type) reports
// ok=false rather than being mapped onto something else or dropped.
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
	case "Field recording":
		return "fieldRecording", true
	default:
		return "", false
	}
}

// mapReleaseStatus folds one MusicBrainz release status onto
// MusicMetadataProfile.ReleaseStatuses' CRD enum
// (official;promotion;bootleg;pseudoRelease). The web service spells them
// "Official", "Promotion", "Bootleg" and "Pseudo-Release" (verified against
// musicbrainz.org/ws/2 on 2026-09-23; pkg/metadata/clients/musicbrainz
// passes them through unchanged), so the comparison folds case first -- an
// exact-case table matched none of them. "Withdrawn" and "Cancelled" have
// no CRD token and report ok=false rather than a guessed mapping.
func mapReleaseStatus(mb string) (string, bool) {
	switch strings.ToLower(mb) {
	case "official":
		return "official", true
	case "promotion":
		return "promotion", true
	case "bootleg":
		return "bootleg", true
	case "pseudo-release":
		return "pseudoRelease", true
	default:
		return "", false
	}
}

// ReleaseStatusAccepted reports whether a MusicBrainz release status
// (spelled as the web service spells it) folds onto a token in
// profile.ReleaseStatuses. It is the one release-status rule shared by
// AlbumAccepted here and the Album controller's release selection
// (catalogarr/controller/album), so the two can never disagree about which
// statuses a profile admits.
func ReleaseStatusAccepted(profile catalogv1alpha1.MusicMetadataProfile, status string) bool {
	token, ok := mapReleaseStatus(status)
	return ok && containsFold(profile.ReleaseStatuses, token)
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
// SecondaryTypes: Lidarr's rule, verbatim (SkyHookProxy.FilterAlbums,
// src/NzbDrone.Core/MetadataSource/SkyHook/SkyHookProxy.cs lines 144-146 at
// Lidarr/Lidarr da7b4dfb1a9e7e1d6625c2dbc3fff96971ab26bd):
//
//	((!album.SecondaryTypes.Any() && secondaryTypes.Contains("Studio")) ||
//	 album.SecondaryTypes.Any(x => secondaryTypes.Contains(x)))
//
// An album with no secondary types is accepted when the profile allows
// "studio" (this project's token for Lidarr's "Studio", the profile's
// +kubebuilder:default={studio}); otherwise it is accepted when ANY one of
// its secondary types folds (mapSecondaryType) onto an allowed token, so a
// "Live"+"Compilation" release group passes a profile allowing only
// "compilation". A secondary type mapSecondaryType cannot fold counts as
// not allowed, as a name outside Lidarr's allowed set does there.
//
// ReleaseStatuses: alb must have at least one release whose status folds
// (ReleaseStatusAccepted) onto a token in profile.ReleaseStatuses -- Lidarr's
// album-level rule (SkyHookProxy.FilterAlbums,
// src/NzbDrone.Core/MetadataSource/SkyHook/SkyHookProxy.cs at Lidarr
// da7b4dfb: `album.ReleaseStatuses.Any(x => releaseStatuses.Contains(x))`).
// This fan-out's own browse, ArtistProvider.Albums(mbArtistID), deliberately
// does not browse releases (one extra request per release group), so here
// alb.Releases is empty and this dimension passes rather than blocking every
// album on data the call does not carry. The Album controller applies the
// same rule once it has fetched its release group's releases: it selects
// only among releases of an accepted status, and reports an album none of
// whose releases qualifies (catalogarr/controller/album's SelectRelease).
func AlbumAccepted(profile catalogv1alpha1.MusicMetadataProfile, alb pkgmetadata.Album) bool {
	if !containsFold(profile.PrimaryTypes, mapPrimaryType(alb.PrimaryType)) {
		return false
	}

	if !secondaryTypesAccepted(profile, alb.SecondaryTypes) {
		return false
	}

	if len(alb.Releases) > 0 {
		accepted := false
		for _, rel := range alb.Releases {
			if ReleaseStatusAccepted(profile, rel.Status) {
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

// secondaryTypesAccepted is AlbumAccepted's secondary-type dimension; see
// its doc comment for the Lidarr lines it follows.
func secondaryTypesAccepted(profile catalogv1alpha1.MusicMetadataProfile, secondaryTypes []string) bool {
	if len(secondaryTypes) == 0 {
		return containsFold(profile.SecondaryTypes, "studio")
	}
	for _, mb := range secondaryTypes {
		if token, ok := mapSecondaryType(mb); ok && containsFold(profile.SecondaryTypes, token) {
			return true
		}
	}
	return false
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
