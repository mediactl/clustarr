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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// An Episode already holding the provider fields a fan-out would apply is
// not patched again (owner, 2026-10-28: every Series pass patched every one
// of its Episodes, 1,457 a pass for Pokémon, about 110 PATCHes a second on
// the control plane). Any field that differs -- one to release included --
// still patches.
func TestAnEpisodeHoldingItsProviderFieldsIsNotPatchedAgain(t *testing.T) {
	aired := time.Date(1997, 4, 1, 0, 0, 0, 0, time.UTC)
	abs := int32(1)
	desired := DesiredEpisode{
		Name: "pokemon-s01e01", Title: "Pokémon, I Choose You!", Overview: "Ash", RuntimeMinutes: 22, TvdbID: 252316,
		AirDate: &aired, AbsoluteNumber: &abs, PlexID: "5d9c0", PlexConsulted: true, Still: "https://img/still.jpg",
	}
	current := func() *catalogv1alpha1.Episode {
		at := metav1.NewTime(aired)
		a := abs
		return &catalogv1alpha1.Episode{Status: catalogv1alpha1.EpisodeStatus{
			Title: desired.Title, Overview: desired.Overview, RuntimeMinutes: 22, TvdbID: 252316,
			AirDate: &at, AbsoluteNumber: &a, PlexID: "5d9c0",
			Images: []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypeScreenshot, URL: desired.Still}},
		}}
	}
	assert.True(t, providerStatusMatches(current(), desired, "5d9c0"))

	for name, mut := range map[string]func(*catalogv1alpha1.Episode){
		"title":           func(e *catalogv1alpha1.Episode) { e.Status.Title = "old" },
		"overview":        func(e *catalogv1alpha1.Episode) { e.Status.Overview = "" },
		"runtime":         func(e *catalogv1alpha1.Episode) { e.Status.RuntimeMinutes = 23 },
		"tvdb id":         func(e *catalogv1alpha1.Episode) { e.Status.TvdbID = 0 },
		"plex id":         func(e *catalogv1alpha1.Episode) { e.Status.PlexID = "" },
		"air date":        func(e *catalogv1alpha1.Episode) { e.Status.AirDate = nil },
		"absolute number": func(e *catalogv1alpha1.Episode) { e.Status.AbsoluteNumber = nil },
		"still":           func(e *catalogv1alpha1.Episode) { e.Status.Images = nil },
	} {
		e := current()
		mut(e)
		assert.False(t, providerStatusMatches(e, desired, "5d9c0"), name)
	}

	gone := desired
	gone.AirDate, gone.AbsoluteNumber, gone.Still = nil, nil, ""
	assert.False(t, providerStatusMatches(current(), gone, "5d9c0"), "values the provider stopped sending are released by an apply")
}

// A Series fetches its episode list again only when the inputs of its last
// complete fan-out in this process moved: its spec, its metadata refresh, its
// add options. 11 of the loop's 16 workers waited on that fetch, made on
// every pass.
func TestASeriesFetchesItsEpisodesOncePerMetadataRefresh(t *testing.T) {
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{UID: "uid-pokemon", Generation: 2},
		Status: catalogv1alpha1.SeriesStatus{
			Metadata:          &catalogv1alpha1.SeriesMetadata{RefreshedAt: metav1.NewTime(time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC))},
			AddOptionsApplied: true,
		},
	}
	var m episodeSyncMemo
	assert.True(t, m.due(s), "never synced in this process")
	m.synced(s)
	assert.False(t, m.due(s))

	refreshed := s.DeepCopy()
	refreshed.Status.Metadata.RefreshedAt = metav1.NewTime(time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC))
	assert.True(t, m.due(refreshed), "a metadata refresh")
	edited := s.DeepCopy()
	edited.Generation = 3
	assert.True(t, m.due(edited), "a spec edit")
	applied := s.DeepCopy()
	applied.Status.AddOptionsApplied = false
	assert.True(t, m.due(applied), "the first fan-out's add options")
	bare := s.DeepCopy()
	bare.Status.Metadata = nil
	m.synced(bare)
	assert.True(t, m.due(bare), "no metadata yet is never remembered")
}
