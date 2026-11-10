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
		"an issue ranks under its comic":     {commonv1.MediaKindIssue, actions.EditRequest{QualityProfileRef: "any"}},
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
		commonv1.MediaKindAlbum, commonv1.MediaKindArtist, commonv1.MediaKindAudiobook, commonv1.MediaKindAuthor,
		commonv1.MediaKindBook, commonv1.MediaKindComic, commonv1.MediaKindMovie, commonv1.MediaKindSeries,
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
	case *catalogv1alpha1.Book:
		return commonv1.MediaKindBook
	case *catalogv1alpha1.Album:
		return commonv1.MediaKindAlbum
	}
	return ""
}

// The non-video kinds' own fields (2026-11-10 design §9.2-§9.5) ride the
// same merge patch: an artist's monitor-new mode and metadata profile, an
// author's, a comic's kind, special version and monitor-new flag, an
// audiobook's region and its links (an empty link clears it), a
// standalone book's profile and root folder, and an album's profile
// override (or its removal) and release choice (design §9.2).
func TestEditItemSendsEachKindsOwnFields(t *testing.T) {
	on, off := true, false
	none, dune := "", "dune"
	for name, tc := range map[string]struct {
		kind commonv1.MediaKind
		req  actions.EditRequest
		want string
	}{
		"artist": {commonv1.MediaKindArtist, actions.EditRequest{
			QualityProfileRef: "lossless", MonitorNewItems: "new",
			MusicProfile: &actions.MusicProfile{PrimaryTypes: []string{"album", "ep", "", "album"}, SecondaryTypes: []string{"studio", "live"}, ReleaseStatuses: []string{"official"}},
		}, `{"spec":{"qualityProfileRef":"lossless","monitorNewItems":"new","metadataProfile":{"primaryTypes":["album","ep"],"secondaryTypes":["studio","live"],"releaseStatuses":["official"]}}}`},
		"author": {commonv1.MediaKindAuthor, actions.EditRequest{
			QualityProfileRef: "ebook", MonitorNewItems: "none",
			BookProfile: &actions.BookProfile{AllowedLanguages: []string{"en", " fr "}, SkipMissingDate: true, MinPages: 50},
		}, `{"spec":{"qualityProfileRef":"ebook","monitorNewItems":"none","metadataProfile":{"allowedLanguages":["en","fr"],"skipMissingDate":true,"skipMissingISBN":false,"skipPartsAndSets":false,"skipSeriesSecondary":false,"minPages":50}}}`},
		"author, every language": {commonv1.MediaKindAuthor, actions.EditRequest{
			QualityProfileRef: "ebook", BookProfile: &actions.BookProfile{},
		}, `{"spec":{"qualityProfileRef":"ebook","metadataProfile":{"allowedLanguages":null,"skipMissingDate":false,"skipMissingISBN":false,"skipPartsAndSets":false,"skipSeriesSecondary":false,"minPages":0}}}`},
		"comic": {commonv1.MediaKindComic, actions.EditRequest{
			QualityProfileRef: "cbz", Monitored: &on, ComicKind: "manga", SpecialVersion: "volumeAsIssue", MonitorNewIssues: &off,
		}, `{"spec":{"qualityProfileRef":"cbz","monitored":true,"kind":"manga","specialVersion":"volumeAsIssue","monitorNewIssues":false}}`},
		"audiobook": {commonv1.MediaKindAudiobook, actions.EditRequest{
			QualityProfileRef: "m4b", Region: "uk", AuthorRef: &none, BookRef: &dune,
		}, `{"spec":{"qualityProfileRef":"m4b","region":"uk","authorRef":null,"bookRef":"dune"}}`},
		"book": {commonv1.MediaKindBook, actions.EditRequest{
			QualityProfileRef: "ebook", RootFolderRef: "books", Monitored: &off,
		}, `{"spec":{"qualityProfileRef":"ebook","rootFolderRef":"books","monitored":false}}`},
		"an author's book, monitored alone": {commonv1.MediaKindBook, actions.EditRequest{Monitored: &on},
			`{"spec":{"monitored":true}}`},
		"album": {commonv1.MediaKindAlbum, actions.EditRequest{
			QualityProfileRef: "lossless", Monitored: &off, AnyReleaseOk: &off, ReleaseID: &dune,
		}, `{"spec":{"qualityProfileRef":"lossless","monitored":false,"anyReleaseOk":false,"releaseID":"dune"}}`},
		"album, the artist's profile and no pin": {commonv1.MediaKindAlbum, actions.EditRequest{
			InheritProfile: true, AnyReleaseOk: &on, ReleaseID: &none,
		}, `{"spec":{"qualityProfileRef":null,"anyReleaseOk":true,"releaseID":null}}`},
		"album, monitored alone": {commonv1.MediaKindAlbum, actions.EditRequest{Monitored: &on},
			`{"spec":{"monitored":true}}`},
	} {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			obj, err := actions.EditItem(t.Context(), w, "media", tc.kind, "x", tc.req)
			require.NoError(t, err)
			require.Len(t, w.patches, 1)
			require.Equal(t, tc.kind, kindOf(obj))
			require.Equal(t, types.MergePatchType, w.patches[0].patchType)
			require.JSONEq(t, tc.want, string(w.patches[0].data))
		})
	}
}

