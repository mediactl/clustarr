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
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

// Settings CRUD (docs/superpowers/specs/2026-09-24-settings-crud-design.md):
// the Settings page adds, edits and deletes every kind it shows, through
// forms derived from the CRDs, with credentials written to Secrets the
// pages never read.

func usenetClient() *downloadv1.DownloadClient {
	return &downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "eweka", Namespace: "media"},
		Spec: downloadv1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet, Enabled: ptr.To(true), Priority: 2,
			Categories: map[string]string{"movie": "films"},
			Usenet: &downloadv1.UsenetSpec{Providers: []downloadv1.NNTPProvider{{
				Name: "main", Host: "news.eweka.nl", Port: 563, TLS: ptr.To(true), Connections: 8,
				SecretRef: corev1.LocalObjectReference{Name: "eweka-main-credentials"},
			}}},
		},
	}
}

func settingsServer(t *testing.T, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	return ui.NewServer(t.Context(), ui.Options{Reader: c, Actions: actions.New(c), Namespace: "media"}), c
}

func get(t *testing.T, srv *ui.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func post(t *testing.T, srv *ui.Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSettingsPageLinksAddEditAndDelete(t *testing.T) {
	qp := &catalogv1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd-1080p"}, Spec: catalogv1.QualityProfileSpec{
		MediaKind: catalogv1.ProfileMediaKindVideo, Tiers: []catalogv1.Tier{{Name: "hd", Qualities: []string{"WEBDL-1080p"}}}, Cutoff: "hd",
	}}
	srv, _ := settingsServer(t, usenetClient(), qp)
	rec := get(t, srv, "/settings")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, k := range actions.ConfigKinds() {
		require.Contains(t, body, `href="/settings/new/`+k.Slug+`"`, "an Add link per kind")
	}
	require.Contains(t, body, `href="/settings/edit/downloadclients/media/eweka"`, "a namespaced kind's Edit link")
	require.Contains(t, body, `href="/settings/edit/qualityprofiles/-/hd-1080p"`, "a cluster-scoped kind's Edit link has no namespace")
	del := tagWith(t, body, `action="/settings/delete/downloadclients/media/eweka"`)
	require.Contains(t, del, "data-confirm=", "a delete asks first")
	require.Contains(t, body, `action="/settings/delete/qualityprofiles/-/hd-1080p"`)
}

func TestNewSettingsFormRendersTheOverlay(t *testing.T) {
	srv, _ := settingsServer(t)
	rec := get(t, srv, "/settings/new/downloadclients")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "New download client")
	form := tagWith(t, body, "data-settings-form")
	require.Contains(t, form, `action="/settings/new/downloadclients"`)
	require.Contains(t, form, `method="post"`)
	requireTag(t, body, `name="__name"`, "required")
	requireTag(t, body, `name="__namespace"`, `value="media"`)
	requireTag(t, body, `data-section="Client"`)
	requireTag(t, body, `data-section="Torrent"`, `data-show-when="protocol=torrent"`)
	requireTag(t, body, `data-section="Usenet"`, `data-show-when="protocol=usenet"`)
	protocol := tagWith(t, body, `name="protocol"`)
	require.True(t, strings.HasPrefix(protocol, "<select"), protocol)
	require.Contains(t, body, `<option value="torrent"`)
	requireTag(t, body, `name="torrent.listenPort"`, `type="number"`, `placeholder="42069"`, `min="1"`, `max="65535"`)
	require.Contains(t, body, "Listen port")
	requireTag(t, body, `data-rows="usenet.providers"`)
	require.Contains(t, body, "<template data-row-template")
	requireTag(t, body, `name="usenet.providers.__i__.host"`, "required")
	requireTag(t, body, `name="__secret.usenet.providers.__i__.secretRef.password"`, `type="password"`, `autocomplete="new-password"`)
	requireTag(t, body, `data-map="categories"`)
	require.Regexp(t, regexp.MustCompile(`<input[^>]*type="hidden"[^>]*name="enabled"[^>]*value="false"`), body, "a checkbox posts false when unchecked")
	requireTag(t, body, `data-tui-checkbox-input`, `name="enabled"`, `value="true"`)
	require.NotContains(t, body, `name="resources`, "workload plumbing is not on the form")
	require.Contains(t, body, `<script src="/static/settings.js"`, "rows, conditions and confirms come from the settings script")

	require.Equal(t, http.StatusNotFound, get(t, srv, "/settings/new/widgets").Code)
	rec = get(t, srv, "/settings/new/qualityprofiles")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `name="__namespace"`, "a cluster-scoped kind takes no namespace")
}

