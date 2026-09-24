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

package series

import (
	"strconv"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// recentMonitorWindow is Sonarr's own "Recent" episode-monitor-mode window
// (docs/research/quality.md, the Add-time monitoring / EpisodeMonitoredService
// paragraph) -- a real, verified *arr value, not a Clustarr-chosen default.
// It is not to be confused with movie.ReleasedRecentWindow or
// series.EndedRecentWindow, this task's own refresh-TTL bucketing defaults
// for an unrelated purpose that happens to reuse the same 90-day figure.
const recentMonitorWindow = 90 * 24 * time.Hour

// EpisodeCandidate is the minimal shape InitialEpisodeMonitored and its
// firstSeason/lastSeason helpers need to decide one episode's initial
// monitored flag against the rest of the series' known episodes.
type EpisodeCandidate struct {
	SeasonNumber  int32
	EpisodeNumber int32
	AirDate       *time.Time
}

// DesiredEpisode is one Episode object the Series reconciler should ensure
// exists, with the provider-sourced fields to write to its status and the
// monitored decision (if any) to write to its spec.
type DesiredEpisode struct {
	Name           string
	SeasonNumber   int32
	EpisodeNumber  int32
	AbsoluteNumber *int32
	Title          string
	Overview       string
	AirDate        *time.Time
	RuntimeMinutes int32
	// TvdbID is the TheTVDB episode ID, parsed from the provider's
	// ExternalIDs (metadata.KeyTVDB). It is left at its Go zero value (0)
	// when the provider sent no tvdb key, or one that does not parse as a
	// base-10 int64 -- the same "leave it unset rather than fail the whole
	// fan-out" behaviour buildMovieMetadataAC uses for CollectionRef's own
	// TmdbID (app/catalog/metadata/patch.go).
	TvdbID int64

	// Monitored is non-nil on the first fan-out (addOptionsApplied == false,
	// decided by InitialEpisodeMonitored) and for a brand-new episode
	// appearing after add (decided by spec.monitorNewItems == all/none). It
	// is nil for an episode that already exists as an Episode object on a
	// later refresh: EpisodeSpec.Monitored's own doc comment says the Series
	// controller sets it only at creation and per monitorNewItems, and after
	// that it belongs to the user, so nil here means "do not touch
	// spec.monitored".
	Monitored *bool
}

// InitialEpisodeMonitored decides ep's monitored flag at add time, per the
// series' SeriesMonitorMode (docs/research/quality.md's Add-time monitoring
// / EpisodeMonitoredService semantics).
//
// MonitorSpecials/UnmonitorSpecials -- ruling from review: these two modes
// set the monitored flag ONLY for season-0 episodes; every other season's
// episode is left at the schema default (+kubebuilder:default=true on
// EpisodeSpec.Monitored), which is observationally identical to what All
// would produce. Concretely: MonitorSpecials returns true for season 0 and
// true for every other season (unchanged from the default);
// UnmonitorSpecials returns false for season 0 and true for every other
// season (unchanged from the default).
func InitialEpisodeMonitored(
	mode catalogv1alpha1.SeriesMonitorMode,
	ep EpisodeCandidate,
	all []EpisodeCandidate,
	runStatus catalogv1alpha1.SeriesRunStatus,
	now time.Time,
) bool {
	aired := ep.AirDate != nil && !ep.AirDate.After(now)
	switch mode {
	case catalogv1alpha1.SeriesMonitorAll:
		return true
	case catalogv1alpha1.SeriesMonitorFuture:
		return !aired && runStatus != catalogv1alpha1.SeriesRunStatusEnded
	case catalogv1alpha1.SeriesMonitorMissing:
		return aired
	case catalogv1alpha1.SeriesMonitorExisting:
		return false
	case catalogv1alpha1.SeriesMonitorFirstSeason:
		return ep.SeasonNumber == firstSeason(all)
	case catalogv1alpha1.SeriesMonitorLastSeason:
		return ep.SeasonNumber == lastSeason(all)
	case catalogv1alpha1.SeriesMonitorPilot:
		return ep.SeasonNumber == 1 && ep.EpisodeNumber == 1
	case catalogv1alpha1.SeriesMonitorRecent:
		return ep.AirDate != nil && !ep.AirDate.After(now) && now.Sub(*ep.AirDate) <= recentMonitorWindow
	case catalogv1alpha1.SeriesMonitorMonitorSpecials:
		// Season 0 -> true; every other season -> true, unchanged from the
		// default (see the doc comment above).
		return true
	case catalogv1alpha1.SeriesMonitorUnmonitorSpecials:
		if ep.SeasonNumber == 0 {
			return false
		}
		return true
	case catalogv1alpha1.SeriesMonitorNone:
		return false
	case catalogv1alpha1.SeriesMonitorSkip:
		return false
	default:
		return false
	}
}

// firstSeason returns the lowest positive (non-specials) season number
// present in all, or -1 when none exists.
func firstSeason(all []EpisodeCandidate) int32 {
	first := int32(-1)
	for _, c := range all {
		if c.SeasonNumber <= 0 {
			continue
		}
		if first == -1 || c.SeasonNumber < first {
			first = c.SeasonNumber
		}
	}
	return first
}

// lastSeason returns the highest positive (non-specials) season number
// present in all, or -1 when none exists.
func lastSeason(all []EpisodeCandidate) int32 {
	last := int32(-1)
	for _, c := range all {
		if c.SeasonNumber <= 0 {
			continue
		}
		if c.SeasonNumber > last {
			last = c.SeasonNumber
		}
	}
	return last
}

// DesiredEpisodes projects a provider's episode list into the Episode
// objects the reconciler should ensure exist for s. existingNames is the
// set of Episode object names already owned by s (from the reconciler's own
// List call), threaded in explicitly -- see DesiredEpisode.Monitored's doc
// comment for why the distinction between "new" and "already exists"
// matters and cannot be inferred from episodes alone.
//
// Episodes with a duplicate (season, episode) pair are deduplicated,
// first occurrence wins: the provider is assumed not to send duplicates,
// but this defends anyway. EffectiveEpisodeOrder is applied upstream of
// this function (by the caller, when building the metadata request) --
// this function does not need to know which order was requested, since the
// fetched []metadata.Episode already reflects it.
func DesiredEpisodes(
	s *catalogv1alpha1.Series,
	addOptionsApplied bool,
	existingNames map[string]bool,
	episodes []metadata.Episode,
	now time.Time,
) []DesiredEpisode {
	type key struct{ season, episode int32 }
	seen := make(map[key]bool, len(episodes))

	all := make([]EpisodeCandidate, 0, len(episodes))
	for _, ep := range episodes {
		k := key{ep.SeasonNumber, ep.EpisodeNumber}
		if seen[k] {
			continue
		}
		seen[k] = true
		all = append(all, EpisodeCandidate{SeasonNumber: ep.SeasonNumber, EpisodeNumber: ep.EpisodeNumber, AirDate: ep.AirDate})
	}

	var runStatus catalogv1alpha1.SeriesRunStatus
	if s.Status.Metadata != nil {
		runStatus = s.Status.Metadata.Status
	}

	seen = make(map[key]bool, len(episodes))
	out := make([]DesiredEpisode, 0, len(episodes))
	for _, ep := range episodes {
		k := key{ep.SeasonNumber, ep.EpisodeNumber}
		if seen[k] {
			continue
		}
		seen[k] = true

		name := EpisodeName(s.Name, s.Spec.SeriesType, ep.SeasonNumber, ep.EpisodeNumber, ep.AirDate)

		var monitored *bool
		switch {
		case !addOptionsApplied:
			v := InitialEpisodeMonitored(s.Spec.AddOptions.Monitor,
				EpisodeCandidate{SeasonNumber: ep.SeasonNumber, EpisodeNumber: ep.EpisodeNumber, AirDate: ep.AirDate},
				all, runStatus, now)
			monitored = &v
		case !existingNames[name] && s.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewChildrenAll:
			v := true
			monitored = &v
		case !existingNames[name] && s.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewChildrenNone:
			v := false
			monitored = &v
		default:
			monitored = nil
		}

		var tvdbID int64
		if raw, ok := ep.IDs[metadata.KeyTVDB]; ok {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
				tvdbID = id
			}
		}

		out = append(out, DesiredEpisode{
			Name: name, SeasonNumber: ep.SeasonNumber, EpisodeNumber: ep.EpisodeNumber,
			AbsoluteNumber: ep.AbsoluteNumber, Title: ep.Title, Overview: ep.Overview,
			AirDate: ep.AirDate, RuntimeMinutes: ep.Runtime, TvdbID: tvdbID, Monitored: monitored,
		})
	}
	return out
}