// A kind field on another kind, a value its CRD refuses, an empty
// metadata-profile list (which would admit nothing), a book or album edit
// naming nothing, or an inherited profile anywhere but alone on an album
// is ErrInvalid before any patch.
func TestEditItemRefusesAKindFieldOnTheWrongKindOrOutOfItsValues(t *testing.T) {
	off := false
	ref := "x"
	cases := map[string]struct {
		kind commonv1.MediaKind
		req  actions.EditRequest
	}{
		"a comic kind on an artist":        {commonv1.MediaKindArtist, actions.EditRequest{QualityProfileRef: "p", ComicKind: "manga"}},
		"an unknown comic kind":            {commonv1.MediaKindComic, actions.EditRequest{QualityProfileRef: "p", ComicKind: "webtoon"}},
		"an unknown special version":       {commonv1.MediaKindComic, actions.EditRequest{QualityProfileRef: "p", SpecialVersion: "annual"}},
		"monitor new issues on an author":  {commonv1.MediaKindAuthor, actions.EditRequest{QualityProfileRef: "p", MonitorNewIssues: &off}},
		"an unknown region":                {commonv1.MediaKindAudiobook, actions.EditRequest{QualityProfileRef: "p", Region: "nz"}},
		"a region on a comic":              {commonv1.MediaKindComic, actions.EditRequest{QualityProfileRef: "p", Region: "us"}},
		"an author link on a book":         {commonv1.MediaKindBook, actions.EditRequest{QualityProfileRef: "p", AuthorRef: &ref}},
		"an author's monitor-new on movie": {commonv1.MediaKindMovie, actions.EditRequest{QualityProfileRef: "p", MonitorNewItems: "all"}},
		"new on an author":                 {commonv1.MediaKindAuthor, actions.EditRequest{QualityProfileRef: "p", MonitorNewItems: "new"}},
		"a music profile on an author":     {commonv1.MediaKindAuthor, actions.EditRequest{QualityProfileRef: "p", MusicProfile: &actions.MusicProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}}}},
		"no primary type at all":           {commonv1.MediaKindArtist, actions.EditRequest{QualityProfileRef: "p", MusicProfile: &actions.MusicProfile{PrimaryTypes: []string{""}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}}}},
		"an unknown secondary type":        {commonv1.MediaKindArtist, actions.EditRequest{QualityProfileRef: "p", MusicProfile: &actions.MusicProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"karaoke"}, ReleaseStatuses: []string{"official"}}}},
		"a book profile on an artist":      {commonv1.MediaKindArtist, actions.EditRequest{QualityProfileRef: "p", BookProfile: &actions.BookProfile{}}},
		"negative pages":                   {commonv1.MediaKindAuthor, actions.EditRequest{QualityProfileRef: "p", BookProfile: &actions.BookProfile{MinPages: -1}}},
		"a root folder on a comic":         {commonv1.MediaKindComic, actions.EditRequest{QualityProfileRef: "p", RootFolderRef: "r"}},
		"a book edit naming nothing":       {commonv1.MediaKindBook, actions.EditRequest{}},
		"an album edit naming nothing":     {commonv1.MediaKindAlbum, actions.EditRequest{}},
		"anyReleaseOk on a movie":          {commonv1.MediaKindMovie, actions.EditRequest{QualityProfileRef: "p", AnyReleaseOk: &off}},
		"a release pin on a book":          {commonv1.MediaKindBook, actions.EditRequest{QualityProfileRef: "p", ReleaseID: &ref}},
		"an inherited profile on a movie":  {commonv1.MediaKindMovie, actions.EditRequest{InheritProfile: true}},
		"inherited beside a profile":       {commonv1.MediaKindAlbum, actions.EditRequest{InheritProfile: true, QualityProfileRef: "p"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			_, err := actions.EditItem(t.Context(), w, "media", c.kind, "x", c.req)
			require.ErrorIs(t, err, actions.ErrInvalid)
			require.Empty(t, w.patches)
		})
	}
}

// The dialog's enumerations are the action's: what a select offers is
// what EditItem admits.
func TestEditChoicesAreTheCRDsEnumerations(t *testing.T) {
	ch := actions.Choices()
	require.Equal(t, []string{"all", "none", "new"}, ch.ArtistMonitorNew)
	require.Equal(t, []string{"all", "none"}, ch.AuthorMonitorNew)
	require.Equal(t, []string{"comic", "manga"}, ch.ComicKinds)
	require.Equal(t, []string{"normal", "tpb", "oneShot", "hardCover", "omnibus", "volumeAsIssue"}, ch.ComicSpecialVersions)
	require.Len(t, ch.AudiobookRegions, 10)
	require.Equal(t, []string{"album", "ep", "single", "broadcast", "other"}, ch.MusicPrimaryTypes)
	require.Contains(t, ch.MusicSecondaryTypes, "studio")
	require.Equal(t, []string{"official", "promotion", "bootleg", "pseudoRelease"}, ch.MusicReleaseStatuses)
	for _, kind := range ch.ComicKinds {
		_, err := actions.EditItem(t.Context(), &fakeWriter{}, "media", commonv1.MediaKindComic, "x", actions.EditRequest{QualityProfileRef: "p", ComicKind: kind})
		require.NoError(t, err, kind)
	}
	for _, region := range ch.AudiobookRegions {
		_, err := actions.EditItem(t.Context(), &fakeWriter{}, "media", commonv1.MediaKindAudiobook, "x", actions.EditRequest{QualityProfileRef: "p", Region: region})
		require.NoError(t, err, region)
	}
}
