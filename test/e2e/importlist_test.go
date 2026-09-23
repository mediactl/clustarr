//go:build e2e

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

// Scenario 9 (docs/superpowers/plans/2026-09-18-remaining-work.md):
// "Import lists (M6, A1). Trakt via the device flow, Plex Discover and
// MDBList -> Movies created with the list's monitor/search sync level;
// ImportExclusion respected; a rerun adds nothing twice."
//
// # All three providers are exercised against one fixture
//
// test/fixtures/importliststub serves Trakt's device-code flow and
// watchlists, the Plex Discover watchlist and an mdblist export from one
// Service. Trakt and Plex reach it through importarr's --trakt-base-url and
// --plex-base-url (X14), which config/e2e/importarr-e2e-patch.yaml sets on
// both importarr Deployments through $CLUSTARR_TRAKT_BASE_URL and
// $CLUSTARR_PLEX_BASE_URL: the controller drives the Trakt device flow and
// the worker runs every sync, so both must name the fixture. Until X14 no
// flag reached either provider, and these two legs created their CRs and
// skipped by name.
//
// mdblist.URL is a required, fully operator-supplied spec field
// (api/catalog/v1alpha1/importlist_types.go's MdbList), so it points
// straight at importlist-stub and is fully exercisable.
//
// # One fixture item, three ImportList objects
//
// testdata/importlist/mdblist/items.json (out of this task's file scope --
// only test/fixtures/, test/e2e/, config/e2e/ and the Dockerfile are) holds
// exactly one movie row, "The Matrix" (tmdb 603, imdb tt0133093), and
// pkg/importlist/mdblist/list_test.go asserts len(items)==1 against it
// twice, so this file adds no second row rather than risk breaking that
// unit test. Three separate ImportList objects against the SAME fixture
// row therefore carry the three separate claims scenario 9 makes:
//
//   - listExcluded: an ImportExclusion for tmdb 603 exists BEFORE this list
//     ever syncs -> ExcludedCount 1, AddedCount 0, no Movie created --
//     proves "ImportExclusion respected" independent of dedupe (nothing
//     else in this file has created the Movie yet).
//   - listCreates: no exclusion -> AddedCount 1, a real Movie appears with
//     the list's own Defaults (monitored, quality/root refs).
//   - listRerun: no exclusion, created only once listCreates' Movie already
//     exists -> AddedCount 0 on ITS OWN sync, and exactly one Movie for
//     tmdb 603 exists overall -- "a rerun adds nothing twice", proven via
//     a second list hitting the same dedupe path a real resync would,
//     since MdbList's own refresh interval is clamped to a 12h floor
//     (ImportListSpec.RefreshInterval's doc comment) and so cannot be
//     observed to fire twice inside this suite's timeout.
//
// Build-tagged e2e. Per the standing instruction, this suite is written and
// has never been run against a kind cluster.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// fixtureImportListStubService is config/e2e/importlist-stub.yaml's Service
// name.
const fixtureImportListStubService = "importlist-stub"

// theMatrixTMDBID is testdata/importlist/mdblist/items.json's one movie
// row's tmdb id -- Movie's own identity field (MovieSpec.TmdbID), the
// import-list controller's natural dedupe/exclusion key for a movie kind.
const theMatrixTMDBID int64 = 603

// importListSyncTimeout bounds one ImportList's first sync: a real HTTP
// round trip to importlist-stub plus the controller's own Dedupe/
// ApplySyncLevel/create pass -- generous margin over indexerReadyTimeout's
// own reasoning for the same shape of wait.
const importListSyncTimeout = 2 * time.Minute

// newMdblistImportList creates an ImportList against importlist-stub's
// mdblist route, movies only.
func newMdblistImportList(ctx context.Context, t *testing.T, prefix, rootFolder string) *catalogv1alpha1.ImportList {
	t.Helper()
	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds: []string{"movie"},
			Mdblist: &catalogv1alpha1.MdbList{
				URL: "http://" + fixtureImportListStubService + "." + Namespace + ".svc/mdblist/list.json",
			},
			SecretRef: &corev1.LocalObjectReference{Name: mdblistFixtureSecret(ctx, t)},
			Defaults: catalogv1alpha1.ListDefaults{
				QualityProfileRef: QualityProfileName,
				RootFolderRef:     rootFolder,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, il))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), il) })
	return il
}

