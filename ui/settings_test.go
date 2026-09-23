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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestSettingsPageRendersWithoutACluster proves GET /settings survives a nil
// Options.Reader (no cluster configured at all), mirroring every other
// page's own such test -- and, since Settings has no live stream, this is
// its own entire "nothing configured" contract, not shared with an SSE
// empty-state test the way Library and Unmatched have one.
func TestSettingsPageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	for _, want := range []string{
		"No root folders configured.",
		"No quality profiles configured.",
		"No indexers configured.",
		"No download clients configured.",
		"No metadata providers configured.",
		"No subtitle providers configured.",
		"No subtitle profiles configured.",
		"No transcode profiles configured.",
	} {
		require.Contains(t, body, want)
	}
}

// TestSettingsPageRendersEachKindWithDataAttributes seeds one object of each
// of the Settings page's eight kinds through a fake Reader and proves the
// handler wiring reaches every one of them: each row's own data-* attribute
// and edit-form action path (ruling R8) shows up in the rendered page.
func TestSettingsPageRendersEachKindWithDataAttributes(t *testing.T) {
	scheme := ui.MustNewReaderScheme()

	rootFolder := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie, ScanSchedule: "0 3 * * *"},
	}
	qualityProfile := &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-1080p"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindVideo,
			Tiers:     []catalogv1.Tier{{Name: "hd", Qualities: []string{"WEBDL-1080p"}}},
			Cutoff:    "hd", UpgradeAllowed: ptr.To(true),
		},
	}
	indexer := &indexv1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "1337x", Namespace: "default"},
		Spec:       indexv1.IndexerSpec{BaseURL: "https://example.invalid", Enabled: ptr.To(true), Priority: 25},
	}
	downloadClient := &downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: "default"},
		Spec:       downloadv1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolTorrent, Enabled: ptr.To(true), Priority: 1},
	}
	metadataProvider := &catalogv1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "default"},
		Spec:       catalogv1.MetadataProviderSpec{Type: catalogv1.MetadataProviderTMDB, Enabled: ptr.To(true), Priority: 50},
	}
	subtitleProvider := &subtitlev1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "opensubtitlescom", Namespace: "default"},
		Spec:       subtitlev1.SubtitleProviderSpec{Type: subtitlev1.SubtitleProviderOpenSubtitlesCom, Enabled: ptr.To(true), Priority: 50},
	}
	subtitleProfile := &subtitlev1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "english"},
		Spec: subtitlev1.SubtitleProfileSpec{
			Languages: []subtitlev1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	transcodeProfile := &transcodev1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hevc-10bit"},
		Spec:       transcodev1.TranscodeProfileSpec{Priority: 50},
	}

	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		rootFolder, qualityProfile, indexer, downloadClient, metadataProvider, subtitleProvider, subtitleProfile, transcodeProfile,
	).Build()

	srv := ui.NewServer(t.Context(), ui.Options{Reader: reader})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, `data-root-folder="default/movies"`)
	require.Contains(t, body, `action="/settings/rootfolders/default/movies"`)

	require.Contains(t, body, `data-quality-profile="hd-1080p"`)
	require.Contains(t, body, `data-upgrade-allowed="true"`)
	require.Contains(t, body, `action="/settings/qualityprofiles/hd-1080p"`)

	require.Contains(t, body, `data-indexer="default/1337x"`)
	require.Contains(t, body, `data-priority="25"`)
	require.Contains(t, body, `action="/settings/indexers/default/1337x"`)

	require.Contains(t, body, `data-download-client="default/qbittorrent"`)
	require.Contains(t, body, `action="/settings/downloadclients/default/qbittorrent"`)

	require.Contains(t, body, `data-metadata-provider="default/tmdb"`)
	require.Contains(t, body, `action="/settings/metadataproviders/default/tmdb"`)

	require.Contains(t, body, `data-subtitle-provider="default/opensubtitlescom"`)
	require.Contains(t, body, `action="/settings/subtitleproviders/default/opensubtitlescom"`)

	require.Contains(t, body, `data-subtitle-profile="english"`)
	require.Contains(t, body, `action="/settings/subtitleprofiles/english"`)

	require.Contains(t, body, `data-transcode-profile="hevc-10bit"`)
	require.Contains(t, body, `action="/settings/transcodeprofiles/hevc-10bit"`)
}

