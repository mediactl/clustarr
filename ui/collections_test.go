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
	"html"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

const pcolNS = "clustarr-system"

func collectionsServer(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	return collectionsServerWith(t, funcs, nil, objs...)
}

// collectionsServerWith is collectionsServer with the Options tweaked.
func collectionsServerWith(t *testing.T, funcs *interceptor.Funcs, tweak func(*ui.Options), objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).
		WithStatusSubresource(&catalogv1.PlexCollection{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	opts := ui.Options{Reader: c, Actions: actions.New(c), Namespace: pcolNS}
	if tweak != nil {
		tweak(&opts)
	}
	srv := ui.NewServer(t.Context(), opts)
	return srv, c
}

func plexCollection(name string, spec catalogv1.PlexCollectionSpec) *catalogv1.PlexCollection {
	// Generation 1, as the apiserver sets it on create.
	return &catalogv1.PlexCollection{ObjectMeta: metav1.ObjectMeta{Namespace: pcolNS, Name: name, Generation: 1}, Spec: spec}
}

func refAnimeDubbed() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr TV", Title: "Anime Dubbed", ItemType: catalogv1.PlexItemShow,
		Filter: &catalogv1.PlexFilter{Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexFilterRule{
			{Field: "episode.audioLanguage", Operator: catalogv1.PlexOpIs, Value: "en"},
			{Field: "show.genre", Operator: catalogv1.PlexOpIs, Value: "Anime"},
		}},
		Sort: &catalogv1.PlexSort{Field: "titleSort"},
	}
}

func refNetflix() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr TV", Title: "Netflix", ItemType: catalogv1.PlexItemShow,
		Filter: &catalogv1.PlexFilter{Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexFilterRule{
			{Field: "show.network", Operator: catalogv1.PlexOpIs, Value: "Netflix"},
		}},
	}
}

