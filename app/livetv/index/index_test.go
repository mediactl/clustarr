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

package index_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/index"
	"github.com/mediactl/clustarr/pkg/names"
)

// A provider's channels come in name order whatever order the index holds
// them in: the tuner takes the same channels in another order for a changed
// configuration and renders the guide again (talos, 2026-11-03).
func TestChannelsOfListsInNameOrder(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, clustarrv1.AddToScheme(s))
	var objs []client.Object
	for _, n := range []string{"zulu", "alpha", "mike", "bravo"} {
		objs = append(objs, &clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: n},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: "argon", Key: "t:" + n, Number: "1"},
		})
	}
	objs = append(objs, &clustarrv1.IPTVChannel{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "other"},
		Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: "xenon", Key: "t:other", Number: "1"},
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithIndex(&clustarrv1.IPTVChannel{}, index.ChannelByProvider, index.Extract).Build()

	chans, err := index.ChannelsOf(context.Background(), c, "media", "argon")
	require.NoError(t, err)
	names := make([]string, 0, len(chans))
	for i := range chans {
		names = append(names, chans[i].Name)
	}
	assert.Equal(t, []string{"alpha", "bravo", "mike", "zulu"}, names)
}

// TestChannelsByKeyKeepsTheCanonicalChannelElseTheFirstByName: one channel a
// key, whatever order the channels come in -- the one named by
// names.ChannelObjectName (the lineup's and the ui's name for the key), else
// the first by name -- so the lineup controller and the provider controller
// choose alike.
func TestChannelsByKeyKeepsTheCanonicalChannelElseTheFirstByName(t *testing.T) {
	ch := func(name, key string) clustarrv1.IPTVChannel {
		return clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: "argon", Key: key},
		}
	}
	canonical := names.ChannelObjectName("argon", "t:espn")
	for name, chans := range map[string][]clustarrv1.IPTVChannel{
		"the canonical last by name": {ch("aaa-espn-copy", "t:espn"), ch(canonical, "t:espn"), ch("zzz-espn", "t:espn"), ch("cnn", "t:cnn")},
		"the canonical first":        {ch(canonical, "t:espn"), ch("aaa-espn-copy", "t:espn"), ch("cnn", "t:cnn")},
	} {
		t.Run(name, func(t *testing.T) {
			got := index.ChannelsByKey("argon", chans)
			require.Len(t, got, 2)
			assert.Equal(t, canonical, got["t:espn"].Name)
			assert.Equal(t, "cnn", got["t:cnn"].Name)
			for i := range chans {
				if chans[i].Name == canonical {
					assert.Same(t, &chans[i], got["t:espn"], "a pointer into the list")
				}
			}
		})
	}

	// No canonical name: the first by name, in any order.
	for _, chans := range [][]clustarrv1.IPTVChannel{
		{ch("mike", "t:x"), ch("bravo", "t:x"), ch("zulu", "t:x")},
		{ch("zulu", "t:x"), ch("mike", "t:x"), ch("bravo", "t:x")},
	} {
		assert.Equal(t, "bravo", index.ChannelsByKey("argon", chans)["t:x"].Name)
	}
	assert.Empty(t, index.ChannelsByKey("argon", nil))
}
