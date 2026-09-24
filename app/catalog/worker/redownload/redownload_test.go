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

package redownload_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/redownload"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestRedownloads pins the Radarr ruling (package doc): a release's failure
// is searched again, a local fault is not, a blocklisted Download always is,
// and no other download event is.
func TestRedownloads(t *testing.T) {
	cases := []struct {
		action string
		reason downloadv1alpha1.DownloadFailureReason
		want   bool
	}{
		{events.ActionFailed, downloadv1alpha1.DownloadFailureMissingArticles, true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureEncrypted, true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureStalled, true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureTimeout, true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureImportRejected, true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureManual, true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureNone, true},
		{events.ActionFailed, "", true},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureDiskFull, false},
		{events.ActionFailed, downloadv1alpha1.DownloadFailureWriteError, false},
		{events.ActionBlocklisted, "", true},
		{events.ActionBlocklisted, downloadv1alpha1.DownloadFailureDiskFull, true},
		{events.ActionCompleted, "", false},
		{events.ActionImported, "", false},
		{events.ActionRemoved, downloadv1alpha1.DownloadFailureManual, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, redownload.Redownloads(tc.action, tc.reason), "%s/%s", tc.action, tc.reason)
	}
}

// TestMsgIDIsPerFailedDownloadAndItem: the same failure and item always give
// the same id, so a redelivery is a duplicate; another item of the same pack,
// or another failed Download of the same item, is a search of its own.
func TestMsgIDIsPerFailedDownloadAndItem(t *testing.T) {
	ref := schema.Ref{Namespace: "media", Name: "the-matrix-abc", UID: "uid-1"}
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}

	assert.Equal(t, "uid-1:redownload:movie/the-matrix", redownload.MsgID(ref, movie))
	assert.Equal(t, redownload.MsgID(ref, movie), redownload.MsgID(ref, movie))

	e1 := commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"}
	e2 := commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e02"}
	assert.NotEqual(t, redownload.MsgID(ref, e1), redownload.MsgID(ref, e2))

	other := ref
	other.UID = "uid-2"
	assert.NotEqual(t, redownload.MsgID(ref, movie), redownload.MsgID(other, movie))

	noUID := ref
	noUID.UID = ""
	assert.Equal(t, "media/the-matrix-abc:redownload:movie/the-matrix", redownload.MsgID(noUID, movie))
}

// TestSubscriptionIsTheTopologysConsumer: the handler reads its tuning from
// the topology it is handed, so the durable it subscribes to is the one
// EnsureTopology created, filtering both download subjects.
func TestSubscriptionIsTheTopologysConsumer(t *testing.T) {
	topo := events.Default().ForSingleNode()
	h := redownload.NewHandler(nil, nil)
	h.Topology = &topo

	sub := h.Subscription()
	assert.Equal(t, events.ConsumerCatalogRedownload, sub.Durable)
	assert.Equal(t, events.StreamEvents, sub.Stream)
	assert.ElementsMatch(t, []string{events.FilterDownloadFailed, events.FilterDownloadBlocklisted}, sub.Filters)
	assert.True(t, events.SubjectMatches(events.FilterDownloadFailed, events.DownloadEventSubject(events.ActionFailed, "u")))
	assert.True(t, events.SubjectMatches(events.FilterDownloadBlocklisted, events.DownloadEventSubject(events.ActionBlocklisted, "u")))
	assert.NoError(t, topo.Validate())
}
