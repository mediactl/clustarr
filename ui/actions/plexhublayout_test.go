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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

const hubNS = "clustarr-system"

func builtin(id, title string, rec, home, friends bool) catalogv1.PlexHubStatus {
	return catalogv1.PlexHubStatus{
		Identifier: id, Title: title, Kind: catalogv1.PlexHubBuiltin,
		LibraryRecommended: rec, Home: home, FriendsHome: friends,
	}
}

// plexHubs is a movie library as Plex reports it: three built-in hubs, a
// collection a resource holds (pixar) and one none does (A24).
func plexHubs() []catalogv1.PlexHubStatus {
	return []catalogv1.PlexHubStatus{
		builtin("movie.recentlyreleased", "Recently Released Movies", true, true, false),
		builtin("movie.genre", "Top Movies in (Genre)", true, false, false),
		{Identifier: "custom.collection.1.77", Title: "Pixar", Kind: catalogv1.PlexHubCollection, Collection: "pixar", RatingKey: "77", LibraryRecommended: true},
		{Identifier: "custom.collection.1.78", Title: "A24", Kind: catalogv1.PlexHubCollection, RatingKey: "78", Home: true},
		builtin("movie.seasonal", "Seasonal Movies", false, false, true),
	}
}

func hubLayout(spec []catalogv1.PlexHubEntry, status []catalogv1.PlexHubStatus) *catalogv1.PlexHubLayout {
	return &catalogv1.PlexHubLayout{
		ObjectMeta: metav1.ObjectMeta{Namespace: hubNS, Name: "movies", Generation: 1},
		Spec:       catalogv1.PlexHubLayoutSpec{Library: "Movies", Hubs: spec},
		Status:     catalogv1.PlexHubLayoutStatus{Hubs: status},
	}
}

func refs(entries []catalogv1.PlexHubEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, actions.HubRefOf(e).Key())
	}
	return out
}

func TestHubRowsAreTheSpecThenTheUnlistedPlexHubs(t *testing.T) {
	l := hubLayout([]catalogv1.PlexHubEntry{
		{CollectionTitle: "A24", Home: false, LibraryRecommended: true},
		{Identifier: "movie.genre"},
		{Collection: "ghibli", Home: true},
	}, plexHubs())
	rows := actions.HubRows(l)
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Ref.Key())
	}
	assert.Equal(t, []string{"title:A24", "id:movie.genre", "col:ghibli", "id:movie.recentlyreleased", "col:pixar", "id:movie.seasonal"}, keys)

	assert.True(t, rows[0].Listed)
	assert.Equal(t, "A24", rows[0].Title)
	assert.Equal(t, catalogv1.PlexHubCollection, rows[0].Kind)
	assert.True(t, rows[0].LibraryRecommended, "a listed hub shows the spec's flags")
	assert.False(t, rows[0].Home)
	assert.True(t, rows[0].InPlex)

	assert.Equal(t, "Top Movies in (Genre)", rows[1].Title)
	assert.Equal(t, catalogv1.PlexHubBuiltin, rows[1].Kind)

	assert.Equal(t, "ghibli", rows[2].Title, "a collection Plex has no hub for yet is titled by its name")
	assert.False(t, rows[2].InPlex)
	assert.Equal(t, catalogv1.PlexHubCollection, rows[2].Kind)

	assert.False(t, rows[3].Listed)
	assert.True(t, rows[3].Home, "an unlisted hub shows Plex's flags")
}

func TestHubRowsOfNoLayout(t *testing.T) {
	assert.Empty(t, actions.HubRows(nil))
}

func TestHubRefKeysRoundTrip(t *testing.T) {
	for _, r := range []actions.HubRef{{Identifier: "movie.genre"}, {Collection: "pixar"}, {CollectionTitle: "A24: the films"}} {
		got, err := actions.ParseHubRef(r.Key())
		require.NoError(t, err)
		assert.Equal(t, r, got)
	}
	for _, bad := range []string{"", "movie.genre", "id:", "nope:x"} {
		_, err := actions.ParseHubRef(bad)
		assert.ErrorIs(t, err, actions.ErrInvalid, bad)
	}
}

