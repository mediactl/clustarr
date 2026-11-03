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

package remediation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/dubs"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The MAL-Dubs source wakes the anime a list change moved -- a Series with
// its Episodes, an anime Movie -- and nothing else: the first list every
// anime (on talos no other change woke them, so 864 not-dubbed episodes kept
// wanting English after rev 177 loaded it), a later one only those whose
// MAL id moved; a K-drama and a film that is no anime never.
func TestTheMALDubsSourceWakesTheAnimeAChangeMoved(t *testing.T) {
	const ns = "media"
	sr := func(name string, typ catalogv1alpha1.SeriesType, mal string) *catalogv1alpha1.Series {
		s := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		s.Spec.SeriesType = typ
		if mal != "" {
			s.Status.Metadata = &catalogv1alpha1.SeriesMetadata{ExternalIDs: map[string]string{"mal": mal}}
		}
		return s
	}
	ep := func(name, seriesRef string) *catalogv1alpha1.Episode {
		e := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		e.Spec.SeriesRef = seriesRef
		return e
	}
	mv := func(name string, anime bool, mal string) *catalogv1alpha1.Movie {
		m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		m.Status.Classification = &catalogv1alpha1.MovieClassification{Anime: anime}
		m.Status.Metadata = &catalogv1alpha1.MovieMetadata{ExternalIDs: map[string]string{"mal": mal}}
		return m
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.Episode{}, series.EpisodeBySeriesRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).
		WithObjects(
			sr("eighty-six", catalogv1alpha1.SeriesTypeAnime, "41457"), ep("eighty-six-s01e01", "eighty-six"),
			sr("seventh-loop", catalogv1alpha1.SeriesTypeAnime, "56352"), ep("seventh-loop-s01e01", "seventh-loop"),
			sr("no-mal", catalogv1alpha1.SeriesTypeAnime, ""), ep("no-mal-s01e01", "no-mal"),
			sr("crash-landing", catalogv1alpha1.SeriesTypeStandard, ""), ep("crash-landing-s01e01", "crash-landing"),
			mv("akira", true, "47"), mv("rushmore", false, ""),
		).Build()
	s := &dubSource{ix: dubs.NewIndex(), c: c}
	ctx := context.Background()

	assert.ElementsMatch(t, []Key{
		{Kind: KindSeries, Namespace: ns, Name: "eighty-six"},
		{Kind: KindEpisode, Namespace: ns, Name: "eighty-six-s01e01"},
		{Kind: KindSeries, Namespace: ns, Name: "seventh-loop"},
		{Kind: KindEpisode, Namespace: ns, Name: "seventh-loop-s01e01"},
		{Kind: KindSeries, Namespace: ns, Name: "no-mal"},
		{Kind: KindEpisode, Namespace: ns, Name: "no-mal-s01e01"},
		{Kind: KindMovie, Namespace: ns, Name: "akira"},
	}, s.keys(ctx, true, nil), "the first list wakes every anime")
	assert.ElementsMatch(t, []Key{
		{Kind: KindSeries, Namespace: ns, Name: "seventh-loop"}, {Kind: KindEpisode, Namespace: ns, Name: "seventh-loop-s01e01"},
	}, s.keys(ctx, false, map[int]struct{}{56352: {}}), "a later change wakes only what moved")
	assert.Empty(t, s.keys(ctx, false, map[int]struct{}{99: {}}))
}
