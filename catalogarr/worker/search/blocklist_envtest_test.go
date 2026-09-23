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
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const (
	blockedHash  = "0123456789abcdef0123456789abcdef01234567"
	blockedTitle = "The.Matrix.1999.720p.BluRay.x264-BAD"
	theMatrix    = "the-matrix"
)

// downloadFixture describes one Download to create. Status is applied with
// k8s.PatchStatus rather than client.Status().Update, which forbidigo bans
// everywhere outside pkg/k8s -- tests included.
type downloadFixture struct {
	name             string
	hash             string
	title            string
	target           commonv1.MediaRef
	blocklisted      bool
	blocklistedUntil *metav1.Time
	phase            downloadv1alpha1.DownloadPhase
	quality          commonv1.Quality
	formatScore      int32
}

func newDownload(t *testing.T, ctx context.Context, c client.Client, ns string, f downloadFixture) {
	t.Helper()
	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: f.name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:" + f.hash)},
			Release: commonv1.ReleaseInfo{
				GUID:        "https://idx.example/details/" + f.name,
				IndexerRef:  "idx",
				Title:       f.title,
				Protocol:    commonv1.ProtocolTorrent,
				InfoHash:    f.hash,
				PublishedAt: ptr.To(metav1.Now()),
				Quality:     f.quality,
				FormatScore: f.formatScore,
			},
			Target: f.target,
		},
	}
	if f.blocklisted {
		d.Labels = map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue}
	}
	require.NoError(t, c.Create(ctx, d))

	if f.phase == "" && f.blocklistedUntil == nil {
		return
	}
	statusAC := downloadac.DownloadStatus()
	if f.phase != "" {
		statusAC = statusAC.WithPhase(f.phase)
	}
	if f.blocklistedUntil != nil {
		statusAC = statusAC.WithBlocklistedUntil(*f.blocklistedUntil)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadac.Download(f.name, ns).WithStatus(statusAC))
	require.NoError(t, err)
}

// TestTheBlocklistAndTheLiveQueueReadThroughTheCache reads both of the
// search worker's Download lookups through a real manager cache: the
// blocklist (one labelled List, LoadBlocklist) and the live queue (the one
// field index, IndexDownloadTarget).
func TestTheBlocklistAndTheLiveQueueReadThroughTheCache(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	const ns = "blocklist-idx"
	newNamespace(t, ctx, c, ns)

	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: theMatrix}
	until := metav1.NewTime(time.Now().Add(24 * time.Hour))

	newDownload(t, ctx, c, ns, downloadFixture{
		name: "blocked-1", hash: blockedHash, title: blockedTitle, target: target,
		blocklisted: true, blocklistedUntil: &until, phase: downloadv1alpha1.DownloadPhaseBlocklisted,
	})
	newDownload(t, ctx, c, ns, downloadFixture{
		name: "active-1", hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		title: "The.Matrix.1999.2160p.UHD.BluRay.x265-GOOD", target: target,
		phase: downloadv1alpha1.DownloadPhaseDownloading,
	})
	newDownload(t, ctx, c, ns, downloadFixture{
		name: "imported-1", hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		title: "The.Matrix.1999.1080p.BluRay.x264-OLD", target: target,
		phase: downloadv1alpha1.DownloadPhaseImported,
	})

	listNames := func(index, value string) []string {
		var list downloadv1alpha1.DownloadList
		require.NoError(t, c.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{index: value}))
		names := make([]string, 0, len(list.Items))
		for _, d := range list.Items {
			names = append(names, d.Name)
		}
		return names
	}

	eventually(t, 10*time.Second, "the cache to see blocked-1 on the blocklist", func() bool {
		bl, err := search.LoadBlocklist(ctx, c, ns, time.Now())
		return err == nil && bl.Contains(blockedHash, "")
	})
	bl, err := search.LoadBlocklist(ctx, c, ns, time.Now())
	require.NoError(t, err)
	require.False(t, bl.Contains("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ""),
		"an un-labelled Download is not on the blocklist")
	require.True(t, bl.Contains("", blockedTitle),
		"a usenet release has no info hash; the normalized title is how it is recognised again")

	eventually(t, 10*time.Second, "the target index to settle", func() bool {
		return len(listNames(search.IndexDownloadTarget, search.TargetIndexValue(target))) == 1
	})
	require.Equal(t, []string{"active-1"}, listNames(search.IndexDownloadTarget, search.TargetIndexValue(target)),
		"Imported and Blocklisted Downloads have both left the queue")
}
