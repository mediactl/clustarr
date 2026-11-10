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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

func plexCollectionClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func upSpec() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr Movies", Title: "UP", ItemType: catalogv1.PlexItemMovie,
		Filter: &catalogv1.PlexFilter{Match: catalogv1.PlexMatchAny, Rules: []catalogv1.PlexFilterRule{
			{Field: "title", Operator: catalogv1.PlexOpIs, Value: "Seven Up!"},
			{Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexCondition{
				{Field: "genre", Operator: catalogv1.PlexOpIs, Value: "Documentary"},
				{Field: "title", Operator: catalogv1.PlexOpContains, Value: "Up"},
			}},
		}},
	}
}

func TestCreatePlexCollectionWritesTheSpecAlone(t *testing.T) {
	c := plexCollectionClient(t)
	pc, err := actions.CreatePlexCollection(t.Context(), c, "clustarr-system", "up", upSpec())
	require.NoError(t, err)
	assert.Equal(t, "up", pc.Name)

	var got catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &got))
	assert.Equal(t, upSpec(), got.Spec)
	assert.Equal(t, catalogv1.PlexCollectionStatus{}, got.Status)
}

func TestUpdatePlexCollectionReplacesTheWholeSpec(t *testing.T) {
	c := plexCollectionClient(t)
	spec := upSpec()
	spec.Poster = &catalogv1.PlexImage{URL: "https://image.tmdb.org/t/p/original/a.jpg"}
	spec.Summary = ptr.To("Every Up film.")
	_, err := actions.CreatePlexCollection(t.Context(), c, "clustarr-system", "up", spec)
	require.NoError(t, err)
	bumpGeneration(t, c, "up") // the apiserver's 1 on create
	var read catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &read))

	// The poster and summary cleared in the editor: a merge patch of the
	// spec would leave them standing.
	edited := upSpec()
	edited.Title = "Up Series"
	require.NoError(t, actions.UpdatePlexCollection(t.Context(), c, "clustarr-system", "up", read.Generation, edited))

	var got catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &got))
	assert.Equal(t, edited, got.Spec)
	assert.Nil(t, got.Spec.Poster)
	assert.Nil(t, got.Spec.Summary)
}

func TestUpdatePlexCollectionRefusesAStaleRead(t *testing.T) {
	c := plexCollectionClient(t)
	_, err := actions.CreatePlexCollection(t.Context(), c, "clustarr-system", "up", upSpec())
	require.NoError(t, err)
	bumpGeneration(t, c, "up") // the apiserver's 1 on create
	var read catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &read))

	// Someone else's edit lands first. The apiserver moves the generation
	// with every spec change; the fake client does not, so the test does.
	other := upSpec()
	other.Title = "UP (theirs)"
	require.NoError(t, actions.UpdatePlexCollection(t.Context(), c, "clustarr-system", "up", read.Generation, other))
	bumpGeneration(t, c, "up")

	mine := upSpec()
	mine.Title = "UP (mine)"
	require.Error(t, actions.UpdatePlexCollection(t.Context(), c, "clustarr-system", "up", read.Generation, mine))

	var got catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &got))
	assert.Equal(t, "UP (theirs)", got.Spec.Title)
}

// bumpGeneration does what the apiserver does on a spec change.
func bumpGeneration(t *testing.T, c client.Client, name string) {
	t.Helper()
	var pc catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: name}, &pc))
	pc.Generation++
	require.NoError(t, c.Update(t.Context(), &pc))
}

// cluster-plex writes the status within seconds of every save and every
// 10 minutes; that moves the resourceVersion, never the generation, and
// must not refuse an edit.
func TestUpdatePlexCollectionIgnoresAStatusWrite(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&catalogv1.PlexCollection{}).Build()
	_, err := actions.CreatePlexCollection(t.Context(), c, "clustarr-system", "up", upSpec())
	require.NoError(t, err)
	bumpGeneration(t, c, "up") // the apiserver's 1 on create
	var read catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &read))

	synced := read.DeepCopy()
	synced.Status.RatingKey = "26252"
	require.NoError(t, c.Status().Update(t.Context(), synced))
	var now catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &now))
	require.NotEqual(t, read.ResourceVersion, now.ResourceVersion)

	edited := upSpec()
	edited.Title = "Up Series"
	require.NoError(t, actions.UpdatePlexCollection(t.Context(), c, "clustarr-system", "up", read.Generation, edited))
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &now))
	assert.Equal(t, "Up Series", now.Spec.Title)
}

func TestDeletePlexCollection(t *testing.T) {
	c := plexCollectionClient(t)
	_, err := actions.CreatePlexCollection(t.Context(), c, "clustarr-system", "up", upSpec())
	require.NoError(t, err)
	require.NoError(t, actions.DeletePlexCollection(t.Context(), c, "clustarr-system", "up"))
	err = c.Get(t.Context(), client.ObjectKey{Namespace: "clustarr-system", Name: "up"}, &catalogv1.PlexCollection{})
	assert.True(t, apierrors.IsNotFound(err), "got %v", err)
}

func TestPlexCollectionName(t *testing.T) {
	for title, want := range map[string]string{
		"Anime Dubbed":                  "anime-dubbed",
		"UP":                            "up",
		"  Studio Ghibli: Essentials! ": "studio-ghibli-essentials",
		"Pokémon":                       "pokemon",
		"80s -- Action":                 "80s-action",
	} {
		assert.Equal(t, want, actions.PlexCollectionName(title), title)
	}
	long := actions.PlexCollectionName("A Very Long Collection Title That Keeps Going Well Past What A Name May Hold")
	assert.LessOrEqual(t, len(long), 63)
	assert.NotEqual(t, '-', rune(long[len(long)-1]))
	// A title with no Latin letter or digit is named by its hash.
	for _, title := range []string{"!!!", "ポケモン", "東京"} {
		name := actions.PlexCollectionName(title)
		assert.Regexp(t, `^collection-[0-9a-f]{10}$`, name, title)
		assert.Equal(t, name, actions.PlexCollectionName(title), "the same title, the same name")
	}
	assert.NotEqual(t, actions.PlexCollectionName("ポケモン"), actions.PlexCollectionName("東京"))
}

func TestGrantsIncludePlexCollectionWrites(t *testing.T) {
	grants := actions.Grants()
	for _, verb := range []string{"create", "patch", "delete"} {
		assert.Contains(t, grants, actions.Grant{Group: "catalog.clustarr.io", Resource: "plexcollections", Verb: verb})
	}
	for _, g := range grants {
		assert.NotEqual(t, "plexcollections/status", g.Resource)
	}
}
