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

package download_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newDownload(ns, name, indexer, guid string, src downloadv1alpha1.DownloadSource) *downloadv1alpha1.Download {
	proto := commonv1.ProtocolTorrent
	if src.NZBURL != nil {
		proto = commonv1.ProtocolUsenet
	}
	return &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: proto,
			Source:   src,
			Release: commonv1.ReleaseInfo{
				GUID: guid, IndexerRef: indexer, IndexerName: indexer,
				Title: "Arrival.2016.1080p", Protocol: proto,
			},
			Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "arrival"},
		},
	}
}

// A grab whose source is a direct torrentURL, magnetURL or nzbURL never
// reaches rpc.indexarr.download, so the direct-grab reconciler -- registered
// through its own SetupWithManager on a real manager -- counts it from the
// Download's creation into the same ring. An indexerDownload grab is left to
// the verb, and the apply leaves the rest of the worker's set standing.
func TestDirectGrabsCountTowardTheGrabWindow(t *testing.T) {
	ctx := t.Context()
	c := newTestClient(t)
	idx, svc := steadyState(t, ctx, c, "dl-direct", "tr")

	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	require.NoError(t, err)
	require.NoError(t, (&download.DirectGrabReconciler{Client: mgr.GetClient(), Bus: svc.Bus}).SetupWithManager(mgr))
	mctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Start(mctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })

	grabs := func() int32 {
		var live indexv1alpha1.Indexer
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &live))
		return live.Status.GrabsInWindow
	}

	torrentURL := "https://tracker.example.invalid/dl/1.torrent"
	require.NoError(t, c.Create(ctx, newDownload(idx.Namespace, "direct-torrent", "tr", "g-1",
		downloadv1alpha1.DownloadSource{TorrentURL: &torrentURL})))
	require.Eventually(t, func() bool { return grabs() == 1 }, 20*time.Second, 100*time.Millisecond,
		"a torrentURL grab was never counted")

	nzbURL := "https://indexer.example.invalid/getnzb/2.nzb"
	require.NoError(t, c.Create(ctx, newDownload(idx.Namespace, "direct-nzb", "tr", "g-2",
		downloadv1alpha1.DownloadSource{NZBURL: &nzbURL})))
	require.Eventually(t, func() bool { return grabs() == 2 }, 20*time.Second, 100*time.Millisecond,
		"an nzbURL grab was never counted")

	// The verb's path, and a second Download for an already-counted GUID:
	// neither moves the count.
	require.NoError(t, c.Create(ctx, newDownload(idx.Namespace, "via-verb", "tr", "g-3",
		downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "tr", GUID: "g-3"}})))
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	require.NoError(t, c.Create(ctx, newDownload(idx.Namespace, "same-guid", "tr", "g-1",
		downloadv1alpha1.DownloadSource{MagnetURL: &magnet})))
	require.Never(t, func() bool { return grabs() != 2 }, 2*time.Second, 100*time.Millisecond,
		"an indexerDownload grab is the verb's to count, and a GUID already in the ring is not a second grab")

	var after indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
	require.Equal(t, int32(11), after.Status.QueriesInWindow, "released by a partial apply")
	require.Equal(t, int64(4211), after.Status.IndexedReleases, "released by a partial apply")
	require.NotNil(t, after.Status.LastRssAt, "released by a partial apply")
	require.Equal(t, int32(2), after.Status.EscalationLevel, "released by a partial apply")
}