// mdblistFixtureSecret ensures the one apiKey Secret every ImportList in
// this file shares (importliststub's mdblist route ignores its value, per
// BuildProvider's own "mdblist requires spec.secretRef with an apiKey key"
// check -- the key must be present, not correct) and returns its name.
func mdblistFixtureSecret(ctx context.Context, t *testing.T) string {
	t.Helper()
	name := "e2e9-mdblist-credentials"
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace},
		StringData: map[string]string{"apiKey": "e2e-fixture-key"},
	}
	if err := k8sClient.Create(ctx, sec); err != nil {
		require.True(t, apierrors.IsAlreadyExists(err), "create mdblist fixture secret: %v", err)
		return name
	}
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), sec) })
	return name
}

// waitForImportListSynced polls il until status reports Synced=True (or
// Failed, which fails the test with whatever status recorded) and returns
// the live object.
func waitForImportListSynced(ctx context.Context, t *testing.T, il *catalogv1alpha1.ImportList) catalogv1alpha1.ImportList {
	t.Helper()
	var done catalogv1alpha1.ImportList
	waitFor(t, ctx, importListSyncTimeout, "ImportList "+il.Name+" Synced", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.ImportList
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(il), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		done = live
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.ImportListConditionSynced), nil
	})
	return done
}

// findMovieByTmdbID returns the Movie with spec.tmdbID == id in Namespace,
// or nil.
func findMovieByTmdbID(ctx context.Context, t *testing.T, id int64) *catalogv1alpha1.Movie {
	t.Helper()
	var list catalogv1alpha1.MovieList
	require.NoError(t, k8sClient.List(ctx, &list, client.InNamespace(Namespace)))
	for i := range list.Items {
		if list.Items[i].Spec.TmdbID == id {
			return &list.Items[i]
		}
	}
	return nil
}

// TestImportListMDBListExclusionCreationAndRerun is scenario 9's mdblist leg
// in full -- see this file's own package doc comment for why three
// ImportList objects, all against the same one fixture row.
func TestImportListMDBListExclusionCreationAndRerun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureImportListStubService)
	rf := newRootFolder(ctx, t, "e2e9-movies-rf", catalogv1alpha1.RootFolderKindMovie, "movies")

	t.Run("ImportExclusion respected", func(t *testing.T) {
		excl := &catalogv1alpha1.ImportExclusion{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e9-excl"), Namespace: Namespace},
			Spec: catalogv1alpha1.ImportExclusionSpec{
				Kind:        catalogv1alpha1.ExclusionKindMovie,
				ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: "603"},
				Title:       "The Matrix",
			},
		}
		require.NoError(t, k8sClient.Create(ctx, excl))
		cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), excl) })

		require.Nil(t, findMovieByTmdbID(ctx, t, theMatrixTMDBID),
			"a Movie for tmdb %d must not already exist before the exclusion is proven", theMatrixTMDBID)

		il := newMdblistImportList(ctx, t, "e2e9-il-excluded", rf.Name)
		live := waitForImportListSynced(ctx, t, il)

		require.EqualValues(t, 1, live.Status.ItemCount, "the fixture serves exactly one movie row")
		require.EqualValues(t, 0, live.Status.AddedCount, "the excluded item must not be added")
		require.GreaterOrEqual(t, live.Status.ExcludedCount, int32(1), "ImportExclusion must be counted")
		require.Nil(t, findMovieByTmdbID(ctx, t, theMatrixTMDBID),
			"ImportExclusion must have prevented the Movie from being created")
	})

	var created *catalogv1alpha1.Movie
	t.Run("creates the Movie with the list's defaults", func(t *testing.T) {
		il := newMdblistImportList(ctx, t, "e2e9-il-creates", rf.Name)
		live := waitForImportListSynced(ctx, t, il)

		require.EqualValues(t, 1, live.Status.ItemCount)
		require.EqualValues(t, 1, live.Status.AddedCount)

		created = findMovieByTmdbID(ctx, t, theMatrixTMDBID)
		require.NotNil(t, created, "ImportList %s reported AddedCount=1 but no Movie for tmdb %d exists", il.Name, theMatrixTMDBID)
		cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), created) })

		require.Equal(t, rf.Name, created.Spec.RootFolderRef)
		require.Equal(t, QualityProfileName, created.Spec.QualityProfileRef)
		// ListDefaults.Monitored defaults true (+kubebuilder:default=true);
		// this ImportList set no override, so the Movie must inherit it.
		require.NotNil(t, created.Spec.Monitored)
		require.True(t, *created.Spec.Monitored, "the list's own monitor sync level (Defaults.Monitored, default true) must carry onto the created Movie")
	})

	t.Run("a rerun adds nothing twice", func(t *testing.T) {
		require.NotNil(t, created, "requires the previous subtest's Movie")
		il := newMdblistImportList(ctx, t, "e2e9-il-rerun", rf.Name)
		live := waitForImportListSynced(ctx, t, il)

		require.EqualValues(t, 1, live.Status.ItemCount)
		require.EqualValues(t, 0, live.Status.AddedCount, "the Movie already exists; this sync must not create a second one")

		var all catalogv1alpha1.MovieList
		require.NoError(t, k8sClient.List(ctx, &all, client.InNamespace(Namespace)))
		count := 0
		for _, m := range all.Items {
			if m.Spec.TmdbID == theMatrixTMDBID {
				count++
			}
		}
		require.Equal(t, 1, count, "exactly one Movie for tmdb %d must exist after two lists synced the same fixture row", theMatrixTMDBID)
	})
}

