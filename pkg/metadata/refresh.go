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

package metadata

import (
	"hash/fnv"
	"strconv"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Refresh states a caller derives from an entity's own fields before
// calling RefreshTTL. This package does not know about CRDs and does not
// compute state itself.
const (
	RefreshStateAnnounced      = "announced"
	RefreshStateInCinemas      = "inCinemas"
	RefreshStateReleasedRecent = "releasedRecent"
	RefreshStateReleasedOld    = "releasedOld"
	RefreshStateContinuing     = "continuing"
	RefreshStateEndedRecent    = "endedRecent"
	RefreshStateEndedOld       = "endedOld"
	RefreshStateActive         = "active"
	RefreshStateOngoing        = "ongoing"
	RefreshStateCompleted      = "completed"
	RefreshStateSearch         = "search"
	RefreshStateCrosswalk      = "crosswalk"
)

// hardRefreshAfter caps how long any cached record may go without a
// refresh, regardless of state, so a record that stops matching any bucket
// (a caller bug, a provider that stopped sending updates) still eventually
// refreshes rather than going stale forever.
const hardRefreshAfter = 180 * 24 * time.Hour

// RefreshRetry is how long an item whose refresh is asked for and not yet
// answered waits before it asks again: past the work stream's one-hour
// duplicate window, inside which a second task under the item's Msg-Id is
// absorbed.
const RefreshRetry = 65 * time.Minute

// RefreshWake is how long from now an item whose document was refreshed at
// lastRefreshed should be looked at again, so that a document is refreshed
// when its ttl runs out and not at the item's next unrelated wake (owner,
// 2026-11-17: a continuing series was refreshed only at a manager start or
// its own next airing, so a new season of one with no airing listed was
// found at the next restart). The wake lands up to a sixteenth of the ttl
// after the document is due, by key, so items refreshed together -- a
// manager start's -- drift apart instead of coming due together for good.
// It is never less than a second.
func RefreshWake(ttl time.Duration, lastRefreshed, now time.Time, key string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	spread := time.Duration(0)
	if span := ttl / 16; span > 0 {
		spread = time.Duration(h.Sum32()) % span
	}
	return max(lastRefreshed.Add(ttl+spread).Sub(now), time.Second)
}

// RefreshTTL ports Radarr/Sonarr's ShouldRefresh heuristics: callers derive
// state from the entity's own fields (release status, air status,
// publication status) before calling RefreshTTL -- this package does not
// know about CRDs and does not compute state itself.
func RefreshTTL(kind commonv1.MediaKind, state string, lastRefreshed time.Time) time.Duration {
	if !lastRefreshed.IsZero() && time.Since(lastRefreshed) >= hardRefreshAfter {
		return 0
	}
	switch state {
	case RefreshStateSearch:
		return time.Hour
	case RefreshStateCrosswalk:
		return 90 * 24 * time.Hour
	}
	switch kind {
	case commonv1.MediaKindMovie:
		switch state {
		case RefreshStateAnnounced, RefreshStateInCinemas:
			return 12 * time.Hour
		case RefreshStateReleasedRecent:
			return 24 * time.Hour
		default:
			return 7 * 24 * time.Hour
		}
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		switch state {
		case RefreshStateContinuing:
			return 6 * time.Hour
		case RefreshStateEndedRecent:
			return 24 * time.Hour
		default:
			return 30 * 24 * time.Hour
		}
	case commonv1.MediaKindArtist, commonv1.MediaKindAlbum:
		return 7 * 24 * time.Hour
	case commonv1.MediaKindAuthor, commonv1.MediaKindBook, commonv1.MediaKindAudiobook:
		return 30 * 24 * time.Hour
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		if state == RefreshStateCompleted {
			return 30 * 24 * time.Hour
		}
		return 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// SchemaVersion is the version of every kind's metadata document the
// gateway writes (status.metadata.schemaVersion; Movie's and Series' since
// 2026-09-30, the six non-video kinds' since the 2026-11-10 design D8:
// their documents carried none, so they read 0 and refresh once under 8,
// which is what the design wants of them, with no Movie or Series touched).
// Raise it when the gateway learns a field: an item whose document is
// older is refreshed once,
// bypassing the L2 cache (whose key carries it), instead of lacking the new
// field until its RefreshTTL -- weeks for a released film -- the way
// mediainfo.ProbeVersion re-probes files.
//
//	1: the full Plex Metadata Response (2026-09-30): tagline, studios,
//	   countries, certifications, people, season posters, stills.
//	2: Movie and Series learn their Plex id (externalIDs["plex"]) and a
//	   Series its plexSeasons (2026-10-06, Plex-native GUIDs).
//	3: a Movie's collection learns its Plex id (collection.plexID) and its
//	   summary and artwork (the extended document), for the Plex
//	   provider's collections (2026-10-06).
//	4: a Series refresh files its episodes' guest cast and crew in each
//	   episode's extended document (2026-10-07), for the Plex provider's
//	   episode Role, Director and Writer.
//	5: a Series' logo and clear art are TheTVDB's ClearLogo and ClearArt,
//	   in the titles' language or none, no longer a season's poster and
//	   banner, and its seasons gain their own banners and posters
//	   (2026-10-21): every series' Plex clearLogo was a season poster.
//	6: a Series' seasons learn their own names (seasonTitles: TheTVDB's
//	   season name, translated into the titles' language), which the Plex
//	   provider shows instead of "Season N" (2026-10-21).
//	7: a Series learns TheTVDB's Sub-Genre tags (subGenres: Dark Comedy,
//	   Psychological Thriller), which the Plex provider lists after its
//	   genres so they fill Plex's categories, and a Movie and a Series
//	   their TMDB keywords (keywords), which cluster-plex writes into
//	   Plex as Labels (2026-10-27).
//	8: a Series' posters, backgrounds, banners and season images rank by
//	   TheTVDB's score instead of leading with the record's own image
//	   (2026-10-28): Cautious Hero's, a title card TheTVDB never scored,
//	   was its poster in Plex beside three scored key arts.
//	9: a Series' images gain its TMDB tv record's logos, posters and
//	   backdrops after TheTVDB's, and every TheTVDB image carries its
//	   language (2026-11-17): 171 of 750 series had no logo in the titles'
//	   language, so an anime among them no titled poster, and TheTVDB's
//	   posters, listed without their tags, all read as posters without text.
const SchemaVersion int32 = 9

// RefreshPurpose is the events.MsgIDForObject purpose of an item's metadata
// task. The refresh an outdated document asks for has one of its own, per
// SchemaVersion: under the plain "metadata" id the bus would drop it as a
// duplicate of the generation's last refresh while that id is still in the
// stream's duplicate window.
func RefreshPurpose(outdated bool) string {
	if outdated {
		return "metadata-schema" + strconv.Itoa(int(SchemaVersion))
	}
	return "metadata"
}