func refUP() catalogv1.PlexCollectionSpec {
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

// editorValues reads the editor form (#collection-editor) out of a page as
// a browser would submit it: named inputs, checked checkboxes and
// textareas, in document order.
func editorValues(t *testing.T, body string) url.Values {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(body))
	require.NoError(t, err)
	attr := func(n *xhtml.Node, k string) (string, bool) {
		for _, a := range n.Attr {
			if a.Key == k {
				return a.Val, true
			}
		}
		return "", false
	}
	var form *xhtml.Node
	var find func(*xhtml.Node)
	find = func(n *xhtml.Node) {
		if form != nil {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "form" {
			if id, _ := attr(n, "id"); id == "collection-editor" {
				form = n
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	require.NotNil(t, form, "no #collection-editor form in the page")
	out := url.Values{}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			name, named := attr(n, "name")
			_, disabled := attr(n, "disabled")
			switch {
			case !named || disabled:
			case n.Data == "input":
				typ, _ := attr(n, "type")
				_, checked := attr(n, "checked")
				if (typ == "checkbox" || typ == "radio") && !checked {
					break
				}
				v, _ := attr(n, "value")
				if (typ == "checkbox" || typ == "radio") && v == "" {
					v = "on"
				}
				out.Add(name, v)
			case n.Data == "textarea":
				var b strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					b.WriteString(c.Data)
				}
				out.Add(name, b.String())
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
	return out
}

func postHTMXTo(t *testing.T, srv *ui.Server, path string, form url.Values) *http.Response {
	t.Helper()
	rec := postHTMX(t, srv, path, form)
	return rec.Result()
}

func bodyOf(t *testing.T, r *http.Response) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func TestCollectionsPageListsByLibrary(t *testing.T) {
	synced := func(reason string, items int32) catalogv1.PlexCollectionStatus {
		return catalogv1.PlexCollectionStatus{ItemCount: items, Conditions: []metav1.Condition{{
			Type: catalogv1.PlexCollectionConditionSynced, Status: metav1.ConditionTrue, Reason: reason, Message: reason,
			LastTransitionTime: metav1.Now(),
		}}}
	}
	anime, netflix, up := plexCollection("anime-dubbed", refAnimeDubbed()), plexCollection("netflix", refNetflix()), plexCollection("up", refUP())
	anime.Status, netflix.Status, up.Status = synced("Adopted", 259), synced("Synced", 23), synced("Adopted", 8)
	srv, _ := collectionsServer(t, nil, anime, netflix, up)

	rec := get(t, srv, "/library/collections")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{"Clustarr Movies", "Clustarr TV", "Anime Dubbed", "Netflix", "UP", "259", "23", "Adopted",
		"Audio Language is en and Genre is Anime",
		html.EscapeString(`Title is "Seven Up!" or (Genre is Documentary and Title contains "Up")`),
	} {
		assert.Contains(t, body, want)
	}
	assert.Less(t, strings.Index(body, "Clustarr Movies"), strings.Index(body, "Clustarr TV"), "libraries in order")
	assert.Contains(t, body, `href="/library/collections/clustarr-system/up/edit"`)
}

func TestParseCollectionFormRoundTripsTheReferences(t *testing.T) {
	for name, spec := range map[string]catalogv1.PlexCollectionSpec{
		"anime-dubbed": refAnimeDubbed(), "netflix": refNetflix(), "up": refUP(),
	} {
		t.Run(name, func(t *testing.T) {
			srv, c := collectionsServer(t, nil, plexCollection(name, spec))
			rec := get(t, srv, "/library/collections/"+pcolNS+"/"+name+"/edit")
			require.Equal(t, http.StatusOK, rec.Code)
			values := editorValues(t, rec.Body.String())

			rec = post(t, srv, "/library/collections/"+pcolNS+"/"+name, values)
			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			var got catalogv1.PlexCollection
			require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: name}, &got))
			assert.Equal(t, spec, got.Spec)
		})
	}
}

func TestEditorOps(t *testing.T) {
	srv, _ := collectionsServer(t, nil, plexCollection("up", refUP()))
	values := editorValues(t, get(t, srv, "/library/collections/"+pcolNS+"/up/edit").Body.String())

	op := func(op, at string) (int, url.Values) {
		r := postHTMXTo(t, srv, "/library/collections/editor?op="+op+"&at="+url.QueryEscape(at), values)
		body := bodyOf(t, r)
		if r.StatusCode != http.StatusOK {
			return r.StatusCode, nil
		}
		return r.StatusCode, editorValues(t, body)
	}

	code, v := op("add-rule", "")
	require.Equal(t, http.StatusOK, code)
	assert.NotEmpty(t, v.Get("rule.2.field"), "a new top-level condition")
	assert.NotEmpty(t, v.Get("rule.2.operator"))

	code, v = op("add-group", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "all", v.Get("rule.2.match"))
	assert.NotEmpty(t, v.Get("rule.2.0.field"))

	code, v = op("add-rule", "1")
	require.Equal(t, http.StatusOK, code)
	assert.NotEmpty(t, v.Get("rule.1.2.field"), "a new condition in the group")

	code, v = op("remove", "1.0")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Up", v.Get("rule.1.0.value"), "the group's second condition moves up")
	assert.Empty(t, v.Get("rule.1.1.field"))

	code, _ = op("remove", "9")
	assert.Equal(t, http.StatusBadRequest, code)
}

func newSmartForm(rules map[string]string) url.Values {
	v := url.Values{
		"library": {"Clustarr Movies"}, "title": {"Docs"}, "itemType": {"movie"}, "kind": {"smart"}, "match": {"all"},
	}
	for k, val := range rules {
		v.Set(k, val)
	}
	return v
}

func TestCreateCollection(t *testing.T) {
	srv, c := collectionsServer(t, nil)
	rec := post(t, srv, "/library/collections", newSmartForm(map[string]string{
		"rule.0.field": "genre", "rule.0.operator": "is", "rule.0.value": "Documentary",
	}))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "/library/collections", rec.Header().Get("Location"))

	var got catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: "docs"}, &got))
	assert.Equal(t, "Docs", got.Spec.Title)
	require.NotNil(t, got.Spec.Filter)
	assert.Equal(t, []catalogv1.PlexFilterRule{{Field: "genre", Operator: catalogv1.PlexOpIs, Value: "Documentary"}}, got.Spec.Filter.Rules)
}

