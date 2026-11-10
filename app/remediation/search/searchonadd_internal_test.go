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

package search

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// addedUnsearched reads searchForMissing off the item or the container it
// was missing from on add (2026-11-10 design D10, D11): a Movie's
// searchForMovie and an Audiobook's flag are their own; an Episode's,
// Issue's, Album's and Book's are the container's, and hold only a child
// out by the container's creation -- one released after it is new and
// searches as any item. A container with no flag, or one that is gone,
// searches on add.
func TestAddedUnsearchedReadsTheFlagForWhatWasMissingOnAdd(t *testing.T) {
	added := metav1.NewTime(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	before, after := metav1.NewTime(added.Add(-30*24*time.Hour)), metav1.NewTime(added.Add(7*24*time.Hour))
	off, on := ptr.To(false), ptr.To(true)
	series := func(flag *bool) *catalogv1alpha1.Series {
		return &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: added}, Spec: catalogv1alpha1.SeriesSpec{AddOptions: catalogv1alpha1.SeriesAddOptions{SearchForMissing: flag}}}
	}
	episode := func(aired *metav1.Time) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{Status: catalogv1alpha1.EpisodeStatus{AirDate: aired}}
	}
	co := &catalogv1alpha1.Comic{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: added}, Spec: catalogv1alpha1.ComicSpec{AddOptions: catalogv1alpha1.ComicAddOptions{SearchForMissing: off}}}
	artist := &catalogv1alpha1.Artist{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: added}, Spec: catalogv1alpha1.ArtistSpec{AddOptions: catalogv1alpha1.ArtistAddOptions{SearchForMissing: off}}}
	author := &catalogv1alpha1.Author{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: added}, Spec: catalogv1alpha1.AuthorSpec{AddOptions: catalogv1alpha1.AuthorAddOptions{SearchForMissing: off}}}
	for _, tc := range []struct {
		name            string
		item, container client.Object
		want            bool
	}{
		{"a movie's own flag off", &catalogv1alpha1.Movie{Spec: catalogv1alpha1.MovieSpec{AddOptions: catalogv1alpha1.MovieAddOptions{SearchForMovie: off}}}, nil, true},
		{"a movie's own flag unset", &catalogv1alpha1.Movie{}, nil, false},
		{"an audiobook's own flag off", &catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: off}}}, nil, true},
		{"an audiobook's own flag on", &catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: on}}}, nil, false},
		{"an episode aired before its series was added", episode(&before), series(off), true},
		{"an episode aired the day its series was added", episode(&added), series(off), true},
		{"an undated episode", episode(nil), series(off), true},
		{"an episode airing after its series was added is new", episode(&after), series(off), false},
		{"a series with the flag unset", episode(&before), series(nil), false},
		{"a series with the flag on", episode(&before), series(on), false},
		{"a series without a creation time holds every episode", episode(&after), &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{AddOptions: catalogv1alpha1.SeriesAddOptions{SearchForMissing: off}}}, true},
		{"an episode whose owner is no series", episode(&before), co, false},
		{"an issue out before its comic was added", &catalogv1alpha1.Issue{Status: catalogv1alpha1.IssueStatus{Date: &before}}, co, true},
		{"an issue out after its comic was added", &catalogv1alpha1.Issue{Status: catalogv1alpha1.IssueStatus{Date: &after}}, co, false},
		{"an album released before its artist was added", &catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{ReleaseDate: &before}}}, artist, true},
		{"an album released after its artist was added", &catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{ReleaseDate: &after}}}, artist, false},
		{"an album without metadata yet", &catalogv1alpha1.Album{}, artist, true},
		{"a book published before its author was added", &catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{ReleaseDate: &before}}}, author, true},
		{"a book published after its author was added", &catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{ReleaseDate: &after}}}, author, false},
		{"a series is not searched itself", series(off), series(off), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, addedUnsearchedUnder(tc.item, tc.container))
		})
	}

	t.Run("an album's artist and a book's author are read", func(t *testing.T) {
		artist := artist.DeepCopy()
		artist.Namespace, artist.Name = "media", "boc"
		author := author.DeepCopy()
		author.Namespace, author.Name = "media", "pratchett"
		r := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(artist, author).Build()
		album := &catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "mhtrtc"}, Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc"},
			Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{ReleaseDate: &before}}}
		got, err := addedUnsearched(t.Context(), r, album, album)
		require.NoError(t, err)
		require.True(t, got, "the Artist's flag off")
		album.Spec.ArtistRef = "gone"
		got, err = addedUnsearched(t.Context(), r, album, album)
		require.NoError(t, err)
		require.False(t, got, "an artist that is gone searches on add")

		book := &catalogv1alpha1.Book{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "guards"}, Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett")},
			Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{ReleaseDate: &before}}}
		got, err = addedUnsearched(t.Context(), r, book, book)
		require.NoError(t, err)
		require.True(t, got, "the Author's flag off")
		book.Spec.AuthorRef = nil
		got, err = addedUnsearched(t.Context(), r, book, book)
		require.NoError(t, err)
		require.False(t, got, "a standalone book searches on add")
	})
}

// comicProfile is the built-in comic QualityProfile as a cluster object.
func comicProfile(t *testing.T) *catalogv1alpha1.QualityProfile {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/comic.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	return &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "comic"}, Spec: seeds[0].Spec}
}

