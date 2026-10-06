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

package grab_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/pkg/events"
)

// TestADonorAndAVideoGrabOfOneItemCoexist (anime dual-audio spec §6.1): a
// donor is grabbed under its own lease and is invisible to the video's
// double-grab guard, and the other way round -- an upgrade never waits on
// a dub, nor a dub on an upgrade -- while a second donor is refused.
func TestADonorAndAVideoGrabOfOneItemCoexist(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	movie := newMovie(t, ctx, c, ns, "monster-2004")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}

	donor := torrentRelease("guid-donor", "my-indexer", profile.Tiers[0][0].Quality, 0)
	donor.InfoHash = "1111111111111111111111111111111111111111"
	require.NoError(t, grab.PerformDonorGrab(ctx, deps, ns, target, donor))

	video := torrentRelease("guid-video", "my-indexer", profile.Tiers[0][0].Quality, 0)
	video.InfoHash = "2222222222222222222222222222222222222222"
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, video, downloadv1alpha1.GrabSourceSearch),
		"a donor in flight must not block the video")

	var list downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
	require.Len(t, list.Items, 2)
	purposes := map[string]downloadv1alpha1.DownloadPurpose{}
	for _, dl := range list.Items {
		purposes[dl.Spec.Release.GUID] = dl.Spec.Purpose
	}
	assert.Equal(t, downloadv1alpha1.DownloadPurposeAudioDonor, purposes["guid-donor"])
	assert.Empty(t, purposes["guid-video"])

	second := torrentRelease("guid-donor-2", "my-indexer", profile.Tiers[0][0].Quality, 0)
	second.InfoHash = "3333333333333333333333333333333333333333"
	require.ErrorIs(t, grab.PerformDonorGrab(ctx, deps, ns, target, second), grab.ErrDuplicateGrab,
		"one donor at a time")

	videoLease, err := bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
	require.NoError(t, err)
	assert.Contains(t, string(videoLease.Value), "monster-2004", "the video holds the item's lease")

	// The redownload path frees the donor's lease by the donor's name.
	var donorName string
	for _, dl := range list.Items {
		if dl.Spec.IsDonor() {
			donorName = dl.Name
		}
	}
	freed, err := grab.FreeLeases(ctx, bus.KV(events.BucketLeases), ns, target, donorName)
	require.NoError(t, err)
	assert.Len(t, freed, 1, "the donor's own lease is freed, and not the video's")
}
