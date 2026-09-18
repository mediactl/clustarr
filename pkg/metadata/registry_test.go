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
