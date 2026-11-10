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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

const pcolNS = "clustarr-system"

func collectionsServer(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).
		WithStatusSubresource(&catalogv1.PlexCollection{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	srv := ui.NewServer(t.Context(), ui.Options{Reader: c, Actions: actions.New(c), Namespace: pcolNS})
	return srv, c
}

func plexCollection(name string, spec catalogv1.PlexCollectionSpec) *catalogv1.PlexCollection {
	return &catalogv1.PlexCollection{ObjectMeta: metav1.ObjectMeta{Namespace: pcolNS, Name: name}, Spec: spec}
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
	series := func(name string, md catalogv1.SeriesMetadata) *catalogv1.Series {
		s := &catalogv1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: pcolNS, Name: name}}
		s.Status.Metadata = &md
		return s
	}
	movie := func(name string, md catalogv1.MovieMetadata) *catalogv1.Movie {
		m := &catalogv1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: pcolNS, Name: name}}
		m.Status.Metadata = &md
		return m
	}
	funcs := &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*catalogv1.EpisodeList); ok {
			t.Error("the editor listed Episodes")
		}
		return c.List(ctx, list, opts...)
	}}
	srv, _ := collectionsServer(t, funcs,
		series("a", catalogv1.SeriesMetadata{Genres: []string{"Drama", "Anime"}, Network: "Netflix", Networks: []string{"Netflix", "HBO"}}),
		series("b", catalogv1.SeriesMetadata{Genres: []string{"Anime"}}),
		movie("m", catalogv1.MovieMetadata{Genres: []string{"Documentary"}, Studios: []string{"Pixar", "A24"}}),
	)

	show := get(t, srv, "/library/collections/new?itemType=show").Body.String()
	assertOptions(t, show, "suggest-show-genre", "Anime", "Drama")
	assertOptions(t, show, "suggest-show-network", "HBO", "Netflix")

	film := get(t, srv, "/library/collections/new?itemType=movie").Body.String()
	assertOptions(t, film, "suggest-studio", "A24", "Pixar")
	assertOptions(t, film, "suggest-genre", "Documentary")
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