func TestAFlagOnAnUnlistedHubMaterialisesTheOrderUpToIt(t *testing.T) {
	l := hubLayout(nil, plexHubs())
	got, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Collection: "pixar"}, Flag: actions.HubFlagHome, On: true})
	require.NoError(t, err)
	assert.Equal(t, []catalogv1.PlexHubEntry{
		{Identifier: "movie.recentlyreleased", LibraryRecommended: true, Home: true},
		{Identifier: "movie.genre", LibraryRecommended: true},
		{Collection: "pixar", LibraryRecommended: true, Home: true},
	}, got, "the hubs before it keep their place and flags; those after stay Plex's")
}

func TestAFlagOnAListedHubChangesThatEntryAlone(t *testing.T) {
	l := hubLayout([]catalogv1.PlexHubEntry{{Identifier: "movie.genre", LibraryRecommended: true}, {CollectionTitle: "A24", Home: true}}, plexHubs())
	got, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Identifier: "movie.genre"}, Flag: actions.HubFlagLibraryRecommended, On: false})
	require.NoError(t, err)
	assert.Equal(t, []catalogv1.PlexHubEntry{{Identifier: "movie.genre"}, {CollectionTitle: "A24", Home: true}}, got)
	assert.True(t, l.Spec.Hubs[0].LibraryRecommended, "the layout read is not changed")
}

func TestAFlagOnACollectionWithNoHubAddsItLast(t *testing.T) {
	l := hubLayout(nil, plexHubs())
	got, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Collection: "ghibli"}, Flag: actions.HubFlagFriendsHome, On: true})
	require.NoError(t, err)
	require.Len(t, got, 6)
	assert.Equal(t, []string{"id:movie.recentlyreleased", "id:movie.genre", "col:pixar", "title:A24", "id:movie.seasonal", "col:ghibli"}, refs(got))
	assert.Equal(t, catalogv1.PlexHubEntry{Collection: "ghibli", FriendsHome: true}, got[5])

	_, err = actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Identifier: "movie.unknown"}, Flag: actions.HubFlagHome, On: true})
	assert.ErrorIs(t, err, actions.ErrInvalid, "a built-in hub Plex does not report cannot be invented")
	_, err = actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Collection: "pixar"}, Flag: "everywhere", On: true})
	assert.ErrorIs(t, err, actions.ErrInvalid)
}

func TestMoveMaterialisesAsFarAsItReaches(t *testing.T) {
	// Spec lists A24 alone; the rows are A24, recentlyreleased, genre,
	// pixar, seasonal.
	l := hubLayout([]catalogv1.PlexHubEntry{{CollectionTitle: "A24", Home: true}}, plexHubs())

	down, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRef{CollectionTitle: "A24"}, To: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"id:movie.recentlyreleased", "title:A24"}, refs(down))

	up, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRef{Collection: "pixar"}, To: 0})
	require.NoError(t, err)
	assert.Equal(t, []string{"col:pixar", "title:A24", "id:movie.recentlyreleased", "id:movie.genre"}, refs(up))
	assert.True(t, up[0].LibraryRecommended, "it keeps Plex's flags")

	last, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRef{CollectionTitle: "A24"}, To: 99})
	require.NoError(t, err)
	assert.Equal(t, []string{"id:movie.recentlyreleased", "id:movie.genre", "col:pixar", "id:movie.seasonal", "title:A24"}, refs(last), "a position past the end is the end")

	same, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRef{CollectionTitle: "A24"}, To: -3})
	require.NoError(t, err)
	assert.Equal(t, []string{"title:A24"}, refs(same))

	_, err = actions.EditHubs(l, actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRef{Identifier: "nope"}, To: 0})
	assert.ErrorIs(t, err, actions.ErrInvalid)
}

func TestAddACollectionHub(t *testing.T) {
	l := hubLayout([]catalogv1.PlexHubEntry{{Identifier: "movie.genre"}}, plexHubs())
	got, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubAdd, Ref: actions.HubRef{Collection: "ghibli"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"id:movie.genre", "id:movie.recentlyreleased", "col:pixar", "title:A24", "id:movie.seasonal", "col:ghibli"}, refs(got),
		"the whole order is kept and the collection comes last")
	assert.Equal(t, catalogv1.PlexHubEntry{Collection: "ghibli", LibraryRecommended: true}, got[5], "a new hub starts on Library Recommended")

	// One Plex already shows as a hub is listed where it is, with its flags.
	got, err = actions.EditHubs(l, actions.HubEdit{Op: actions.HubAdd, Ref: actions.HubRef{Collection: "pixar"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"id:movie.genre", "id:movie.recentlyreleased", "col:pixar"}, refs(got))

	_, err = actions.EditHubs(l, actions.HubEdit{Op: actions.HubAdd, Ref: actions.HubRef{Identifier: "movie.genre"}})
	assert.ErrorIs(t, err, actions.ErrInvalid, "only a collection is added")
}

