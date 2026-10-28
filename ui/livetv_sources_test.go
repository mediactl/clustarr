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
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

// tvNow is the Live TV pages' clock in these tests.
var tvNow = time.Date(2026, 10, 27, 19, 10, 0, 0, time.UTC)

const tvAddr = "clustarr-livetv.media.svc.cluster.local:80"

// tvProvider is argon-tv as a reconciled provider: two blocks with their
// URIs, an account expiring in three days, a guide from a guideURL.
func tvProvider() *clustarrv1.IPTVProvider {
	exp := metav1.NewTime(tvNow.Add(72 * time.Hour))
	fetched := metav1.NewTime(tvNow.Add(-time.Hour))
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon-tv"}}
	p.Spec.Server = "https://panel.example:8080"
	p.Spec.CredentialsRef = corev1.LocalObjectReference{Name: "argon-tv-credentials"}
	p.Spec.GuideURL = "https://epg.example/{username}/guide.xml?key={password}"
	p.Status = clustarrv1.IPTVProviderStatus{
		Address:    tvAddr,
		Conditions: []metav1.Condition{{Type: clustarrv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "serving 257 channels", LastTransitionTime: fetched}},
		Account:    &clustarrv1.AccountStatus{Status: "Active", ExpiresAt: &exp, MaxConnections: 4},
		Playlist:   &clustarrv1.PlaylistStatus{Entries: 28527, Groups: 410, Candidates: 3120, FetchedAt: &fetched},
		Guide:      &clustarrv1.GuideStatus{Channels: 13025, Programmes: 1100000, FetchedAt: &fetched},
		Lineup:     &clustarrv1.LineupStatus{Active: 257, First: "1001", Last: "1344"},
		Tuners:     &clustarrv1.TunerStatus{Total: 4, InUse: 1},
		Blocks: []clustarrv1.BlockStatus{
			{Start: "1001", First: "1001", Last: "1199", Channels: 200, DeviceID: "AAAA0001", Path: "/hdhr/AAAA0001", URI: "http://" + tvAddr + "/hdhr/AAAA0001", Owner: "plex-a", LineupHash: "0123456789abcdef", GuideHash: "fedcba9876543210"},
			{Start: "1200", First: "1200", Last: "1344", Channels: 57, DeviceID: "AAAA0002", Path: "/hdhr/AAAA0002", URI: "http://" + tvAddr + "/hdhr/AAAA0002"},
		},
	}
	return p
}

// tvServer is a ui over a fake cluster holding objs, writing through it,
// its Live TV clock at tvNow.
func tvServer(t *testing.T, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	return tvServerWith(t, liveTVStore(t), objs...)
}

// tvServerWith is tvServer reading store.
func tvServerWith(t *testing.T, store events.ObjectStore, objs ...client.Object) (*ui.Server, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ui.MustNewReaderScheme()).WithObjects(objs...).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Reader: c, Actions: actions.New(c), Namespace: "media", LiveTV: store})
	ui.SetNowForTest(srv, func() time.Time { return tvNow })
	return srv, c
}

// postHTMX posts form to path as an htmx action aimed at #action-status.
func postHTMX(t *testing.T, srv *ui.Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSourcesPageListsEachProviderItsGuideAndItsBlocks(t *testing.T) {
	srv, _ := tvServer(t, tvProvider())
	rec := get(t, srv, "/livetv/sources")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	row := tagWith(t, body, `data-provider="media/argon-tv"`)
	require.NotEmpty(t, row)
	for _, want := range []string{"argon-tv", "Xtream", "serving 257 channels", "28527", "3120", "257", "1 / 4"} {
		assert.Contains(t, body, want)
	}
	requireTag(t, body, `data-expires-soon`)
	assert.Contains(t, body, "https://epg.example/{username}/guide.xml?key={password}", "the guide's source as written")
	assert.Contains(t, body, "the panel's guide fills the channels it lacks")
	for _, uri := range []string{"http://" + tvAddr + "/hdhr/AAAA0001", "http://" + tvAddr + "/hdhr/AAAA0002"} {
		requireTag(t, body, `data-copy="`+uri+`"`)
	}
	assert.Contains(t, body, "plex-a", "the block's Plex owner")
	assert.Contains(t, body, "01234567", "the lineup hash's first 8")
	requireTag(t, body, `href="/settings/new/iptvproviders?return=/livetv/sources"`)
	requireTag(t, body, `href="/settings/edit/iptvproviders/media/argon-tv?return=/livetv/sources"`)
	requireTag(t, body, `hx-post="/livetv/providers/media/argon-tv/refresh"`)
}

func TestSourcesPageNeverShowsTheAccount(t *testing.T) {
	p := tvProvider()
	p.Spec.GuideURL = ""
	srv, _ := tvServer(t, p)
	body := get(t, srv, "/livetv/sources").Body.String()
	assert.Contains(t, body, "https://panel.example:8080/xmltv.php")
	assert.NotContains(t, body, "username=")
	assert.NotContains(t, body, "password=")
}

func TestAnEPGSourcePMSProviderShowsPlexsGuide(t *testing.T) {
	p := tvProvider()
	p.Spec.EPGSource = clustarrv1.EPGSourcePMS
	srv, _ := tvServer(t, p)
	body := get(t, srv, "/livetv/sources").Body.String()
	guide := tagWith(t, body, `data-guide="media/argon-tv"`)
	require.NotEmpty(t, guide)
	assert.Contains(t, body, "Plex's")
	assert.NotContains(t, guide, "hx-post", "nothing to refresh: Plex fetches its own guide")
}

func TestSourcesTablesRefreshThemselves(t *testing.T) {
	srv, _ := tvServer(t, tvProvider())
	body := get(t, srv, "/livetv/sources").Body.String()
	requireTag(t, body, `id="livetv-sources"`, `hx-get="/livetv/sources/tables"`, `hx-trigger="every 10s"`, `hx-swap="outerHTML"`)
	rec := get(t, srv, "/livetv/sources/tables")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "<div"), "the fragment alone")
	assert.NotContains(t, rec.Body.String(), "<html")
	requireTag(t, rec.Body.String(), `id="livetv-sources"`)
}