// TestImportListTraktDeviceFlowCRDAccepted and
// TestImportListPlexWatchlistCRDAccepted are scenario 9's Trakt and Plex
// legs: each creates the real CR and waits for a sync that reaches
// test/fixtures/importliststub (see this file's package doc comment).
func TestImportListTraktDeviceFlowCRDAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	rf := newRootFolder(ctx, t, "e2e9-trakt-rf", catalogv1alpha1.RootFolderKindMovie, "movies")

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e9-trakt-creds"), Namespace: Namespace},
		StringData: map[string]string{"clientID": "e2e-fixture-client-id", "clientSecret": "e2e-fixture-client-secret"},
	}
	require.NoError(t, k8sClient.Create(ctx, sec))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), sec) })

	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e9-trakt"), Namespace: Namespace},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:     []string{"movie"},
			SecretRef: &corev1.LocalObjectReference{Name: sec.Name},
			Trakt:     &catalogv1alpha1.TraktList{ListType: catalogv1alpha1.TraktListTypeWatchlist, Username: "e2e-fixture-user"},
			Defaults:  catalogv1alpha1.ListDefaults{QualityProfileRef: QualityProfileName, RootFolderRef: rf.Name},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, il))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), il) })

	// The controller starts the device-code flow against the fixture, which
	// answers "authorization_pending" twice and then authorizes; only then
	// can the worker sync the watchlist, so Synced=True is the proof that
	// both the device flow and the sync reached the fixture rather than
	// api.trakt.tv. testdata/importlist/trakt/watchlist_movies.json holds
	// one movie.
	live := waitForImportListSynced(ctx, t, il)
	require.Empty(t, live.Status.LastError, "the Trakt sync reported an error")
	require.GreaterOrEqual(t, live.Status.ItemCount, int32(1),
		"the Trakt watchlist fixture holds a movie; the sync fetched nothing")
}

func TestImportListPlexWatchlistCRDAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	rf := newRootFolder(ctx, t, "e2e9-plex-rf", catalogv1alpha1.RootFolderKindMovie, "movies")

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e9-plex-creds"), Namespace: Namespace},
		StringData: map[string]string{"token": "e2e-fixture-token", "clientID": "e2e-fixture-client-id"},
	}
	require.NoError(t, k8sClient.Create(ctx, sec))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), sec) })

	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e9-plex"), Namespace: Namespace},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:     []string{"movie"},
			SecretRef: &corev1.LocalObjectReference{Name: sec.Name},
			Plex:      &catalogv1alpha1.PlexWatchlist{},
			Defaults:  catalogv1alpha1.ListDefaults{QualityProfileRef: QualityProfileName, RootFolderRef: rf.Name},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, il))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), il) })

	// testdata/importlist/plex/watchlist_page1.json holds two movies.
	live := waitForImportListSynced(ctx, t, il)
	require.Empty(t, live.Status.LastError, "the Plex sync reported an error")
	require.GreaterOrEqual(t, live.Status.ItemCount, int32(1),
		"the Plex watchlist fixture holds movies; the sync fetched nothing")
}
