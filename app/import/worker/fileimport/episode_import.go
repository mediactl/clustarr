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

package fileimport

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// episodePlan is everything an episode import needs, resolved from the
// target and its Series before any file is touched.
type episodePlan struct {
	namespace  string
	series     *catalogv1alpha1.Series
	rootFolder *catalogv1alpha1.RootFolder
	profile    quality.Profile

	// originalLanguageName is the series' original language, in the
	// vocabulary pkg/release and the custom-format catalogue share.
	originalLanguageName string

	// episodes are every Episode of the series.
	episodes []EpisodeCandidate

	// named is the one episode the target names (an episode target, or a
	// series target keyed to one episode); nil for a pack.
	named *EpisodeCandidate

	// sceneName is the Download's release title when it names the one
	// episode file the download holds, else "": see runEpisodes.
	sceneName string
}

// resolveSeries reads the target's Series, its root folder, profile and
// episodes. Errors wrapping errBlocked are terminal for this Download.
func (w *Worker) resolveSeries(ctx context.Context, ns string, target importtarget.ImportTarget, profileRef string) (episodePlan, error) {
	plan := episodePlan{namespace: ns}

	seriesName := target.Name
	namedEpisode := ""
	switch ref := target.FileRef(); {
	case target.Kind == commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := w.getItem(ctx, ns, "episode", target.Name, &ep); err != nil {
			return plan, err
		}
		seriesName, namedEpisode = ep.Spec.SeriesRef, ep.Name
	case ref.Kind == commonv1.MediaKindEpisode:
		namedEpisode = ref.Name
	}

	var series catalogv1alpha1.Series
	if err := w.getItem(ctx, ns, "series", seriesName, &series); err != nil {
		return plan, err
	}
	if series.Status.Metadata == nil || series.Status.Metadata.Title == "" {
		return plan, fmt.Errorf("fileimport: series %s/%s has no metadata yet", ns, series.Name)
	}
	plan.series = &series

	root, err := w.getRoot(ctx, ns, series.Spec.RootFolderRef)
	if err != nil {
		return plan, err
	}
	if !importtarget.FileRefFitsRoot(commonv1.MediaRef{Kind: commonv1.MediaKindEpisode}, root.Spec.Kind) {
		return plan, blocked("root folder %q is a %s root; an episode file does not belong there", root.Name, root.Spec.Kind)
	}
	plan.rootFolder = root

	profile, err := w.resolveProfile(ctx, profileRef, series.Spec.QualityProfileRef)
	if err != nil {
		return plan, err
	}
	plan.profile = profile

	if lang := series.Status.Metadata.OriginalLanguage; lang != "" {
		if n, ok := catalogue.LanguageName(lang); ok {
			plan.originalLanguageName = n
		}
	}

	var episodes catalogv1alpha1.EpisodeList
	if err := w.Client.List(ctx, &episodes, client.InNamespace(ns)); err != nil {
		return plan, fmt.Errorf("fileimport: list episodes in %s: %w", ns, err)
	}
	for i := range episodes.Items {
		if ep := &episodes.Items[i]; ep.Spec.SeriesRef == series.Name {
			plan.episodes = append(plan.episodes, EpisodeCandidateFor(ep))
		}
	}
	if namedEpisode != "" {
		for i := range plan.episodes {
			if plan.episodes[i].Name == namedEpisode {
				plan.named = &plan.episodes[i]
			}
		}
		if plan.named == nil {
			return plan, blocked("episode %q is not an episode of series %q", namedEpisode, series.Name)
		}
	}
	return plan, nil
}

// episodesFor rebuilds the catalogv1alpha1.Episode objects
// catalogctx.Episode needs from the EpisodeCandidates MatchEpisodes settled
// on -- the inverse of EpisodeCandidateFor's own projection, not a second
// source of truth: every field catalogctx.Episode reads (spec.seasonNumber,
// spec.episodeNumber, status.title, status.absoluteNumber, status.airDate)
// is already carried on EpisodeCandidate, from the one real Episode
// resolveSeries read via the apiserver.
func episodesFor(cands []EpisodeCandidate) []catalogv1alpha1.Episode {
	out := make([]catalogv1alpha1.Episode, len(cands))
	for i, c := range cands {
		ep := catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: c.Name},
			Spec: catalogv1alpha1.EpisodeSpec{
				SeasonNumber:  int32(c.Season),
				EpisodeNumber: int32(c.Number),
			},
			Status: catalogv1alpha1.EpisodeStatus{Title: c.Title},
		}
		if c.Absolute != 0 {
			absolute := int32(c.Absolute)
			ep.Status.AbsoluteNumber = &absolute
		}
		if c.AirDate != "" {
			if t, err := time.Parse("2006-01-02", c.AirDate); err == nil {
				airDate := metav1.NewTime(t)
				ep.Status.AirDate = &airDate
			}
		}
		out[i] = ep
	}
	return out
}
