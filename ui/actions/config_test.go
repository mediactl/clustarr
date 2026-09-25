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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/ui/actions"
)

// The eight Settings-page kinds the UI configures (settings CRUD design,
// 2026-09-24), each with the group, kind and resource its writes name and
// whether it is namespaced.
func TestConfigKindsAreTheSettingsPageKinds(t *testing.T) {
	kinds := actions.ConfigKinds()
	bySlug := map[string]actions.ConfigKind{}
	for _, k := range kinds {
		bySlug[k.Slug] = k
	}
	require.Len(t, kinds, 9)
	for slug, want := range map[string]struct {
		group, kind, resource string
		namespaced            bool
	}{
		"rootfolders":       {"catalog.clustarr.io", "RootFolder", "rootfolders", true},
		"qualityprofiles":   {"catalog.clustarr.io", "QualityProfile", "qualityprofiles", false},
		"metadataproviders": {"catalog.clustarr.io", "MetadataProvider", "metadataproviders", true},
		"indexers":          {"index.clustarr.io", "Indexer", "indexers", true},
		"downloadclients":   {"download.clustarr.io", "DownloadClient", "downloadclients", true},
		"subtitleproviders": {"subtitle.clustarr.io", "SubtitleProvider", "subtitleproviders", true},
		"subtitleprofiles":  {"subtitle.clustarr.io", "SubtitleProfile", "subtitleprofiles", false},
		"transcodeprofiles": {"transcode.clustarr.io", "TranscodeProfile", "transcodeprofiles", false},
	} {
		k, ok := bySlug[slug]
		require.True(t, ok, slug)
		require.Equal(t, want.group, k.Group, slug)
		require.Equal(t, want.kind, k.Kind, slug)
		require.Equal(t, want.resource, k.Resource, slug)
		require.Equal(t, want.namespaced, k.Namespaced, slug)
		require.Equal(t, "v1alpha1", k.Version, slug)
		got, ok := actions.ConfigKindBySlug(slug)
		require.True(t, ok)
		require.Equal(t, k, got)
	}
	_, ok := actions.ConfigKindBySlug("secrets")
	require.False(t, ok, "a Secret is not a settings kind; WriteSecret is its own action")
}

// CreateConfig sends one unstructured object of the kind under the UI's
// manager, labelled as UI-made like every object this package creates.
func TestCreateConfigSendsOneLabelledObjectUnderTheUIManager(t *testing.T) {
	w := &fakeWriter{}
	dc, _ := actions.ConfigKindBySlug("downloadclients")
	spec := map[string]any{"protocol": "torrent", "torrent": map[string]any{"listenPort": int64(51413)}}
	obj, err := actions.CreateConfig(context.Background(), w, dc, "media", "qbit", spec)
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	require.Empty(t, w.patches)
	u, ok := w.creates[0].obj.(*unstructured.Unstructured)
	require.True(t, ok, "the object is unstructured: the spec is whatever the form decoded, not a typed struct")
	require.Same(t, u, obj)
	require.Equal(t, schema.GroupVersionKind{Group: "download.clustarr.io", Version: "v1alpha1", Kind: "DownloadClient"}, u.GroupVersionKind())
	require.Equal(t, "media", u.GetNamespace())
	require.Equal(t, "qbit", u.GetName())
	require.Equal(t, actions.OriginUI, u.GetLabels()[actions.LabelOrigin])
	require.Equal(t, spec, u.Object["spec"])
	require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)

	qp, _ := actions.ConfigKindBySlug("qualityprofiles")
	obj, err = actions.CreateConfig(context.Background(), w, qp, "", "my-hd", map[string]any{"mediaKind": "video"})
	require.NoError(t, err)
	require.Empty(t, obj.GetNamespace(), "a cluster-scoped kind has no namespace")

	for name, tc := range map[string]struct {
		kind     actions.ConfigKind
		ns, name string
	}{
		"cluster-scoped kind with a namespace":    {qp, "media", "x"},
		"namespaced kind without a namespace":     {dc, "", "x"},
		"a name that is not a DNS-1123 subdomain": {dc, "media", "Not Valid"},
		"an empty name": {dc, "media", ""},
	} {
		_, err := actions.CreateConfig(context.Background(), &fakeWriter{}, tc.kind, tc.ns, tc.name, map[string]any{})
		require.ErrorIs(t, err, actions.ErrInvalid, name)
	}
	_, err = actions.CreateConfig(context.Background(), &fakeWriter{err: errors.New("boom")}, dc, "media", "x", map[string]any{})
	require.ErrorContains(t, err, "boom")
	require.ErrorContains(t, err, "DownloadClient media/x")
}

// UpdateConfig sends the spec merge patch it is given, under the manager;
// an empty patch sends nothing.
func TestUpdateConfigSendsTheMergePatchOfSpec(t *testing.T) {
	w := &fakeWriter{}
	idx, _ := actions.ConfigKindBySlug("indexers")
	patch := map[string]any{"priority": int64(3), "limits": nil}
	obj, err := actions.UpdateConfig(context.Background(), w, idx, "media", "rarbg", patch)
	require.NoError(t, err)
	require.Len(t, w.patches, 1)
	require.Equal(t, types.MergePatchType, w.patches[0].patchType)
	require.JSONEq(t, `{"spec":{"priority":3,"limits":null}}`, string(w.patches[0].data))
	require.Equal(t, actions.FieldManager, w.patches[0].opts.FieldManager)
	require.Equal(t, "media", obj.GetNamespace())
	require.Equal(t, "rarbg", obj.GetName())
	require.Equal(t, "Indexer", obj.GetKind())

	w = &fakeWriter{}
	_, err = actions.UpdateConfig(context.Background(), w, idx, "media", "rarbg", map[string]any{})
	require.NoError(t, err)
	require.Empty(t, w.patches, "nothing changed, nothing sent")

	_, err = actions.UpdateConfig(context.Background(), w, idx, "", "rarbg", patch)
	require.ErrorIs(t, err, actions.ErrInvalid)
}

