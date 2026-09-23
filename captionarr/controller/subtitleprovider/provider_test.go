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

package subtitleprovider

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestImplementedTypesPerRulingR5(t *testing.T) {
	assert.True(t, implemented(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom))
	assert.True(t, implemented(subtitlev1alpha1.SubtitleProviderGestdown))
	assert.True(t, implemented(subtitlev1alpha1.SubtitleProviderEmbedded))

	// R5: no client behind these three -- Ready=False with a clear reason,
	// never an error loop.
	assert.False(t, implemented(subtitlev1alpha1.SubtitleProviderSubDL))
	assert.False(t, implemented(subtitlev1alpha1.SubtitleProviderSubSource))
	assert.False(t, implemented(subtitlev1alpha1.SubtitleProviderWhisper))
}

func TestRequiredSecretKeysMatchesEachProvidersCapabilities(t *testing.T) {
	// pkg/subtitles/providers/opensubtitlescom/client.go's Capabilities:
	// NeedsSecrets: []string{"apiKey", "username", "password"}.
	assert.ElementsMatch(t, []string{
		subtitlev1alpha1.ProviderSecretKeyAPIKey,
		subtitlev1alpha1.ProviderSecretKeyUsername,
		subtitlev1alpha1.ProviderSecretKeyPassword,
	}, requiredSecretKeys(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom))

	assert.Empty(t, requiredSecretKeys(subtitlev1alpha1.SubtitleProviderGestdown))
	assert.Empty(t, requiredSecretKeys(subtitlev1alpha1.SubtitleProviderEmbedded))
}

func TestHIVerifiableMatchesEachShippedProvider(t *testing.T) {
	assert.True(t, hiVerifiable(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom))
	assert.True(t, hiVerifiable(subtitlev1alpha1.SubtitleProviderGestdown))
	assert.True(t, hiVerifiable(subtitlev1alpha1.SubtitleProviderEmbedded))
	assert.False(t, hiVerifiable(subtitlev1alpha1.SubtitleProviderSubDL))
}

func secretRef(name string) *corev1.LocalObjectReference {
	return &corev1.LocalObjectReference{Name: name}
}

func TestCheckAuthenticationNoCredentialsRequired(t *testing.T) {
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderGestdown, nil, nil, nil)
	assert.True(t, res.authenticated)
	assert.Equal(t, ReasonNoCredentialsRequired, res.reason)
}

func TestCheckAuthenticationNoSecretRef(t *testing.T) {
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, nil, nil, nil)
	assert.False(t, res.authenticated)
	assert.Equal(t, k8s.ReasonDependencyNotReady, res.reason)
	assert.Contains(t, res.message, "secretRef")
}

func TestCheckAuthenticationSecretNotFound(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "creds")
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, secretRef("creds"), nil, notFound)
	assert.False(t, res.authenticated)
	assert.Equal(t, k8s.ReasonDependencyNotReady, res.reason)
	assert.Contains(t, res.message, "creds")
}

func TestCheckAuthenticationMissingKeys(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data:       map[string][]byte{"apiKey": []byte("k")}, // username, password missing
	}
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, secretRef("creds"), secret, nil)
	assert.False(t, res.authenticated)
	assert.Equal(t, k8s.ReasonDependencyNotReady, res.reason)
	assert.Contains(t, res.message, "username")
	assert.Contains(t, res.message, "password")
	assert.NotContains(t, res.message, "apiKey,")
}

func TestCheckAuthenticationEveryKeyPresent(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data: map[string][]byte{
			"apiKey":   []byte("k"),
			"username": []byte("u"),
			"password": []byte("p"),
		},
	}
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, secretRef("creds"), secret, nil)
	require.True(t, res.authenticated)
	assert.Equal(t, ReasonCredentialsPresent, res.reason)
}

func TestCheckAuthenticationRejectsEmptyValue(t *testing.T) {
	// An explicitly present but empty-valued key must not count as present --
	// len(secret.Data[key]) == 0 catches both "absent" and "zero bytes".
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds"},
		Data: map[string][]byte{
			"apiKey":   []byte("k"),
			"username": []byte("u"),
			"password": []byte(""),
		},
	}
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, secretRef("creds"), secret, nil)
	assert.False(t, res.authenticated)
	assert.Contains(t, res.message, "password")
}

func TestCheckAuthenticationOtherErrorStillReportsDependencyNotReady(t *testing.T) {
	// checkAuthentication itself never distinguishes NotFound from any other
	// pre-fetched error -- that split is the caller's (Reconcile treats a
	// non-NotFound error as a hard reconcile failure and never reaches this
	// function with one). This just proves the function does not panic or
	// misreport when handed an arbitrary error.
	res := checkAuthentication(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, secretRef("creds"), nil, errors.New("boom"))
	assert.False(t, res.authenticated)
	assert.Contains(t, res.message, "boom")
}
