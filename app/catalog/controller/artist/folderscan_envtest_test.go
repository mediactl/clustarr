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

package artist_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/app/catalog/controller/folderscan"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// gatedGateway is the metadata gateway's lookup RPC as a scanned artist
// meets it on talos: the artist's release groups list at once, and each
// album's track listing is refused -- MusicBrainz at one lookup a second
// behind thousands of others -- until the test opens its release group. The
// refusal is the gateway's own answer, as app/catalog/metadata's lookup
// makes it: the provider's error in MetadataResponse.Error, here the shared
// limiter's refusal of a wait past the caller's deadline (refusal).
type gatedGateway struct {
	groups  []pkgmetadata.Album
	refusal string

	mu   sync.Mutex
	open map[string]bool
}

func (g *gatedGateway) Open(group string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open[group] = true
}

func (g *gatedGateway) Request(_ context.Context, _ string, in, out any) error {
	req, ok := in.(schema.MetadataRequest)
	if !ok {
		panic(fmt.Sprintf("unexpected request %T", in))
	}
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		panic(fmt.Sprintf("unexpected response %T", out))
	}
	resp.Kind = commonv1.MediaKindAlbum
	if req.IDs[pkgmetadata.KeyMBArtist] != "" {
		for _, alb := range g.groups {
			b, err := json.Marshal(alb)
			if err != nil {
				return err
			}
			resp.Results = append(resp.Results, b)
		}
		return nil
	}
	group := req.IDs[pkgmetadata.KeyMBReleaseGroup]
	g.mu.Lock()
	open := g.open[group]
	g.mu.Unlock()
	if !open {
		resp.Error = "musicbrainz: release-group " + group + ": " + g.refusal
		return nil
	}
	b, err := json.Marshal(pkgmetadata.Album{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: group},
		Releases: []pkgmetadata.AlbumRelease{{
			IDs:    pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "0a1b2c3d-0000-4000-8000-" + group[len(group)-12:]},
			Status: "Official",
			Media: []pkgmetadata.Medium{{Position: 1, Format: "CD", Tracks: []pkgmetadata.Track{
				{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "1a1b2c3d-0000-4000-8000-" + group[len(group)-12:]}, Title: "One", Position: 1, MediumNumber: 1},
				{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "2a1b2c3d-0000-4000-8000-" + group[len(group)-12:]}, Title: "Two", Position: 2, MediumNumber: 1},
			}}},
		}},
	})
	if err != nil {
		return err
	}
	resp.Result = b
	return nil
}

// fixedFolders answers HoldsFiles for one folder: the scanned artist's,
// which holds its files.
type fixedFolders struct{ folder string }

func (f fixedFolders) HoldsFiles(_ context.Context, path string) (bool, error) {
	return path == f.folder, nil
}

// limiterRefusal is the error the metadata agent's shared MusicBrainz
// limiter (one lookup a second) returns when the asks queued ahead would
// carry a lookup past its caller's deadline, as rate.Limiter.Wait makes it.
func limiterRefusal(t *testing.T) string {
	t.Helper()
	lim := rate.NewLimiter(1, 1)
	require.True(t, lim.Allow())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := lim.Wait(ctx)
	require.Error(t, err)
	return err.Error()
}

// startArtistAndAlbums runs the real Artist and Album reconcilers on one
// item path, as the remediation loop runs them: each Album's own pass
// writes its track list, and the Artist's watch on its Albums wakes it.
func startArtistAndAlbums(t *testing.T, ctx context.Context, cfg *rest.Config, b combinedBus, folders folderscan.Folders) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	ar := &artist.Reconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorder("artist"),
		Bus: b, Folders: folders,
	}
	al := &album.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorder("album"), Bus: b}
	require.NoError(t, remediation.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, itemtest.Start(mgr, map[remediation.KeyKind]rollup.Item{remediation.KindArtist: ar, remediation.KindAlbum: al}))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

