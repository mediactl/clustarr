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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// liveUnmatchedServer serves the Unmatched page off a projection that
// rounds again on every read after a few milliseconds, so a page read
// after an add sees it.
func liveUnmatchedServer(t *testing.T, c client.Client) *ui.Server {
	t.Helper()
	proj := projection.New(c, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = proj.Run(ctx) }()
	require.Eventually(t, func() bool { return proj.Projected() }, 2*time.Second, 5*time.Millisecond)
	return ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Namespace: "media", Unmatched: proj.Unmatched,
	})
}

// unmatchedBody is the Unmatched page once it satisfies ok, read until it
// does: the projection rounds on a read, so the page catches up within a
// read or two.
func unmatchedBody(t *testing.T, srv *ui.Server, ok func(string) bool) string {
	t.Helper()
	var body string
	require.Eventually(t, func() bool {
		body = get(t, srv, "/unmatched").Body.String()
		return ok(body)
	}, 2*time.Second, 10*time.Millisecond, "the Unmatched page never caught up")
	return body
}

// One folder holds one item (D20): the Unmatched page's folder row stands
// until the root's next scan, so its other candidate's "Add as" -- the
// person's second try, after the first artist proved the wrong one -- is
// refused, naming the artist in the folder, and creates nothing; the row
// leaves the page once an item holds its folder, the scan's other entries
// staying; and the first candidate posted again is the artist the folder
// already holds, never a refusal.
func TestAFolderHoldsOneItem(t *testing.T) {
	objs := append(unmatchedFolderScans(), scannedMusicRoot, scannedAudioRoot, losslessProfile, m4bProfile)
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	srv := liveUnmatchedServer(t, c)

	body := unmatchedBody(t, srv, func(b string) bool { return strings.Contains(b, `data-folder-add="`+sixtiesBand+`"`) })
	firstAction, first, _ := folderAddForm(t, body, grungeNirvana)
	secondAction, second, _ := folderAddForm(t, body, sixtiesBand)

	rec := post(t, srv, firstAction, first)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	held := names.Artist("Nirvana", grungeNirvana)

	rec = post(t, srv, secondAction, second)
	require.Equal(t, http.StatusBadRequest, rec.Code, "a second item is never pinned to a held folder")
	require.Contains(t, html.UnescapeString(rec.Body.String()), `the folder "Nirvana" already holds the artist media/`+held)
	var other catalogv1.Artist
	err := c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: names.Artist("Nirvana", sixtiesBand)}, &other)
	require.True(t, apierrors.IsNotFound(err), "the refused artist was created: %v", err)

	body = unmatchedBody(t, srv, func(b string) bool { return !strings.Contains(b, `data-path="Nirvana"`) })
	require.Contains(t, body, `data-path="Loose/track.flac"`, "the scan's file entry stays")
	require.Contains(t, body, `data-path="Frank Herbert/Dune"`, "a folder no item holds stays")
	require.NotContains(t, body, `data-folder-add="`+sixtiesBand+`"`)

	rec = post(t, srv, firstAction, first)
	require.Equal(t, http.StatusSeeOther, rec.Code, "the folder's own artist is no other item: %s", rec.Body.String())
	require.Equal(t, "/library/media/artist/"+held, rec.Header().Get("Location"))
}

