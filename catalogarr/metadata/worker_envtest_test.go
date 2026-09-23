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
	"github.com/mediactl/clustarr/catalogarr/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

// newTestClient mirrors pkg/k8s/patch_envtest_test.go's helper: an
// apiserver with the real CRDs, skipped when KUBEBUILDER_ASSETS is unset.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
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
// calls Envelope(). A later step (envtest via membus directly) exercises
// the real Ack/Nak/Term/InProgress wiring; this one isolates the fetch +
// patch behaviour from the bus.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope               { return m.env }
func (m testMessage) Subject() string                          { return "" }
func (m testMessage) Attempt() uint64                          { return 1 }
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

func TestHandlerFetchesFromTheProviderAndPatchesOnlyStatusMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hworker", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/movie/27205", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	h := &metadata.Handler{
		Client:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}},
		Cache:    noopCache{},
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
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Inception", got.Status.Metadata.Title)
	require.EqualValues(t, 148, got.Status.Metadata.RuntimeMinutes)
	require.Equal(t, "tt1375666", got.Status.Metadata.ExternalIDs["imdb"])
	require.Empty(t, got.Status.Phase, "the worker must never set phase; that is the movie controller's field")
	require.Empty(t, got.Status.Conditions, "the worker must never set conditions")
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

func TestHandlerSkipsTheProviderOnACacheHit(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hcache", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	h := &metadata.Handler{
		Client:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{failIfCalledMovieProvider{t: t}}},
		Cache:    &fakeCache{movie: &pkgmetadata.Movie{Title: "Inception (cached)", Runtime: 148}},
	}

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, "Inception (cached)", got.Status.Metadata.Title)
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
	h := &metadata.Handler{Client: c, Registry: &pkgmetadata.Registry{}, Cache: noopCache{}}

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
// counterpart, proving G2-1's new dispatch end to end: newTarget ->
// externalIDs -> Registry.Lookup -> buildArtistMetadataAC ->
// k8s.ManagerCatalogarrMetadata.
func TestHandlerFetchesArtistAndPatchesOnlyStatusMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name, mbid = "hartist", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	newArtist(t, ctx, c, ns, name, mbid)

	h := &metadata.Handler{
		Client: c,
		Registry: &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{stubArtistOnlyProvider{
			artist: &pkgmetadata.Artist{
				IDs:    pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: mbid},
				Name:   "Radiohead",
				Type:   "Group",
				Genres: []string{"Alternative Rock"},
				Images: []pkgmetadata.Image{{Type: pkgmetadata.ImageTypePoster, URL: "https://example.test/radiohead.jpg"}},
			},
		}}},
		Cache: noopCache{},
	}

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Radiohead", got.Status.Metadata.Name)
	require.Equal(t, "Group", got.Status.Metadata.Type)
	require.Equal(t, []string{"Alternative Rock"}, got.Status.Metadata.Genres)
	require.Equal(t, mbid, got.Status.Metadata.ExternalIDs["mb-artist"])
	require.Empty(t, got.Status.Conditions, "the worker must never set conditions; that is the artist controller's field")
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
// releases the rest), applied to the specific question this task had to
// answer for Album and Book: patch.go's buildAlbumMetadataAC doc comment
// decides the metadata gateway (ManagerCatalogarrMetadata) is the sole
// writer of every leaf of AlbumStatus.metadata, and G2-2's Artist fan-out
// (ManagerCatalogarrFanout) must never apply to it. This proves both
// halves against a real apiserver:
//  1. A full fetch sets every leaf this builder maps.
//  2. A second fetch, from a provider response with almost every field now
//     empty, is a complete rebuild (buildAlbumMetadataAC is called in
//     full both times, never a partial early return) -- so every omitted
//     leaf is deliberately cleared, not accidentally retained, and the one
//     leaf still present (Title) survives.
//  3. Across both applies, status.metadata's managedFields entries show
//     ONLY k8s.ManagerCatalogarrMetadata -- proving nothing else (in
//     particular no future ManagerCatalogarrFanout write from G2-2) has
//     claimed a leaf there.
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
	h := &metadata.Handler{Client: c, Registry: &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{full}}, Cache: noopCache{}}

	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: name}}
	var err error
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Album
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "OK Computer", got.Status.Metadata.Title)
	require.Equal(t, "1997", got.Status.Metadata.Disambiguation)
	require.Equal(t, "Album", got.Status.Metadata.AlbumType)
	require.Equal(t, []string{"Live"}, got.Status.Metadata.SecondaryTypes)
	require.NotNil(t, got.Status.Metadata.ReleaseDate)
	require.Len(t, got.Status.Metadata.Images, 1)

	// Second fetch: the provider now answers with only a Title (e.g. a
	// stale/degraded upstream response). noopCache (not fakeCache) ensures
	// this really calls the provider again rather than replaying the cache.
	partial := stubArtistOnlyProvider{album: &pkgmetadata.Album{
		IDs:   pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: rgid},
		Title: "OK Computer",
	}}
	h.Registry = &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{partial}}
	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
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
	require.Equal(t, map[string]bool{string(k8s.ManagerCatalogarrMetadata): true}, managers,
		"only the metadata gateway may own a status field on Album -- ManagerCatalogarrFanout must never apply to status.metadata here (see buildAlbumMetadataAC's doc comment)")
}

// TestHandlerWritesSecondaryYearThroughTheRealTMDBClient drives ruling R-7
// end to end against a real apiserver: TMDB's release_dates carry a
// premiere a year before the primary release, the client derives
// SecondaryYear, and buildMovieMetadataAC applies it into
// status.metadata.secondaryYear, which the CRD admits.
func TestHandlerWritesSecondaryYearThroughTheRealTMDBClient(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hsecondary", "festival-premiere"
	newMovie(t, ctx, c, ns, name, 900001)

	body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_premiere_prior_year.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	h := &metadata.Handler{Client: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}}, Cache: noopCache{}}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}})
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.EqualValues(t, 2021, got.Status.Metadata.Year)
	require.EqualValues(t, 2020, got.Status.Metadata.SecondaryYear)
}

// TestHandlerWritesEveryImageType proves the nine pkg/metadata image roles
// mapImageType now passes through are all admitted by the CRD's widened
// ImageType enum -- one value outside it would reject the whole status
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

	h := &metadata.Handler{
		Client: c,
		Registry: &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{stubArtistOnlyProvider{
			artist: &pkgmetadata.Artist{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: mbid}, Name: "Radiohead", Images: images},
		}}},
		Cache: noopCache{},
	}
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: name}})
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
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
