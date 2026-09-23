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

package usenet_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/grabarr/engine/usenet"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func ptr[T any](v T) *T { return &v }

// TestPostProcessFromSpec is the regression test for D2-6's carried
// hazard: pkg/download/usenet.Config.PostProcess is a plain (non-pointer)
// struct whose Go zero value is every switch OFF, while PostProcessSpec's
// three pointers each default to true on the CRD. Treating a nil pointer as
// false rather than "operator configured nothing" ships an engine that
// silently repairs, unpacks and cleans up nothing.
func TestPostProcessFromSpec(t *testing.T) {
	tests := []struct {
		name string
		in   *downloadv1alpha1.PostProcessSpec
		want usenetPostProcess
	}{
		{
			name: "nil spec means every CRD default is on",
			in:   nil,
			want: usenetPostProcess{Par2: true, Unpack: true, DeleteArchives: true},
		},
		{
			name: "empty spec (every pointer nil) means the same as nil",
			in:   &downloadv1alpha1.PostProcessSpec{},
			want: usenetPostProcess{Par2: true, Unpack: true, DeleteArchives: true},
		},
		{
			name: "an explicit false overrides its default",
			in:   &downloadv1alpha1.PostProcessSpec{Par2: ptr(false)},
			want: usenetPostProcess{Par2: false, Unpack: true, DeleteArchives: true},
		},
		{
			name: "explicit false on the other two, independently",
			in:   &downloadv1alpha1.PostProcessSpec{Unpack: ptr(false), DeleteArchives: ptr(false)},
			want: usenetPostProcess{Par2: true, Unpack: false, DeleteArchives: false},
		},
		{
			name: "an explicit true is honoured identically to the default",
			in:   &downloadv1alpha1.PostProcessSpec{Par2: ptr(true), Unpack: ptr(true), DeleteArchives: ptr(true)},
			want: usenetPostProcess{Par2: true, Unpack: true, DeleteArchives: true},
		},
		{
			name: "cleanup patterns pass through untouched",
			in:   &downloadv1alpha1.PostProcessSpec{CleanupPatterns: []string{"*.nfo", "*sample*"}},
			want: usenetPostProcess{Par2: true, Unpack: true, DeleteArchives: true, CleanupPatterns: []string{"*.nfo", "*sample*"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := usenetengine.PostProcessFromSpec(tt.in)
			assert.Equal(t, tt.want.Par2, got.Par2)
			assert.Equal(t, tt.want.Unpack, got.Unpack)
			assert.Equal(t, tt.want.DeleteArchives, got.DeleteArchives)
			assert.Equal(t, tt.want.CleanupPatterns, got.CleanupPatterns)
		})
	}
}

// usenetPostProcess mirrors pkg/download/usenet.PostProcess's fields so this
// test does not need to import that package under a second alias just to
// name the type being compared against.
type usenetPostProcess struct {
	Par2            bool
	Unpack          bool
	DeleteArchives  bool
	CleanupPatterns []string
}

func fakeClientWithScheme(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, k8s.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestBuildConfigResolvesProvidersAndDefaultsPostProcess(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "usenet-creds", Namespace: "media"},
		Data:       map[string][]byte{"username": []byte("alice"), "password": []byte("s3cret")},
	}
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd", Namespace: "media"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{
					{
						Name:        "primary",
						Host:        "news.example.invalid",
						SecretRef:   corev1.LocalObjectReference{Name: "usenet-creds"},
						Connections: 10,
					},
				},
			},
		},
	}
	c := fakeClientWithScheme(t, dc, secret)

	cfg, err := usenetengine.BuildConfig(t.Context(), c, dc, "/data", "/scratch")
	require.NoError(t, err)

	require.Len(t, cfg.Providers, 1)
	p := cfg.Providers[0]
	assert.Equal(t, "primary", p.Name)
	assert.Equal(t, "news.example.invalid", p.Host)
	assert.Equal(t, "alice", p.Username)
	assert.Equal(t, "s3cret", p.Password)
	assert.Equal(t, 10, p.Connections)
	// TLS/Port default per usenetclient.ProviderFromSpec, restated here only
	// to prove BuildConfig did not bypass it.
	assert.True(t, p.TLS)
	assert.Equal(t, 563, p.Port)

	assert.Equal(t, "/data", cfg.DataDir)
	assert.Equal(t, "/scratch", cfg.ScratchDir)
	// The whole point of this test: PostProcess was never set on the CRD
	// object above, and the result must still be every switch on.
	assert.True(t, cfg.PostProcess.Par2)
	assert.True(t, cfg.PostProcess.Unpack)
	assert.True(t, cfg.PostProcess.DeleteArchives)
}

func TestBuildConfigFailsOnMissingSecret(t *testing.T) {
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd", Namespace: "media"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{
					{Name: "primary", Host: "news.example.invalid", SecretRef: corev1.LocalObjectReference{Name: "missing"}},
				},
			},
		},
	}
	c := fakeClientWithScheme(t, dc)

	_, err := usenetengine.BuildConfig(t.Context(), c, dc, "/data", "/scratch")
	require.Error(t, err)
}

func TestLoadDownloadClientRejectsTorrentProtocol(t *testing.T) {
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbit", Namespace: "media"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	c := fakeClientWithScheme(t, dc)

	_, err := usenetengine.LoadDownloadClient(t.Context(), c, "media", "qbit")
	require.Error(t, err)
}

func TestLoadDownloadClientRejectsMissingUsenetSpec(t *testing.T) {
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd", Namespace: "media"},
		Spec:       downloadv1alpha1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolUsenet},
	}
	c := fakeClientWithScheme(t, dc)

	_, err := usenetengine.LoadDownloadClient(t.Context(), c, "media", "sabnzbd")
	require.Error(t, err)
}