// An item holds a folder when its resolved status.path is the folder,
// pinned or not -- the root's preset put an artist added without a pin
// there -- and an item of another root pinning the same relative folder
// holds none of this root's.
func TestAFolderItsItemResolvedToIsHeld(t *testing.T) {
	placed := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "nirvana-placed", Namespace: "media"},
		Spec:       catalogv1.ArtistSpec{MusicBrainzID: "f1d3c1e0-0000-4000-8000-000000000001", RootFolderRef: "music", QualityProfileRef: "lossless"},
		Status:     catalogv1.ArtistStatus{Path: "/data/media/music/Nirvana"},
	}
	elsewhere := &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "dune-elsewhere", Namespace: "media"},
		Spec: catalogv1.AudiobookSpec{
			ASIN: "B00ELSEWHER", RootFolderRef: "audio-2", QualityProfileRef: "m4b", Folder: new("Frank Herbert/Dune"),
		},
	}
	objs := append(unmatchedFolderScans(), scannedMusicRoot, scannedAudioRoot, losslessProfile, m4bProfile, placed, elsewhere)
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	srv := liveUnmatchedServer(t, c)

	body := unmatchedBody(t, srv, func(b string) bool { return strings.Contains(b, `data-path="Frank Herbert/Dune"`) })
	require.NotContains(t, body, `data-path="Nirvana"`, "the placed artist holds the folder")
	require.Contains(t, body, `data-folder-add="B002V1OF70"`, "another root's pin holds none of this root's folders")

	rec := post(t, srv, "/library/music/add", url.Values{
		"title": {"Nirvana"}, "id": {grungeNirvana}, "rootFolder": {"media/music"}, "folder": {"Nirvana/"},
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, html.UnescapeString(rec.Body.String()), `the folder "Nirvana" already holds the artist media/nirvana-placed`)

	action, form, _ := folderAddForm(t, body, "B002V1OF70")
	rec = post(t, srv, action, form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
}

// The confirmation takes the root's defaults.tags, as the root scan's
// create does (scanapply's applyArtist, applyAuthor, applyAudiobook), so
// an artist confirmed on the Unmatched page is matched by the tagged
// DelayProfiles its scanned siblings are.
func TestTheConfirmationTakesTheRootsTags(t *testing.T) {
	music := scannedMusicRoot.DeepCopy()
	music.Spec.Defaults.Tags = []string{"lossless", "archive"}
	audio := scannedAudioRoot.DeepCopy()
	audio.Spec.Defaults.Tags = []string{"spoken"}
	objs := append(unmatchedFolderScans(), music, audio, losslessProfile, m4bProfile)
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	srv := unmatchedServer(t, c)
	body := get(t, srv, "/unmatched").Body.String()

	action, form, _ := folderAddForm(t, body, grungeNirvana)
	require.NotContains(t, form, "tags", "the row posts no tags: the root decides")
	rec := post(t, srv, action, form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var artist catalogv1.Artist
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: names.Artist("Nirvana", grungeNirvana)}, &artist))
	require.Equal(t, []string{"lossless", "archive"}, artist.Spec.Tags)

	action, form, _ = folderAddForm(t, body, "B002V1OF70")
	rec = post(t, srv, action, form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var book catalogv1.Audiobook
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: names.Audiobook("Dune", "B002V1OF70")}, &book))
	require.Equal(t, []string{"spoken"}, book.Spec.Tags)
}

// browserPost is what a browser posts from the hit form whose action is
// action in body, untouched: every named input, a checkbox only when
// checked (the hidden "false" beside each box always).
func browserPost(t *testing.T, body string) url.Values {
	t.Helper()
	m := regexp.MustCompile(`(?s)<form method="post" action="/library/[a-z]+/add"[^>]*>.*?</form>`).FindString(body)
	require.NotEmpty(t, m, "no hit form")
	attr := func(tag, name string) (string, bool) {
		a := regexp.MustCompile(`\s` + name + `(?:="([^"]*)")?[\s/>]`).FindStringSubmatch(tag)
		if a == nil {
			return "", false
		}
		return html.UnescapeString(a[1]), true
	}
	out := url.Values{}
	for _, tag := range regexp.MustCompile(`<input[^>]*>`).FindAllString(m, -1) {
		name, ok := attr(tag, "name")
		if !ok {
			continue
		}
		if typ, _ := attr(tag, "type"); typ == "checkbox" {
			if _, on := attr(tag, "checked"); !on {
				continue
			}
		}
		value, _ := attr(tag, "value")
		out.Add(name, value)
	}
	return out
}

