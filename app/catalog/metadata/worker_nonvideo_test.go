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

package metadata_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
)

// The non-video Handler tests run against the fake client: since ADR-0019
// the Handler writes no Kubernetes object (it Gets the target and answers
// in the item-metadata record, recordHarness), so a real apiserver proves
// nothing the fake does not, and these run without KUBEBUILDER_ASSETS.

type stubAudiobookProvider struct {
	audiobook *pkgmetadata.Audiobook
}

func (p stubAudiobookProvider) Name() string { return "audnexus" }
func (p stubAudiobookProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}
func (p stubAudiobookProvider) Audiobook(context.Context, string, string) (*pkgmetadata.Audiobook, error) {
	return p.audiobook, nil
}
func (p stubAudiobookProvider) Chapters(context.Context, string, string) ([]pkgmetadata.Chapter, error) {
	return nil, nil
}

// stubKindRatings is a RatingsProvider for one kind, one source.
type stubKindRatings struct {
	kind    commonv1.MediaKind
	source  string
	ratings pkgmetadata.Ratings
	seen    *[]pkgmetadata.ExternalIDs
}

func (p stubKindRatings) Name() string                           { return "ratings-" + p.source }
func (p stubKindRatings) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubKindRatings) RatingSources(kind commonv1.MediaKind) []string {
	if kind != p.kind {
		return nil
	}
	return []string{p.source}
}
func (p stubKindRatings) Ratings(_ context.Context, _ commonv1.MediaKind, ids pkgmetadata.ExternalIDs) (pkgmetadata.Ratings, error) {
	if p.seen != nil {
		*p.seen = append(*p.seen, ids.Merge(nil))
	}
	return p.ratings, nil
}

// An Artist's refresh writes the fetch language its provider's
// MetadataProvider names, its ratings through enrichRatings (MusicBrainz's
// own "mb" as the seed, under its CRD name, a RatingsProvider's beside it,
// and a prior rating no provider filled kept), and its members and similar
// artists into the extended document (2026-11-10 design §5.4).
func TestHandlerWritesANonVideoDocumentsLanguageRatingsAndExtendedDocument(t *testing.T) {
	ctx := context.Background()
	const ns, name, mbid = "nv", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: name},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: mbid, QualityProfileRef: "lossless", RootFolderRef: "music"},
		Status: catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{
			Name:    "Radiohead",
			Ratings: []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceAniList, ValueCentis: 700, Votes: 2}},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(artist).WithStatusSubresource(artist).Build()
	h := newRecordHarness(t)
	var seen []pkgmetadata.ExternalIDs
	doc := &pkgmetadata.Artist{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: mbid}, Name: "Radiohead",
		Ratings: pkgmetadata.Ratings{"mb": {Source: "mb", ValueCentis: 860, Votes: 12}},
		Members: []pkgmetadata.Person{{Kind: pkgmetadata.PersonMember, Name: "Thom Yorke", Job: "lead vocals"}},
		Similar: []pkgmetadata.SimilarRef{{Title: "Portishead", IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11"}}},
	}
	handler := &metadata.Handler{
		Client: c, Reader: c, Cache: noopCache{}, Records: h.records,
		Extended: h.bus.KV(events.BucketMetadataExtended),
		Registry: &pkgmetadata.Registry{
			Artists: []pkgmetadata.ArtistProvider{stubArtistOnlyProvider{artist: doc}},
			Ratings: []pkgmetadata.RatingsProvider{
				stubKindRatings{kind: commonv1.MediaKindArtist, source: pkgmetadata.RatingSourceHardcover,
					ratings: pkgmetadata.Ratings{pkgmetadata.RatingSourceHardcover: {ValueCentis: 500, Votes: 1}}, seen: &seen},
				stubKindRatings{kind: commonv1.MediaKindBook, source: pkgmetadata.RatingSourceAudnexus},
			},
		},
		Languages: metadata.FetchLanguages{catalogv1alpha1.MetadataProviderMusicBrainz: "de"},
	}

	rec := h.handle(t, handler, commonv1.MediaKindArtist, ns, name)

	var md catalogv1alpha1.ArtistMetadata
	require.NoError(t, json.Unmarshal(rec.Metadata, &md))
	require.Equal(t, "Radiohead", md.Name)
	require.Equal(t, "de", md.Language, "the musicbrainz provider's spec.language")
	require.EqualValues(t, pkgmetadata.SchemaVersion, md.SchemaVersion)
	require.Equal(t, []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceAniList, ValueCentis: 700, Votes: 2},
		{Source: catalogv1alpha1.RatingSourceHardcover, ValueCentis: 500, Votes: 1},
		{Source: catalogv1alpha1.RatingSourceMusicBrainz, ValueCentis: 860, Votes: 12},
	}, md.Ratings, "the seed under its CRD name, the provider's, and the prior anilist carried forward, sorted")
	require.Equal(t, []pkgmetadata.ExternalIDs{{pkgmetadata.KeyMBArtist: mbid}}, seen, "the artist's provider was asked once, by the document's ids")
	require.Empty(t, rec.Inputs.Language, "the record's inputs stay the task's, so the item still incorporates it")

	e, err := handler.Extended.Get(ctx, extended.Key(commonv1.MediaKindArtist, artist.UID))
	require.NoError(t, err)
	ext, err := extended.Decode(e.Value)
	require.NoError(t, err)
	require.Equal(t, []extended.Person{{Name: "Thom Yorke", Character: "lead vocals", Job: "lead vocals"}}, ext.Role)
	require.Equal(t, []extended.Similar{{Title: "Portishead", MBArtistID: "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11"}}, ext.Similar)
}

