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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
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
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Artist("radiohead", ns).WithStatus(
		catalogac.ArtistStatus().WithMetadata(catalogac.ArtistMetadata().WithName("Radiohead").WithSortName("Radiohead"))))
	require.NoError(t, err)

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead-kid-a", Namespace: ns},
		Spec: catalogv1alpha1.AlbumSpec{
			ArtistRef: "radiohead", ReleaseGroupID: "b8048f24-c026-3398-b23a-b5e30716ea6f",
		},
	}))
	released := metav1.NewTime(time.Date(2000, 10, 2, 0, 0, 0, 0, time.UTC))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Album("radiohead-kid-a", ns).WithStatus(
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
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-album", UID: string(srch.UID)},
		UserInvoked: true,
		Seq:         1,
	})}))

	reqs := f.rpc.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, commonv1.MediaKindAlbum, reqs[0].Kind)
	require.Equal(t, "Radiohead Kid A", reqs[0].Text, "Lidarr's \"<artist> <album>\" query")
	require.Equal(t, []int32{3000, 3010, 3040}, reqs[0].Categories)
	require.Nil(t, reqs[0].IDs, "an album has no id an indexer takes")

	rec := f.record(t, agentrecords.SearchKey(string(srch.UID)))
	byGUID := map[string]commonv1.ReleaseDecision{}
	for _, c := range rec.Candidates {
		byGUID[c.Decision.GUID] = c.Decision
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

// TestWorkerSkipsAnAutomaticSearchTheGrabPathCannotGrab: an automatic
// search of a kind the worker cannot search costs no indexer query. Since
// A4.3 the task is answered in the item's record, failed -- the planner
// closes the dispatch on it and backs off -- rather than dead-lettered.
//
// Since task X4a extended the grab path to albums, books, audiobooks and
// issues, every kind the worker searches is one the grab path grabs, and
// the kinds it refuses are the containers: an Artist, an Author, a Comic, a
// Series, whose items are searched instead. So this now asks about an
// Artist (it used to ask about an Album, and skipped itself once albums
// became grabbable): the task is discarded -- no number of redeliveries
// makes a container searchable -- before any query is spent.
func TestWorkerSkipsAnAutomaticSearchTheGrabPathCannotGrab(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-artist-automatic")
	createKidA(t, ctx, f.mgr, f.ns, "music-anything")

	ref := commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "radiohead"}
	require.False(t, search.Searchable(ref.Kind), "the worker searches artists now; point this at a kind it cannot")

	item := itemRef(t, ctx, f.mgr, "Artist", &catalogv1alpha1.Artist{}, client.ObjectKey{Namespace: f.ns, Name: "radiohead"})
	err := f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef: ref, Reason: schema.SearchReasonMissing, Item: item, Seq: 1,
	})})
	require.NoError(t, err, "answered, not retried: no redelivery makes a container searchable")
	rec := f.record(t, agentrecords.SearchKey(item.UID))
	require.Equal(t, records.StateFailed, rec.State)
	require.Contains(t, rec.Failure, "is not searchable")
	require.Empty(t, f.rpc.Requests(), "no indexer query for a result nothing can grab")
}