func TestCreateRejectsAnInvalidSpecWithoutWriting(t *testing.T) {
	srv, c := collectionsServer(t, nil)
	rec := post(t, srv, "/library/collections", newSmartForm(map[string]string{
		"rule.0.field": "genre", "rule.0.operator": "contains", "rule.0.value": "Doc",
	}))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "filter.rules[0]")
	assert.Contains(t, rec.Body.String(), `id="collection-editor"`, "the editor comes back with the error")

	var list catalogv1.PlexCollectionList
	require.NoError(t, c.List(t.Context(), &list))
	assert.Empty(t, list.Items)
}

func TestDeleteCollection(t *testing.T) {
	srv, c := collectionsServer(t, nil, plexCollection("up", refUP()))
	rec := postHTMX(t, srv, "/library/collections/"+pcolNS+"/up/delete", url.Values{})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "collections-changed", rec.Header().Get("HX-Trigger"))
	err := c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: "up"}, &catalogv1.PlexCollection{})
	assert.Error(t, err)
}

func TestValueSuggestions(t *testing.T) {
	// From the library projection: the editor never lists Movies, Series
	// or Episodes itself.
	funcs := &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		switch list.(type) {
		case *catalogv1.EpisodeList, *catalogv1.MovieList, *catalogv1.SeriesList:
			t.Errorf("the editor listed %T", list)
		}
		return c.List(ctx, list, opts...)
	}}
	srv, _ := collectionsServerWith(t, funcs, libraryWith(
		projection.LibraryItem{Ref: types.NamespacedName{Namespace: pcolNS, Name: "a"}, Kind: commonv1.MediaKindSeries,
			Genres: []string{"Drama", "Anime"}, SubGenres: []string{"Dark Comedy"}, Networks: []string{"Netflix", "HBO"}, Keywords: []string{"time travel"}},
		projection.LibraryItem{Ref: types.NamespacedName{Namespace: pcolNS, Name: "b"}, Kind: commonv1.MediaKindSeries, Genres: []string{"Anime"}},
		projection.LibraryItem{Ref: types.NamespacedName{Namespace: pcolNS, Name: "m"}, Kind: commonv1.MediaKindMovie,
			Genres: []string{"Documentary"}, Studios: []string{"Pixar", "A24"}, Keywords: []string{"heist"}},
	))

	show := get(t, srv, "/library/collections/new?itemType=show").Body.String()
	assertOptions(t, show, "suggest-show-genre", "Anime", "Dark Comedy", "Drama")
	assertOptions(t, show, "suggest-show-network", "HBO", "Netflix")
	assertOptions(t, show, "suggest-show-label", "time travel")

	film := get(t, srv, "/library/collections/new?itemType=movie").Body.String()
	assertOptions(t, film, "suggest-studio", "A24", "Pixar")
	assertOptions(t, film, "suggest-genre", "Documentary")
	assertOptions(t, film, "suggest-label", "heist")
}

// assertOptions finds a datalist by id and checks its options, in order.
func assertOptions(t *testing.T, body, id string, want ...string) {
	t.Helper()
	start := strings.Index(body, `<datalist id="`+id+`"`)
	require.GreaterOrEqual(t, start, 0, "no datalist %s", id)
	end := strings.Index(body[start:], "</datalist>")
	require.Greater(t, end, 0)
	list := body[start : start+end]
	var got []string
	for _, part := range strings.Split(list, `<option value="`)[1:] {
		got = append(got, html.UnescapeString(part[:strings.Index(part, `"`)]))
	}
	assert.Equal(t, want, got, id)
}

