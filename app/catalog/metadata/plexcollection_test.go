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

package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

const (
	bttfPlexID       = "5d7768244de0ee001fcc7fed"
	bttfCollectionID = "5ec2eb574592b6004137f444"
)

type fakePlexCollections struct {
	fakeExtras
	collection string
	err        error
	asked      []string
}

func (f *fakePlexCollections) MovieCollection(_ context.Context, moviePlexID string) (string, error) {
	f.asked = append(f.asked, moviePlexID)
	return f.collection, f.err
}

type fakeCollections struct {
	overview string
	err      error
	calls    int
}

func (f *fakeCollections) Name() string                           { return "tmdb" }
func (f *fakeCollections) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }

func (f *fakeCollections) Collection(_ context.Context, id string) (*pkgmetadata.Collection, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &pkgmetadata.Collection{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: id}, Overview: f.overview}, nil
}

// fakeCollectionMovies is a MovieProvider that is also a CollectionProvider,
// as the tmdb client is.
type fakeCollectionMovies struct {
	pkgmetadata.MovieProvider
	*fakeCollections
}

func (f fakeCollectionMovies) Name() string { return "tmdb" }

func (f fakeCollectionMovies) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func TestPlexCollectionIsReadFromTheMoviesPlexID(t *testing.T) {
	p := &fakePlexCollections{collection: bttfCollectionID}
	reg := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{p}}
	require.Equal(t, bttfCollectionID, plexCollection(context.Background(), reg, bttfPlexID, ""))
	require.Equal(t, []string{bttfPlexID}, p.asked)
}

// A failed lookup keeps what an earlier refresh found: a blip never
// releases the collection's plex:// GUID.
func TestPlexCollectionKeepsThePriorIDOnFailure(t *testing.T) {
	p := &fakePlexCollections{err: errors.New("plex.tv: 503")}
	reg := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{p}}
	require.Equal(t, bttfCollectionID, plexCollection(context.Background(), reg, bttfPlexID, bttfCollectionID))
}

// A movie Plex files in no collection answers none, even over a prior id:
// that is Plex's answer, not a failure.
func TestPlexCollectionOfAMovieInNoneIsEmpty(t *testing.T) {
	p := &fakePlexCollections{}
	reg := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{p}}
	require.Empty(t, plexCollection(context.Background(), reg, bttfPlexID, bttfCollectionID))
}

// A movie with no Plex id is not looked up, and keeps its prior id.
func TestPlexCollectionNeedsTheMoviesPlexID(t *testing.T) {
	p := &fakePlexCollections{collection: bttfCollectionID}
	reg := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{p}}
	require.Equal(t, "prior", plexCollection(context.Background(), reg, "", "prior"))
	require.Empty(t, p.asked)
}

func TestCollectionSummaryIsFetchedOnce(t *testing.T) {
	f := &fakeCollections{overview: "Marty McFly travels in time."}
	reg := &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{fakeCollectionMovies{fakeCollections: f}}}
	c := &pkgmetadata.Collection{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "264"}}
	withCollectionSummary(context.Background(), reg, c)
	require.Equal(t, "Marty McFly travels in time.", c.Overview)

	withCollectionSummary(context.Background(), reg, c)
	require.Equal(t, 1, f.calls, "a collection that has its summary is not fetched again")
}

// A failed fetch leaves the collection as it was: the summary is
// decoration, never a reason to fail a refresh.
func TestCollectionSummaryFailureIsNotFatal(t *testing.T) {
	f := &fakeCollections{err: errors.New("tmdb: 500")}
	reg := &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{fakeCollectionMovies{fakeCollections: f}}}
	c := &pkgmetadata.Collection{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "264"}, Title: "Back to the Future Collection"}
	withCollectionSummary(context.Background(), reg, c)
	require.Empty(t, c.Overview)
	require.Equal(t, "Back to the Future Collection", c.Title)
}

func TestKnownPlexCollectionIsOnlyForTheSameCollection(t *testing.T) {
	m := &catalogv1alpha1.Movie{}
	m.Status.Metadata = &catalogv1alpha1.MovieMetadata{Collection: &catalogv1alpha1.CollectionRef{TmdbID: 264, PlexID: bttfCollectionID}}
	require.Equal(t, bttfCollectionID, knownPlexCollection(m, &pkgmetadata.Collection{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "264"}}))
	require.Empty(t, knownPlexCollection(m, &pkgmetadata.Collection{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "87096"}}))
	require.Empty(t, knownPlexCollection(&catalogv1alpha1.Movie{}, &pkgmetadata.Collection{}))
}
