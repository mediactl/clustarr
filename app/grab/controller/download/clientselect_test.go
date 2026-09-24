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

package download

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

func fakeDownloadClient(name string, protocol commonv1alpha1.Protocol, enabled *bool, priority int32) downloadv1alpha1.DownloadClient {
	return downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: protocol,
			Enabled:  enabled,
			Priority: priority,
		},
	}
}

func TestPickClientIgnoresOtherProtocols(t *testing.T) {
	items := []downloadv1alpha1.DownloadClient{
		fakeDownloadClient("usenet-a", commonv1alpha1.ProtocolUsenet, ptr.To(true), 1),
	}
	_, ok := pickClient(items, commonv1alpha1.ProtocolTorrent)
	assert.False(t, ok)
}

func TestPickClientIgnoresDisabled(t *testing.T) {
	items := []downloadv1alpha1.DownloadClient{
		fakeDownloadClient("torrent-off", commonv1alpha1.ProtocolTorrent, ptr.To(false), 1),
	}
	_, ok := pickClient(items, commonv1alpha1.ProtocolTorrent)
	assert.False(t, ok)
}

func TestPickClientTreatsNilEnabledAsTrue(t *testing.T) {
	items := []downloadv1alpha1.DownloadClient{
		fakeDownloadClient("torrent-default", commonv1alpha1.ProtocolTorrent, nil, 1),
	}
	got, ok := pickClient(items, commonv1alpha1.ProtocolTorrent)
	if assert.True(t, ok) {
		assert.Equal(t, "torrent-default", got.Name)
	}
}

func TestPickClientPrefersLowestPriorityNumber(t *testing.T) {
	items := []downloadv1alpha1.DownloadClient{
		fakeDownloadClient("torrent-high", commonv1alpha1.ProtocolTorrent, ptr.To(true), 10),
		fakeDownloadClient("torrent-low", commonv1alpha1.ProtocolTorrent, ptr.To(true), 1),
		fakeDownloadClient("torrent-mid", commonv1alpha1.ProtocolTorrent, ptr.To(true), 5),
	}
	got, ok := pickClient(items, commonv1alpha1.ProtocolTorrent)
	if assert.True(t, ok) {
		assert.Equal(t, "torrent-low", got.Name)
	}
}

func TestPickClientBreaksPriorityTiesOnName(t *testing.T) {
	items := []downloadv1alpha1.DownloadClient{
		fakeDownloadClient("torrent-zeta", commonv1alpha1.ProtocolTorrent, ptr.To(true), 1),
		fakeDownloadClient("torrent-alpha", commonv1alpha1.ProtocolTorrent, ptr.To(true), 1),
	}
	got, ok := pickClient(items, commonv1alpha1.ProtocolTorrent)
	if assert.True(t, ok) {
		assert.Equal(t, "torrent-alpha", got.Name, "ties must break deterministically")
	}
}

func TestPickClientWithNoCandidatesReturnsFalse(t *testing.T) {
	_, ok := pickClient(nil, commonv1alpha1.ProtocolTorrent)
	assert.False(t, ok)
}
