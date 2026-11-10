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
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	plexclient "github.com/mediactl/clustarr/pkg/metadata/clients/plex"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
)

// newTestClient mirrors pkg/k8s/patch_envtest_test.go's helper: an
// apiserver with the real CRDs, skipped when KUBEBUILDER_ASSETS is unset.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string, tmdbID int64) *catalogv1alpha1.Movie {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: tmdbID, QualityProfileRef: "hd-1080p", RootFolderRef: "movies",
		},
	}
	require.NoError(t, c.Create(ctx, m))
	return m
}

type noopCache struct{}

func (noopCache) Get(context.Context, string, any) (bool, error)        { return false, nil }
func (noopCache) Set(context.Context, string, any, time.Duration) error { return nil }

// testMessage is the minimal events.Message this test needs: Handle only
// calls Envelope() and, on a long Series refresh, InProgress(). A later
// step (envtest via membus directly) exercises the real Ack/Nak/Term/
// InProgress wiring; this one isolates the fetch + record behaviour from
// the bus.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope               { return m.env }
func (m testMessage) Subject() string                          { return "" }
func (m testMessage) Attempt() uint64                          { return 1 }
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

// TestHandlerFetchesFromTheProviderAndPatchesOnlyStatusMetadata: the
// Handler fetches through a real TMDB client and answers the Movie's whole
// status.metadata block -- and only that block -- in its item-metadata
// record. Since 01a223a4 (ADR-0019 §7.1, A5.1, 2026-10-07) it patches
// nothing itself: the Movie on the apiserver is untouched after the pass,
// and the manager's metadata stage incorporates the block under
// catalog-metadata (recordHarness.incorporate), after which the leaves are
// the Movie's status.metadata and phase and conditions are still nobody's.
func TestHandlerFetchesFromTheProviderAndPatchesOnlyStatusMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hworker", "inception"
	created := newMovie(t, ctx, c, ns, name, 27205)

	body, err := os.ReadFile("../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/movie/27205", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	rh := newRecordHarness(t)
	h := &metadata.Handler{
		Client:   c,
		Reader:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}},
		Cache:    noopCache{},
		Records:  rh.records,
	}

	env := &events.Envelope{
		Key:    ns + "/" + name,
		Schema: schema.MetadataTask{}.Schema(),
	}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, created.ResourceVersion, got.ResourceVersion, "the gateway writes no Kubernetes object (ADR-0019)")
	require.Nil(t, got.Status.Metadata, "status.metadata is the manager's to write, from the record")

	rec := rh.answered(t, &got)
	require.Equal(t, metadatainputs.For(&got), rec.Inputs, "the record answers the Movie's own ask, which is what the manager incorporates")
	var md catalogv1alpha1.MovieMetadata
	require.NoError(t, json.Unmarshal(rec.Metadata, &md))
	require.Equal(t, "Inception", md.Title)
	require.EqualValues(t, 148, md.RuntimeMinutes)
	require.Equal(t, "tt1375666", md.ExternalIDs["imdb"])
	require.Equal(t, []string{"A Origem", "Origen", "Inception: Le Origini", "盗梦空间", "Начало"}, md.AlternateTitles,
		"TMDB's alternative titles reach status.metadata.alternateTitles, the movie's own title and repeats dropped")
	require.True(t, md.RefreshedAt.Time.Equal(rec.RefreshedAt), "the record's refreshedAt and the block's are one instant")

	incorporate(t, ctx, c, &got, rec)
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Inception", got.Status.Metadata.Title)
	require.EqualValues(t, 148, got.Status.Metadata.RuntimeMinutes)
	require.Empty(t, got.Status.Phase, "the metadata set never sets phase; that is the movie's own catalog apply")
	require.Empty(t, got.Status.Conditions, "the metadata set never sets conditions")
}

type failIfCalledMovieProvider struct{ t *testing.T }

func (p failIfCalledMovieProvider) Name() string { return "fail-if-called" }
func (p failIfCalledMovieProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p failIfCalledMovieProvider) Movie(context.Context, string, string) (*pkgmetadata.Movie, error) {
	p.t.Fatal("Movie called despite a cache hit")
	return nil, nil
}

