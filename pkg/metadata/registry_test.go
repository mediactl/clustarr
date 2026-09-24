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
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

type stubMovieProvider struct {
	movie *metadata.Movie
	err   error
}

func (s stubMovieProvider) Name() string                        { return "stub" }
func (s stubMovieProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (s stubMovieProvider) Movie(context.Context, string, string) (*metadata.Movie, error) {
	return s.movie, s.err
}

func (s stubMovieProvider) FindMovie(context.Context, metadata.ExternalIDs) (*metadata.Movie, error) {
	return s.movie, s.err
}

func (s stubMovieProvider) SearchMovies(context.Context, string, int) ([]metadata.MovieHit, error) {
	return nil, nil
}

func TestRegistryLookupFallsThroughToTheNextProviderOnFailure(t *testing.T) {
	reg := &metadata.Registry{Movies: []metadata.MovieProvider{
		stubMovieProvider{err: metadata.ErrNotFound},
		stubMovieProvider{movie: &metadata.Movie{Title: "Inception"}},
	}}

	got, err := reg.Lookup(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "27205"})

	require.NoError(t, err)
	movie, ok := got.(*metadata.Movie)
	require.True(t, ok)
	require.Equal(t, "Inception", movie.Title)
}

func TestRegistryLookupJoinsErrorsWhenEveryProviderFails(t *testing.T) {
	reg := &metadata.Registry{Movies: []metadata.MovieProvider{
		stubMovieProvider{err: metadata.ErrNotFound},
		stubMovieProvider{err: metadata.ErrNotFound},
	}}

	_, err := reg.Lookup(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "27205"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestRegistryLookupRejectsAnUnsupportedKind(t *testing.T) {
	reg := &metadata.Registry{}

	_, err := reg.Lookup(context.Background(), commonv1.MediaKindEpisode, metadata.ExternalIDs{})

	require.Error(t, err)
}

// TestRegistryLookupRejectsIssue pins that MediaKindIssue -- like Episode --
// is deliberately absent from Lookup's switch, not merely unimplemented: see
// Lookup's default-case comment for why ("first entity from the first
// provider that succeeds" is the wrong shape for the list Issues(volumeID)
// returns). app/catalog/metadata/rpc.go's lookupIssues is where Issue is
// actually served.
func TestRegistryLookupRejectsIssue(t *testing.T) {
	reg := &metadata.Registry{}

	_, err := reg.Lookup(context.Background(), commonv1.MediaKindIssue, metadata.ExternalIDs{})

	require.Error(t, err)
}

type stubArtistAlbumProvider struct {
	album *metadata.Album
	err   error
}

func (p stubArtistAlbumProvider) Name() string                        { return "musicbrainz" }
func (p stubArtistAlbumProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }

func (p stubArtistAlbumProvider) SearchArtists(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, nil
}

func (p stubArtistAlbumProvider) Artist(context.Context, string) (*metadata.Artist, error) {
	return nil, metadata.ErrNotFound
}

func (p stubArtistAlbumProvider) Albums(context.Context, string) ([]metadata.Album, error) {
	return nil, nil
}

func (p stubArtistAlbumProvider) Album(context.Context, string) (*metadata.Album, error) {
	return p.album, p.err
}

func TestRegistryLookupAlbumUsesArtistProviderAlbum(t *testing.T) {
	reg := &metadata.Registry{Artists: []metadata.ArtistProvider{
		stubArtistAlbumProvider{album: &metadata.Album{Title: "OK Computer"}},
	}}

	got, err := reg.Lookup(context.Background(), commonv1.MediaKindAlbum,
		metadata.ExternalIDs{metadata.KeyMBReleaseGroup: "b1392450-e666-3926-9ce9-9b7f7b62f699"})

	require.NoError(t, err)
	album, ok := got.(*metadata.Album)
	require.True(t, ok)
	require.Equal(t, "OK Computer", album.Title)
}

func TestRegistryLookupAlbumRequiresAReleaseGroupID(t *testing.T) {
	reg := &metadata.Registry{Artists: []metadata.ArtistProvider{stubArtistAlbumProvider{}}}

	_, err := reg.Lookup(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{})

	require.Error(t, err)
}

type stubBookProvider struct {
	author *metadata.Author
	book   *metadata.Book
	err    error
}

func (p stubBookProvider) Name() string                        { return "openlibrary" }
func (p stubBookProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (p stubBookProvider) SearchBooks(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, nil
}

func (p stubBookProvider) Author(context.Context, metadata.ExternalIDs) (*metadata.Author, error) {
	return p.author, p.err
}

func (p stubBookProvider) Books(context.Context, string) ([]metadata.Book, error) { return nil, nil }

func (p stubBookProvider) Book(context.Context, metadata.ExternalIDs) (*metadata.Book, error) {
	return p.book, p.err
}

func (p stubBookProvider) Edition(context.Context, metadata.ExternalIDs) (*metadata.Edition, error) {
	return nil, metadata.ErrNotFound
}

func TestRegistryLookupBookUsesBookProviderBook(t *testing.T) {
	reg := &metadata.Registry{Books: []metadata.BookProvider{
		stubBookProvider{book: &metadata.Book{Title: "The Colour of Magic"}},
	}}

	got, err := reg.Lookup(context.Background(), commonv1.MediaKindBook,
		metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL45883W"})

	require.NoError(t, err)
	book, ok := got.(*metadata.Book)
	require.True(t, ok)
	require.Equal(t, "The Colour of Magic", book.Title)
}

// TestRegistryLookupAudiobookThreadsTheRegionIDThrough proves the fix
// alongside this task's new Album/Book cases: the pre-existing Audiobook
// case hardcoded region "us" regardless of AudiobookSpec.Region, which task
// G2-1 makes reachable for the first time by wiring Audiobook into
// app/catalog/metadata's worker -- so it is fixed here rather than shipped as
// a newly-reachable bug. ids["region"] threads through exactly like
// rpc.go's lookupEpisodes threads ids["order"].
func TestRegistryLookupAudiobookThreadsTheRegionIDThrough(t *testing.T) {
	var gotRegion string
	reg := &metadata.Registry{Audiobooks: []metadata.AudiobookProvider{
		stubAudiobookProvider{fn: func(_ context.Context, asin, region string) (*metadata.Audiobook, error) {
			gotRegion = region
			return &metadata.Audiobook{IDs: metadata.ExternalIDs{metadata.KeyASIN: asin}, Title: "The Hobbit"}, nil
		}},
	}}

	_, err := reg.Lookup(context.Background(), commonv1.MediaKindAudiobook,
		metadata.ExternalIDs{metadata.KeyASIN: "B0863H3FYS", "region": "uk"})
	require.NoError(t, err)
	require.Equal(t, "uk", gotRegion)

	_, err = reg.Lookup(context.Background(), commonv1.MediaKindAudiobook,
		metadata.ExternalIDs{metadata.KeyASIN: "B0863H3FYS"})
	require.NoError(t, err)
	require.Equal(t, "us", gotRegion, "an absent region must default to us, matching every caller that predates this field")
}

type stubAudiobookProvider struct {
	fn func(ctx context.Context, asin, region string) (*metadata.Audiobook, error)
}

func (p stubAudiobookProvider) Name() string                        { return "audnexus" }
func (p stubAudiobookProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }

func (p stubAudiobookProvider) Audiobook(ctx context.Context, asin, region string) (*metadata.Audiobook, error) {
	return p.fn(ctx, asin, region)
}

func (p stubAudiobookProvider) Chapters(context.Context, string, string) ([]metadata.Chapter, error) {
	return nil, nil
}
