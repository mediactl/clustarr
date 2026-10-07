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

package search

import (
	"context"
	"fmt"
	"time"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// itemSnapshot is what the worker reads off the cluster before it can
// decide anything: the catalog item's search identity, its QualityProfile
// ref, and the decision engine's Target for the release rules -- its
// identity, availability and runtime. Since ADR-0019 (§7.4) it reads no
// current file, queue or blocklist: those are the item-state rules the
// manager runs (pkg/decision.ItemStateRejections).
type itemSnapshot struct {
	IDs               TargetIDs
	QualityProfileRef string
	Target            decision.Target
	// Owner is the grab owner (the item, or an Episode's Series and an
	// Issue's Comic, ADR-0019 §6.1); the request asks the release index for
	// block state under its BlockScopeOf (§6.14) when the task names no
	// scope.
	Owner schema.ItemRef
}

// snapshot fetches the item named by ref in namespace ns: movie, episode,
// album, book, audiobook and issue -- the kinds Searchable admits. A season
// of a series is snapshotSeason's.
func (w *Worker) snapshot(ctx context.Context, ns string, ref commonv1.MediaRef) (itemSnapshot, error) {
	snap := itemSnapshot{
		Target: decision.Target{
			Kind: ref.Kind,
			Key:  events.MediaKey(string(ref.Kind), ns, ref.Name),
		},
	}

	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &m); err != nil {
			return snap, fmt.Errorf("get Movie %s/%s: %w", ns, ref.Name, err)
		}
		snap.QualityProfileRef = m.Spec.QualityProfileRef
		snap.IDs.TmdbID = m.Spec.TmdbID
		snap.Target.Monitored = ptr.Deref(m.Spec.Monitored, true)
		snap.Target.Available = m.Status.Available
		if md := m.Status.Metadata; md != nil {
			snap.IDs.Year = md.Year
			snap.IDs.ImdbID = md.ExternalIDs[commonv1.IDKeyIMDB]
			snap.IDs.OriginalLanguageTag = md.OriginalLanguage
			snap.IDs.Title = md.Title
			snap.Target.RuntimeMinutes = int(md.RuntimeMinutes)
			snap.Target.OriginalLanguageTag = md.OriginalLanguage
		}
		snap.Target.Identity = MovieIdentity(&m)
		snap.Target.Identity.Namesakes = w.namesakes(ctx, ns, &m)
		snap.Owner = ownerRef(string(commonv1.MediaKindMovie), &m)

	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &e); err != nil {
			return snap, fmt.Errorf("get Episode %s/%s: %w", ns, ref.Name, err)
		}
		var s catalogv1alpha1.Series
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: e.Spec.SeriesRef}, &s); err != nil {
			return snap, fmt.Errorf("get Series %s/%s: %w", ns, e.Spec.SeriesRef, err)
		}

		// An Episode carries no QualityProfileRef of its own: episodes are
		// ranked against the owning Series' profile (spec §4.2).
		snap.QualityProfileRef = s.Spec.QualityProfileRef
		// The series' whole TheXEM table: the identity check reads every
		// release number through it, and the request below asks for the
		// target's scene numbering from it.
		scene := SceneMappings(ctx, w.SceneMaps, s.Spec.TvdbID)
		// The SERIES tvdb id, not Episode.status.tvdbID. A Newznab/Torznab
		// tv-search is keyed `tvdbid=<series>&season=&ep=`; the episode's own
		// tvdb id identifies a different entity and no indexer accepts it.
		// (The task brief said status.tvdbID here -- see the task report.)
		snap.IDs.TvdbID = s.Spec.TvdbID
		snap.IDs.Anime = s.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime
		season, episode, absolute := searchNumbering(scene, &e)
		snap.IDs.Season = new(season)
		snap.IDs.Episode = new(episode)
		if snap.IDs.Anime && absolute != nil {
			// Anime indexers key releases by absolute number, so the
			// absolute number -- not the in-season number -- is what goes
			// on the wire. See BuildSearchRequest for the Season/Episode
			// folding this feeds.
			snap.IDs.Episode = absolute
		}
		// Prefer the episode's own runtime; fall back to the series'
		// average when metadata has not filled it in yet. Zero is left as
		// zero so pkg/decision applies its own 45-minute fallback rather
		// than this package inventing a second one.
		runtime := e.Status.RuntimeMinutes
		if runtime == 0 && s.Status.Metadata != nil {
			runtime = s.Status.Metadata.RuntimeMinutes
		}
		// Sonarr's MonitoredEpisodeSpecification: the series as well.
		snap.Target.Monitored = ptr.Deref(e.Spec.Monitored, true) && ptr.Deref(s.Spec.Monitored, true)
		snap.Target.Available = episodeAvailable(&e, w.now())
		snap.Target.RuntimeMinutes = int(runtime)
		// pkg/decision reads an episode's runtime from EpisodeRuntimes, not
		// RuntimeMinutes (see targetRuntimeMinutes). A single-episode search
		// therefore carries exactly one entry; a pack search covering several
		// episodes is the wanted-cron task's job and would carry one entry
		// per key in SearchTask.Keys.
		snap.Target.EpisodeRuntimes = []int{int(runtime)}
		if md := s.Status.Metadata; md != nil {
			snap.IDs.Year = md.Year
			snap.IDs.OriginalLanguageTag = md.OriginalLanguage
			// The SERIES title, not the episode's own: a text fallback
			// query is "<series> SxxEyy" (see resolvedText), and an
			// Episode carries no title of its own to put there.
			snap.IDs.Title = md.Title
			snap.Target.OriginalLanguageTag = md.OriginalLanguage
		}
		snap.Target.Identity = EpisodeIdentity(&s, &e)
		snap.Owner = ownerRef(string(commonv1.MediaKindSeries), &s)
		snap.Target.Identity.SceneMappings = scene
		// This is a search for exactly one episode, so a whole-season pack
		// is not what was asked for (ruling R-3, Sonarr's
		// SingleEpisodeSearchMatchSpecification). It is set here, at the
		// search's own call site, not in EpisodeIdentity: the RSS matcher
		// shares that builder, and an RSS pack target can resolve to a
		// single episode too.
		snap.Target.Identity.SingleEpisodeSearch = true
		snap.Target.Identity.Namesakes = w.namesakes(ctx, ns, &s)
		snap.Target.Anime = snap.IDs.Anime

	case commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindIssue:
		v, err := ReadNonVideo(ctx, w.Client, ns, ref, w.now())
		if err != nil {
			return snap, err
		}
		snap.QualityProfileRef = v.QualityProfileRef
		snap.Target.Monitored = v.Monitored
		snap.Target.Available = v.Available
		snap.Target.Identity = v.Identity
		snap.Owner = v.Owner
		snap.IDs = nonVideoIDs(v.Identity)

	default:
		return snap, fmt.Errorf("unsupported media kind %q", ref.Kind)
	}

	return snap, nil
}

// episodeAvailable decides §8.2's availability input for an episode. The air
// date is the fact; the phase is the derived state, so the air date wins when
// it is known and the phase is the fallback for an Episode the controller has
// already classified but whose metadata has not landed. A blank phase on an
// object with no air date is treated as NOT available: guessing "available"
// there would let a search for an unaired episode grab a fake.
func episodeAvailable(e *catalogv1alpha1.Episode, now time.Time) bool {
	if e.Status.AirDate != nil {
		return !e.Status.AirDate.After(now)
	}
	return e.Status.Phase != "" && e.Status.Phase != catalogv1alpha1.EpisodePhaseUnaired
}