func TestRemoveDropsAListedEntry(t *testing.T) {
	l := hubLayout([]catalogv1.PlexHubEntry{{Identifier: "movie.genre"}, {Collection: "pixar"}}, plexHubs())
	got, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubRemove, Ref: actions.HubRef{Identifier: "movie.genre"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"col:pixar"}, refs(got))

	_, err = actions.EditHubs(l, actions.HubEdit{Op: actions.HubRemove, Ref: actions.HubRef{Identifier: "movie.seasonal"}})
	assert.ErrorIs(t, err, actions.ErrInvalid, "an unlisted hub is not removed")
}

func TestALayoutListsAtMostAHundredHubs(t *testing.T) {
	var status []catalogv1.PlexHubStatus
	for i := range 120 {
		status = append(status, builtin(fmt.Sprintf("movie.hub%d", i), "", false, false, false))
	}
	l := hubLayout(nil, status)
	_, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Identifier: "movie.hub110"}, Flag: actions.HubFlagHome, On: true})
	assert.ErrorIs(t, err, actions.ErrInvalid)
	got, err := actions.EditHubs(l, actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Identifier: "movie.hub99"}, Flag: actions.HubFlagHome, On: true})
	require.NoError(t, err)
	assert.Len(t, got, 100)
}

func TestPlexHubLayoutName(t *testing.T) {
	assert.Equal(t, "movies", actions.PlexHubLayoutName("Movies"))
	assert.Equal(t, "tv-shows", actions.PlexHubLayoutName("TV Shows"))
	assert.Regexp(t, `^hubs-[0-9a-f]{10}$`, actions.PlexHubLayoutName("映画"))
}

func hubClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(catalogScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&catalogv1.PlexHubLayout{}).Build()
}

func getLayout(t *testing.T, c client.Client, name string) catalogv1.PlexHubLayout {
	t.Helper()
	var got catalogv1.PlexHubLayout
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: hubNS, Name: name}, &got))
	return got
}

func TestEditPlexHubLayoutCreatesTheLayoutOnFirstWrite(t *testing.T) {
	c := hubClient(t)
	made, err := actions.EditPlexHubLayout(t.Context(), c, c, hubNS, "tv-shows", "TV Shows", actions.HubEdit{Op: actions.HubEnsure})
	require.NoError(t, err)
	assert.Equal(t, "TV Shows", made.Spec.Library)
	got := getLayout(t, c, "tv-shows")
	assert.Equal(t, catalogv1.PlexHubLayoutSpec{Library: "TV Shows"}, got.Spec)
	assert.Equal(t, catalogv1.PlexHubLayoutStatus{}, got.Status)

	// A collection's switch before cluster-plex reported anything.
	_, err = actions.EditPlexHubLayout(t.Context(), c, c, hubNS, "movies", "Movies",
		actions.HubEdit{Op: actions.HubSetFlag, Ref: actions.HubRef{Collection: "pixar"}, Flag: actions.HubFlagHome, On: true})
	require.NoError(t, err)
	assert.Equal(t, []catalogv1.PlexHubEntry{{Collection: "pixar", Home: true}}, getLayout(t, c, "movies").Spec.Hubs)

	_, err = actions.EditPlexHubLayout(t.Context(), c, c, hubNS, "music", "", actions.HubEdit{Op: actions.HubEnsure})
	assert.ErrorIs(t, err, actions.ErrInvalid, "a new layout needs its library")
}

