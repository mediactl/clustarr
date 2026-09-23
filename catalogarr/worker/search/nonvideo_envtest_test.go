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

package search_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// musicProfile is Lidarr's lossless ladder, as the built-in music-lossless
// profile ships it.
func musicProfile(name string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindMusic,
			Cutoff:    "FLAC",
			Tiers: []catalogv1alpha1.Tier{
				{Name: "FLAC", Qualities: []string{"FLAC"}},
				{Name: "High", Qualities: []string{"High"}},
				{Name: "Mid", Qualities: []string{"Mid"}},
			},
		},
	}
}

// createKidA creates Radiohead and Kid A with the metadata the gateway would
// have written, and waits for both to reach the cache.
func createKidA(t *testing.T, ctx context.Context, c client.Client, ns, profile string) {
	t.Helper()
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID:     "a74b1b7f-71a5-4011-9441-d0b5e4122711",
			QualityProfileRef: profile, RootFolderRef: "music",
		},
	}))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Artist("radiohead", ns).WithStatus(
		catalogac.ArtistStatus().WithMetadata(catalogac.ArtistMetadata().WithName("Radiohead").WithSortName("Radiohead"))))
	require.NoError(t, err)

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead-kid-a", Namespace: ns},
		Spec: catalogv1alpha1.AlbumSpec{
			ArtistRef: "radiohead", ReleaseGroupID: "b8048f24-c026-3398-b23a-b5e30716ea6f",
		},
	}))
	released := metav1.NewTime(time.Date(2000, 10, 2, 0, 0, 0, 0, time.UTC))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Album("radiohead-kid-a", ns).WithStatus(
		catalogac.AlbumStatus().
			WithPhase(catalogv1alpha1.AlbumPhaseWanted).
			WithMetadata(catalogac.AlbumMetadata().WithTitle("Kid A").WithReleaseDate(released))))
	require.NoError(t, err)

	eventually(t, 10*time.Second, "the artist's and album's metadata to reach the cache", func() bool {
		var a catalogv1alpha1.Artist
		var al catalogv1alpha1.Album
		return c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "radiohead"}, &a) == nil && a.Status.Metadata != nil &&
			c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "radiohead-kid-a"}, &al) == nil && al.Status.Metadata != nil
	})
}

// TestWorkerSearchesAnAlbumAndDecidesItByName pins the carried "automatic
// search is movie and episode only" defect from the search worker's side,
// through the REAL decision engine: an album is snapshotted with its
// artist, searched by "<artist> <album>" in Lidarr's categories, and its
// releases are identified by what their titles name -- the right album is
// approved, another album by the same artist and the same album title by
// another artist are refused as the wrong item.
func TestWorkerSearchesAnAlbumAndDecidesItByName(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-album")
	qp := musicProfile("music-" + f.ns)
	require.NoError(t, f.mgr.Create(ctx, qp))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Name: qp.Name}, &catalogv1alpha1.QualityProfile{})
	createKidA(t, ctx, f.mgr, f.ns, qp.Name)

	f.worker.Evaluate = decision.Evaluate
	f.rpc.Response = schema.SearchResponse{Releases: []schema.Release{
		nonVideoRelease("right", "Radiohead - Kid A (2000) [FLAC]"),
		nonVideoRelease("other-album", "Radiohead - Amnesiac (2001) [FLAC]"),
		nonVideoRelease("other-artist", "Muse - Kid A (2000) [FLAC]"),
	}}

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-album", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "radiohead-kid-a"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-album"}, &catalogv1alpha1.Search{})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "radiohead-kid-a"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-album"},
		UserInvoked: true,
	})}))

	reqs := f.rpc.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, commonv1.MediaKindAlbum, reqs[0].Kind)
	require.Equal(t, "Radiohead Kid A", reqs[0].Text, "Lidarr's \"<artist> <album>\" query")
	require.Equal(t, []int32{3000, 3010, 3040}, reqs[0].Categories)
	require.Nil(t, reqs[0].IDs, "an album has no id an indexer takes")

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-album"}, got))
	byGUID := map[string]commonv1.ReleaseDecision{}
	for _, r := range got.Status.Results {
		byGUID[r.GUID] = r
	}
	require.Len(t, byGUID, 3)
	require.True(t, byGUID["right"].Approved, "the right album is approved: %+v", byGUID["right"].Rejections)
	for _, guid := range []string{"other-album", "other-artist"} {
		d := byGUID[guid]
		require.False(t, d.Approved, guid)
		require.True(t, hasRejection(d, decision.ReasonWrongItem.Code), "%s: %+v", guid, d.Rejections)
	}
}

func nonVideoRelease(guid, title string) schema.Release {
	return schema.Release{
		Info: commonv1.ReleaseInfo{
			GUID: guid, IndexerRef: "idx", IndexerName: "Example", Title: title,
			Protocol: commonv1.ProtocolTorrent, SizeBytes: 400 << 20,
			DownloadURL: "https://idx.example/dl/" + guid,
		},
		ParsedTitle: title,
		FetchedAt:   time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC),
	}
}

// hasRejection reports whether d carries a rejection of reason code;
// pkg/decision prefixes every rejection message with its reason's code.
func hasRejection(d commonv1.ReleaseDecision, code string) bool {
	for _, r := range d.Rejections {
		if strings.HasPrefix(r.Reason, code+":") {
			return true
		}
	}
	return false
}

// TestWorkerWantedScanIncludesNonVideoItems: a WantedScan naming no kinds
// expands into searches for wanted albums and issues as well as movies. An
// issue whose store date is still ahead is not searched.
func TestWorkerWantedScanIncludesNonVideoItems(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-wantedscan-nonvideo")
	pub := &recordingPublisher{}
	f.worker.Publisher = pub
	createKidA(t, ctx, f.mgr, f.ns, "music-anything")

	require.NoError(t, f.mgr.Create(ctx, &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: f.ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceProvider("comicvine"), SourceID: "46644",
			QualityProfileRef: "comic", RootFolderRef: "comics",
		},
	}))
	issue := func(name, number string, centis int32, date time.Time) {
		require.NoError(t, f.mgr.Create(ctx, &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
			Spec:       catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: number, CalculatedNumberCentis: centis},
		}))
		_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalogarr, catalogac.Issue(name, f.ns).WithStatus(
			catalogac.IssueStatus().WithState(catalogv1alpha1.IssueStateWanted).WithDate(metav1.NewTime(date))))
		require.NoError(t, err)
	}
	issue("saga-050", "50", 5000, testNow.Add(-30*24*time.Hour))
	issue("saga-070", "70", 7000, testNow.Add(90*24*time.Hour))
	eventually(t, 10*time.Second, "both issues' state to reach the cache", func() bool {
		var list catalogv1alpha1.IssueList
		if err := f.mgr.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		n := 0
		for i := range list.Items {
			if list.Items[i].Status.State != "" {
				n++
			}
		}
		return n == 2
	})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: wantedScanEnvelope(t, schema.WantedScan{
		Namespace: f.ns, CutoffUnmet: true, Epoch: 1700000001,
	})}))

	_, envs := pub.snapshot()
	var got []string
	for _, e := range envs {
		var task schema.SearchTask
		require.NoError(t, json.Unmarshal(e.Data, &task))
		got = append(got, string(task.MediaRef.Kind)+"/"+task.MediaRef.Name)
	}
	sort.Strings(got)
	require.Equal(t, []string{"album/radiohead-kid-a", "issue/saga-050"}, got,
		"the wanted album and the released issue; not the fixture's phaseless movie, not the unreleased issue")
}
