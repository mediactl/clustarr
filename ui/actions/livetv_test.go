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

package actions_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)


func TestEditChannelsSendsOneMergePatchPerChannelUnderItsResourceVersion(t *testing.T) {
	w := &fakeWriter{}
	got, err := actions.EditChannels(t.Context(), w, "media", []actions.ChannelEdit{
		{Name: "argon-tv-espn", ResourceVersion: "7", Number: ptr.To("1005"), Active: ptr.To(true)},
		{Name: "argon-tv-cnn", ResourceVersion: "9", Active: ptr.To(false)},
	})
	require.NoError(t, err)
	require.Len(t, w.patches, 2)
	require.Len(t, got, 2)
	p := w.patches[0]
	require.IsType(t, &clustarrv1.IPTVChannel{}, p.obj)
	assert.Equal(t, "media", p.obj.GetNamespace())
	assert.Equal(t, "argon-tv-espn", p.obj.GetName())
	assert.Equal(t, types.MergePatchType, p.patchType)
	assert.Equal(t, actions.FieldManager, p.opts.FieldManager)
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"7"},"spec":{"active":true,"number":"1005"}}`, string(p.data))
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"9"},"spec":{"active":false}}`, string(w.patches[1].data), "false is sent")
	assert.Same(t, p.obj, got[0], "the patched object is returned")
}

func TestEditChannelsRefusesABadNumberBeforeAnyWrite(t *testing.T) {
	w := &fakeWriter{}
	_, err := actions.EditChannels(t.Context(), w, "media", []actions.ChannelEdit{
		{Name: "a", ResourceVersion: "1", Number: ptr.To("1001")},
		{Name: "b", ResourceVersion: "1", Number: ptr.To("10a")},
	})
	require.ErrorIs(t, err, actions.ErrInvalid)
	assert.Empty(t, w.patches, "nothing is written when any edit is refused")
	_, err = actions.EditChannels(t.Context(), w, "media", []actions.ChannelEdit{{Name: "a", ResourceVersion: "", Number: ptr.To("1001")}})
	require.ErrorIs(t, err, actions.ErrInvalid, "an edit names the version it read")
}

func TestEditChannelsMapsTheEPGChoiceAndClearsOverrides(t *testing.T) {
	for _, c := range []struct {
		edit actions.ChannelEdit
		want string
	}{
		{actions.ChannelEdit{EPG: ptr.To("")}, `{"epg":null}`},
		{actions.ChannelEdit{EPG: ptr.To("dummy")}, `{"epg":{"dummy":true}}`},
		{actions.ChannelEdit{EPG: ptr.To("US-ESPN")}, `{"epg":{"channelID":"US-ESPN"}}`},
		{actions.ChannelEdit{DisplayName: ptr.To("")}, `{"name":null}`},
		{actions.ChannelEdit{DisplayName: ptr.To("ESPN HD"), Group: ptr.To("Sports"), Logo: ptr.To("")}, `{"name":"ESPN HD","group":"Sports","logo":null}`},
	} {
		w := &fakeWriter{}
		e := c.edit
		e.Name, e.ResourceVersion = "a", "3"
		_, err := actions.EditChannels(t.Context(), w, "media", []actions.ChannelEdit{e})
		require.NoError(t, err)
		require.Len(t, w.patches, 1)
		assert.JSONEq(t, `{"metadata":{"resourceVersion":"3"},"spec":`+c.want+`}`, string(w.patches[0].data), c.want)
	}
}

func TestEditChannelsStopsAtTheFirstRefusal(t *testing.T) {
	w := &fakeWriter{err: errors.New("conflict")}
	_, err := actions.EditChannels(t.Context(), w, "media", []actions.ChannelEdit{
		{Name: "a", ResourceVersion: "1", Active: ptr.To(true)}, {Name: "b", ResourceVersion: "1", Active: ptr.To(true)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media/a")
	assert.Len(t, w.patches, 1)
}

func TestAddChannelNamesTheObjectByItsKey(t *testing.T) {
	w := &fakeWriter{}
	ch, err := actions.AddChannel(t.Context(), w, "media", "argon-tv", "n:abc", "1011", "ESPN")
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	sum := sha256.Sum256([]byte("n:abc"))
	assert.Equal(t, "argon-tv-"+hex.EncodeToString(sum[:])[:10], ch.Name)
	assert.Equal(t, actions.ChannelObjectName("argon-tv", "n:abc"), ch.Name)
	assert.Equal(t, "media", ch.Namespace)
	assert.Equal(t, clustarrv1.IPTVChannelSpec{ProviderRef: "argon-tv", Key: "n:abc", Number: "1011", Name: "ESPN", Active: true}, ch.Spec)
	assert.Equal(t, actions.OriginUI, ch.Labels[actions.LabelOrigin])
	assert.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)

	long := strings.Repeat("p", 70)
	assert.LessOrEqual(t, len(actions.ChannelObjectName(long, "n:abc")), 63)
	_, err = actions.AddChannel(t.Context(), w, "media", "argon-tv", "n:abc", "abc", "ESPN")
	require.ErrorIs(t, err, actions.ErrInvalid)
}

func TestRefreshLiveTVWritesTheAnnotation(t *testing.T) {
	w := &fakeWriter{}
	require.NoError(t, actions.RefreshLiveTV(t.Context(), w, "media", "argon-tv", time.Unix(1793142000, 0)))
	require.Len(t, w.patches, 1)
	require.IsType(t, &clustarrv1.IPTVProvider{}, w.patches[0].obj)
	assert.Equal(t, "argon-tv", w.patches[0].obj.GetName())
	assert.JSONEq(t, `{"metadata":{"annotations":{"clustarr.io/livetv-refresh":"1793142000"}}}`, string(w.patches[0].data))
	assert.Equal(t, "clustarr.io/livetv-refresh", clustarrv1.AnnotationLiveTVRefresh)
}

func TestTheLiveTVKindsAreSettingsKinds(t *testing.T) {
	for _, slug := range []string{"iptvproviders", "iptvchannels"} {
		k, ok := actions.ConfigKindBySlug(slug)
		require.True(t, ok, slug)
		assert.Equal(t, "clustarr.io", k.Group)
		assert.True(t, k.Namespaced)
	}
}