func refGhibli() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr Movies", Title: "Studio Ghibli Essentials", ItemType: catalogv1.PlexItemMovie,
		Members: &catalogv1.PlexMembers{Items: []catalogv1.PlexMember{
			{Kind: "Movie", Name: "spirited-away-2001"},
			{Kind: "Movie", Name: "my-neighbor-totoro-1988"},
		}},
		Summary:    ptr.To("Every Ghibli film worth a rewatch."),
		SortTitle:  ptr.To("Ghibli 00"),
		Poster:     &catalogv1.PlexImage{URL: "https://image.tmdb.org/t/p/original/poster.jpg"},
		Background: &catalogv1.PlexImage{URL: "https://image.tmdb.org/t/p/original/bg.jpg"},
		Settings:   &catalogv1.PlexCollectionSettings{Mode: catalogv1.PlexModeHideItems, Order: catalogv1.PlexOrderReleaseDate},
		Visibility: &catalogv1.PlexVisibility{LibraryRecommended: true, Home: true},
	}
}

func withArtBase(o *ui.Options) {
	o.Plex = &ui.PlexOptions{ExternalURL: "https://clustarr.k8s.appkins.io"}
}

func TestParseCollectionFormNormalAndPresentation(t *testing.T) {
	srv, c := collectionsServerWith(t, nil, withArtBase, plexCollection("ghibli", refGhibli()))
	edit := "/library/collections/" + pcolNS + "/ghibli/edit"
	save := "/library/collections/" + pcolNS + "/ghibli"
	current := func() catalogv1.PlexCollectionSpec {
		var got catalogv1.PlexCollection
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: "ghibli"}, &got))
		return got.Spec
	}

	values := editorValues(t, get(t, srv, edit).Body.String())
	rec := post(t, srv, save, values)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, refGhibli(), current(), "the normal collection and its presentation round-trip")

	// "Keep Plex's" leaves the settings out; visibility unmanaged leaves it out.
	values = editorValues(t, get(t, srv, edit).Body.String())
	values.Set("settings.mode", "")
	values.Set("settings.order", "")
	values.Del("visibility.managed")
	require.Equal(t, http.StatusSeeOther, post(t, srv, save, values).Code)
	got := current()
	assert.Nil(t, got.Settings)
	assert.Nil(t, got.Visibility)

	// Managed, with Home alone ticked.
	values = editorValues(t, get(t, srv, edit).Body.String())
	values.Set("visibility.managed", "1")
	values.Del("visibility.libraryRecommended")
	values.Del("visibility.friendsHome")
	values.Set("visibility.home", "1")
	require.Equal(t, http.StatusSeeOther, post(t, srv, save, values).Code)
	assert.Equal(t, &catalogv1.PlexVisibility{Home: true}, current().Visibility)
}

func libraryWith(items ...projection.LibraryItem) func(*ui.Options) {
	return func(o *ui.Options) {
		withArtBase(o)
		o.Library = func(context.Context) []projection.LibraryItem { return items }
	}
}

func TestMemberSearchListsOnlyTheCollectionsType(t *testing.T) {
	srv, _ := collectionsServerWith(t, nil, libraryWith(
		projection.LibraryItem{Ref: types.NamespacedName{Namespace: pcolNS, Name: "my-neighbor-totoro-1988"}, Kind: commonv1.MediaKindMovie, Title: "My Neighbor Totoro", Year: 1988},
		projection.LibraryItem{Ref: types.NamespacedName{Namespace: pcolNS, Name: "totoro-tales"}, Kind: commonv1.MediaKindSeries, Title: "Totoro Tales"},
	))
	body := get(t, srv, "/library/collections/members?itemType=movie&q=tot").Body.String()
	assert.Contains(t, body, "My Neighbor Totoro")
	assert.Contains(t, body, "op=add-member&amp;at=Movie%2Fmy-neighbor-totoro-1988")
	assert.NotContains(t, body, "Totoro Tales")

	shows := get(t, srv, "/library/collections/members?itemType=show&q=tot").Body.String()
	assert.Contains(t, shows, "Totoro Tales")
	assert.NotContains(t, shows, "My Neighbor Totoro")
}

