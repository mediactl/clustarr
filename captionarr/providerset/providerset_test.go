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

package providerset_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/providerset"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const ns = "media"

func provider(name string, typ subtitlev1alpha1.SubtitleProviderType, prio int32, secret string) *subtitlev1alpha1.SubtitleProvider {
	sp := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name), Generation: 1},
		Spec: subtitlev1alpha1.SubtitleProviderSpec{
			Type: typ, Enabled: true, Priority: prio, RequestsPerSecondMilli: 5000,
		},
	}
	if secret != "" {
		sp.Spec.SecretRef = &corev1.LocalObjectReference{Name: secret}
	}
	return sp
}

func osSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("sec-" + name)}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

var fullOSCreds = map[string]string{"apiKey": "k", "username": "u", "password": "p"}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
}

func names(es []providerset.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func TestBuildOrdersByPriorityThenNameAndSkipsWhatItCannotBuild(t *testing.T) {
	disabled := provider("off", subtitlev1alpha1.SubtitleProviderGestdown, 1, "")
	disabled.Spec.Enabled = false
	c := newClient(t,
		provider("zeta", subtitlev1alpha1.SubtitleProviderGestdown, 20, ""),
		provider("alpha", subtitlev1alpha1.SubtitleProviderGestdown, 20, ""),
		provider("first", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, 5, "os"),
		provider("local", subtitlev1alpha1.SubtitleProviderEmbedded, 50, ""),
		provider("subdl", subtitlev1alpha1.SubtitleProviderSubDL, 1, ""),     // no client (R5)
		provider("whisper", subtitlev1alpha1.SubtitleProviderWhisper, 1, ""), // no client (R5)
		provider("nocreds", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, 1, "missing"),
		disabled,
		osSecret("os", fullOSCreds),
	)
	b := providerset.NewBuilder(c, c)

	got, err := b.Build(context.Background(), ns)
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "alpha", "zeta", "local"}, names(got))

	for _, e := range got {
		assert.Equal(t, e.Type == subtitlev1alpha1.SubtitleProviderEmbedded, e.Local(), e.Name)
		assert.Equal(t, "uid-"+e.Name, e.UID, "the throttle KV is keyed by the provider's UID")
		assert.Equal(t, int32(5000), e.RateMilli)
		if e.Local() {
			assert.Nil(t, e.Client)
			p := e.Provider(providerset.FileSource{Path: "/data/x.mkv"})
			require.NotNil(t, p)
			assert.Equal(t, "embedded", p.Name())
		} else {
			require.NotNil(t, e.Client)
		}
	}
}

func TestEntryReportsWhyAProviderCannotBeBuilt(t *testing.T) {
	c := newClient(t, osSecret("partial", map[string]string{"apiKey": "k", "username": "u"}))
	b := providerset.NewBuilder(c, c)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		sp   *subtitlev1alpha1.SubtitleProvider
		want error
	}{
		{"subsource has no client", provider("a", subtitlev1alpha1.SubtitleProviderSubSource, 1, ""), providerset.ErrNoClient},
		{"opensubtitles without a secretRef", provider("b", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, 1, ""), providerset.ErrMissingSecret},
		{"opensubtitles with a missing secret", provider("c", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, 1, "nope"), providerset.ErrMissingSecret},
		{"opensubtitles without a password", provider("d", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, 1, "partial"), providerset.ErrMissingSecret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := b.Entry(ctx, tc.sp)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v", err)
		})
	}
}

// The cache exists so the OpenSubtitles client -- which holds its login
// token -- survives from one fetch task to the next. It must still be
// rebuilt when the credentials or the provider spec change.
func TestEntryReusesARemoteClientUntilItsSpecOrSecretChanges(t *testing.T) {
	ctx := context.Background()
	sp := provider("os", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, 1, "os")
	sec := osSecret("os", fullOSCreds)
	c := newClient(t, sp, sec)
	b := providerset.NewBuilder(c, c)

	first, err := b.Entry(ctx, sp)
	require.NoError(t, err)
	again, err := b.Entry(ctx, sp)
	require.NoError(t, err)
	assert.Same(t, first.Client, again.Client, "an unchanged provider reuses its client (and its login)")

	var live corev1.Secret
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(sec), &live))
	live.Data["password"] = []byte("rotated")
	require.NoError(t, c.Update(ctx, &live))
	rotated, err := b.Entry(ctx, sp)
	require.NoError(t, err)
	assert.NotSame(t, first.Client, rotated.Client, "rotated credentials build a new client")

	bumped := sp.DeepCopy()
	bumped.Generation++
	respec, err := b.Entry(ctx, bumped)
	require.NoError(t, err)
	assert.NotSame(t, rotated.Client, respec.Client, "a spec change builds a new client")
}

func TestOrderAppliesTheProfilesProviderList(t *testing.T) {
	entries := []providerset.Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	assert.Equal(t, []string{"a", "b", "c"}, names(providerset.Order(entries, nil)), "no list keeps priority order")
	assert.Equal(t, []string{"c", "a"}, names(providerset.Order(entries, []string{"c", "gone", "a", "c"})),
		"a list picks and orders, dropping unknown and repeated names")
}

func TestServesComparesNormalisedLanguageTags(t *testing.T) {
	assert.True(t, providerset.Entry{}.Serves([]string{"fr"}), "no restriction serves everything")
	e := providerset.Entry{Languages: []string{"pt_BR", "eng"}}
	assert.True(t, e.Serves([]string{"pt-BR"}))
	assert.True(t, e.Serves([]string{"en"}), "ISO 639-2 eng normalises to en")
	assert.False(t, e.Serves([]string{"pt"}), "pt and pt-BR are distinct")
}
