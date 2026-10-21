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

// The Edit dialog's save is one merge patch of the spec leaves it names:
// the profile always, the rest only when given, so a field left out keeps
// its value.
func TestEditItemSendsOneMergePatchOfTheFieldsGiven(t *testing.T) {
	w := &fakeWriter{}
	off := false
	obj, err := actions.EditItem(t.Context(), w, "media", commonv1.MediaKindMovie, "nerve",
		actions.EditRequest{QualityProfileRef: "uhd-bluray-web", Monitored: &off, MinimumAvailability: "released"})
	require.NoError(t, err)
	require.Len(t, w.patches, 1)
	require.Empty(t, w.creates)
	p := w.patches[0]
	require.Same(t, obj, p.obj)
	require.IsType(t, &catalogv1alpha1.Movie{}, p.obj)
	require.Equal(t, "media", p.obj.GetNamespace())
	require.Equal(t, "nerve", p.obj.GetName())
	require.Equal(t, types.MergePatchType, p.patchType)
	require.JSONEq(t, `{"spec":{"qualityProfileRef":"uhd-bluray-web","monitored":false,"minimumAvailability":"released"}}`, string(p.data),
		"false is sent, not omitted: it is what the person chose")
	require.Equal(t, actions.FieldManager, p.opts.FieldManager)

	w = &fakeWriter{}
	_, err = actions.EditItem(t.Context(), w, "media", commonv1.MediaKindSeries, "monster",
		actions.EditRequest{QualityProfileRef: "anime-web-1080p", SeriesType: "anime"})
	require.NoError(t, err)
	require.IsType(t, &catalogv1alpha1.Series{}, w.patches[0].obj)
	require.JSONEq(t, `{"spec":{"qualityProfileRef":"anime-web-1080p","seriesType":"anime"}}`, string(w.patches[0].data),
		"monitored was not given, so it is not sent")
}

// A kind without a profile of its own, a missing profile, or a video
// field on the wrong kind or with a value the CRD would refuse, is
// ErrInvalid before any patch.
func TestEditItemRefusesWhatTheDialogCannotMean(t *testing.T) {
	cases := map[string]struct {
		kind commonv1.MediaKind
		req  actions.EditRequest
	}{
		"an episode ranks under its series":  {commonv1.MediaKindEpisode, actions.EditRequest{QualityProfileRef: "any"}},
		"a book ranks under its author":      {commonv1.MediaKindBook, actions.EditRequest{QualityProfileRef: "any"}},
		"no profile":                         {commonv1.MediaKindMovie, actions.EditRequest{}},
		"a series type on a movie":           {commonv1.MediaKindMovie, actions.EditRequest{QualityProfileRef: "any", SeriesType: "anime"}},
		"an unknown series type":             {commonv1.MediaKindSeries, actions.EditRequest{QualityProfileRef: "any", SeriesType: "cartoon"}},
		"a minimum availability on a series": {commonv1.MediaKindSeries, actions.EditRequest{QualityProfileRef: "any", MinimumAvailability: "released"}},
		"an unknown minimum availability":    {commonv1.MediaKindMovie, actions.EditRequest{QualityProfileRef: "any", MinimumAvailability: "soon"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			_, err := actions.EditItem(t.Context(), w, "media", c.kind, "x", c.req)
			require.True(t, errors.Is(err, actions.ErrInvalid), "%v", err)
			require.Empty(t, w.patches)
		})
	}
	_, err := actions.EditItem(t.Context(), &fakeWriter{}, "", commonv1.MediaKindMovie, "x", actions.EditRequest{QualityProfileRef: "any"})
	require.True(t, errors.Is(err, actions.ErrInvalid))
}

// Every editable kind is one with a top-level spec.qualityProfileRef, and
// each goes to its own type.
func TestEditableKindsAreTheOnesWithAProfileOfTheirOwn(t *testing.T) {
	require.Equal(t, []commonv1.MediaKind{
		commonv1.MediaKindArtist, commonv1.MediaKindAudiobook, commonv1.MediaKindAuthor,
		commonv1.MediaKindComic, commonv1.MediaKindMovie, commonv1.MediaKindSeries,
	}, actions.EditableKinds())
	for _, kind := range actions.EditableKinds() {
		w := &fakeWriter{}
		obj, err := actions.EditItem(t.Context(), w, "media", kind, "x", actions.EditRequest{QualityProfileRef: "p"})
		require.NoError(t, err, kind)
		require.Equal(t, kind, kindOf(obj), kind)
	}
}

func kindOf(obj any) commonv1.MediaKind {
	switch obj.(type) {
	case *catalogv1alpha1.Movie:
		return commonv1.MediaKindMovie
	case *catalogv1alpha1.Series:
		return commonv1.MediaKindSeries
	case *catalogv1alpha1.Artist:
		return commonv1.MediaKindArtist
	case *catalogv1alpha1.Author:
		return commonv1.MediaKindAuthor
	case *catalogv1alpha1.Comic:
		return commonv1.MediaKindComic
	case *catalogv1alpha1.Audiobook:
		return commonv1.MediaKindAudiobook
	}
	return ""
}
