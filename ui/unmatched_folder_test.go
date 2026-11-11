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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/libraryscan"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

const (
	grungeNirvana = "5b11f4ce-a62d-471e-81fc-a69a8278c7da"
	sixtiesBand   = "9282c8b4-ca0b-4c6b-b7e3-4f7762dfc4d6"
)

var (
	// The owner's music root as D16 has it scanned: a profile and the
	// monitoring the scan's creates take, which the Unmatched page's
	// confirmation takes too.
	scannedMusicRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: "media"},
		Spec: catalogv1.RootFolderSpec{
			Path: "/data/media/music", Kind: catalogv1.RootFolderKindMusic,
			Defaults: catalogv1.RootDefaults{
				QualityProfileRef: "lossless", Monitored: new(false), MonitorNewItems: catalogv1.MonitorNewItemsNew,
			},
		},
	}
	scannedAudioRoot = &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "audio", Namespace: "media"},
		Spec: catalogv1.RootFolderSpec{
			Path: "/data/media/audio", Kind: catalogv1.RootFolderKindAudiobook,
			Defaults: catalogv1.RootDefaults{QualityProfileRef: "m4b", Region: catalogv1.AudiobookRegionUK},
		},
	}
	losslessProfile = &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "lossless"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindMusic, Cutoff: "flac",
			Tiers: []catalogv1.Tier{{Name: "flac", Qualities: []string{"FLAC"}}},
		},
	}
)

// unmatchedFolderScans are the LibraryScans of a music root and an
// audiobook root as the LibraryScan controller writes them: each folder the
// walk could not identify listed once, folder true, with the provider's
// hits as "Name (disambiguation) [id]" (D16), beside a file entry.
func unmatchedFolderScans() []client.Object {
	seen := metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	return []client.Object{
		&catalogv1.LibraryScan{
			ObjectMeta: metav1.ObjectMeta{Name: "music-scan", Namespace: "media"},
			Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "music"},
			Status: catalogv1.LibraryScanStatus{Unmatched: []catalogv1.UnmatchedFile{
				{
					Path: "Nirvana", Reason: "ambiguous_title: 2 MusicBrainz artists are named Nirvana", Folder: true, SeenAt: seen,
					Candidates: []string{
						"Nirvana (90s US grunge band) [" + grungeNirvana + "]",
						"Nirvana (60s band from the UK) [" + sixtiesBand + "]",
					},
				},
				{Path: "Loose/track.flac", Reason: "unrecognised_layout", SeenAt: metav1.NewTime(seen.Add(-time.Minute))},
			}},
		},
		&catalogv1.LibraryScan{
			ObjectMeta: metav1.ObjectMeta{Name: "audio-scan", Namespace: "media"},
			Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "audio"},
			Status: catalogv1.LibraryScanStatus{Unmatched: []catalogv1.UnmatchedFile{{
				Path: "Frank Herbert/Dune", Reason: "ambiguous_title", Folder: true, SeenAt: metav1.NewTime(seen.Add(-2 * time.Minute)),
				Candidates: []string{"Dune (Frank Herbert) [B002V1OF70]"},
			}}},
		},
	}
}

// unmatchedServer serves the Unmatched page off a real projection over c,
// with Add New's create on the same client.
func unmatchedServer(t *testing.T, c client.Client) *ui.Server {
	t.Helper()
	proj := projection.New(c, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = proj.Run(ctx) }()
	require.Eventually(t, func() bool { return len(proj.Unmatched(ctx)) == 3 }, 2*time.Second, 10*time.Millisecond,
		"the projection never listed the scans' entries")
	return ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c), Namespace: "media", Unmatched: proj.Unmatched,
	})
}

// folderAddForm is the folder row's form for the candidate id: its action,
// its hidden fields and its markup.
func folderAddForm(t *testing.T, body, id string) (action string, form url.Values, markup string) {
	t.Helper()
	m := regexp.MustCompile(`(?s)<form[^>]*data-folder-add="` + regexp.QuoteMeta(id) + `"[^>]*>.*?</form>`).FindString(body)
	require.NotEmpty(t, m, "no Add form for candidate %s", id)
	action = regexp.MustCompile(`action="([^"]*)"`).FindStringSubmatch(m)[1]
	form = url.Values{}
	for _, in := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(m, -1) {
		form.Add(in[1], html.UnescapeString(in[2]))
	}
	return html.UnescapeString(action), form, m
}