func TestEditorMemberOps(t *testing.T) {
	srv, _ := collectionsServerWith(t, nil, withArtBase, plexCollection("ghibli", refGhibli()))
	values := editorValues(t, get(t, srv, "/library/collections/"+pcolNS+"/ghibli/edit").Body.String())
	op := func(v url.Values, op, at string) (int, url.Values) {
		r := postHTMXTo(t, srv, "/library/collections/editor?op="+op+"&at="+url.QueryEscape(at), v)
		body := bodyOf(t, r)
		if r.StatusCode != http.StatusOK {
			return r.StatusCode, nil
		}
		return r.StatusCode, editorValues(t, body)
	}

	code, v := op(values, "add-member", "Movie/kikis-delivery-service-1989")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "kikis-delivery-service-1989", v.Get("member.2.name"))
	assert.Equal(t, "Movie", v.Get("member.2.kind"))

	code, v = op(v, "add-member", "Movie/kikis-delivery-service-1989")
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, v.Get("member.3.name"), "a member is added once")

	code, v = op(v, "remove-member", "0")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "my-neighbor-totoro-1988", v.Get("member.0.name"))
	assert.Empty(t, v.Get("member.2.name"))
}

func TestImagePreviewOnlyForAllowedHosts(t *testing.T) {
	proxied := refGhibli()
	other := refGhibli()
	other.Poster = &catalogv1.PlexImage{URL: "https://images.example.com/poster.jpg"}
	other.Background = nil
	srv, _ := collectionsServerWith(t, nil, withArtBase, plexCollection("proxied", proxied), plexCollection("other", other))

	body := get(t, srv, "/library/collections/"+pcolNS+"/proxied/edit").Body.String()
	assert.Contains(t, body, "/art/search?src="+url.QueryEscape("https://image.tmdb.org/t/p/original/poster.jpg"))

	body = get(t, srv, "/library/collections/"+pcolNS+"/other/edit").Body.String()
	assert.NotContains(t, body, "/art/search?src=")
	assert.Equal(t, "https://images.example.com/poster.jpg", editorValues(t, body).Get("poster.url"), "the URL is kept")
}

func TestCreateRefusesAnImageURLTheRuleRefuses(t *testing.T) {
	srv, c := collectionsServerWith(t, nil, withArtBase)
	form := newSmartForm(map[string]string{"rule.0.field": "genre", "rule.0.operator": "is", "rule.0.value": "Documentary"})
	form.Set("poster.url", "https://10.0.0.5/x.jpg")
	rec := post(t, srv, "/library/collections", form)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not a public address")

	var list catalogv1.PlexCollectionList
	require.NoError(t, c.List(t.Context(), &list))
	assert.Empty(t, list.Items)
}

func TestUpdateRefusesASpecEditedSinceTheEditorOpened(t *testing.T) {
	srv, c := collectionsServerWith(t, nil, withArtBase, plexCollection("ghibli", refGhibli()))
	values := editorValues(t, get(t, srv, "/library/collections/"+pcolNS+"/ghibli/edit").Body.String())
	assert.Equal(t, "1", values.Get("generation"))

	// Someone else's edit moves the generation (the fake client does not).
	var pc catalogv1.PlexCollection
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: "ghibli"}, &pc))
	pc.Spec.Title = "Ghibli (theirs)"
	pc.Generation = 2
	require.NoError(t, c.Update(t.Context(), &pc))

	values.Set("title", "Ghibli (mine)")
	rec := post(t, srv, "/library/collections/"+pcolNS+"/ghibli", values)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "edited since this editor opened it")
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: "ghibli"}, &pc))
	assert.Equal(t, "Ghibli (theirs)", pc.Spec.Title)
}