// An Audiobook's refresh writes fetchLanguage (its language stays the
// recording's), Audnexus's rating through enrichRatings, abridged, tags and
// the adult flag, and its narrators and authors into the extended document;
// without a MetadataProvider language the fetch language is "en".
func TestHandlerWritesAnAudiobooksFetchLanguageRatingAndPeople(t *testing.T) {
	ctx := context.Background()
	const ns, name = "nv", "hobbit"
	ab := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: name},
		Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B0036I54I6", QualityProfileRef: "audio", RootFolderRef: "audiobooks"},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(ab).WithStatusSubresource(ab).Build()
	h := newRecordHarness(t)
	doc := &pkgmetadata.Audiobook{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyASIN: "B0036I54I6"}, Title: "The Hobbit", Language: "english",
		Authors: []pkgmetadata.NamedRef{{Name: "J.R.R. Tolkien", ASIN: "B000AQ1S22"}}, Narrators: []string{"Rob Inglis"},
		Abridged: true, Tags: []string{"Epic"}, IsAdult: true,
		Rating: &pkgmetadata.Rating{Source: pkgmetadata.RatingSourceAudnexus, ValueCentis: 960},
	}
	handler := &metadata.Handler{
		Client: c, Reader: c, Cache: noopCache{}, Records: h.records,
		Extended: h.bus.KV(events.BucketMetadataExtended),
		Registry: &pkgmetadata.Registry{Audiobooks: []pkgmetadata.AudiobookProvider{stubAudiobookProvider{audiobook: doc}}},
	}

	rec := h.handle(t, handler, commonv1.MediaKindAudiobook, ns, name)

	var md catalogv1alpha1.AudiobookMetadata
	require.NoError(t, json.Unmarshal(rec.Metadata, &md))
	require.Equal(t, "The Hobbit", md.Title)
	require.Equal(t, "english", md.Language)
	require.Equal(t, metadata.DefaultFetchLanguage, md.FetchLanguage, "no Languages table: en")
	require.Equal(t, []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceAudnexus, ValueCentis: 960}}, md.Ratings)
	require.True(t, md.Abridged)
	require.True(t, md.IsAdult)
	require.Equal(t, []string{"Epic"}, md.Tags)

	e, err := handler.Extended.Get(ctx, extended.Key(commonv1.MediaKindAudiobook, ab.UID))
	require.NoError(t, err)
	ext, err := extended.Decode(e.Value)
	require.NoError(t, err)
	require.Equal(t, []extended.Person{{Name: "Rob Inglis", Character: "Narrator", Job: "Narrator"}}, ext.Role)
	require.Equal(t, []extended.Person{{Name: "J.R.R. Tolkien", Job: "Author"}}, ext.Writer)
}

// A Book's ratings are asked by the work's ids plus its first edition's
// ISBN-13, which is how Hardcover finds an Open Library work; a Book whose
// editions carry none is asked by the work's ids alone.
func TestHandlerAsksABooksRatingsByItsFirstEditionsISBN(t *testing.T) {
	const ns, name = "nv", "out-of-my-mind"
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: name},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL15181484W"},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(book).WithStatusSubresource(book).Build()
	h := newRecordHarness(t)
	var seen []pkgmetadata.ExternalIDs
	doc := &pkgmetadata.Book{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: "OL15181484W"}, Title: "Out of My Mind",
		Editions: []pkgmetadata.Edition{
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryEdition: "OL1M"}},
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryEdition: "OL2M", pkgmetadata.KeyISBN13: "9781416971702"}},
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryEdition: "OL3M", pkgmetadata.KeyISBN13: "9780000000002"}},
		},
	}
	handler := &metadata.Handler{
		Client: c, Reader: c, Cache: noopCache{}, Records: h.records,
		Registry: &pkgmetadata.Registry{
			Books: []pkgmetadata.BookProvider{stubBookProvider{book: doc}},
			Ratings: []pkgmetadata.RatingsProvider{stubKindRatings{kind: commonv1.MediaKindBook, source: pkgmetadata.RatingSourceHardcover,
				ratings: pkgmetadata.Ratings{pkgmetadata.RatingSourceHardcover: {ValueCentis: 822, Votes: 63}}, seen: &seen}},
		},
		Languages: metadata.FetchLanguages{catalogv1alpha1.MetadataProviderOpenLibrary: "pt-BR"},
	}

	rec := h.handle(t, handler, commonv1.MediaKindBook, ns, name)

	require.Equal(t, []pkgmetadata.ExternalIDs{{pkgmetadata.KeyOpenLibraryWork: "OL15181484W", pkgmetadata.KeyISBN13: "9781416971702"}}, seen,
		"the first edition carrying an ISBN, and the work's own ids untouched")
	var md catalogv1alpha1.BookMetadata
	require.NoError(t, json.Unmarshal(rec.Metadata, &md))
	require.Equal(t, "pt-BR", md.Language)
	require.Equal(t, []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceHardcover, ValueCentis: 822, Votes: 63}}, md.Ratings)
	require.Equal(t, map[string]string{pkgmetadata.KeyOpenLibraryWork: "OL15181484W"}, md.ExternalIDs, "the edition's ISBN is the ratings ask's, not the work's id")
}

type stubBookProvider struct{ book *pkgmetadata.Book }

func (p stubBookProvider) Name() string                           { return "openlibrary" }
func (p stubBookProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubBookProvider) SearchBooks(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	return nil, nil
}
func (p stubBookProvider) Author(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Author, error) {
	return nil, pkgmetadata.ErrNotFound
}
func (p stubBookProvider) Books(context.Context, string) ([]pkgmetadata.Book, error) { return nil, nil }
func (p stubBookProvider) Book(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Book, error) {
	return p.book, nil
}
func (p stubBookProvider) Edition(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Edition, error) {
	return nil, pkgmetadata.ErrNotFound
}