func (p failIfCalledMovieProvider) FindMovie(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Movie, error) {
	p.t.Fatal("FindMovie called despite a cache hit")
	return nil, nil
}

func (p failIfCalledMovieProvider) SearchMovies(context.Context, string, int) ([]pkgmetadata.MovieHit, error) {
	return nil, nil
}

type fakeCache struct{ movie *pkgmetadata.Movie }

func (c *fakeCache) Get(_ context.Context, _ string, out any) (bool, error) {
	if c.movie == nil {
		return false, nil
	}
	*out.(*pkgmetadata.Movie) = *c.movie
	return true, nil
}
func (c *fakeCache) Set(context.Context, string, any, time.Duration) error { return nil }

// TestHandlerSkipsTheProviderOnACacheHit: a cached document is rendered
// into the record without a provider call. The record, not the Movie, is
// where the title lands since 01a223a4 (ADR-0019 A5.1).
func TestHandlerSkipsTheProviderOnACacheHit(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hcache", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	rh := newRecordHarness(t)
	h := &metadata.Handler{
		Client:   c,
		Reader:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{failIfCalledMovieProvider{t: t}}},
		Cache:    &fakeCache{movie: &pkgmetadata.Movie{Title: "Inception (cached)", Runtime: 148}},
		Records:  rh.records,
	}

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	var md catalogv1alpha1.MovieMetadata
	require.NoError(t, json.Unmarshal(rh.answered(t, &got).Metadata, &md))
	require.Equal(t, "Inception (cached)", md.Title)
}

type erroringMovieProvider struct{ err error }

func (p erroringMovieProvider) Name() string { return "erroring" }
func (p erroringMovieProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p erroringMovieProvider) Movie(context.Context, string, string) (*pkgmetadata.Movie, error) {
	return nil, p.err
}

func (p erroringMovieProvider) FindMovie(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Movie, error) {
	return nil, p.err
}

func (p erroringMovieProvider) SearchMovies(context.Context, string, int) ([]pkgmetadata.MovieHit, error) {
	return nil, p.err
}

func TestHandlerMapsRateLimitedToRetry(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hratelimit", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	h := &metadata.Handler{
		Client: c,
		Reader: c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{
			erroringMovieProvider{err: &pkgmetadata.RateLimitedError{Provider: "tmdb", RetryAfter: 45 * time.Second}},
		}},
		Cache: noopCache{},
	}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	err = h.Handle(ctx, testMessage{env: env})
	var re *events.RetryError
	require.ErrorAs(t, err, &re)
	require.Equal(t, 45*time.Second, re.After)
}

func TestHandlerMapsNotFoundToDiscard(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hnotfound", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	h := &metadata.Handler{
		Client:   c,
		Reader:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{erroringMovieProvider{err: pkgmetadata.ErrNotFound}}},
		Cache:    noopCache{},
	}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	err = h.Handle(ctx, testMessage{env: env})
	var de *events.DiscardError
	require.ErrorAs(t, err, &de)
}

// TestHandlerDiscardsAnUnsupportedKind used to pin Artist as unsupported
// (M6 was not yet in scope). Task G2-1 wired Artist into this worker, so
// the case pinning "this worker rejects a kind with no status.metadata of
// its own" now uses Issue -- see target.go's errUnsupportedKind doc comment
// for why Issue (like Episode) is a deliberate, permanent exclusion rather
// than a gap this worker will ever grow a case for.
func TestHandlerDiscardsAnUnsupportedKind(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	h := &metadata.Handler{Client: c, Reader: c, Registry: &pkgmetadata.Registry{}, Cache: noopCache{}}

	env := &events.Envelope{Key: "ns/issue-1", Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "issue-1"}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	err = h.Handle(ctx, testMessage{env: env})
	var de *events.DiscardError
	require.ErrorAs(t, err, &de)
}

// stubArtistOnlyProvider is the minimal pkgmetadata.ArtistProvider this file
// needs for the two Artist/Album Handler tests below: SearchArtists is never
// called by Handle (only Registry.Lookup is), so it panics if it ever is.
type stubArtistOnlyProvider struct {
	artist *pkgmetadata.Artist
	album  *pkgmetadata.Album
	err    error
}