// searchPublished is the one SearchTask the result publishes, decoded, and
// its effect; nil when it publishes none.
func searchPublished(t *testing.T, res remediation.ItemResult) (*schema.SearchTask, remediation.DispatchPublish) {
	t.Helper()
	for _, eff := range res.Effects {
		p, ok := eff.(remediation.DispatchPublish)
		if !ok {
			continue
		}
		var task schema.SearchTask
		require.NoError(t, schema.Decode(p.Envelope.Schema, p.Envelope.Data, &task))
		return &task, p
	}
	return nil, remediation.DispatchPublish{}
}

// TestAnIssueOfAComicAddedUnsearchedIsNotDispatchedLive is the stage's half
// of searchForMissing (2026-11-10 design D10; plan W1.5's leftover): Saga,
// added with the flag off while two issues were out and missing, is asked
// for no volume pack by its own pass (FirstSearch counts neither issue),
// and each issue's first pass publishes an index-only search on the low
// lane with no pace reservation. The same comic added with the flag on
// asks for the volume, and its issues wait for it; with one issue missing
// (no pack first) an issue's first search is live.
func TestAnIssueOfAComicAddedUnsearchedIsNotDispatchedLive(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	added := metav1.NewTime(now.Add(-time.Hour))
	out := metav1.NewTime(now.Add(-90 * 24 * time.Hour))
	fixture := func(t *testing.T, flag *bool, missing int32) (*Stage, *catalogv1alpha1.Comic, *catalogv1alpha1.Issue) {
		t.Helper()
		co := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "saga", UID: "uid-saga", CreationTimestamp: added},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", RootFolderRef: "comics", QualityProfileRef: "comic",
				AddOptions: catalogv1alpha1.ComicAddOptions{SearchForMissing: flag},
			},
		}
		co.Status.MissingChildCount = missing
		issue := func(n string) *catalogv1alpha1.Issue {
			return &catalogv1alpha1.Issue{
				ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "saga-" + n, UID: types.UID("uid-saga-" + n)},
				Spec:       catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: n, Monitored: ptr.To(true)},
				Status:     catalogv1alpha1.IssueStatus{Phase: catalogv1alpha1.IssuePhaseWanted, Date: &out},
			}
		}
		objs := []client.Object{comicProfile(t), co, issue("001")}
		if missing > 1 {
			objs = append(objs, issue("002"))
		}
		r := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
			WithIndex(&catalogv1alpha1.Issue{}, comic.IssueByComicRefIndex, func(o client.Object) []string {
				return []string{o.(*catalogv1alpha1.Issue).Spec.ComicRef}
			}).
			// The Comic's pass lists its planned volume Searches (C6).
			WithIndex(&catalogv1alpha1.Search{}, seriessearch.IndexSearchesBySeries, func(o client.Object) []string {
				if s := o.(*catalogv1alpha1.Search); s.Spec.PlannedBySeries() && s.Spec.MediaRef.Name != "" {
					return []string{s.Spec.MediaRef.Name}
				}
				return nil
			}).
			WithObjects(objs...).Build()
		bus := membus.New(clockwork.NewFakeClockAt(now))
		require.NoError(t, bus.Ensure(context.Background(), events.Default()))
		t.Cleanup(func() { _ = bus.Close() })
		return New(Options{Reader: r, Bus: bus, Clock: func() time.Time { return now }}), co, objs[2].(*catalogv1alpha1.Issue)
	}
	plan := func(t *testing.T, st *Stage, kind remediation.KeyKind, item client.Object) remediation.ItemResult {
		t.Helper()
		res, err := st.Plan(t.Context(), &remediation.Env{Reader: st.o.Reader, Bus: st.o.Bus},
			&remediation.ItemView{Key: remediation.Key{Kind: kind, Namespace: item.GetNamespace(), Name: item.GetName()}, Item: item, Now: metav1.NewTime(now)})
		require.NoError(t, err)
		return res
	}

	t.Run("added with searchForMissing false", func(t *testing.T) {
		st, co, iss := fixture(t, ptr.To(false), 2)
		task, _ := searchPublished(t, plan(t, st, remediation.KindComic, co))
		require.Nil(t, task, "the comic asks for no volume pack: neither issue waits for one")

		task, eff := searchPublished(t, plan(t, st, remediation.KindIssue, iss))
		require.NotNil(t, task, "the issue is searched on its first pass")
		require.True(t, task.IndexOnly, "against the release index alone")
		require.Equal(t, events.WorkSearchSubject(events.PriorityLow, events.MediaKey("issue", "media", "saga-001")), eff.Subject)
		require.Empty(t, eff.Pace, "an index-only search reserves no pace slot")
	})
	t.Run("added with the flag unset, two issues missing", func(t *testing.T) {
		st, co, iss := fixture(t, nil, 2)
		task, _ := searchPublished(t, plan(t, st, remediation.KindComic, co))
		require.NotNil(t, task, "the comic asks for the volume pack first")
		require.False(t, task.IndexOnly)
		task, _ = searchPublished(t, plan(t, st, remediation.KindIssue, iss))
		require.Nil(t, task, "the issue waits for the volume's automatic search")
	})
	t.Run("added with the flag unset, one issue missing", func(t *testing.T) {
		st, _, iss := fixture(t, nil, 1)
		task, eff := searchPublished(t, plan(t, st, remediation.KindIssue, iss))
		require.NotNil(t, task)
		require.False(t, task.IndexOnly, "an item's first search is live")
		require.Equal(t, events.WorkSearchSubject(events.PriorityNormal, events.MediaKey("issue", "media", "saga-001")), eff.Subject)
		require.NotEmpty(t, eff.Pace, "a live search consumes its pace reservation")
	})
}
