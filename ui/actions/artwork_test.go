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

package actions_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// An image set is one merge patch of the lists it fills: spec.artwork and,
// on a series, spec.seasonArtwork; a list not given is left alone.
func TestSetArtworkSendsOneMergePatchOfTheListsGiven(t *testing.T) {
	w := &fakeWriter{}
	three := int32(3)
	obj, err := actions.SetArtwork(t.Context(), w, "media", commonv1.MediaKindSeries, "edgerunners",
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://api.mediux.pro/assets/p"}},
		[]catalogv1alpha1.SeasonArtworkOverride{
			{Season: 1, Type: catalogv1alpha1.ImageTypePoster, URL: "https://api.mediux.pro/assets/s1"},
			{Season: 1, Episode: &three, Type: catalogv1alpha1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/e3"},
		})
	require.NoError(t, err)
	require.Len(t, w.patches, 1)
	p := w.patches[0]
	require.Same(t, obj, p.obj)
	require.IsType(t, &catalogv1alpha1.Series{}, p.obj)
	require.Equal(t, types.MergePatchType, p.patchType)
	require.JSONEq(t, `{"spec":{"artwork":[{"type":"poster","url":"https://api.mediux.pro/assets/p"}],
		"seasonArtwork":[{"season":1,"type":"poster","url":"https://api.mediux.pro/assets/s1"},
		{"season":1,"episode":3,"type":"screenshot","url":"https://api.mediux.pro/assets/e3"}]}}`, string(p.data))
	require.Equal(t, actions.FieldManager, p.opts.FieldManager)

	w = &fakeWriter{}
	_, err = actions.SetArtwork(t.Context(), w, "media", commonv1.MediaKindMovie, "nerve",
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://theposterdb.com/api/assets/1"}}, nil)
	require.NoError(t, err)
	require.IsType(t, &catalogv1alpha1.Movie{}, w.patches[0].obj)
	require.JSONEq(t, `{"spec":{"artwork":[{"type":"fanart","url":"https://theposterdb.com/api/assets/1"}]}}`, string(w.patches[0].data),
		"seasonArtwork not given is not sent")
}

func TestSetArtworkRefusesWhatNoSetCanMean(t *testing.T) {
	poster := []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://a/p"}}
	cases := map[string]func(w *fakeWriter) error{
		"an artist has no Plex provider": func(w *fakeWriter) error {
			_, err := actions.SetArtwork(t.Context(), w, "media", commonv1.MediaKindArtist, "x", poster, nil)
			return err
		},
		"a movie has no seasons": func(w *fakeWriter) error {
			_, err := actions.SetArtwork(t.Context(), w, "media", commonv1.MediaKindMovie, "x", poster,
				[]catalogv1alpha1.SeasonArtworkOverride{{Season: 1, Type: catalogv1alpha1.ImageTypePoster, URL: "https://a/s"}})
			return err
		},
		"nothing to set": func(w *fakeWriter) error {
			_, err := actions.SetArtwork(t.Context(), w, "media", commonv1.MediaKindSeries, "x", nil, nil)
			return err
		},
		"no name": func(w *fakeWriter) error {
			_, err := actions.SetArtwork(t.Context(), w, "media", commonv1.MediaKindSeries, "", poster, nil)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			require.True(t, errors.Is(run(w), actions.ErrInvalid))
			require.Empty(t, w.patches)
		})
	}
}