func (p stubArtistOnlyProvider) Name() string { return "musicbrainz" }
func (p stubArtistOnlyProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p stubArtistOnlyProvider) SearchArtists(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	panic("SearchArtists must not be called by the metadata worker")
}

func (p stubArtistOnlyProvider) Artist(context.Context, string) (*pkgmetadata.Artist, error) {
	return p.artist, p.err
}

func (p stubArtistOnlyProvider) Albums(context.Context, string) ([]pkgmetadata.Album, error) {
	panic("Albums must not be called by the metadata worker")
}

func (p stubArtistOnlyProvider) Album(context.Context, string) (*pkgmetadata.Album, error) {
	return p.album, p.err
}

func newArtist(t *testing.T, ctx context.Context, c client.Client, ns, name, mbid string) *catalogv1alpha1.Artist {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: mbid, QualityProfileRef: "lossless", RootFolderRef: "music",
		},
	}
	require.NoError(t, c.Create(ctx, a))
	return a
}

// TestHandlerFetchesArtistAndPatchesOnlyStatusMetadata is
// TestHandlerFetchesFromTheProviderAndPatchesOnlyStatusMetadata's Artist
// counterpart, proving G2-1's dispatch end to end: newTarget ->
// externalIDs -> Registry.Lookup -> buildArtistMetadataAC -> the
// item-metadata record (since 01a223a4, ADR-0019 A5.1; the apply under
// k8s.ManagerCatalogMetadata is the manager's, from the record), the Artist
// itself untouched by the gateway.
func TestHandlerFetchesArtistAndPatchesOnlyStatusMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name, mbid = "hartist", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	created := newArtist(t, ctx, c, ns, name, mbid)

	rh := newRecordHarness(t)
	h := &metadata.Handler{
		Client: c,
		Reader: c,
		Registry: &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{stubArtistOnlyProvider{
			artist: &pkgmetadata.Artist{
				IDs:    pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: mbid},
				Name:   "Radiohead",
				Type:   "Group",
				Genres: []string{"Alternative Rock"},
				Images: []pkgmetadata.Image{{Type: pkgmetadata.ImageTypePoster, URL: "https://example.test/radiohead.jpg"}},
			},
		}}},
		Cache:   noopCache{},
		Records: rh.records,
	}

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, created.ResourceVersion, got.ResourceVersion, "the gateway writes no Kubernetes object (ADR-0019)")
	require.Nil(t, got.Status.Metadata)
	var md catalogv1alpha1.ArtistMetadata
	require.NoError(t, json.Unmarshal(rh.answered(t, &got).Metadata, &md))
	require.Equal(t, "Radiohead", md.Name)
	require.Equal(t, "Group", md.Type)
	require.Equal(t, []string{"Alternative Rock"}, md.Genres)
	require.Equal(t, mbid, md.ExternalIDs["mb-artist"])
	require.Equal(t, metadata.DefaultFetchLanguage, md.Language, "no Languages table: en (2026-11-10 design §5.4)")
}

func newAlbum(t *testing.T, ctx context.Context, c client.Client, ns, name, artistRef, releaseGroupID string) *catalogv1alpha1.Album {
	t.Helper()
	a := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artistRef, ReleaseGroupID: releaseGroupID},
	}
	require.NoError(t, c.Create(ctx, a))
	return a
}