// DeleteConfig deletes the one object.
func TestDeleteConfigDeletesTheObject(t *testing.T) {
	w := &fakeWriter{}
	rf, _ := actions.ConfigKindBySlug("rootfolders")
	require.NoError(t, actions.DeleteConfig(context.Background(), w, rf, "media", "movies"))
	require.Len(t, w.deletes, 1)
	require.Equal(t, "media", w.deletes[0].GetNamespace())
	require.Equal(t, "movies", w.deletes[0].GetName())
	require.Equal(t, "RootFolder", w.deletes[0].(*unstructured.Unstructured).GetKind())
	require.ErrorIs(t, actions.DeleteConfig(context.Background(), w, rf, "media", ""), actions.ErrInvalid)
	err := actions.DeleteConfig(context.Background(), &fakeWriter{err: apierrors.NewNotFound(schema.GroupResource{Group: "catalog.clustarr.io", Resource: "rootfolders"}, "movies")}, rf, "media", "movies")
	require.True(t, apierrors.IsNotFound(err), "the apiserver's error is wrapped, not replaced: %v", err)
}

// WriteSecret creates the Secret with the given entries, and when it
// exists patches exactly those entries in, never reading it and never
// touching an entry it was not given (a blank password input must not
// clear the stored one).
func TestWriteSecretCreatesThenPatchesOnlyTheGivenKeys(t *testing.T) {
	w := &fakeWriter{}
	require.NoError(t, actions.WriteSecret(context.Background(), w, "media", "rarbg-credentials", map[string]string{"apikey": "k1"}))
	require.Len(t, w.creates, 1)
	s, ok := w.creates[0].obj.(*corev1.Secret)
	require.True(t, ok)
	require.Equal(t, "media", s.Namespace)
	require.Equal(t, "rarbg-credentials", s.Name)
	require.Equal(t, map[string]string{"apikey": "k1"}, s.StringData)
	require.Empty(t, s.Data, "entries go through stringData; this package never base64s a credential")
	require.Equal(t, actions.OriginUI, s.Labels[actions.LabelOrigin])
	require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)
	require.Empty(t, w.patches)

	exists := &fakeWriter{createErr: apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "rarbg-credentials")}
	require.NoError(t, actions.WriteSecret(context.Background(), exists, "media", "rarbg-credentials", map[string]string{"password": "p2"}))
	require.Len(t, exists.creates, 1)
	require.Len(t, exists.patches, 1)
	require.Equal(t, types.MergePatchType, exists.patches[0].patchType)
	require.JSONEq(t, `{"stringData":{"password":"p2"}}`, string(exists.patches[0].data))
	require.Equal(t, actions.FieldManager, exists.patches[0].opts.FieldManager)

	require.ErrorIs(t, actions.WriteSecret(context.Background(), w, "media", "x", map[string]string{}), actions.ErrInvalid, "nothing to write")
	require.ErrorIs(t, actions.WriteSecret(context.Background(), w, "", "x", map[string]string{"a": "b"}), actions.ErrInvalid)
	require.ErrorIs(t, actions.WriteSecret(context.Background(), w, "media", "", map[string]string{"a": "b"}), actions.ErrInvalid)
}

// Grants grows by create, patch and delete on every settings kind and
// create and patch on secrets: never get, list or watch on a Secret.
func TestGrantsCoverTheSettingsWritesAndOnlyWriteSecrets(t *testing.T) {
	have := map[actions.Grant]bool{}
	for _, g := range actions.Grants() {
		have[g] = true
	}
	for _, k := range actions.ConfigKinds() {
		for _, verb := range []string{"create", "patch", "delete"} {
			require.True(t, have[actions.Grant{Group: k.Group, Resource: k.Resource, Verb: verb}], "%s %s", verb, k.Resource)
		}
	}
	require.True(t, have[actions.Grant{Group: "", Resource: "secrets", Verb: "create"}])
	require.True(t, have[actions.Grant{Group: "", Resource: "secrets", Verb: "patch"}])
	for _, verb := range []string{"get", "list", "watch", "delete", "update"} {
		require.False(t, have[actions.Grant{Group: "", Resource: "secrets", Verb: verb}], "the UI never %ss a Secret", verb)
	}
	require.False(t, have[actions.Grant{Group: "catalog.clustarr.io", Resource: "movies", Verb: "delete"}], "delete stays on the settings kinds only")

	// The Actions methods refuse without a writer, like every other action.
	var a *actions.Actions
	dc, _ := actions.ConfigKindBySlug("downloadclients")
	_, err := a.CreateConfig(context.Background(), dc, "media", "x", map[string]any{})
	require.ErrorIs(t, err, actions.ErrNoWriter)
	_, err = a.UpdateConfig(context.Background(), dc, "media", "x", map[string]any{"a": 1})
	require.ErrorIs(t, err, actions.ErrNoWriter)
	require.ErrorIs(t, a.DeleteConfig(context.Background(), dc, "media", "x"), actions.ErrNoWriter)
	require.ErrorIs(t, a.WriteSecret(context.Background(), "media", "x", map[string]string{"a": "b"}), actions.ErrNoWriter)
}

var _ client.Object = (*unstructured.Unstructured)(nil)
