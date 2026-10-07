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
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// The settings pages are built from shadcn-templ's components on the
// theme's tokens (2026-09-29): no hard-coded palette colour, and every
// control a component -- a visible <input>, <button>, <label> or
// <textarea> carries its component's data-slot (or, for a component's
// inner input, is visually hidden: aria-hidden and out of the tab order),
// and a choice is the
// component select, never a bare <select>. The guard reads each page's
// body only, below the chrome.
var (
	paletteColour = regexp.MustCompile(`\b(?:bg|text|border|ring|fill|stroke|divide|outline)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d`)
	openTag       = regexp.MustCompile(`<(input|button|label|textarea|select)\b[^>]*>`)
)

func pageBody(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, `id="page-body"`)
	require.GreaterOrEqual(t, i, 0)
	return html[i:]
}

func requireThemedComponents(t *testing.T, page, html string) {
	t.Helper()
	body := pageBody(t, html)
	require.Empty(t, paletteColour.FindAllString(body, -1), "%s uses palette colours instead of the theme's tokens", page)
	for _, tag := range openTag.FindAllString(body, -1) {
		if strings.HasPrefix(tag, "<select") {
			t.Errorf("%s: a bare select, not the select component: %s", page, tag)
			continue
		}
		if strings.HasPrefix(tag, "<input") && strings.Contains(tag, `type="hidden"`) {
			continue
		}
		// a component's own parts carry its data-slot; its inner input (the
		// select's and the checkbox's) is aria-hidden and out of the tab order
		inner := strings.HasPrefix(tag, "<input") && strings.Contains(tag, `aria-hidden="true"`) && strings.Contains(tag, `tabindex="-1"`)
		if !strings.Contains(tag, "data-slot=") && !inner {
			t.Errorf("%s: a raw control, not a component: %s", page, tag)
		}
	}
}

func TestSettingsPagesUseComponentsOnTheTheme(t *testing.T) {
	srv, _ := settingsServer(t,
		&catalogv1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "media"},
			Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie, ScanSchedule: "0 3 * * *"},
		},
		&catalogv1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd"}, Spec: catalogv1.QualityProfileSpec{
			MediaKind: catalogv1.ProfileMediaKindVideo, BuiltIn: true, Cutoff: "hd",
			Tiers: []catalogv1.Tier{{Name: "hd", Qualities: []string{"WEBDL-1080p"}}},
		}},
		&indexv1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Name: "geek", Namespace: "media"},
			Spec: indexv1.IndexerSpec{
				BaseURL: "https://api.example.invalid", Enabled: new(false), Priority: 25,
				SecretRef: &corev1.LocalObjectReference{Name: "geek-credentials"},
			},
		},
		usenetClient(),
		&catalogv1.MetadataProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "media"},
			Spec:       catalogv1.MetadataProviderSpec{Type: catalogv1.MetadataProviderTMDB, Priority: 50},
		},
		&subtitlev1.SubtitleProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "osdb", Namespace: "media"},
			Spec:       subtitlev1.SubtitleProviderSpec{Type: subtitlev1.SubtitleProviderOpenSubtitlesCom, Priority: 50},
		},
		&subtitlev1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "english"}, Spec: subtitlev1.SubtitleProfileSpec{
			Default: true, Languages: []subtitlev1.LanguageItem{{Key: "en", Language: "en"}},
		}},
		&transcodev1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc"}, Spec: transcodev1.TranscodeProfileSpec{Priority: 10, Default: true}},
	)

	pages := []string{"/settings", "/settings/edit/downloadclients/media/eweka", "/settings/edit/qualityprofiles/-/hd"}
	for _, k := range actions.ConfigKinds() {
		pages = append(pages, "/settings/new/"+k.Slug)
	}
	for _, path := range pages {
		rec := get(t, srv, path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		requireThemedComponents(t, path, rec.Body.String())
	}

	// A rejected submission renders the action error, which is the alert
	// component.
	rec := post(t, srv, "/settings/new/rootfolders", nil)
	require.NotEqual(t, http.StatusOK, rec.Code)
	requireThemedComponents(t, "a rejected form", rec.Body.String())
	requireTag(t, rec.Body.String(), `data-action-error=`, `data-slot="alert"`)
}

func TestImportListsPageUsesComponentsOnTheTheme(t *testing.T) {
	entries := []projection.ImportListEntry{
		{
			Ref: types.NamespacedName{Namespace: "media", Name: "watch"}, Enabled: true, SourceType: "trakt", SyncLevel: "logOnly", Kinds: []string{"movie"},
			Auth: &projection.AuthView{State: "pending", UserCode: "AB12", VerificationURL: "https://trakt.tv/activate"}, LastError: "token expired",
		},
		{Ref: types.NamespacedName{Namespace: "media", Name: "off"}, SourceType: "plex"},
	}
	for _, e := range [][]projection.ImportListEntry{entries, nil} {
		srv := ui.NewServer(t.Context(), ui.Options{ImportLists: func(context.Context) []projection.ImportListEntry { return e }})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/import-lists", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		requireThemedComponents(t, "/import-lists", rec.Body.String())
	}
}