func TestTheSortIsAChoiceOfTheItemTypesKeys(t *testing.T) {
	srv, c := collectionsServerWith(t, nil, withArtBase)
	show := get(t, srv, "/library/collections/new?itemType=show").Body.String()
	assert.Contains(t, show, "Last Episode Date Added")
	assert.Contains(t, show, "Plex&#39;s own")

	form := newSmartForm(map[string]string{"rule.0.field": "genre", "rule.0.operator": "is", "rule.0.value": "Documentary"})
	form.Set("sort.field", "keep")
	form.Set("sort.descending", "1")
	rec := post(t, srv, "/library/collections", form)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "sort.descending")

	form.Set("sort.field", "unviewedLeafCount")
	rec = post(t, srv, "/library/collections", form)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "sort.field")

	var list catalogv1.PlexCollectionList
	require.NoError(t, c.List(t.Context(), &list))
	assert.Empty(t, list.Items)
}

func TestTheListShowsThePoster(t *testing.T) {
	srv, _ := collectionsServerWith(t, nil, withArtBase, plexCollection("ghibli", refGhibli()), plexCollection("up", refUP()))
	body := get(t, srv, "/library/collections").Body.String()
	assert.Contains(t, body, "/art/search?src="+url.QueryEscape("https://image.tmdb.org/t/p/original/poster.jpg"))
}

func TestCreateNamesATitleWithoutLatinLetters(t *testing.T) {
	srv, c := collectionsServerWith(t, nil, withArtBase)
	form := newSmartForm(map[string]string{"rule.0.field": "genre", "rule.0.operator": "is", "rule.0.value": "Documentary"})
	form.Set("title", "ポケモン")
	rec := post(t, srv, "/library/collections", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var list catalogv1.PlexCollectionList
	require.NoError(t, c.List(t.Context(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, actions.PlexCollectionName("ポケモン"), list.Items[0].Name)
	assert.Equal(t, "ポケモン", list.Items[0].Spec.Title)
}

// fullSpecs are a smart and a normal collection that between them set
// every field of the spec.
func fullSpecs() []catalogv1.PlexCollectionSpec {
	smart := refUP()
	smart.Sort = &catalogv1.PlexSort{Field: "addedAt", Descending: true}
	smart.Limit = ptr.To[int32](25)
	smart.Summary = ptr.To("Every Up film.")
	smart.SortTitle = ptr.To("Up 00")
	smart.Poster = &catalogv1.PlexImage{URL: "https://image.tmdb.org/t/p/original/up.jpg"}
	smart.Background = &catalogv1.PlexImage{URL: "https://image.tmdb.org/t/p/original/up-bg.jpg"}
	smart.Settings = &catalogv1.PlexCollectionSettings{Mode: catalogv1.PlexModeShowCollectionAndItems}
	smart.Visibility = &catalogv1.PlexVisibility{FriendsHome: true}
	return []catalogv1.PlexCollectionSpec{smart, refGhibli()}
}

// Every field of PlexCollectionSpec is set by one of fullSpecs, so a field
// added to the spec fails here until the editor carries it: the editor
// replaces the whole spec, and a field it does not read would be erased.
func TestFullSpecsSetEverySpecField(t *testing.T) {
	ty := reflect.TypeOf(catalogv1.PlexCollectionSpec{})
	for i := range ty.NumField() {
		set := false
		for _, s := range fullSpecs() {
			if !reflect.ValueOf(s).Field(i).IsZero() {
				set = true
			}
		}
		assert.True(t, set, "PlexCollectionSpec.%s: no fullSpecs sets it, so the editor's round trip does not cover it", ty.Field(i).Name)
	}
}

func TestTheEditorRoundTripsEverySpecField(t *testing.T) {
	for _, spec := range fullSpecs() {
		t.Run(spec.Title, func(t *testing.T) {
			srv, c := collectionsServerWith(t, nil, withArtBase, plexCollection("full", spec))
			values := editorValues(t, get(t, srv, "/library/collections/"+pcolNS+"/full/edit").Body.String())
			rec := post(t, srv, "/library/collections/"+pcolNS+"/full", values)
			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			var got catalogv1.PlexCollection
			require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: pcolNS, Name: "full"}, &got))
			assert.Equal(t, spec, got.Spec)
		})
	}
}
