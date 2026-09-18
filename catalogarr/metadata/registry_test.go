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

package metadata

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func enabled() *bool { b := true; return &b }

func TestBuildRegistryWiresOnlyTheImplementedTypesInPriorityOrder(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb-key", Namespace: "clustarr"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("test-key")},
	}
	providers := []catalogv1alpha1.MetadataProvider{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tmdb-backup", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(), Priority: 90,
				SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tmdb-primary", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(), Priority: 10,
				SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "off", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: catalogv1alpha1.MetadataProviderTVDB, Enabled: func() *bool { b := false; return &b }(),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "no-client-yet", Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderFanart, Enabled: enabled()},
		},
	}

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()
	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.NoError(t, err)

	require.Len(t, reg.Movies, 2, "both tmdb providers wire a MovieProvider; the disabled tvdb and unimplemented fanart do not")
	require.Equal(t, "tmdb", reg.Movies[0].Name())
	require.Empty(t, reg.Series, "tvdb was disabled; tmdb implements MovieProvider only (see Judgment call 3)")
}

func TestBuildRegistryErrorsOnAMissingSecret(t *testing.T) {
	providers := []catalogv1alpha1.MetadataProvider{{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "clustarr"},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(),
			SecretRef: &corev1.LocalObjectReference{Name: "does-not-exist"},
		},
	}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	_, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.Error(t, err)
}