// TestAScannedArtistScansItsFolderOnlyOnceEveryAlbumListsItsTracks is design
// D18 on talos's shape: a root scan created 390 artists, whose albums'
// documents and track lists are thousands of MusicBrainz lookups at one a
// second, hours after the artists exist. An artist's one folder scan
// attributes its files to its albums by their documents (the title, year
// and release ids MatchAlbum reads) and to their tracks, so a scan asked 30
// s after the albums were listed -- what the folder scan did until now --
// would have found no album to take a file and spent the artist's only
// scan. The real Album reconciler lists each album's tracks as its release
// group's lookup comes through, one by one, and the gateway refuses the
// rest with an error of its own, as its limiter does, which is no answer;
// the documents arrive on their own, before a listing or after it. The
// artist waits, naming how many albums are still to come, each album's
// arrival waking it, and asks for its scan only once the last is in and the
// settle after it has run.
func TestAScannedArtistScansItsFolderOnlyOnceEveryAlbumListsItsTracks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	realBus := membus.New(nil)
	require.NoError(t, realBus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = realBus.Close() })

	groups := []string{
		"b1392450-e5a3-37d1-83d3-b8b08ca6c4d9",
		"c2492450-e5a3-37d1-83d3-b8b08ca6c4d9",
		"d3592450-e5a3-37d1-83d3-b8b08ca6c4d9",
	}
	gw := &gatedGateway{open: map[string]bool{}, refusal: limiterRefusal(t)}
	for i, g := range groups {
		gw.groups = append(gw.groups, pkgmetadata.Album{
			IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: g}, Title: fmt.Sprintf("Album %d", i+1), PrimaryType: "Album",
		})
	}
	// Album 1's lookup comes through on its first pass, long before its
	// document: its tracks are listed and its title is not known.
	gw.Open(groups[0])
	const ns = "artist-folderscan-ns"
	c := startArtistAndAlbums(t, ctx, cfg, combinedBus{Publisher: realBus, requester: gw},
		fixedFolders{folder: "/data/media/music/Muse"})
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "music-root", "/data/media/music")))

	// The Artist as a root scan creates it (D16, scanapply's applyArtist):
	// pinned to the folder its files are in, every album monitored, no
	// add-time search -- and, on a root whose missing albums are not wanted,
	// the artist unmonitored, so its Albums read Unmonitored from their first
	// pass and a track list or a document arriving moves no other leaf of
	// theirs.
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "muse", Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "9c9f1380-2516-4fc9-a3e6-f9f61941d090", Monitored: ptr.To(false),
			QualityProfileRef: "none", RootFolderRef: "music-root", Folder: ptr.To("Muse"),
			AddOptions: catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorAll, SearchForMissing: ptr.To(false)},
		},
	}
	require.NoError(t, c.Create(ctx, a))
	key := client.ObjectKeyFromObject(a)

	// The gateway's document wakes the artist, whose fan-out lists its
	// three albums.
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Artist(a.Name, ns).WithStatus(catalogac.ArtistStatus().WithMetadata(
		catalogac.ArtistMetadata().WithName("Muse").WithRefreshedAt(metav1.Now()).WithSchemaVersion(pkgmetadata.SchemaVersion))))
	require.NoError(t, err)

	albumKeys := make([]types.NamespacedName, len(groups))
	for i, g := range groups {
		albumKeys[i] = types.NamespacedName{Namespace: ns, Name: artist.AlbumName(a.Name, g)}
	}
	// tracksSynced reads album i's TracksSynced reason and whether it holds
	// its document and its two tracks.
	tracksSynced := func(i int) (reason string, documented, listed bool) {
		var alb catalogv1alpha1.Album
		if c.Get(ctx, albumKeys[i], &alb) != nil || alb.Status.Phase != commonv1.LeafPhaseUnmonitored {
			return "", false, false
		}
		if cond := k8s.FindCondition(alb.Status.Conditions, "TracksSynced"); cond != nil {
			reason = cond.Reason
		}
		return reason, alb.Status.Metadata != nil && alb.Status.Path != "", len(alb.Status.Tracks) == 2
	}
	// Each album's first pass lands: album 1's listing, the others' asks
	// refused by the gateway, which the Album reads as RPCError.
	require.Eventually(t, func() bool {
		if _, _, listed := tracksSynced(0); !listed {
			return false
		}
		for i := 1; i < len(groups); i++ {
			if reason, _, _ := tracksSynced(i); reason != events.RequestOther.Reason() {
				return false
			}
		}
		return true
	}, 10*time.Second, 20*time.Millisecond, "the albums' first passes never landed")

	// folderScanned waits for the artist's FolderScanned condition to read
	// status and message, and asserts no LibraryScan exists then.
	folderScanned := func(status metav1.ConditionStatus, message string) metav1.Condition {
		t.Helper()
		var got catalogv1alpha1.Artist
		var last *metav1.Condition
		ok := assert.Eventually(t, func() bool {
			if c.Get(ctx, key, &got) != nil {
				return false
			}
			last = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned)
			return last != nil && last.Status == status && last.Message == message
		}, 10*time.Second, 20*time.Millisecond)
		require.True(t, ok, "FolderScanned never read %s %q; it read %+v", status, message, last)
		var scans catalogv1alpha1.LibraryScanList
		require.NoError(t, c.List(ctx, &scans, client.InNamespace(ns)))
		assert.Empty(t, scans.Items, "no folder scan while %q", message)
		return *k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned)
	}
	waiting := func(n int) string {
		return fmt.Sprintf("waiting for the metadata and track lists of %d of its 3 albums; its folder is scanned for their files once they are in", n)
	}
	// stillWaiting holds the artist at waiting(n) for long enough that a
	// pass of it woken by the album just changed would have moved it.
	stillWaiting := func(n int, why string) {
		t.Helper()
		folderScanned(metav1.ConditionUnknown, waiting(n))
		assert.Never(t, func() bool {
			var got catalogv1alpha1.Artist
			if c.Get(ctx, key, &got) != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned)
			return cond == nil || cond.Message != waiting(n)
		}, 2*time.Second, 50*time.Millisecond, why)
	}
	// Album 1's tracks are listed, its document is not: all three to come.
	stillWaiting(3, "an album whose tracks are listed is still to come without its document")

	// document gives album i its document, refreshed at the given time
	// (each later one a cache miss for the Album's track lookup, which it
	// asks again).
	document := func(i int, refreshed time.Time) {
		t.Helper()
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Album(albumKeys[i].Name, ns).WithStatus(
			catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().
				WithTitle(fmt.Sprintf("Album %d", i+1)).WithRefreshedAt(metav1.NewTime(refreshed)).WithSchemaVersion(pkgmetadata.SchemaVersion))))
		require.NoError(t, err)
	}
	refreshed := time.Now().Add(-time.Hour).Truncate(time.Second)

	// Album 2's document arrives while the gateway still refuses its
	// lookup: its pass asks again and is refused again, which is no answer.
	document(1, refreshed)
	require.Eventually(t, func() bool {
		reason, documented, _ := tracksSynced(1)
		return documented && reason == events.RequestOther.Reason()
	}, 10*time.Second, 20*time.Millisecond, "album 2's document never landed")
	stillWaiting(3, "a refused lookup is not a track list, whatever the album's document")

	// Album 1's document arrives: album 1 is in.
	document(0, refreshed)
	folderScanned(metav1.ConditionUnknown, waiting(2))

	// The rest of the lookups come through, each with a fresh document that
	// wakes its album to ask again; each listing wakes the artist, which
	// keeps waiting while any album is to come.
	for i := 1; i < len(groups); i++ {
		gw.Open(groups[i])
		document(i, refreshed.Add(time.Minute))
		require.Eventually(t, func() bool {
			_, documented, listed := tracksSynced(i)
			return documented && listed
		}, 10*time.Second, 20*time.Millisecond, "album %d never listed its tracks", i+1)
		if left := len(groups) - i - 1; left > 0 {
			folderScanned(metav1.ConditionUnknown, waiting(left))
		}
	}

	// The last album is in: the settle starts now, and only once it has run
	// is the one scan of the artist's folder asked for.
	settling := folderScanned(metav1.ConditionFalse,
		"the metadata and track lists of all 3 of its albums are in; its folder is scanned for their files once they settle")
	assert.Equal(t, folderscan.ReasonSettling, settling.Reason)
	var scan catalogv1alpha1.LibraryScan
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: folderscan.ScanName(a)}, &scan) == nil
	}, folderscan.SettleAfter+15*time.Second, 100*time.Millisecond, "the artist never asked for its folder scan")
	assert.Equal(t, "music-root", scan.Spec.RootFolderRef)
	assert.Equal(t, "Muse", scan.Spec.Subpath, "the artist's own folder")
	assert.Equal(t, catalogv1alpha1.ScanModeIncremental, scan.Spec.Mode)
	assert.False(t, scan.CreationTimestamp.Before(ptr.To(metav1.NewTime(settling.LastTransitionTime.Add(folderscan.SettleAfter-time.Second)))),
		"the scan waited out the settle after the last album")
	var got catalogv1alpha1.Artist
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil &&
			k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned)
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, folderscan.ReasonScanRequested,
		k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned).Reason)
}