// The Unmatched page confirms a folder the scan could not identify (the
// 2026-11-10 non-video libraries design's D20): the folder's row, built by
// the projection from the scan's status, offers one "Add as artist" button
// per candidate -- a form posting Add New's create to the Music tab with
// the hit's title and MusicBrainz id, the scan's root and the folder -- and
// a "Search Add New" link carrying the folder's name, the folder and the
// root, and no manual-assign form, which a file row keeps. Posting the
// button's own form creates the Artist with the folder pinned, the root's
// profile and monitoring, and no search, as the scan's create would; an
// audiobook folder's form also names its author, whom the add links.
func TestTheUnmatchedPageConfirmsAFolder(t *testing.T) {
	objs := append(unmatchedFolderScans(), scannedMusicRoot, scannedAudioRoot, losslessProfile, m4bProfile,
		anAuthor("media", "frank-herbert-x", "Frank Herbert"))
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	srv := unmatchedServer(t, c)

	rec := get(t, srv, "/unmatched")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	requireTag(t, body, `data-path="Nirvana"`, `data-root-folder="music"`, ` data-folder`)
	require.NotContains(t, body, `data-manual-assign-form="Nirvana"`, "a folder is confirmed, not assigned")
	require.Contains(t, body, `data-manual-assign-form="Loose/track.flac"`, "a file row keeps its manual-assign form")
	require.NotRegexp(t, ` data-folder( |>|$)`, tagWith(t, body, `data-path="Loose/track.flac"`), "a file row is no folder")

	action, form, markup := folderAddForm(t, body, grungeNirvana)
	require.Equal(t, "/library/music/add", action)
	require.Equal(t, url.Values{
		"title": {"Nirvana"}, "id": {grungeNirvana}, "rootFolder": {"media/music"}, "folder": {"Nirvana"},
	}, form, "the hit's title and id, the scan's root and the folder, nothing the root's defaults decide")
	require.Contains(t, markup, "Add as artist: Nirvana (90s US grunge band)")
	_, other, otherMarkup := folderAddForm(t, body, sixtiesBand)
	require.Equal(t, sixtiesBand, other.Get("id"))
	require.Contains(t, otherMarkup, "Add as artist: Nirvana (60s band from the UK)")

	href := regexp.MustCompile(`href="([^"]*)"[^>]*data-action="search-folder"|data-action="search-folder"[^>]*href="([^"]*)"`).FindStringSubmatch(body)
	require.NotNil(t, href, "the folder row links to Add New")
	link, err := url.Parse(html.UnescapeString(href[1] + href[2]))
	require.NoError(t, err)
	require.Equal(t, "/library/music/add", link.Path)
	require.Equal(t, url.Values{"q": {"Nirvana"}, "folder": {"Nirvana"}, "root": {"media/music"}}, link.Query())

	created := post(t, srv, action, form)
	name := names.Artist("Nirvana", grungeNirvana)
	require.Equal(t, http.StatusSeeOther, created.Code, created.Body.String())
	require.Equal(t, "/library/media/artist/"+name, created.Header().Get("Location"))
	var artist catalogv1.Artist
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: name}, &artist))
	require.Equal(t, grungeNirvana, artist.Spec.MusicBrainzID)
	require.NotNil(t, artist.Spec.Folder)
	require.Equal(t, "Nirvana", *artist.Spec.Folder, "the folder the files are in, pinned")
	require.Equal(t, "music", artist.Spec.RootFolderRef)
	require.Equal(t, "lossless", artist.Spec.QualityProfileRef, "the root's default profile")
	require.False(t, *artist.Spec.Monitored, "the root's defaults.monitored")
	require.Equal(t, catalogv1.MonitorNewItemsNew, artist.Spec.MonitorNewItems, "the root's defaults.monitorNewItems")
	require.False(t, *artist.Spec.AddOptions.SearchForMissing, "a confirmation starts no search")
	require.Empty(t, artist.Spec.AddOptions.Monitor, "the CRD's default, all")

	action, form, markup = folderAddForm(t, body, "B002V1OF70")
	require.Equal(t, "/library/audiobooks/add", action)
	require.Equal(t, "Frank Herbert", form.Get("authorName"), "an audiobook folder names its author")
	require.Equal(t, "Frank Herbert/Dune", form.Get("folder"))
	require.Equal(t, "media/audio", form.Get("rootFolder"))
	require.Contains(t, markup, "Add as audiobook: Dune (Frank Herbert)")
	created = post(t, srv, action, form)
	require.Equal(t, http.StatusSeeOther, created.Code, created.Body.String())
	var book catalogv1.Audiobook
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: names.Audiobook("Dune", "B002V1OF70")}, &book))
	require.Equal(t, "Frank Herbert/Dune", *book.Spec.Folder)
	require.Equal(t, "m4b", book.Spec.QualityProfileRef)
	require.Equal(t, catalogv1.AudiobookRegionUK, book.Spec.Region, "the root's defaults.region")
	require.True(t, *book.Spec.Monitored, "an unset defaults.monitored is monitored")
	require.NotNil(t, book.Spec.AuthorRef)
	require.Equal(t, "frank-herbert-x", *book.Spec.AuthorRef, "the one Author of the folder's author name")
}

// A folder whose RootFolder is gone offers nothing to add it as.
func TestAFolderOfAGoneRootOffersNoAdd(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(unmatchedFolderScans()...).Build()
	body := get(t, unmatchedServer(t, c), "/unmatched").Body.String()
	requireTag(t, body, `data-path="Nirvana"`, ` data-folder`)
	require.Contains(t, body, `data-folder-actions="Nirvana"`)
	require.NotContains(t, body, `data-folder-add=`)
	require.NotContains(t, body, `data-action="search-folder"`)
}

