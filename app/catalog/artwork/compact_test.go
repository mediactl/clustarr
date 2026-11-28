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

package artwork_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The compact twin of a titled poster (the compact-posters design,
// 2026-11-27) is drawn in the fetch that draws the poster, so the plan asks
// for a fetch when a titled poster has no twin, or one drawn under another
// CompactTitleVersion or for another source. A poster stored plain under a
// titled source (every candidate carried text) has no twin and wants none.
func TestPlanFetch_Compact(t *testing.T) {
	const poster, logo = "https://img.example/textless.jpg", "https://img.example/logo.png"
	src := artwork.Source{URL: poster, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: logo}
	sources := map[catalogv1alpha1.ImageType]artwork.Source{catalogv1alpha1.ImageTypePoster: src}
	now := planNow.Add(time.Hour)
	object := func(titled bool, compact string) map[catalogv1alpha1.ImageType]artwork.Object {
		return map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypePoster: {
			Present: true, Current: true, Source: catalogv1alpha1.ArtworkSourceProvider, SourceURL: poster, Digest: "d",
			UpdatedAt: metav1.NewTime(planNow), Title: src.TitleMark(), Titled: titled, Compact: compact,
		}}
	}
	assert.Equal(t, artwork.CompactTitleVersion+":"+src.TitleMark(), src.CompactMark())
	assert.Empty(t, artwork.Source{URL: poster}.CompactMark(), "no logo, no twin")

	for _, tc := range []struct {
		name    string
		objects map[catalogv1alpha1.ImageType]artwork.Object
		fetched bool
	}{
		{"a titled poster with no twin", object(true, ""), true},
		{"a titled poster with its twin", object(true, src.CompactMark()), false},
		{"a twin drawn under an older compact version", object(true, "0:"+src.TitleMark()), true},
		{"a twin drawn for another source", object(true, artwork.CompactTitleVersion+":1 0000"), true},
		{"a poster stored plain under a titled source", object(false, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := artwork.PlanFetch(sources, tc.objects, nil, now)
			assert.Equal(t, tc.fetched, len(plan.Fetch) == 1, "%+v", plan.Fetch)
		})
	}

	// A resting failure holds this fetch as it holds any.
	failed := map[catalogv1alpha1.ImageType]artwork.Failure{catalogv1alpha1.ImageTypePoster: {URL: poster, At: now.Add(-time.Minute)}}
	assert.True(t, artwork.PlanFetch(sources, object(true, ""), failed, now).Empty())

	// Only a poster has a twin: a titled mark on another type asks nothing.
	fanart := map[catalogv1alpha1.ImageType]artwork.Source{catalogv1alpha1.ImageTypeFanart: {URL: poster, Kind: catalogv1alpha1.ArtworkSourceProvider}}
	stored := map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypeFanart: {
		Present: true, Current: true, Source: catalogv1alpha1.ArtworkSourceProvider, SourceURL: poster,
		UpdatedAt: metav1.NewTime(planNow), Titled: true,
	}}
	assert.True(t, artwork.PlanFetch(fanart, stored, nil, now).Empty())
}

// The plan's hash is the fetch task's Msg-Id: a plan that names a titled
// poster hashes anew now that the fetch also draws the twin, so the task for
// a missing twin is published at once instead of being taken for the one
// already sent; a plan with no titled poster keeps the hash it had.
func TestFetchPlanHash_Compact(t *testing.T) {
	plain := artwork.FetchPlan{Fetch: []schema.ArtworkFetch{{Type: "poster", URL: "https://img.example/p.jpg", Kind: "provider"}}}
	titled := artwork.FetchPlan{Fetch: []schema.ArtworkFetch{{Type: "poster", URL: "https://img.example/p.jpg", Kind: "provider", Logo: "https://img.example/logo.png"}}}
	// Both values are those of the commit before the twin (aecf0331).
	assert.Equal(t, "02bab91565e03669282d0448fd87172085715a4444225aeb3d87bbb74d3fbf81", plain.Hash())
	assert.NotEqual(t, "c9599d39a24a251875d1cefe45154bdbd69d7f59cba8fe5cc721ed53cec85de7", titled.Hash())
}