func TestEditSettingsFormIsPrefilledAndNeverShowsASecret(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "eweka-main-credentials", Namespace: "media"}, StringData: map[string]string{"password": "hunter2"}, Data: map[string][]byte{"password": []byte("hunter2")}}
	srv, _ := settingsServer(t, usenetClient(), secret)
	rec := get(t, srv, "/settings/edit/downloadclients/media/eweka")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, "Edit download client")
	requireTag(t, body, `name="__name"`, `value="eweka"`, "readonly")
	requireTag(t, body, "data-settings-form", `action="/settings/edit/downloadclients/media/eweka"`)
	protocol := tagWith(t, body, `name="protocol"`)
	require.Contains(t, protocol, "disabled", "protocol is immutable")
	require.Regexp(t, regexp.MustCompile(`<input[^>]*type="hidden"[^>]*name="protocol"[^>]*value="usenet"`), body, "a read-only select still posts its value")
	requireTag(t, body, `name="priority"`, `value="2"`)
	requireTag(t, body, `name="usenet.providers.0.host"`, `value="news.eweka.nl"`)
	requireTag(t, body, `name="usenet.providers.0.secretRef.name"`, `value="eweka-main-credentials"`)
	requireTag(t, body, `name="categories.__k.0"`, `value="movie"`)
	requireTag(t, body, `name="categories.__v.0"`, `value="films"`)
	pw := tagWith(t, body, `name="__secret.usenet.providers.0.secretRef.password"`)
	require.NotContains(t, pw, "value=")
	require.NotContains(t, body, "hunter2", "a Secret's contents never reach a page")
	requireTag(t, body, `action="/settings/delete/downloadclients/media/eweka"`)

	require.Equal(t, http.StatusNotFound, get(t, srv, "/settings/edit/downloadclients/media/nope").Code)
}

func TestCreateSettingsObjectFromTheForm(t *testing.T) {
	srv, c := settingsServer(t)
	rec := post(t, srv, "/settings/new/downloadclients", url.Values{
		"__name": {"qbit"}, "__namespace": {"media"}, "protocol": {"torrent"}, "enabled": {"false", "true"}, "priority": {"3"},
		"torrent.listenPort": {"51413"}, "categories.__k.0": {"movie"}, "categories.__v.0": {"films"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/settings", rec.Header().Get("Location"))
	var dc downloadv1.DownloadClient
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "qbit"}, &dc))
	require.Equal(t, commonv1alpha1.ProtocolTorrent, dc.Spec.Protocol)
	require.Equal(t, int32(3), dc.Spec.Priority)
	require.True(t, *dc.Spec.Enabled)
	require.NotNil(t, dc.Spec.Torrent)
	require.Equal(t, int32(51413), dc.Spec.Torrent.ListenPort)
	require.Nil(t, dc.Spec.Usenet)
	require.Equal(t, map[string]string{"movie": "films"}, dc.Spec.Categories)
	require.Equal(t, actions.OriginUI, dc.Labels[actions.LabelOrigin])

	// A usenet client: the provider's credentials go to a Secret named after
	// the object and the server, and the provider's secretRef names it.
	rec = post(t, srv, "/settings/new/downloadclients", url.Values{
		"__name": {"eweka"}, "__namespace": {"media"}, "protocol": {"usenet"},
		"usenet.providers.0.name": {"main"}, "usenet.providers.0.host": {"news.eweka.nl"}, "usenet.providers.0.port": {"563"},
		"__secret.usenet.providers.0.secretRef.username": {"user"}, "__secret.usenet.providers.0.secretRef.password": {"pass"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "eweka"}, &dc))
	require.Len(t, dc.Spec.Usenet.Providers, 1)
	require.Equal(t, "eweka-main-credentials", dc.Spec.Usenet.Providers[0].SecretRef.Name)
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "eweka-main-credentials"}, &s))
	require.Equal(t, map[string]string{"username": "user", "password": "pass"}, s.StringData)

	// A cluster-scoped kind, from a form with rows and lines.
	rec = post(t, srv, "/settings/new/qualityprofiles", url.Values{
		"__name": {"my-hd"}, "mediaKind": {"video"}, "cutoff": {"hd"}, "upgradeAllowed": {"false", "true"},
		"tiers.0.name": {"hd"}, "tiers.0.qualities": {"WEBDL-1080p\nBluray-1080p"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var qp catalogv1.QualityProfile
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "my-hd"}, &qp))
	require.Equal(t, []catalogv1.Tier{{Name: "hd", Qualities: []string{"WEBDL-1080p", "Bluray-1080p"}}}, qp.Spec.Tiers)
	require.Equal(t, "hd", qp.Spec.Cutoff)
}