// TestHandlerAlbumMetadataStaysSolelyOwnedByTheGatewayAcrossReapplies is the
// SSA-per-leaf proof this task's brief calls for (CLAUDE.md's Phase D1 caps
// lesson: a renderer that sends only some of a struct's leaves silently
// releases the rest), applied to the question this task had to answer for
// Album and Book: patch.go's buildAlbumMetadataAC doc comment decides
// catalog-metadata is the sole writer of every leaf of
// AlbumStatus.metadata. Since 01a223a4 (ADR-0019 A5.1) the gateway renders
// the block into its record and the manager's metadata stage applies it
// (recordHarness.incorporate, the loop's metadataset.Build and
// itemstatus.ApplySet), so the proof runs both halves against a real
// apiserver:
//  1. A full fetch's record, incorporated, sets every leaf this builder maps.
//  2. A second fetch's record, from a provider response with almost every
//     field now empty, is a complete rebuild (buildAlbumMetadataAC is called
//     in full both times, never a partial early return) -- so once
//     incorporated every omitted leaf is deliberately cleared, not
//     accidentally retained, and the one leaf still present (Title) survives.
//  3. Across both applies, status's managedFields entries show ONLY
//     k8s.ManagerCatalogMetadata -- proving nothing else (the Artist's
//     fan-out, since 2026-11-10 the loop's childsync, included) has claimed
//     a leaf there.
func TestHandlerAlbumMetadataStaysSolelyOwnedByTheGatewayAcrossReapplies(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name, rgid = "halbum", "radiohead-ok-computer", "b1392450-e666-3926-9ce9-9b7f7b62f699"
	newArtist(t, ctx, c, ns, "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711")
	newAlbum(t, ctx, c, ns, name, "radiohead", rgid)

	fullDate := time.Date(1997, 5, 21, 0, 0, 0, 0, time.UTC)
	full := stubArtistOnlyProvider{album: &pkgmetadata.Album{
		IDs:            pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: rgid},
		Title:          "OK Computer",
		Disambiguation: "1997",
		PrimaryType:    "Album",
		SecondaryTypes: []string{"Live"},
		ReleaseDate:    &fullDate,
		Images:         []pkgmetadata.Image{{Type: pkgmetadata.ImageTypePoster, URL: "https://example.test/okc.jpg"}},
	}}
	rh := newRecordHarness(t)
	clock := newStepClock()
	h := &metadata.Handler{Client: c, Reader: c, Registry: &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{full}}, Cache: noopCache{}, Records: rh.records, Now: clock.Now}

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Album
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	incorporate(t, ctx, c, &got, rh.answered(t, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "OK Computer", got.Status.Metadata.Title)
	require.Equal(t, "1997", got.Status.Metadata.Disambiguation)
	require.Equal(t, "Album", got.Status.Metadata.AlbumType)
	require.Equal(t, []string{"Live"}, got.Status.Metadata.SecondaryTypes)
	require.NotNil(t, got.Status.Metadata.ReleaseDate)
	require.Len(t, got.Status.Metadata.Images, 1)

	// Second fetch: the provider now answers with only a Title (e.g. a
	// stale/degraded upstream response). noopCache (not fakeCache) ensures
	// this really calls the provider again rather than replaying the cache,
	// and the clock steps so the second record is newer than the first's
	// document on the Album.
	partial := stubArtistOnlyProvider{album: &pkgmetadata.Album{
		IDs:   pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: rgid},
		Title: "OK Computer",
	}}
	h.Registry = &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{partial}}
	clock.Step()
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))
	incorporate(t, ctx, c, &got, rh.answered(t, &got))

	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "OK Computer", got.Status.Metadata.Title, "the one leaf still present must survive")
	require.Empty(t, got.Status.Metadata.Disambiguation, "an omitted leaf must be deliberately cleared, not retained")
	require.Empty(t, got.Status.Metadata.AlbumType, "an omitted leaf must be deliberately cleared, not retained")
	require.Empty(t, got.Status.Metadata.SecondaryTypes, "an omitted leaf must be deliberately cleared, not retained")
	require.Nil(t, got.Status.Metadata.ReleaseDate, "an omitted leaf must be deliberately cleared, not retained")
	require.Empty(t, got.Status.Metadata.Images, "an omitted leaf must be deliberately cleared, not retained")

	managers := map[string]bool{}
	for _, e := range got.ManagedFields {
		if e.Subresource == "status" {
			managers[e.Manager] = true
		}
	}
	require.Equal(t, map[string]bool{string(k8s.ManagerCatalogMetadata): true}, managers,
		"only the metadata set may own a status field on Album -- no other manager applies to status.metadata (see buildAlbumMetadataAC's doc comment)")
}

