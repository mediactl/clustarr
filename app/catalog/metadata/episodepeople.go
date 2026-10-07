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
	"context"
	"errors"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Episodes' guest cast and crew (the full Plex Metadata Response design,
// 2026-09-30 §3.5, built 2026-10-07): after a Series refresh, the gateway
// fetches TheTVDB's /episodes/{id}/extended for every episode of the series
// the library holds a file of, and files it in that episode's extended
// document, which the Plex provider answers as the episode's Role,
// Director, Writer and Producer. People never go into a CRD (§3.6).
//
// The episodes are listed from the apiserver by Episode's selectable field
// spec.seriesRef -- one series' episodes, filtered server-side, no cache
// and no index -- which is what the design's deferral ("the gateway's pod
// has no Episode index") lacked.

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch

const (
	// EpisodePeopleTTL is how long an episode's people stand before a
	// refresh asks again.
	EpisodePeopleTTL = 30 * 24 * time.Hour

	// EpisodePeoplePerPass caps the episodes one Series refresh asks
	// TheTVDB about; the rest wait for the series' next refresh. Every
	// refresh shares TheTVDB's limiter (pkg/metadata.DefaultLimits).
	EpisodePeoplePerPass = 300

	// episodePeopleHeartbeat is how often a pass resets its delivery's
	// acknowledgement deadline: the metadata consumer's first is 30 s
	// (BackOff[0] replaces AckWait), shorter than a long pass.
	episodePeopleHeartbeat = 10 * time.Second

	// episodeSeriesRefField is Episode's selectable field
	// (+kubebuilder:selectablefield, api/catalog/v1alpha1/episode_types.go).
	episodeSeriesRefField = "spec.seriesRef"
)

// episodePeoplePerPass is EpisodePeoplePerPass, lowered by tests.
var episodePeoplePerPass = EpisodePeoplePerPass

// refreshEpisodePeople files the people of series' episodes with a file
// whose document is missing or older than EpisodePeopleTTL, at most
// EpisodePeoplePerPass of them, calling inProgress every
// episodePeopleHeartbeat. Like writeExtended it never fails the refresh: a
// failure is logged and the episode is asked again next time. An episode
// TheTVDB does not know is filed with no people, so it is not asked again
// until the TTL; a rate limit ends the pass.
func (h *Handler) refreshEpisodePeople(
	ctx context.Context, series *catalogv1alpha1.Series, now time.Time, inProgress func(context.Context) error,
) {
	provider := pkgmetadata.EpisodePeopleOf(h.Registry)
	if provider == nil || h.Extended == nil || h.Reader == nil {
		return
	}
	ctx, span := tracing.Start(ctx, "metadata.refreshEpisodePeople")
	defer span.End()
	log := logging.FromContext(ctx).With("series", series.Namespace+"/"+series.Name)

	var episodes catalogv1alpha1.EpisodeList
	if err := h.Reader.List(ctx, &episodes, client.InNamespace(series.Namespace),
		client.MatchingFields{episodeSeriesRefField: series.Name}); err != nil {
		tracing.RecordError(span, err)
		log.WarnContext(ctx, "metadata: list the series' episodes for their people (non-fatal)", "error", err)
		return
	}

	fetched, deferred := 0, 0
	beat := time.Now()
	for i := range episodes.Items {
		if ctx.Err() != nil {
			break
		}
		ep := &episodes.Items[i]
		if !ep.Status.HasFile || ep.Status.TvdbID == 0 || ep.Spec.SeriesRef != series.Name {
			continue
		}
		key := extended.Key(commonv1.MediaKindEpisode, ep.UID)
		if e, err := h.Extended.Get(ctx, key); err == nil && now.Sub(e.Created) < EpisodePeopleTTL {
			continue
		} else if err != nil && !errors.Is(err, events.ErrKeyNotFound) {
			log.WarnContext(ctx, "metadata: read an episode's extended document (non-fatal)", "episode", ep.Name, "error", err)
			continue
		}
		if fetched == episodePeoplePerPass {
			deferred++
			continue
		}
		if inProgress != nil && time.Since(beat) >= episodePeopleHeartbeat {
			if err := inProgress(ctx); err != nil {
				log.WarnContext(ctx, "metadata: extend the refresh's deadline (non-fatal)", "error", err)
			}
			beat = time.Now()
		}
		fetched++
		people, err := provider.EpisodePeople(ctx, strconv.FormatInt(ep.Status.TvdbID, 10))
		switch {
		case errors.Is(err, pkgmetadata.ErrRateLimited):
			log.WarnContext(ctx, "metadata: TheTVDB is rate limiting episode people; the rest wait for the next refresh", "episode", ep.Name)
			return
		case errors.Is(err, pkgmetadata.ErrNotFound):
			people = nil
		case err != nil:
			log.WarnContext(ctx, "metadata: fetch an episode's people (non-fatal)", "episode", ep.Name, "error", err)
			continue
		}
		b, err := extended.Encode(extended.FromPeople(people, nil))
		if err == nil {
			_, err = h.Extended.Put(ctx, key, b)
		}
		if err != nil {
			log.WarnContext(ctx, "metadata: write an episode's extended document (non-fatal)", "episode", ep.Name, "error", err)
		}
	}
	if fetched > 0 || deferred > 0 {
		log.InfoContext(ctx, "metadata: episode people filed", "fetched", fetched, "deferred", deferred)
	}
}