// TestSettingsActionHandlersSucceedAndRedirect POSTs to each of the Settings
// page's eight action routes against a real (fake) writer and proves each
// one patches the field it claims to and redirects to /settings (the
// settingsReturnPath every settings.templ form carries).
func TestSettingsActionHandlersSucceedAndRedirect(t *testing.T) {
	scheme := ui.MustNewReaderScheme()

	rootFolder := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie},
	}
	qualityProfile := &catalogv1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-1080p"},
		Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindVideo,
			Tiers:     []catalogv1.Tier{{Name: "hd", Qualities: []string{"WEBDL-1080p"}}},
			Cutoff:    "hd", UpgradeAllowed: ptr.To(true),
		},
	}
	indexer := &indexv1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "1337x", Namespace: "default"},
		Spec:       indexv1.IndexerSpec{BaseURL: "https://example.invalid", Enabled: ptr.To(true), Priority: 25},
	}
	downloadClient := &downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: "default"},
		Spec:       downloadv1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolTorrent, Enabled: ptr.To(true), Priority: 1},
	}
	metadataProvider := &catalogv1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "default"},
		Spec:       catalogv1.MetadataProviderSpec{Type: catalogv1.MetadataProviderTMDB, Enabled: ptr.To(true), Priority: 50},
	}
	subtitleProvider := &subtitlev1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "opensubtitlescom", Namespace: "default"},
		Spec:       subtitlev1.SubtitleProviderSpec{Type: subtitlev1.SubtitleProviderOpenSubtitlesCom, Enabled: ptr.To(true), Priority: 50},
	}
	subtitleProfile := &subtitlev1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "english"},
		Spec: subtitlev1.SubtitleProfileSpec{
			Languages: []subtitlev1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	transcodeProfile := &transcodev1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hevc-10bit"},
		Spec:       transcodev1.TranscodeProfileSpec{Priority: 50},
	}

	writer := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		rootFolder, qualityProfile, indexer, downloadClient, metadataProvider, subtitleProvider, subtitleProfile, transcodeProfile,
	).Build()

	srv := ui.NewServer(t.Context(), ui.Options{Actions: actions.New(writer)})

	post := func(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusSeeOther, rec.Code, "response body: %s", rec.Body.String())
		require.Equal(t, "/settings", rec.Header().Get("Location"))
		return rec
	}

	t.Run("root folder scan schedule", func(t *testing.T) {
		post(t, "/settings/rootfolders/default/movies",
			url.Values{"scanSchedule": {"0 4 * * *"}, "return": {"/settings"}})
		var got catalogv1.RootFolder
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "movies"}, &got))
		require.Equal(t, "0 4 * * *", got.Spec.ScanSchedule)
	})

	t.Run("quality profile upgrade allowed", func(t *testing.T) {
		post(t, "/settings/qualityprofiles/hd-1080p",
			url.Values{"upgradeAllowed": {"false"}, "return": {"/settings"}})
		var got catalogv1.QualityProfile
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Name: "hd-1080p"}, &got))
		require.NotNil(t, got.Spec.UpgradeAllowed)
		require.False(t, *got.Spec.UpgradeAllowed)
	})

	t.Run("indexer enabled and priority", func(t *testing.T) {
		post(t, "/settings/indexers/default/1337x",
			url.Values{"enabled": {"false"}, "priority": {"10"}, "return": {"/settings"}})
		var got indexv1.Indexer
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "1337x"}, &got))
		require.NotNil(t, got.Spec.Enabled)
		require.False(t, *got.Spec.Enabled)
		require.EqualValues(t, 10, got.Spec.Priority)
	})

	t.Run("download client enabled and priority", func(t *testing.T) {
		post(t, "/settings/downloadclients/default/qbittorrent",
			url.Values{"enabled": {"false"}, "priority": {"5"}, "return": {"/settings"}})
		var got downloadv1.DownloadClient
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "qbittorrent"}, &got))
		require.NotNil(t, got.Spec.Enabled)
		require.False(t, *got.Spec.Enabled)
		require.EqualValues(t, 5, got.Spec.Priority)
	})

	t.Run("metadata provider enabled and priority", func(t *testing.T) {
		post(t, "/settings/metadataproviders/default/tmdb",
			url.Values{"enabled": {"false"}, "priority": {"90"}, "return": {"/settings"}})
		var got catalogv1.MetadataProvider
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "tmdb"}, &got))
		require.NotNil(t, got.Spec.Enabled)
		require.False(t, *got.Spec.Enabled)
		require.EqualValues(t, 90, got.Spec.Priority)
	})

	t.Run("subtitle provider enabled and priority", func(t *testing.T) {
		post(t, "/settings/subtitleproviders/default/opensubtitlescom",
			url.Values{"enabled": {"false"}, "priority": {"75"}, "return": {"/settings"}})
		var got subtitlev1.SubtitleProvider
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "opensubtitlescom"}, &got))
		require.NotNil(t, got.Spec.Enabled)
		require.False(t, *got.Spec.Enabled,
			"spec.enabled must really be false -- the merge patch body has no omitempty tag, "+
				"see ui/actions/settings.go's own doc comment")
		require.EqualValues(t, 75, got.Spec.Priority)
	})

	t.Run("subtitle profile default", func(t *testing.T) {
		post(t, "/settings/subtitleprofiles/english",
			url.Values{"default": {"true"}, "return": {"/settings"}})
		var got subtitlev1.SubtitleProfile
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Name: "english"}, &got))
		require.True(t, got.Spec.Default)
	})

	t.Run("transcode profile priority", func(t *testing.T) {
		post(t, "/settings/transcodeprofiles/hevc-10bit",
			url.Values{"priority": {"99"}, "return": {"/settings"}})
		var got transcodev1.TranscodeProfile
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Name: "hevc-10bit"}, &got))
		require.EqualValues(t, 99, got.Spec.Priority)
	})
}

// TestSettingsActionWithNoWriterRendersVisibleError mirrors
// ui/library_test.go's TestSetMonitoredActionWithNoWriterRendersVisibleError
// for the Settings page's own actions: a ui process with no cluster
// configured has a nil Options.Actions, which must still answer
// with a visible, machine-checkable error rather than silently doing
// nothing.
func TestSettingsActionWithNoWriterRendersVisibleError(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	form := url.Values{"enabled": {"false"}, "priority": {"10"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/indexers/default/1337x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="no-writer"`)
}

// TestSettingsAndImportListsNavLinksOnEveryPage extends
// ui/library_test.go's TestLibraryNavLinksOnEveryPage to the two pages this
// task adds.
func TestSettingsAndImportListsNavLinksOnEveryPage(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})

	for _, path := range []string{"/pipeline", "/downloads", "/library", "/unmatched", "/import-lists", "/settings"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Contains(t, rec.Body.String(), `href="/import-lists"`, "page %s is missing the Import Lists nav link", path)
		require.Contains(t, rec.Body.String(), `href="/settings"`, "page %s is missing the Settings nav link", path)
	}
}