// TestHandlerWritesSecondaryYearThroughTheRealTMDBClient drives ruling R-7
// end to end against a real apiserver: TMDB's release_dates carry a
// premiere a year before the primary release, the client derives
// SecondaryYear, buildMovieMetadataAC renders it into the record's block
// (since 01a223a4, ADR-0019 A5.1) and the manager's incorporation lands it
// in status.metadata.secondaryYear, which the CRD admits.
func TestHandlerWritesSecondaryYearThroughTheRealTMDBClient(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hsecondary", "festival-premiere"
	newMovie(t, ctx, c, ns, name, 900001)

	body, err := os.ReadFile("../../../test/data/metadata/tmdb/movie_premiere_prior_year.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	rh := newRecordHarness(t)
	h := &metadata.Handler{Client: c, Reader: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}}, Cache: noopCache{}, Records: rh.records}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}})
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	incorporate(t, ctx, c, &got, rh.answered(t, &got))
	require.NotNil(t, got.Status.Metadata)
	require.EqualValues(t, 2021, got.Status.Metadata.Year)
	require.EqualValues(t, 2020, got.Status.Metadata.SecondaryYear)
}

// TestHandlerWritesEveryImageType proves the nine pkg/metadata image roles
// mapImageType passes through are all admitted by the CRD's widened
// ImageType enum once the manager incorporates the record (since 01a223a4,
// ADR-0019 A5.1) -- one value outside it would reject the whole status
// apply, which is why mapImageType still drops anything unknown.
func TestHandlerWritesEveryImageType(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name, mbid = "himages", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	newArtist(t, ctx, c, ns, name, mbid)

	roles := []pkgmetadata.ImageType{
		pkgmetadata.ImageTypePoster, pkgmetadata.ImageTypeFanart, pkgmetadata.ImageTypeBanner,
		pkgmetadata.ImageTypeLogo, pkgmetadata.ImageTypeClearart, pkgmetadata.ImageTypeThumb,
		pkgmetadata.ImageTypeScreenshot, pkgmetadata.ImageTypeDisc, pkgmetadata.ImageTypeHeadshot,
	}
	images := make([]pkgmetadata.Image, 0, len(roles)+1)
	for _, r := range roles {
		images = append(images, pkgmetadata.Image{Type: r, URL: "https://example.test/" + string(r) + ".jpg"})
	}
	images = append(images, pkgmetadata.Image{Type: "", URL: "https://example.test/unclassified.jpg"})

	rh := newRecordHarness(t)
	h := &metadata.Handler{
		Client: c,
		Reader: c,
		Registry: &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{stubArtistOnlyProvider{
			artist: &pkgmetadata.Artist{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: mbid}, Name: "Radiohead", Images: images},
		}}},
		Cache:   noopCache{},
		Records: rh.records,
	}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: name}})
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	incorporate(t, ctx, c, &got, rh.answered(t, &got))
	require.NotNil(t, got.Status.Metadata)
	var gotTypes []catalogv1alpha1.ImageType
	for _, img := range got.Status.Metadata.Images {
		gotTypes = append(gotTypes, img.Type)
	}
	want := make([]catalogv1alpha1.ImageType, 0, len(roles))
	for _, r := range roles {
		want = append(want, catalogv1alpha1.ImageType(r))
	}
	require.Equal(t, want, gotTypes, "all nine roles written; the unclassified image dropped")
}

// imagedMovieProvider answers every lookup with one movie that carries a
// poster, and counts the calls.
type imagedMovieProvider struct{ calls int }

func (p *imagedMovieProvider) Name() string { return "imaged" }
func (p *imagedMovieProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p *imagedMovieProvider) Movie(context.Context, string, string) (*pkgmetadata.Movie, error) {
	p.calls++
	return &pkgmetadata.Movie{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205"}, Title: "Inception (fresh)", Runtime: 148,
		Images: []pkgmetadata.Image{{Type: pkgmetadata.ImageTypePoster, URL: "https://image.tmdb.org/t/p/w500/inception.jpg"}},
	}, nil
}

func (p *imagedMovieProvider) FindMovie(ctx context.Context, _ pkgmetadata.ExternalIDs) (*pkgmetadata.Movie, error) {
	return p.Movie(ctx, "", "")
}

func (p *imagedMovieProvider) SearchMovies(context.Context, string, int) ([]pkgmetadata.MovieHit, error) {
	return nil, nil
}

