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
