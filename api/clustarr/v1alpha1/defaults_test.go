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

package v1alpha1_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

func TestAnEmptySpecReadsEveryDefault(t *testing.T) {
	var s clustarrv1.IPTVProviderSpec
	assert.True(t, s.EnabledOrDefault())
	assert.Equal(t, clustarrv1.EPGSourceXEPG, s.EPGSourceOrDefault())
	assert.Equal(t, int32(1000), s.ChannelNumberStartOrDefault())
	assert.Equal(t, 24*time.Hour, s.RefreshOrDefault())
	assert.Equal(t, "clustarr", s.UserAgentOrDefault())
	assert.Equal(t, 0, s.TunersOverride(), "the account's own limit")
	assert.True(t, clustarrv1.ChannelFilter{}.EnabledOrDefault())
	assert.Equal(t, int64(8<<20), s.Stream.BufferOrDefault())
	assert.Equal(t, 10*time.Second, s.Stream.LingerOrDefault())
	assert.Equal(t, int32(60), clustarrv1.ChannelEPG{Dummy: true}.DummyMinutesOrDefault())
}

func TestASetValueWinsOverItsDefault(t *testing.T) {
	q := resource.MustParse("4Mi")
	s := clustarrv1.IPTVProviderSpec{
		Enabled:            ptr.To(false),
		EPGSource:          clustarrv1.EPGSourcePMS,
		ChannelNumberStart: ptr.To(int32(1)),
		UserAgent:          "VLC",
		Refresh:            &metav1.Duration{Duration: time.Hour},
		Tuners:             ptr.To(int32(2)),
		Stream:             clustarrv1.StreamSpec{Buffer: &q, Linger: &metav1.Duration{Duration: time.Second}},
	}
	assert.False(t, s.EnabledOrDefault())
	assert.Equal(t, clustarrv1.EPGSourcePMS, s.EPGSourceOrDefault())
	assert.Equal(t, int32(1), s.ChannelNumberStartOrDefault())
	assert.Equal(t, "VLC", s.UserAgentOrDefault())
	assert.Equal(t, time.Hour, s.RefreshOrDefault())
	assert.Equal(t, 2, s.TunersOverride())
	assert.Equal(t, int64(4<<20), s.Stream.BufferOrDefault())
	assert.Equal(t, time.Second, s.Stream.LingerOrDefault())
	assert.Equal(t, int32(120), clustarrv1.ChannelEPG{Dummy: true, DummyMinutes: ptr.To(int32(120))}.DummyMinutesOrDefault())
	assert.False(t, clustarrv1.ChannelFilter{Enabled: ptr.To(false)}.EnabledOrDefault())
}