// A forced refresh -- a MetadataTask with a RefreshEpoch (spec §5) -- goes
// to the provider even when the cache holds the document, and answers what
// it fetched in the record (since 01a223a4, ADR-0019 A5.1); that is what
// lets an operator pick up fields a provider now publishes (the artwork the
// clients did not map before) without waiting out RefreshTTL. A task with
// no epoch keeps taking the cache hit.
func TestHandlerBypassesTheCacheOnAForcedRefresh(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hforce", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	provider := &imagedMovieProvider{}
	stale := &fakeCache{movie: &pkgmetadata.Movie{Title: "Inception (cached)", Runtime: 148}}
	rh := newRecordHarness(t)
	h := &metadata.Handler{
		Client:   c,
		Reader:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{provider}},
		Cache:    stale,
		Records:  rh.records,
	}
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	handle := func(epoch int64) catalogv1alpha1.MovieMetadata {
		t.Helper()
		env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
		task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, RefreshEpoch: epoch}
		var err error
		_, env.Data, err = schema.Encode(task)
		require.NoError(t, err)
		require.NoError(t, h.Handle(ctx, testMessage{env: env}))
		var md catalogv1alpha1.MovieMetadata
		require.NoError(t, json.Unmarshal(rh.answered(t, &got).Metadata, &md))
		return md
	}

	md := handle(0)
	require.Equal(t, "Inception (cached)", md.Title, "no epoch: the cache hit stands")
	require.Zero(t, provider.calls)

	md = handle(1758665000)
	require.Equal(t, 1, provider.calls, "an epoch reaches the provider despite the cache hit")
	require.Equal(t, "Inception (fresh)", md.Title)
	require.Len(t, md.Images, 1)
	require.Equal(t, catalogv1alpha1.ImageTypePoster, md.Images[0].Type)
	require.Equal(t, "https://image.tmdb.org/t/p/w500/inception.jpg", md.Images[0].URL)
}

// TestHandlerRecordsWeekendsFullMetadata runs the recorded TMDB record for
// Weekend (2011) through the real client and the gateway, against a Movie
// that already has status.metadata (a first record incorporated, since
// 01a223a4 the manager's half of a refresh, ADR-0019 A5.1): the new fields
// arrive and the earlier ones stay -- the certification most of all, which
// no Movie ever had.
func TestHandlerRecordsWeekendsFullMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hfull", "weekend"
	newMovie(t, ctx, c, ns, name, 79120)

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	rh := newRecordHarness(t)
	clock := newStepClock()
	seed := &metadata.Handler{
		Client: c, Reader: c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{failIfCalledMovieProvider{t: t}}},
		Cache:    &fakeCache{movie: &pkgmetadata.Movie{Title: "Weekend", Genres: []string{"Drama"}}},
		Records:  rh.records, Now: clock.Now,
	}
	require.NoError(t, seed.Handle(ctx, testMessage{env: env}))
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	incorporate(t, ctx, c, &got, rh.answered(t, &got))
	require.Equal(t, []string{"Drama"}, got.Status.Metadata.Genres, "the seed is on the Movie")

	body, err := os.ReadFile("../../../test/data/metadata/tmdb/movie-79120.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)
	h := &metadata.Handler{Client: c, Reader: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}}, Cache: noopCache{}, Records: rh.records, Now: clock.Now}
	clock.Step()
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))
	incorporate(t, ctx, c, &got, rh.answered(t, &got))

	md := got.Status.Metadata
	require.NotNil(t, md)
	require.Equal(t, "Weekend", md.Title)
	require.Equal(t, []string{"Drama", "Romance"}, md.Genres)
	require.Equal(t, "18", md.Certification)
	require.Contains(t, md.Certifications, catalogv1alpha1.Certification{Country: "GB", Rating: "18"})
	require.Equal(t, "A (sort of) love story between two guys over a cold weekend in October.", md.Tagline)
	require.Equal(t, []string{"United Kingdom"}, md.Countries)
	require.NotEmpty(t, md.Studios)
}