func TestEditPlexHubLayoutPatchesTheHubsAlone(t *testing.T) {
	l := hubLayout(nil, plexHubs())
	l.Annotations = map[string]string{"example.com/kept": "yes"}
	c := hubClient(t, l)
	left, err := actions.EditPlexHubLayout(t.Context(), c, c, hubNS, "movies", "Movies",
		actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRef{Identifier: "movie.genre"}, To: 0})
	require.NoError(t, err)
	assert.Equal(t, []string{"id:movie.genre", "id:movie.recentlyreleased"}, refs(left.Spec.Hubs), "the layout as written comes back")
	assert.Equal(t, plexHubs(), left.Status.Hubs)
	got := getLayout(t, c, "movies")
	assert.Equal(t, []string{"id:movie.genre", "id:movie.recentlyreleased"}, refs(got.Spec.Hubs))
	assert.Equal(t, "Movies", got.Spec.Library)
	assert.Equal(t, plexHubs(), got.Status.Hubs, "status is cluster-plex's")
	assert.Equal(t, "yes", got.Annotations["example.com/kept"])

	// Ensuring a layout that exists writes nothing.
	_, err = actions.EditPlexHubLayout(t.Context(), c, c, hubNS, "movies", "Movies", actions.HubEdit{Op: actions.HubEnsure})
	require.NoError(t, err)
	assert.Equal(t, got.ResourceVersion, getLayout(t, c, "movies").ResourceVersion)
}

func TestResetPlexHubLayoutSetsTheNonceAndClearsTheHubsInOnePatch(t *testing.T) {
	c := hubClient(t, hubLayout([]catalogv1.PlexHubEntry{{Identifier: "movie.genre"}}, plexHubs()))
	at := time.Date(2026, 11, 16, 20, 30, 0, 0, time.UTC)
	before := getLayout(t, c, "movies")
	require.NoError(t, actions.ResetPlexHubLayout(t.Context(), c, hubNS, "movies", "Movies", at))
	got := getLayout(t, c, "movies")
	assert.Equal(t, "2026-11-16T20:30:00Z", got.Annotations[catalogv1.AnnotationPlexHubsReset])
	assert.Empty(t, got.Spec.Hubs)
	assert.Equal(t, "Movies", got.Spec.Library)
	assert.NotEqual(t, before.ResourceVersion, got.ResourceVersion)

	// A library with no layout yet gets one carrying the request.
	require.NoError(t, actions.ResetPlexHubLayout(t.Context(), c, hubNS, "tv-shows", "TV Shows", at))
	tv := getLayout(t, c, "tv-shows")
	assert.Equal(t, "2026-11-16T20:30:00Z", tv.Annotations[catalogv1.AnnotationPlexHubsReset])
	assert.Equal(t, "TV Shows", tv.Spec.Library)
}

func TestDeletePlexHubLayout(t *testing.T) {
	c := hubClient(t, hubLayout(nil, nil))
	require.NoError(t, actions.DeletePlexHubLayout(t.Context(), c, hubNS, "movies"))
	var got catalogv1.PlexHubLayout
	assert.Error(t, c.Get(t.Context(), client.ObjectKey{Namespace: hubNS, Name: "movies"}, &got))
}

func TestGrantsCoverTheHubLayoutWrites(t *testing.T) {
	for _, verb := range []string{"create", "patch", "delete"} {
		assert.Contains(t, actions.Grants(), actions.Grant{Group: "catalog.clustarr.io", Resource: "plexhublayouts", Verb: verb})
	}
}

func catalogScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	return scheme
}

// The sample decodes strictly into the API, names each hub one way and
// once (the CRD's CEL rules, which no unit test compiles), and edits as
// any layout does.
func TestThePlexHubLayoutSampleDecodes(t *testing.T) {
	raw, err := os.ReadFile("../../config/samples/catalog_v1alpha1_plexhublayout_movies.yaml")
	require.NoError(t, err)
	var l catalogv1.PlexHubLayout
	require.NoError(t, yaml.UnmarshalStrict(raw, &l))
	assert.Equal(t, "PlexHubLayout", l.Kind)
	assert.Equal(t, actions.PlexHubLayoutName(l.Spec.Library), l.Name, "named as the ui names a library's layout")
	require.NotEmpty(t, l.Spec.Hubs)
	seen := map[string]bool{}
	for _, h := range l.Spec.Hubs {
		set := 0
		for _, v := range []string{h.Identifier, h.Collection, h.CollectionTitle} {
			if v != "" {
				set++
			}
		}
		assert.Equal(t, 1, set, "%+v", h)
		key := actions.HubRefOf(h).Key()
		assert.False(t, seen[key], key)
		seen[key] = true
	}
	_, err = actions.EditHubs(&l, actions.HubEdit{Op: actions.HubMove, Ref: actions.HubRefOf(l.Spec.Hubs[0]), To: 1})
	assert.NoError(t, err)
}
