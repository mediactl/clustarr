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

package downloadclient_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	grabarrstatus "github.com/mediactl/clustarr/app/grab/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func mkBlocklistedDownload(t *testing.T, ctx context.Context, c client.Client, name string) *downloadv1alpha1.Download {
	t.Helper()
	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			Labels: map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue},
		},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:1111111111111111111111111111111111111111")},
			// GUID, IndexerRef, Title, Protocol and InfoHash are all set,
			// even though InfoHash has no bearing on this test: DownloadSpec's
			// "release identity is immutable" CEL rule reads all five of
			// self.<field> == oldSelf.<field> unguarded by has(), and an
			// optional field genuinely absent from the object (omitempty, no
			// default) makes CEL error "no such key" on EVERY subsequent
			// write to the object -- including a status-only apply that never
			// touches spec at all. Verified empirically against the real
			// generated CRD (config/crd/bases) via this suite; every fixture
			// in this package sets all five for that reason. This is a
			// pre-existing landmine in api/download/v1alpha1/download_types.go,
			// out of scope for grabarr/controller/downloadclient/ to fix --
			// see the D2-3 report.
			Release: commonv1alpha1.ReleaseInfo{
				GUID: name, IndexerRef: "idx", Title: name,
				Protocol: commonv1alpha1.ProtocolTorrent, InfoHash: "deadbeef",
			},
			Target: commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "m-" + name},
		},
	}
	require.NoError(t, c.Create(ctx, d))
	return d
}

func TestBlocklistSweeperDeletesExpiredEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	past := metav1.NewTime(time.Now().Add(-time.Hour))
	d := mkBlocklistedDownload(t, ctx, c, "expired")
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarr, d,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			ac.WithPhase(downloadv1alpha1.DownloadPhaseBlocklisted).WithBlocklistedUntil(past)
		}))

	s := downloadclient.NewBlocklistSweeper(c, events.NewFakeRecorder(10))
	_, err := s.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "expired"}})
	require.NoError(t, err)

	var got downloadv1alpha1.Download
	err = c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "expired"}, &got)
	require.Error(t, err)
	require.True(t, apierrors.IsNotFound(err), "expected the expired Download to be deleted, got: %v", err)
}

func TestBlocklistSweeperRequeuesUnexpiredEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	future := metav1.NewTime(time.Now().Add(time.Hour))
	d := mkBlocklistedDownload(t, ctx, c, "future")
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarr, d,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			ac.WithPhase(downloadv1alpha1.DownloadPhaseBlocklisted).WithBlocklistedUntil(future)
		}))

	s := downloadclient.NewBlocklistSweeper(c, events.NewFakeRecorder(10))
	res, err := s.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "future"}})
	require.NoError(t, err)
	require.Positive(t, res.RequeueAfter)
	require.LessOrEqual(t, res.RequeueAfter, time.Hour)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "future"}, &got))
}

func TestBlocklistSweeperIgnoresUnlabelledDownload(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	past := metav1.NewTime(time.Now().Add(-time.Hour))
	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "notblocklisted", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:2222222222222222222222222222222222222222")},
			Release: commonv1alpha1.ReleaseInfo{
				GUID: "notblocklisted", IndexerRef: "idx", Title: "notblocklisted",
				Protocol: commonv1alpha1.ProtocolTorrent, InfoHash: "deadbeef",
			},
			Target: commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "m-notblocklisted"},
		},
	}
	require.NoError(t, c.Create(ctx, d))
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarr, d,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithBlocklistedUntil(past) }))

	s := downloadclient.NewBlocklistSweeper(c, events.NewFakeRecorder(10))
	_, err := s.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "notblocklisted"}})
	require.NoError(t, err)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "notblocklisted"}, &got),
		"a Download with no blocklisted label must never be deleted by the sweep")
}