// TestHandlerWritesTheExtendedDocument: after a movie refresh the people
// and similar titles land in the clustarr-metadata-extended document under
// the Movie's UID, never in its status; the refresh itself is answered in
// the item-metadata record (since 01a223a4, ADR-0019 A5.1), on the same bus.
func TestHandlerWritesTheExtendedDocument(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hext", "weekend"
	newMovie(t, ctx, c, ns, name, 79120)
	body, err := os.ReadFile("../../../test/data/metadata/tmdb/movie-79120.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	rh := newRecordHarness(t)
	kv := rh.bus.KV(events.BucketMetadataExtended)
	h := &metadata.Handler{
		Client: c, Reader: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}},
		Cache: noopCache{}, Extended: kv, Records: rh.records,
	}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	var md catalogv1alpha1.MovieMetadata
	require.NoError(t, json.Unmarshal(rh.answered(t, &got).Metadata, &md))
	require.Equal(t, "Weekend", md.Title)
	e, err := kv.Get(ctx, extended.Key(commonv1.MediaKindMovie, got.UID))
	require.NoError(t, err)
	doc, err := extended.Decode(e.Value)
	require.NoError(t, err)
	require.Len(t, doc.Role, 16)
	require.Equal(t, "Andrew Haigh", doc.Director[0].Name)
	require.Len(t, doc.Similar, 20)
}

// TestHandlerLearnsTheMoviesCollection: a movie refresh records the TMDB
// collection's Plex id -- which Plex only names in the movie's own Plex
// metadata -- in the record's status.metadata.collection.plexID (since
// 01a223a4 the manager applies the block, ADR-0019 A5.1), and the
// collection's summary, poster and background in the movie's extended
// document, all from recorded answers (Back to the Future, 2026-10-06).
func TestHandlerLearnsTheMoviesCollection(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hcoll", "back-to-the-future"
	newMovie(t, ctx, c, ns, name, 105)

	read := func(dir, file string) []byte {
		b, err := os.ReadFile("../../../test/data/metadata/" + dir + "/" + file)
		require.NoError(t, err)
		return b
	}
	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/105":
			_, _ = w.Write(read("tmdb", "movie-105.json"))
		case "/collection/264":
			_, _ = w.Write(read("tmdb", "collection_264.json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(tmdbSrv.Close)
	plexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/library/metadata/matches" && r.URL.Query().Get("guid") == "tmdb://105":
			_, _ = w.Write(read("plex", "matches_movie_tmdb_105.json"))
		case r.URL.Path == "/library/metadata/5d7768244de0ee001fcc7fed":
			_, _ = w.Write(read("plex", "metadata_5d7768244de0ee001fcc7fed.json"))
		default:
			_, _ = w.Write(read("plex", "matches_empty.json"))
		}
	}))
	t.Cleanup(plexSrv.Close)
	tm, err := tmdb.New("test-key", tmdbSrv.Client(), tmdbSrv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)
	px, err := plexclient.New(plexclient.Config{HTTPClient: plexSrv.Client(), BaseURL: plexSrv.URL, Token: "test-token"})
	require.NoError(t, err)

	rh := newRecordHarness(t)
	kv := rh.bus.KV(events.BucketMetadataExtended)
	h := &metadata.Handler{
		Client: c, Reader: c,
		Registry: &pkgmetadata.Registry{
			Movies:    []pkgmetadata.MovieProvider{tm},
			Resolvers: []pkgmetadata.IDResolver{px},
			Plex:      []pkgmetadata.PlexProvider{px},
		},
		Cache: noopCache{}, Extended: kv, Records: rh.records,
	}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	var md catalogv1alpha1.MovieMetadata
	require.NoError(t, json.Unmarshal(rh.answered(t, &got).Metadata, &md))
	require.Equal(t, &catalogv1alpha1.CollectionRef{
		TmdbID: 264, Name: "Back to the Future Collection", PlexID: "5ec2eb574592b6004137f444",
	}, md.Collection)

	e, err := kv.Get(ctx, extended.Key(commonv1.MediaKindMovie, got.UID))
	require.NoError(t, err)
	doc, err := extended.Decode(e.Value)
	require.NoError(t, err)
	require.NotNil(t, doc.Collection)
	require.Contains(t, doc.Collection.Summary, "Marty McFly")
	require.Equal(t, "https://image.tmdb.org/t/p/w500/5Xsu2o5IsZRuuxCEVZ9nVve21FP.jpg", doc.Collection.Poster)
	require.Equal(t, "https://image.tmdb.org/t/p/original/c9C9Pg2QctyjZHRmS0P8rZg1OTA.jpg", doc.Collection.Art)
}
