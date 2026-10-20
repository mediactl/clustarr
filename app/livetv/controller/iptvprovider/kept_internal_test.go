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

package iptvprovider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// stubTuner is a tuner that has fetched (kept non-nil) or not, and records
// what is restored into it.
type stubTuner struct {
	fetched  bool
	kept     *tuner.KeptPlaylist
	restored []tuner.KeptPlaylist
}

func (*stubTuner) Apply(tuner.Config)          {}
func (*stubTuner) Remove(types.NamespacedName) {}
func (s *stubTuner) Snapshot(types.NamespacedName) (tuner.Snapshot, bool) {
	return tuner.Snapshot{PlaylistFetched: s.fetched}, true
}
func (s *stubTuner) Kept(types.NamespacedName) (tuner.KeptPlaylist, bool) {
	if s.kept == nil {
		return tuner.KeptPlaylist{}, false
	}
	return *s.kept, true
}
func (s *stubTuner) Restore(_ types.NamespacedName, k tuner.KeptPlaylist) {
	s.restored = append(s.restored, k)
}

func keptFixture() tuner.KeptPlaylist {
	return tuner.KeptPlaylist{
		Hash: "abc", FetchedAt: time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC),
		Entries: 53483, Groups: 400, Candidates: 501,
		Channels: []m3u.Entry{{Key: "n:fyi", Name: "FYI", Group: "USA: ENTERTAINMENT", URL: "http://panel.example/live/u/p/1118.ts"}},
	}
}

func keptProvider(name string) *clustarrv1.IPTVProvider {
	return &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "clustarr-system", Name: name, UID: types.UID(name + "-uid")}}
}

func keptSecret(t *testing.T, c client.Client, p *clustarrv1.IPTVProvider) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: p.Namespace, Name: KeptSecretName(p.Name)}, &s))
	return &s
}

// TestAFetchedPlaylistIsKeptAndRestoredAfterARestart: the controller writes
// the tuner's kept playlist into the provider's Secret once per change, and
// after a restart hands it to a tuner that has fetched nothing.
func TestAFetchedPlaylistIsKeptAndRestoredAfterARestart(t *testing.T) {
	ctx := context.Background()
	p := keptProvider("freeiptv")
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(p).Build()
	k := keptFixture()

	st := &stubTuner{fetched: true, kept: &k}
	r := &Reconciler{Client: c, SecretReader: c, Tuner: st}
	require.NoError(t, r.restoreKept(ctx, p))
	assert.Empty(t, st.restored, "no Secret yet")
	require.NoError(t, r.keepPlaylist(ctx, p))
	s := keptSecret(t, c, p)
	require.Len(t, s.OwnerReferences, 1)
	assert.Equal(t, p.UID, s.OwnerReferences[0].UID, "deleted with its provider")
	got, err := decodeKept(s.Data[KeptSecretKey])
	require.NoError(t, err)
	assert.Equal(t, k, got)
	rv := s.ResourceVersion
	require.NoError(t, r.keepPlaylist(ctx, p))
	assert.Equal(t, rv, keptSecret(t, c, p).ResourceVersion, "an unchanged playlist is not written again")

	restarted := &stubTuner{}
	r2 := &Reconciler{Client: c, SecretReader: c, Tuner: restarted}
	require.NoError(t, r2.restoreKept(ctx, p))
	require.Len(t, restarted.restored, 1)
	assert.Equal(t, k, restarted.restored[0])
	require.NoError(t, r2.restoreKept(ctx, p))
	assert.Len(t, restarted.restored, 1, "read once per process")

	fetched := &stubTuner{fetched: true}
	r3 := &Reconciler{Client: c, SecretReader: c, Tuner: fetched}
	require.NoError(t, r3.restoreKept(ctx, p))
	assert.Empty(t, fetched.restored, "a tuner that has fetched keeps its own")
}

// TestASecretOfTheNameThatIsNotTheProvidersIsLeftAlone: the controller
// neither restores from nor writes over a Secret its provider does not own.
func TestASecretOfTheNameThatIsNotTheProvidersIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	p := keptProvider("other")
	data, err := encodeKept(keptFixture())
	require.NoError(t, err)
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: KeptSecretName(p.Name)},
		Data:       map[string][]byte{KeptSecretKey: data, "mine": []byte("x")},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(p, foreign).Build()
	k := keptFixture()
	k.Hash = "new"
	st := &stubTuner{kept: &k}
	r := &Reconciler{Client: c, SecretReader: c, Tuner: st}
	require.NoError(t, r.restoreKept(ctx, p))
	assert.Empty(t, st.restored)
	require.NoError(t, r.keepPlaylist(ctx, p))
	s := keptSecret(t, c, p)
	assert.Empty(t, s.OwnerReferences)
	assert.Equal(t, []byte("x"), s.Data["mine"])
	assert.Equal(t, data, s.Data[KeptSecretKey], "not written over")
}
