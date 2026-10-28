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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// channelGetter is the cluster as EditChannels and DeleteChannels read it
// before they write: each channel at its generation and resourceVersion.
type channelGetter map[string]*clustarrv1.IPTVChannel

func (g channelGetter) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	ch, ok := g[key.Name]
	if !ok || key.Namespace != "media" {
		return apierrors.NewNotFound(schema.GroupResource{Group: "clustarr.io", Resource: "iptvchannels"}, key.Name)
	}
	ch.DeepCopyInto(obj.(*clustarrv1.IPTVChannel))
	return nil
}

func readChannel(name string, generation int64, rv string) *clustarrv1.IPTVChannel {
	return &clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{
		Namespace: "media", Name: name, Generation: generation, ResourceVersion: rv, UID: types.UID("uid-" + name),
	}}
}

func TestEditChannelsPatchesUnderTheVersionItReadsWhileTheSpecIsTheOneThePageRead(t *testing.T) {
	w := &fakeWriter{}
	g := channelGetter{"argon-tv-espn": readChannel("argon-tv-espn", 2, "7"), "argon-tv-cnn": readChannel("argon-tv-cnn", 5, "9")}
	got, err := actions.EditChannels(t.Context(), g, w, "media", []actions.ChannelEdit{
		{Name: "argon-tv-espn", Generation: 2, Number: ptr.To("1005"), Active: ptr.To(true)},
		{Name: "argon-tv-cnn", Generation: 5, Active: ptr.To(false)},
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
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"7"},"spec":{"active":true,"number":"1005"}}`, string(p.data),
		"the version read just now: a status write since the page loaded is no conflict")
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"9"},"spec":{"active":false}}`, string(w.patches[1].data), "false is sent")
	assert.Same(t, p.obj, got[0], "the patched object is returned")
}

func TestEditChannelsRefusesEveryEditWhenAnySpecMovedSinceThePageRead(t *testing.T) {
	w := &fakeWriter{}
	g := channelGetter{"argon-tv-espn": readChannel("argon-tv-espn", 2, "7"), "argon-tv-cnn": readChannel("argon-tv-cnn", 6, "11")}
	_, err := actions.EditChannels(t.Context(), g, w, "media", []actions.ChannelEdit{
		{Name: "argon-tv-espn", Generation: 2, Active: ptr.To(true)},
		{Name: "argon-tv-cnn", Generation: 5, Active: ptr.To(true)},
	})
	require.True(t, apierrors.IsConflict(err), "another writer changed cnn's spec: %v", err)
	assert.Contains(t, err.Error(), "argon-tv-cnn")
	assert.Empty(t, w.patches, "nothing is written when any channel moved")
}

func TestEditChannelsRefusesABadNumberBeforeAnyWrite(t *testing.T) {
	w := &fakeWriter{}
	g := channelGetter{"a": readChannel("a", 1, "1"), "b": readChannel("b", 1, "1")}
	_, err := actions.EditChannels(t.Context(), g, w, "media", []actions.ChannelEdit{
		{Name: "a", Generation: 1, Number: ptr.To("1001")},
		{Name: "b", Generation: 1, Number: ptr.To("10a")},
	})
	require.ErrorIs(t, err, actions.ErrInvalid)
	assert.Empty(t, w.patches, "nothing is written when any edit is refused")
	_, err = actions.EditChannels(t.Context(), g, w, "media", []actions.ChannelEdit{{Name: "a", Number: ptr.To("1001")}})
	require.ErrorIs(t, err, actions.ErrInvalid, "an edit names the generation it read")
}

func TestEditChannelsMapsTheEPGChoiceAndClearsOverrides(t *testing.T) {
	for _, c := range []struct {
		edit actions.ChannelEdit
		want string
	}{
		{actions.ChannelEdit{EPG: ptr.To("")}, `{"epg":null}`},
		// A merge patch merges nested objects, so each choice clears the
		// other's fields: the CRD takes exactly one of channelID and dummy.
		{actions.ChannelEdit{EPG: ptr.To("dummy")}, `{"epg":{"dummy":true,"channelID":null}}`},
		{actions.ChannelEdit{EPG: ptr.To("US-ESPN")}, `{"epg":{"channelID":"US-ESPN","dummy":null,"dummyMinutes":null}}`},
		{actions.ChannelEdit{DisplayName: ptr.To("")}, `{"name":null}`},
		{actions.ChannelEdit{DisplayName: ptr.To("ESPN HD"), Group: ptr.To("Sports"), Logo: ptr.To("")}, `{"name":"ESPN HD","group":"Sports","logo":null}`},
	} {
		w := &fakeWriter{}
		e := c.edit
		e.Name, e.Generation = "a", 1
		_, err := actions.EditChannels(t.Context(), channelGetter{"a": readChannel("a", 1, "3")}, w, "media", []actions.ChannelEdit{e})
		require.NoError(t, err)
		require.Len(t, w.patches, 1)
		assert.JSONEq(t, `{"metadata":{"resourceVersion":"3"},"spec":`+c.want+`}`, string(w.patches[0].data), c.want)
	}
}

func TestEditChannelsStopsAtTheFirstRefusal(t *testing.T) {
	w := &fakeWriter{err: errors.New("conflict")}
	g := channelGetter{"a": readChannel("a", 1, "1"), "b": readChannel("b", 1, "1")}
	_, err := actions.EditChannels(t.Context(), g, w, "media", []actions.ChannelEdit{
		{Name: "a", Generation: 1, Active: ptr.To(true)}, {Name: "b", Generation: 1, Active: ptr.To(true)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media/a")
	assert.Len(t, w.patches, 1)
}

func TestDeleteChannelsDeletesUnderThePreconditionsItReads(t *testing.T) {
	w := &fakeWriter{}
	g := channelGetter{"a": readChannel("a", 2, "7"), "b": readChannel("b", 4, "9")}
	n, err := actions.DeleteChannels(t.Context(), g, w, "media", []actions.ChannelRef{{Name: "a", Generation: 2}, {Name: "b", Generation: 4}})
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	require.Len(t, w.deletes, 2)
	assert.Equal(t, "a", w.deletes[0].GetName())
	require.NotNil(t, w.deleteOpts[0].Preconditions)
	assert.Equal(t, ptr.To("7"), w.deleteOpts[0].Preconditions.ResourceVersion)
	assert.Equal(t, ptr.To(types.UID("uid-a")), w.deleteOpts[0].Preconditions.UID)
}

func TestDeleteChannelsDeletesNoneWhenAnySpecMoved(t *testing.T) {
	w := &fakeWriter{}
	g := channelGetter{"a": readChannel("a", 2, "7"), "b": readChannel("b", 5, "9")}
	_, err := actions.DeleteChannels(t.Context(), g, w, "media", []actions.ChannelRef{{Name: "a", Generation: 2}, {Name: "b", Generation: 4}})
	require.True(t, apierrors.IsConflict(err), "%v", err)
	assert.Empty(t, w.deletes)
	_, err = actions.DeleteChannels(t.Context(), g, w, "media", []actions.ChannelRef{{Name: "a"}})
	require.ErrorIs(t, err, actions.ErrInvalid)
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
