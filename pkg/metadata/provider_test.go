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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
)

type fakeMovieProvider struct{}

func (fakeMovieProvider) Name() string { return "fake" }
func (fakeMovieProvider) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTMDB}, Search: true}
}

func (fakeMovieProvider) Movie(context.Context, string, string) (*metadata.Movie, error) {
	return nil, metadata.ErrNotFound
}

func (fakeMovieProvider) FindMovie(context.Context, metadata.ExternalIDs) (*metadata.Movie, error) {
	return nil, metadata.ErrNotFound
}

func (fakeMovieProvider) SearchMovies(context.Context, string, int) ([]metadata.MovieHit, error) {
	return nil, nil
}

var _ metadata.MovieProvider = fakeMovieProvider{}

type fakeSeriesProvider struct{}

func (fakeSeriesProvider) Name() string                        { return "fake" }
func (fakeSeriesProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (fakeSeriesProvider) Series(context.Context, string) (*metadata.Series, error) {
	return nil, metadata.ErrNotFound
}

func (fakeSeriesProvider) Episodes(context.Context, string, string) ([]metadata.Episode, error) {
	return nil, nil
}

func (fakeSeriesProvider) Updates(context.Context, time.Time) ([]string, error) {
	return nil, nil
}

var _ metadata.SeriesProvider = fakeSeriesProvider{}

type fakeArtistProvider struct{}

func (fakeArtistProvider) Name() string                        { return "fake" }
func (fakeArtistProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (fakeArtistProvider) SearchArtists(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, nil
}

func (fakeArtistProvider) Artist(context.Context, string) (*metadata.Artist, error) {
	return nil, metadata.ErrNotFound
}

func (fakeArtistProvider) Albums(context.Context, string) ([]metadata.Album, error) {
	return nil, nil
}

func (fakeArtistProvider) Album(context.Context, string) (*metadata.Album, error) {
	return nil, metadata.ErrNotFound
}

var _ metadata.ArtistProvider = fakeArtistProvider{}

type fakeBookProvider struct{}

func (fakeBookProvider) Name() string                        { return "fake" }
func (fakeBookProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (fakeBookProvider) SearchBooks(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, nil
}

func (fakeBookProvider) Author(context.Context, metadata.ExternalIDs) (*metadata.Author, error) {
	return nil, metadata.ErrNotFound
}

func (fakeBookProvider) Books(context.Context, string) ([]metadata.Book, error) {
	return nil, nil
}

func (fakeBookProvider) Book(context.Context, metadata.ExternalIDs) (*metadata.Book, error) {
	return nil, metadata.ErrNotFound
}

func (fakeBookProvider) Edition(context.Context, metadata.ExternalIDs) (*metadata.Edition, error) {
	return nil, metadata.ErrNotFound
}

var _ metadata.BookProvider = fakeBookProvider{}

type fakeAudiobookProvider struct{}

func (fakeAudiobookProvider) Name() string                        { return "fake" }
func (fakeAudiobookProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (fakeAudiobookProvider) Audiobook(context.Context, string, string) (*metadata.Audiobook, error) {
	return nil, metadata.ErrNotFound
}

func (fakeAudiobookProvider) Chapters(context.Context, string, string) ([]metadata.Chapter, error) {
	return nil, nil
}

var _ metadata.AudiobookProvider = fakeAudiobookProvider{}

type fakeComicProvider struct{}

func (fakeComicProvider) Name() string                        { return "fake" }
func (fakeComicProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (fakeComicProvider) SearchVolumes(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, nil
}

func (fakeComicProvider) Volume(context.Context, metadata.ExternalIDs) (*metadata.ComicVolume, error) {
	return nil, metadata.ErrNotFound
}

func (fakeComicProvider) Issues(context.Context, string) ([]metadata.ComicIssue, error) {
	return nil, nil
}

var _ metadata.ComicProvider = fakeComicProvider{}

func TestRateLimitedErrorUnwrapsToErrRateLimited(t *testing.T) {
	err := &metadata.RateLimitedError{Provider: "tmdb", RetryAfter: 2 * time.Second}

	require.ErrorIs(t, err, metadata.ErrRateLimited)
	require.Contains(t, err.Error(), "tmdb")
	require.Contains(t, err.Error(), "2s")
}
