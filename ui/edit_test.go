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
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// The item page's toolbar has Radarr's Edit: a wrench opening "Edit -
// <title>" with Monitored, the Quality Profile select over the cluster's
// QualityProfiles (the item's own selected), and a movie's Minimum
// Availability; Save patches those spec leaves and returns to the page.
func TestEditDialogChangesTheQualityProfile(t *testing.T) {
	srv, c := movieFixture(t, true)
	for _, name := range []string{"uhd-bluray-web", "hd-bluray-web", "any"} {
		require.NoError(t, c.Create(t.Context(), &catalogv1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	}
	page := detailPage(t, srv, "/library/default/movie/nerve")
	bar := section(t, page, `data-toolbar`)
	requireTag(t, bar, `data-action="edit-item"`, `data-toolbar-button`)
	require.Contains(t, bar, "Edit")
	require.Less(t, strings.Index(page, `data-action="edit-item"`), strings.Index(page, `data-action="delete-confirm"`),
		"Edit comes before Delete, as in Radarr")
	dlg := section(t, page, `data-edit-dialog="nerve"`)
	require.Contains(t, dlg, "Edit - Nerve")
	require.Contains(t, dlg, `action="/library/default/movie/nerve/edit"`)
	require.Contains(t, dlg, `name="qualityProfile"`)
	for _, name := range []string{"any", "hd-bluray-web", "uhd-bluray-web"} {
		require.Contains(t, dlg, `data-templ-value="`+name+`"`, "every QualityProfile is a choice")
	}
	require.Contains(t, dlg, `value="hd-bluray-web"`, "the movie's own profile is selected")
	require.Contains(t, dlg, `name="minimumAvailability"`)
	require.NotContains(t, dlg, `name="seriesType"`, "a movie has no series type")
	require.Contains(t, dlg, `name="monitored"`)
	require.Contains(t, dlg, `data-action="edit-save"`)

	rec := postForm(t, srv, "/library/default/movie/nerve/edit", url.Values{
		"return": {"/library/default/movie/nerve"}, "qualityProfile": {"uhd-bluray-web"},
		"monitored": {"false", "true"}, "minimumAvailability": {"released"},
	}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/library/default/movie/nerve", rec.Header().Get("Location"))
	var m catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &m))
	require.Equal(t, "uhd-bluray-web", m.Spec.QualityProfileRef)
	require.NotNil(t, m.Spec.Monitored)
	require.True(t, *m.Spec.Monitored, "the switch's true follows the form's false")
	require.Equal(t, "released", string(m.Spec.MinimumAvailability))
	require.Equal(t, int64(328387), m.Spec.TmdbID, "a merge patch leaves the rest of the spec")

	// The switch off sends only the form's false.
	rec = postForm(t, srv, "/library/default/movie/nerve/edit", url.Values{
		"return": {"/library/default/movie/nerve"}, "qualityProfile": {"any"}, "monitored": {"false"},
	}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &m))
	require.Equal(t, "any", m.Spec.QualityProfileRef)
	require.False(t, *m.Spec.Monitored)
	require.Equal(t, "released", string(m.Spec.MinimumAvailability), "a field left out keeps its value")

	// What the dialog cannot mean is refused, and nothing is written.
	rec = postForm(t, srv, "/library/default/movie/nerve/edit", url.Values{"qualityProfile": {""}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = postForm(t, srv, "/library/default/episode/nerve/edit", url.Values{"qualityProfile": {"any"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// The dialog's kinds and the action's are one list.
func TestTheEditDialogsKindsAreTheActionsKinds(t *testing.T) {
	require.Equal(t, actions.EditableKinds(), views.EditableKinds())
}
