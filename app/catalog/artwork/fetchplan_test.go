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

// These are the source-selection rules the gateway's Fetcher.Sync carried
// until ADR-0019 A5.2 moved them into the item key's plan: the item key
// resolves its sources (ResolveSources) and plans the fetch (PlanFetch)
// against what the artwork index holds, and the fetcher fetches exactly
// what the plan names.

var planNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

const (
	providerURL = "https://image.tmdb.org/t/p/original/provider.png"
	customURL   = "https://example.org/custom.png"
)

// stored is a current original fetched from url of kind at planNow.
func stored(url string, kind catalogv1alpha1.ArtworkSource) artwork.Object {
	return artwork.Object{Present: true, Current: true, Source: kind, SourceURL: url, Digest: "d", UpdatedAt: metav1.NewTime(planNow)}
}

func posterImages() []catalogv1alpha1.Image {
	return []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: providerURL}}
}

func TestPlanFetchFetchesTheCustomURLNotTheProviderURL(t *testing.T) {
	plan := artwork.PlanFetch(artwork.ResolveSources(
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: customURL}}, posterImages()),
		nil, nil, planNow)
	assert.Equal(t, []schema.ArtworkFetch{{Type: "poster", URL: customURL, Kind: "custom"}}, plan.Fetch,
		"the provider's poster is never fetched while an override names another")
	assert.Empty(t, plan.Drop)
}

func TestPlanFetchSkipsAnUnchangedSourceWhoseObjectIsPresent(t *testing.T) {
	plan := artwork.PlanFetch(artwork.ResolveSources(nil, posterImages()),
		map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypePoster: stored(providerURL, catalogv1alpha1.ArtworkSourceProvider)},
		nil, planNow.Add(time.Hour))
	assert.True(t, plan.Empty(), "an unchanged source URL with its object present is not fetched again")
}

func TestPlanFetchRefetchesAnUnchangedSourceWhoseObjectIsMissing(t *testing.T) {
	plan := artwork.PlanFetch(artwork.ResolveSources(nil, posterImages()), nil, nil, planNow)
	assert.Equal(t, []schema.ArtworkFetch{{Type: "poster", URL: providerURL, Kind: "provider"}}, plan.Fetch,
		"a missing object is fetched even though its source URL is unchanged")
}

// R3: the user asked for that image; a silent substitute is a guess. A
// custom URL that failed rests for FailureRetry, and the provider's image
// is not fetched in its place.
func TestPlanFetchNeverFallsBackToTheProviderWhenACustomURLFails(t *testing.T) {
	failedAt := planNow.Add(-time.Minute)
	plan := artwork.PlanFetch(artwork.ResolveSources(
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: customURL}}, posterImages()),
		nil,
		map[catalogv1alpha1.ImageType]artwork.Failure{catalogv1alpha1.ImageTypePoster: {URL: customURL, At: failedAt}},
		planNow)
	assert.Empty(t, plan.Fetch, "neither the resting custom URL nor the provider's")
	assert.Equal(t, failedAt.Add(artwork.FailureRetry), plan.Due)
}

func TestPlanFetchDropsACustomOriginalWhoseOverrideIsGoneAndNoProviderImageExists(t *testing.T) {
	plan := artwork.PlanFetch(artwork.ResolveSources(nil, nil),
		map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypeLogo: stored(customURL, catalogv1alpha1.ArtworkSourceCustom)},
		nil, planNow)
	assert.Empty(t, plan.Fetch)
	assert.Equal(t, []string{"logo"}, plan.Drop,
		"a custom original whose override was removed, with no provider image to replace it, is dropped")
}

func TestPlanFetchKeepsAProviderOriginalTheProviderStoppedListing(t *testing.T) {
	plan := artwork.PlanFetch(artwork.ResolveSources(nil, nil),
		map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypeBanner: stored(providerURL, catalogv1alpha1.ArtworkSourceProvider)},
		nil, planNow)
	assert.True(t, plan.Empty(), "a provider that did not list a type this time is not a reason to drop art already stored")
}

func TestPlanFetchReplacesTheProviderImageWhenACustomOverrideIsRemoved(t *testing.T) {
	plan := artwork.PlanFetch(artwork.ResolveSources(nil, posterImages()),
		map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypePoster: stored(customURL, catalogv1alpha1.ArtworkSourceCustom)},
		nil, planNow)
	assert.Equal(t, []schema.ArtworkFetch{{Type: "poster", URL: providerURL, Kind: "provider"}}, plan.Fetch)
	assert.Empty(t, plan.Drop)
}