func TestUpdateSettingsObjectFromTheForm(t *testing.T) {
	srv, c := settingsServer(t, usenetClient())
	rec := post(t, srv, "/settings/edit/downloadclients/media/eweka", url.Values{
		"__name": {"eweka"}, "protocol": {"usenet"}, "priority": {"7"}, "enabled": {"false"},
		"usenet.providers.0.name": {"main"}, "usenet.providers.0.host": {"news.eweka.nl"}, "usenet.providers.0.port": {"563"},
		"usenet.providers.0.secretRef.name": {"eweka-main-credentials"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var dc downloadv1.DownloadClient
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "eweka"}, &dc))
	require.Equal(t, int32(7), dc.Spec.Priority)
	require.False(t, *dc.Spec.Enabled)
	require.Nil(t, dc.Spec.Categories, "a map the form no longer lists is cleared")
	require.Equal(t, commonv1alpha1.ProtocolUsenet, dc.Spec.Protocol)
	require.Equal(t, "eweka-main-credentials", dc.Spec.Usenet.Providers[0].SecretRef.Name, "an unchanged secretRef stays")
	var s corev1.Secret
	require.Error(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "eweka-main-credentials"}, &s), "blank credential inputs write no Secret")

	// Filling a credential on edit patches the Secret the object names.
	rec = post(t, srv, "/settings/edit/downloadclients/media/eweka", url.Values{
		"__name": {"eweka"}, "protocol": {"usenet"}, "priority": {"7"},
		"usenet.providers.0.name": {"main"}, "usenet.providers.0.host": {"news.eweka.nl"},
		"usenet.providers.0.secretRef.name":              {"eweka-main-credentials"},
		"__secret.usenet.providers.0.secretRef.password": {"new-pass"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "eweka-main-credentials"}, &s))
	require.Equal(t, map[string]string{"password": "new-pass"}, s.StringData)

	require.Equal(t, http.StatusNotFound, post(t, srv, "/settings/edit/downloadclients/media/nope", url.Values{"__name": {"nope"}, "protocol": {"usenet"}}).Code)
}

func TestSettingsFormErrorsRerenderWithTheMessage(t *testing.T) {
	srv, _ := settingsServer(t)
	rec := post(t, srv, "/settings/new/downloadclients", url.Values{"__name": {"qbit"}, "__namespace": {"media"}, "protocol": {"torrent"}, "priority": {"abc"}})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	requireTag(t, body, `data-action-error="invalid"`)
	require.Contains(t, body, "priority")
	requireTag(t, body, "data-settings-form", `action="/settings/new/downloadclients"`)
	requireTag(t, body, `name="__name"`, `value="qbit"`) // the form comes back with what was typed

	rec = post(t, srv, "/settings/new/downloadclients", url.Values{"__name": {"Bad Name"}, "__namespace": {"media"}, "protocol": {"torrent"}})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "RFC 1123", "the apiserver wording for a bad name")

	// An apiserver rejection (here: the object exists) is shown the same way.
	srv, _ = settingsServer(t, usenetClient())
	rec = post(t, srv, "/settings/new/downloadclients", url.Values{"__name": {"eweka"}, "__namespace": {"media"}, "protocol": {"usenet"}})
	require.Equal(t, http.StatusConflict, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-error="conflict"`)

	noWriter := ui.NewServer(t.Context(), ui.Options{Namespace: "media"})
	rec = post(t, noWriter, "/settings/new/downloadclients", url.Values{"__name": {"x"}, "__namespace": {"media"}, "protocol": {"torrent"}})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-error="no-writer"`)
}

func TestDeleteSettingsObjectFromTheForm(t *testing.T) {
	srv, c := settingsServer(t, usenetClient())
	rec := post(t, srv, "/settings/delete/downloadclients/media/eweka", url.Values{"return": {"/settings"}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/settings", rec.Header().Get("Location"))
	var u unstructured.Unstructured
	u.SetGroupVersionKind(downloadv1.GroupVersion.WithKind("DownloadClient"))
	require.Error(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "eweka"}, &u))
	require.Equal(t, http.StatusNotFound, post(t, srv, "/settings/delete/downloadclients/media/eweka", nil).Code, "deleting what is gone is a 404")
}

func TestIndexerFormOffersDefinitionsClientsAndProxies(t *testing.T) {
	def := &indexv1.IndexerDefinition{ObjectMeta: metav1.ObjectMeta{Name: "1337x"}, Spec: indexv1.IndexerDefinitionSpec{YAML: "id: 1337x\nname: 1337x"}, Status: indexv1.IndexerDefinitionStatus{ID: "1337x", Name: "1337x", Type: "public"}}
	proxy := &indexv1.IndexerProxy{ObjectMeta: metav1.ObjectMeta{Name: "flare", Namespace: "media"}}
	dc := &downloadv1.DownloadClient{ObjectMeta: metav1.ObjectMeta{Name: "qbit", Namespace: "media"}, Spec: downloadv1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolTorrent, Torrent: &downloadv1.TorrentSpec{}}}
	srv, _ := settingsServer(t, def, proxy, dc)
	rec := get(t, srv, "/settings/new/indexers")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	definition := tagWith(t, body, `name="definition"`)
	require.True(t, strings.HasPrefix(definition, "<select"), "definitions come from the cluster's IndexerDefinitions: %s", definition)
	require.Contains(t, body, `<option value="1337x"`)
	require.Contains(t, body, "1337x (public)")
	require.Contains(t, body, `<option value="qbit"`)
	require.Contains(t, body, `<option value="flare"`)
	requireTag(t, body, `data-section="Generic Newznab/Torznab"`, `data-show-when="definition="`)
	requireTag(t, body, `name="__secret.secretRef.apikey"`, `type="password"`)
}
