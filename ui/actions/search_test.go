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
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// An interactive search is "search now" without grabBest: the Search
// answers its ranked results and grabs nothing until a person picks.
func TestInteractiveSearchCreatesALabelledSearchThatGrabsNothing(t *testing.T) {
	w := &fakeWriter{}
	s, err := actions.InteractiveSearch(t.Context(), w, "media", commonv1.MediaKindEpisode, "andor-s01e01")
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	require.Same(t, s, w.creates[0].obj)
	require.Equal(t, "andor-s01e01-", s.GenerateName)
	require.Equal(t, "media", s.Namespace)
	require.Equal(t, map[string]string{actions.LabelOrigin: actions.OriginUI}, s.Labels)
	require.Equal(t, catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e01"},
	}, s.Spec, "no grabBest: the person picks")
	require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)
}

// readSearch is a Search as the ui's reader returns it.
func readSearch(grab ...string) *catalogv1alpha1.Search {
	s := &catalogv1alpha1.Search{Spec: catalogv1alpha1.SearchSpec{Grab: grab}}
	s.Namespace, s.Name, s.ResourceVersion = "media", "andor-s01e01-x7k2p", "42"
	return s
}

// A grab sends the list read plus the new guid, with the read
// resourceVersion: a merge patch replaces a list whole, so a grab made
// meanwhile must conflict rather than be dropped.
func TestGrabReleaseAppendsToSpecGrabUnderTheReadResourceVersion(t *testing.T) {
	w := &fakeWriter{}
	_, err := actions.GrabRelease(t.Context(), w, readSearch("guid-a"), "guid-b", false)
	require.NoError(t, err)
	require.Len(t, w.patches, 1)
	p := w.patches[0]
	require.Equal(t, types.MergePatchType, p.patchType)
	require.JSONEq(t, `{"metadata":{"resourceVersion":"42"},"spec":{"grab":["guid-a","guid-b"]}}`, string(p.data))
	require.Equal(t, "media", p.obj.GetNamespace())
	require.Equal(t, "andor-s01e01-x7k2p", p.obj.GetName())
	require.Equal(t, actions.FieldManager, p.opts.FieldManager)
}

// A permanently rejected release is grabbed with spec.override, which the
// Search controller requires for one.
func TestGrabReleaseSetsOverrideWhenAsked(t *testing.T) {
	w := &fakeWriter{}
	_, err := actions.GrabRelease(t.Context(), w, readSearch(), "guid-a", true)
	require.NoError(t, err)
	require.JSONEq(t, `{"metadata":{"resourceVersion":"42"},"spec":{"grab":["guid-a"],"override":true}}`, string(w.patches[0].data))
}

// A release already requested is not requested again -- unless it now
// needs the override the first request lacked.
func TestGrabReleaseOfARequestedReleaseSendsNothing(t *testing.T) {
	w := &fakeWriter{}
	s := readSearch("guid-a")
	got, err := actions.GrabRelease(t.Context(), w, s, "guid-a", false)
	require.NoError(t, err)
	require.Same(t, s, got)
	require.Empty(t, w.patches)

	_, err = actions.GrabRelease(t.Context(), w, s, "guid-a", true)
	require.NoError(t, err)
	require.JSONEq(t, `{"metadata":{"resourceVersion":"42"},"spec":{"grab":["guid-a"],"override":true}}`, string(w.patches[0].data))
}

func TestGrabReleaseRejectsInvalidInputWithoutWriting(t *testing.T) {
	full := readSearch()
	for range 50 {
		full.Spec.Grab = append(full.Spec.Grab, "g")
	}
	unread := readSearch()
	unread.ResourceVersion = ""
	cases := map[string]struct {
		search *catalogv1alpha1.Search
		guid   string
	}{
		"no search":         {nil, "guid-a"},
		"unnamed search":    {&catalogv1alpha1.Search{}, "guid-a"},
		"search never read": {unread, "guid-a"},
		"no guid":           {readSearch(), ""},
		"spec.grab is full": {full, "guid-a"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			_, err := actions.GrabRelease(t.Context(), w, tc.search, tc.guid, false)
			require.ErrorIs(t, err, actions.ErrInvalid)
			require.Empty(t, w.patches)
		})
	}
	_, err := actions.InteractiveSearch(t.Context(), &fakeWriter{}, "media", "podcast", "x")
	require.ErrorIs(t, err, actions.ErrInvalid)
}