// Add New opened from a folder's "Search Add New" (?folder=&root=, D20)
// carries the folder through its search to every hit's form, hidden, with
// the folder's root preselected, so the hit chosen there pins it too; a
// rejected add keeps it on the reopened form and the search box.
func TestAddNewCarriesTheUnmatchedFolder(t *testing.T) {
	other := scannedMusicRoot.DeepCopy()
	other.Name, other.Spec.Path, other.Spec.Defaults = "music-2", "/data/media/music-2", catalogv1.RootDefaults{}
	search := hits(commonv1.MediaKindArtist, `{"IDs":{"mb-artist":"`+grungeNirvana+`"},"Title":"Nirvana"}`)
	srv, c := addServerWith(t, search, nil, scannedMusicRoot, other, losslessProfile)

	page := get(t, srv, "/library/music/add?q=Nirvana&folder=Nirvana&root=media%2Fmusic").Body.String()
	requireTag(t, page, `hx-get="/library/music/add/search"`, `hx-vals="{&#34;folder&#34;:&#34;Nirvana&#34;,&#34;root&#34;:&#34;media/music&#34;}"`)

	res := get(t, srv, "/library/music/add/search?q=nirvana&folder=Nirvana&root=media%2Fmusic").Body.String()
	requireTag(t, res, `data-add-folder`, `name="folder"`, `value="Nirvana"`)
	requireTag(t, res, `name="rootFolder"`, `value="media/music"`)

	rec := post(t, srv, "/library/music/add", url.Values{
		"title": {"Nirvana"}, "id": {grungeNirvana}, "rootFolder": {"media/music"}, "qualityProfile": {"lossless"},
		"monitored": {"false", "true"}, "searchOnAdd": {"false"}, "folder": {"Nirvana"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	var artist catalogv1.Artist
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "media", Name: names.Artist("Nirvana", grungeNirvana)}, &artist))
	require.Equal(t, "Nirvana", *artist.Spec.Folder)
	require.True(t, *artist.Spec.Monitored, "a posted field is the form's, not the root's")

	// music-2 has no default profile: the confirmation is refused, and the
	// page it answers keeps the folder on the hit's reopened form.
	rec = post(t, srv, "/library/music/add", url.Values{
		"title": {"Nirvana"}, "id": {sixtiesBand}, "rootFolder": {"media/music-2"}, "folder": {"Nirvana"},
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	requireTag(t, body, `data-add-folder`, `value="Nirvana"`)
	requireTag(t, body, `hx-get="/library/music/add/search"`, `&#34;folder&#34;:&#34;Nirvana&#34;`)
}

// The scan page's progress line says how many files await their items'
// children (the scan progress's awaitingChildren, D20), from the Ready
// condition's message the LibraryScan controller writes as the tally's
// summary; a scan still walking reads as progress, not as a failure.
func TestTheScanPageShowsWhatAwaitsItsChildren(t *testing.T) {
	require.Equal(t, libraryscan.ReasonScanning, "Scanning", "the scan page restates the walking reason")
	progress := scanprogress.Progress{FilesSeen: 415, FilesMatched: 0}
	for range 412 {
		progress.Await("artist")
	}
	for range 3 {
		progress.Await("series")
	}
	scan := &catalogv1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "music-scan", Namespace: "media"},
		Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "music"},
		Status: catalogv1.LibraryScanStatus{
			Phase: catalogv1.ScanPhaseRunning, FilesSeen: 415,
			Conditions: []metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionFalse, Reason: libraryscan.ReasonScanning,
				Message: progress.Summary(), LastTransitionTime: metav1.Now(),
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(scan).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Reader: c})

	rec := get(t, srv, "/library-scans/media/music-scan")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	requireTag(t, body, `data-scan-awaiting=`, `data-scan-awaiting="412 awaiting their artists&#39; albums, 3 awaiting their series&#39; episodes"`)
	require.Contains(t, body, `data-scan-progress`)
	require.Contains(t, body, "415 files seen")
	require.NotContains(t, body, `data-scan-failure`, "a scan still walking has not failed")

	// Done, the same line stands; a failed scan keeps its alert and says
	// what awaited.
	scan.Status.Conditions[0].Status, scan.Status.Conditions[0].Reason = metav1.ConditionTrue, "Succeeded"
	require.NoError(t, c.Update(t.Context(), scan))
	body = get(t, srv, "/library-scans/media/music-scan").Body.String()
	require.Contains(t, body, `data-scan-awaiting="412 awaiting`)
	require.NotContains(t, body, `data-scan-failure`)

	scan.Status.Conditions[0].Status, scan.Status.Conditions[0].Reason = metav1.ConditionFalse, "NoProgress"
	scan.Status.Conditions[0].Message = "no worker reported progress since 2026-11-10T12:00:00Z (10m0s): " + progress.Summary()
	require.NoError(t, c.Update(t.Context(), scan))
	body = get(t, srv, "/library-scans/media/music-scan").Body.String()
	require.Contains(t, body, `data-scan-failure="NoProgress"`)
	require.Contains(t, body, `data-scan-awaiting="412 awaiting`)
	require.NotContains(t, body, `data-scan-progress`, "a failed scan's message is its alert")
}
