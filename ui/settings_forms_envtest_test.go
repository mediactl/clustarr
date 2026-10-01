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

package ui_test

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestSettingsFormsNeverWriteASecretTheyMustNot runs the Settings forms
// against a real apiserver, which alone can show the two guards on the
// credentials path: a Secret the UI did not create is refused by the
// patch's own label test (the role may patch any Secret and read none),
// and a create the apiserver refuses -- its dry run, before any Secret is
// written -- writes no Secret. The fake client neither runs a dry run nor
// applies stringData, so neither is visible in settings_forms_test.go.
func TestSettingsFormsNeverWriteASecretTheyMustNot(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	c, err := client.New(cfg, client.Options{Scheme: ui.MustNewReaderScheme()})
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "media"}}))
	srv := ui.NewServer(ctx, ui.Options{Reader: c, Actions: actions.New(c), Namespace: "media"})

	secret := func(t *testing.T, name string, labels map[string]string, data map[string][]byte) *corev1.Secret {
		t.Helper()
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, Labels: labels}, Data: data}
		require.NoError(t, c.Create(ctx, s))
		return s
	}
	requireUntouched := func(t *testing.T, want *corev1.Secret) {
		t.Helper()
		var got corev1.Secret
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(want), &got))
		require.Equal(t, want.Data, got.Data, "Secret %s must keep its data", want.Name)
		require.Equal(t, want.ResourceVersion, got.ResourceVersion, "nothing may have been written to Secret %s", want.Name)
	}
	usenetForm := func(name, secretName string) url.Values {
		return url.Values{
			"__name": {name}, "__namespace": {"media"}, "protocol": {"usenet"},
			"usenet.providers.0.name": {"main"}, "usenet.providers.0.host": {"news.example.org"}, "usenet.providers.0.port": {"563"},
			"usenet.providers.0.secretRef.name":              {secretName},
			"__secret.usenet.providers.0.secretRef.username": {"intruder"},
			"__secret.usenet.providers.0.secretRef.password": {"injected"},
		}
	}

	t.Run("a new object naming a Secret the UI did not create is refused and writes nothing", func(t *testing.T) {
		foreign := secret(t, "ingress-tls", nil, map[string][]byte{"tls.key": []byte("orig")})

		rec := post(t, srv, "/settings/new/downloadclients", usenetForm("intruder", foreign.Name))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
		require.Contains(t, rec.Body.String(), "kubectl", "the message says where the Secret is edited instead")
		requireUntouched(t, foreign)

		var dc downloadv1.DownloadClient
		getErr := c.Get(ctx, types.NamespacedName{Namespace: "media", Name: "intruder"}, &dc)
		require.True(t, apierrors.IsNotFound(getErr), "a refused Secret must leave no object behind: %v", getErr)
	})

	t.Run("a new object whose name is taken never touches the existing object's Secret", func(t *testing.T) {
		creds := secret(t, "eweka-main-credentials", map[string]string{actions.LabelOrigin: actions.OriginUI},
			map[string][]byte{"username": []byte("owner"), "password": []byte("orig")})
		require.NoError(t, c.Create(ctx, usenetClient()))

		rec := post(t, srv, "/settings/new/downloadclients", usenetForm("eweka", creds.Name))
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		requireTag(t, rec.Body.String(), `data-action-error="conflict"`)
		requireUntouched(t, creds)
	})

	t.Run("an edit the apiserver refuses never touches the object's Secret", func(t *testing.T) {
		creds := secret(t, "edited-credentials", map[string]string{actions.LabelOrigin: actions.OriginUI},
			map[string][]byte{"password": []byte("orig")})
		dc := usenetClient()
		dc.Name = "edited"
		require.NoError(t, c.Create(ctx, dc))
		form := usenetForm(dc.Name, creds.Name)
		form.Set("replicas", "2") // the form takes it; only the CRD's CEL rule refuses it
		rec := post(t, srv, "/settings/edit/downloadclients/media/edited", form)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "replicas == 1", "the apiserver's refusal, not the form's")
		requireUntouched(t, creds)
	})

	t.Run("a new object's credentials land in its UI-made Secret", func(t *testing.T) {
		rec := post(t, srv, "/settings/new/downloadclients", usenetForm("fresh", ""))
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

		var dc downloadv1.DownloadClient
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "media", Name: "fresh"}, &dc))
		require.Equal(t, "fresh-main-credentials", dc.Spec.Usenet.Providers[0].SecretRef.Name)
		var s corev1.Secret
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "media", Name: "fresh-main-credentials"}, &s))
		require.Equal(t, map[string][]byte{"username": []byte("intruder"), "password": []byte("injected")}, s.Data)
		require.Equal(t, actions.OriginUI, s.Labels[actions.LabelOrigin])

		// An edit filling one credential patches it into that same Secret.
		form := usenetForm("fresh", "fresh-main-credentials")
		form.Del("__secret.usenet.providers.0.secretRef.username")
		form.Set("__secret.usenet.providers.0.secretRef.password", "rotated")
		rec = post(t, srv, "/settings/edit/downloadclients/media/fresh", form)
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "media", Name: "fresh-main-credentials"}, &s))
		require.Equal(t, map[string][]byte{"username": []byte("intruder"), "password": []byte("rotated")}, s.Data)
	})
}