func TestSearchActionsWrapTheAPIServersErrorAndNeedAWriter(t *testing.T) {
	conflict := apierrors.NewConflict(schema.GroupResource{Group: "catalog.clustarr.io", Resource: "searches"}, "x", nil)
	_, err := actions.GrabRelease(t.Context(), &fakeWriter{err: conflict}, readSearch(), "guid-a", false)
	require.True(t, apierrors.IsConflict(err), "a stale read must read as a Conflict to the handler: %v", err)

	for name, a := range map[string]*actions.Actions{"nil *Actions": nil, "nil Writer": actions.New(nil)} {
		t.Run(name, func(t *testing.T) {
			_, err := a.InteractiveSearch(t.Context(), "media", commonv1.MediaKindEpisode, "x")
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.GrabRelease(t.Context(), readSearch(), "guid-a", false)
			require.ErrorIs(t, err, actions.ErrNoWriter)
		})
	}
}

// TestSearchSeasonAndInteractiveSeasonSearchCreateTheRightSpec: a season
// row's two buttons (Wave S S10) each create one labelled Search on the
// Series with spec.season, under the ui's field manager -- the magnifier
// with grabBest, which the Series plans; the person without, its results
// for a person to pick from. Season 0 is the specials; a negative season,
// or a Series with no namespace or name, is refused without a write; with
// no writer the methods refuse.
func TestSearchSeasonAndInteractiveSeasonSearchCreateTheRightSpec(t *testing.T) {
	for _, tc := range []struct {
		name     string
		create   func(c actions.Creator, ns, series string, season int32) (*catalogv1alpha1.Search, error)
		grabBest bool
	}{
		{"the magnifier", func(c actions.Creator, ns, series string, season int32) (*catalogv1alpha1.Search, error) {
			return actions.SearchSeason(t.Context(), c, ns, series, season)
		}, true},
		{"the person", func(c actions.Creator, ns, series string, season int32) (*catalogv1alpha1.Search, error) {
			return actions.InteractiveSeasonSearch(t.Context(), c, ns, series, season)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, season := range []int32{2, 0} {
				w := &fakeWriter{}
				s, err := tc.create(w, "media", "andor", season)
				require.NoError(t, err)
				require.Len(t, w.creates, 1)
				require.Same(t, s, w.creates[0].obj)
				require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)
				require.Equal(t, "andor-", s.GenerateName)
				require.Equal(t, "media", s.Namespace)
				require.Equal(t, map[string]string{actions.LabelOrigin: actions.OriginUI}, s.Labels)
				require.Equal(t, catalogv1alpha1.SearchSpec{
					MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "andor"},
					Season:   &season,
					GrabBest: tc.grabBest,
				}, s.Spec, "season %d", season)
			}

			for name, args := range map[string]struct {
				ns, series string
				season     int32
			}{
				"a negative season": {"media", "andor", -1},
				"no namespace":      {"", "andor", 1},
				"no series":         {"media", "", 1},
			} {
				w := &fakeWriter{}
				_, err := tc.create(w, args.ns, args.series, args.season)
				require.ErrorIs(t, err, actions.ErrInvalid, name)
				require.Empty(t, w.creates, "%s: refused, so nothing is created", name)
			}
		})
	}

	var none *actions.Actions
	_, err := none.SearchSeason(t.Context(), "media", "andor", 1)
	require.ErrorIs(t, err, actions.ErrNoWriter)
	_, err = actions.New(nil).InteractiveSeasonSearch(t.Context(), "media", "andor", 1)
	require.ErrorIs(t, err, actions.ErrNoWriter)

	w := &fakeWriter{}
	_, err = actions.New(w).SearchSeason(t.Context(), "media", "andor", 3)
	require.NoError(t, err)
	_, err = actions.New(w).InteractiveSeasonSearch(t.Context(), "media", "andor", 3)
	require.NoError(t, err)
	require.Len(t, w.creates, 2, "each method writes through the Actions' writer")
	require.True(t, w.creates[0].obj.(*catalogv1alpha1.Search).Spec.GrabBest)
	require.False(t, w.creates[1].obj.(*catalogv1alpha1.Search).Spec.GrabBest)
}
