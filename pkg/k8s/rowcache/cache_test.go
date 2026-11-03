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

package rowcache

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// New resolves each kind against the scheme and the REST mapper, refuses a
// cluster-scoped one, and leaves every other kind to the cache's own
// informers.
func TestNewResolvesTheKinds(t *testing.T) {
	scheme := k8s.MustNewScheme()
	mapper := apimeta.NewDefaultRESTMapper(nil)
	episodeGVK := catalogv1alpha1.GroupVersion.WithKind("Episode")
	mapper.Add(episodeGVK, apimeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersion{Version: "v1"}.WithKind("Namespace"), apimeta.RESTScopeRoot)
	cfg := &rest.Config{Host: "http://127.0.0.1:1"}
	opts := func() cache.Options {
		return cache.Options{Scheme: scheme, Mapper: mapper, HTTPClient: &http.Client{}}
	}

	c, err := New(Options{Kinds: []client.Object{&catalogv1alpha1.Episode{}}})(cfg, opts())
	require.NoError(t, err)
	rc, ok := c.(*Cache)
	require.True(t, ok)
	k, ok := rc.kinds[reflect.TypeOf(&catalogv1alpha1.Episode{})]
	require.True(t, ok)
	assert.Equal(t, episodeGVK, k.gvk)
	assert.Equal(t, reflect.TypeFor[catalogv1alpha1.Episode](), k.typ)
	assert.Same(t, k, rc.lists[reflect.TypeOf(&catalogv1alpha1.EpisodeList{})], "the list type names the same kind")
	assert.NotNil(t, k.codec)

	_, err = New(Options{Kinds: []client.Object{&corev1.Namespace{}}})(cfg, opts())
	require.ErrorContains(t, err, "cluster-scoped")

	_, err = New(Options{Kinds: []client.Object{&corev1.ConfigMap{}}})(cfg, opts())
	require.Error(t, err, "a kind the mapper does not know")

	_, err = New(Options{Kinds: []client.Object{&catalogv1alpha1.Episode{}}})(cfg, cache.Options{Scheme: scheme})
	require.Error(t, err, "no mapper")
}