func TestRefreshNowWritesTheAnnotation(t *testing.T) {
	srv, c := tvServer(t, tvProvider())
	rec := postHTMX(t, srv, "/livetv/providers/media/argon-tv/refresh", url.Values{})
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	var got clustarrv1.IPTVProvider
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "argon-tv"}, &got))
	assert.Regexp(t, regexp.MustCompile(`^[0-9]+$`), got.Annotations[clustarrv1.AnnotationLiveTVRefresh])
}

func TestTheEnabledSwitchPatchesSpecEnabled(t *testing.T) {
	srv, c := tvServer(t, tvProvider())
	rec := postHTMX(t, srv, "/livetv/providers/media/argon-tv/enabled", url.Values{"enabled": {"false"}})
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	var got clustarrv1.IPTVProvider
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "argon-tv"}, &got))
	require.NotNil(t, got.Spec.Enabled)
	assert.False(t, *got.Spec.Enabled)
	rec = postHTMX(t, srv, "/livetv/providers/media/argon-tv/enabled", url.Values{"enabled": {"maybe"}})
	requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
}

func TestLiveTVPagesArePreloadable(t *testing.T) {
	srv, _ := tvServer(t, tvProvider())
	rec := get(t, srv, "/livetv/sources")
	assert.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Header().Values("Vary"), "HX-Request")
	rec = get(t, srv, "/livetv")
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/livetv/channels", rec.Header().Get("Location"))
}

func TestTheLiveTVSubMenuSwapsThePageBody(t *testing.T) {
	srv, _ := tvServer(t, tvProvider())
	body := get(t, srv, "/livetv/sources").Body.String()
	start := strings.Index(body, `data-slot="sidebar"`)
	inset := strings.Index(body, `data-slot="sidebar-inset"`)
	require.True(t, start > 0 && inset > start)
	sidebarHTML := body[start:inset]
	requireTag(t, sidebarHTML, `id="livetv-subnav"`, `data-slot="sidebar-menu-sub"`)
	requireTag(t, sidebarHTML, `id="app-nav"`)
	activeAttr := regexp.MustCompile(`\sdata-active(\s|>)`)
	for _, e := range []struct{ href, label string }{
		{"/livetv/channels", "Channels"}, {"/livetv/sources", "M3U/EPG Manager"}, {"/livetv/guide", "TV Guide"},
	} {
		entry := requireTag(t, sidebarHTML, `href="`+e.href+`"`, `data-slot="sidebar-menu-sub-button"`,
			`hx-get="`+e.href+`"`, `preload="mouseover"`, `hx-push-url="true"`, `hx-select="#page-body"`,
			`hx-select-oob="#app-nav,#page-crumbs,#header-find"`, `hx-target="#page-body"`, `hx-swap="outerHTML"`)
		if e.href == "/livetv/sources" {
			assert.Regexp(t, activeAttr, entry, "the current Live TV page is marked")
		} else {
			assert.NotRegexp(t, activeAttr, entry)
		}
		assert.Contains(t, sidebarHTML, e.label)
	}
	assert.Regexp(t, activeAttr, tagWith(t, sidebarHTML, `href="/livetv"`), "the Live TV entry is active")
	assert.NotRegexp(t, activeAttr, tagWith(t, sidebarHTML, `href="/library"`), "not the library's")
	crumbs := body[strings.Index(body, `id="page-crumbs"`):strings.Index(body, `id="header-find"`)]
	assert.Contains(t, crumbs, ">Live TV<")
	assert.Contains(t, crumbs, "M3U/EPG Manager")
}