// An Unmatched folder's "Search Add New" (D20) starts every hit's form
// from the folder's root's defaults, as its "Add as" adds the item: the
// root's profile and monitor-new mode, monitored as the root says, and no
// search -- so a root set unmonitored before an existing library's first
// scan (D16) is neither monitored nor searched by a box ticked from the
// start. Posted untouched, the form adds the artist "Add as" would. Add New
// without a folder starts as it always has.
func TestAnUnmatchedFoldersAddNewStartsFromTheRootsDefaults(t *testing.T) {
	flacOnly := losslessProfile.DeepCopy()
	flacOnly.Name = "flac-only"
	search := hits(commonv1.MediaKindArtist, `{"IDs":{"mb-artist":"`+grungeNirvana+`"},"Title":"Nirvana"}`)
	srv, c := addServerWith(t, search, nil, scannedMusicRoot, losslessProfile, flacOnly)

	res := get(t, srv, "/library/music/add/search?q=nirvana&folder=Nirvana&root=media%2Fmusic").Body.String()
	form := browserPost(t, res)
	require.Equal(t, "lossless", form.Get("qualityProfile"), "the root's profile, of two")
	require.Equal(t, "new", form.Get("monitorNewItems"), "the root's monitor-new mode")
	require.Equal(t, []string{"false"}, form["monitored"], "the root's defaults.monitored")
	require.Equal(t, []string{"false"}, form["searchOnAdd"], "confirming a folder starts no search")
	require.Equal(t, "Nirvana", form.Get("folder"))

	rec := post(t, srv, "/library/music/add", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var artist catalogv1.Artist
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: names.Artist("Nirvana", grungeNirvana)}, &artist))
	require.Equal(t, "lossless", artist.Spec.QualityProfileRef)
	require.False(t, *artist.Spec.Monitored)
	require.Equal(t, catalogv1.MonitorNewItemsNew, artist.Spec.MonitorNewItems)
	require.False(t, *artist.Spec.AddOptions.SearchForMissing)
	require.Equal(t, "Nirvana", *artist.Spec.Folder)

	plain := browserPost(t, get(t, srv, "/library/music/add/search?q=nirvana").Body.String())
	require.Empty(t, plain.Get("qualityProfile"), "two profiles and no folder: a person picks")
	require.Equal(t, "all", plain.Get("monitorNewItems"))
	require.Equal(t, []string{"false", "true"}, plain["monitored"])
	require.Equal(t, []string{"false", "true"}, plain["searchOnAdd"])
	require.Empty(t, plain.Get("folder"))
}

// An Unmatched audiobook folder's Add New starts on its root's region; a
// root that sets none starts on us, as "Add as" leaves the CRD's default,
// never the search's region read off another root.
func TestAnUnmatchedAudiobookFolderStartsOnItsRootsRegion(t *testing.T) {
	plainAudio := scannedAudioRoot.DeepCopy()
	plainAudio.Name, plainAudio.Spec.Path, plainAudio.Spec.Defaults.Region = "audio-2", "/data/media/audio-2", ""
	search := hits(commonv1.MediaKindAudiobook, `{"IDs":{"asin":"B002V1OF70"},"Title":"Dune","Authors":[{"Name":"Frank Herbert"}]}`)
	srv, _ := addServerWith(t, search, nil, scannedAudioRoot, plainAudio, m4bProfile)

	form := browserPost(t, get(t, srv, "/library/audiobooks/add/search?q=dune&folder=Frank+Herbert%2FDune&root=media%2Faudio").Body.String())
	require.Equal(t, "uk", form.Get("region"))
	require.Equal(t, []string{"false", "true"}, form["monitored"], "an unset defaults.monitored is monitored")
	require.Equal(t, []string{"false"}, form["searchOnAdd"])

	form = browserPost(t, get(t, srv, "/library/audiobooks/add/search?q=dune&folder=Frank+Herbert%2FDune&root=media%2Faudio-2").Body.String())
	require.Equal(t, "us", form.Get("region"), "the CRD's default, not the first root's uk")
	require.Equal(t, "media/audio-2", form.Get("rootFolder"))
}
