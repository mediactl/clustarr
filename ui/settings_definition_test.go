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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
)

// The Indexer form's Definition section (owner, 2026-11-16: "The Cardigann
// definition select in the indexer settings pane should fill all known
// settings defined by its definition file. It should also exclude usenet
// settings when the selection is a torrent indexer"). The definition is the
// real 1337x one under test/data, held as the IndexerDefinition the bundle
// loader would have applied.

func definitionYAML(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "test", "data", "cardigann", file))
	require.NoError(t, err)
	return string(data)
}

func bundled1337x(t *testing.T) (*indexv1.IndexerDefinition, *cardigann.Definition) {
	t.Helper()
	yaml := definitionYAML(t, "1337x.yml")
	def, err := cardigann.Load([]byte(yaml))
	require.NoError(t, err)
	return &indexv1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "x1337x"},
		Spec:       indexv1.IndexerDefinitionSpec{YAML: yaml},
		Status:     indexv1.IndexerDefinitionStatus{ID: "1337x", Name: def.Name, Type: indexv1.DefinitionTypePublic, Protocol: commonv1alpha1.ProtocolTorrent},
	}, def
}

func torrentIndexer(settings map[string]string) *indexv1.Indexer {
	return &indexv1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "leet", Namespace: "media"},
		Spec:       indexv1.IndexerSpec{Definition: new("1337x"), BaseURL: "https://1337x.to/", Settings: settings},
	}
}

// settingIndex is the map index a declared setting posts under.
func settingIndex(t *testing.T, def *cardigann.Definition, name string) int {
	t.Helper()
	for i, f := range def.Settings {
		if f.Name == name {
			return 1000 + i
		}
	}
	require.Failf(t, "no setting", "%q", name)
	return 0
}

func TestTheIndexerFormDrawsItsDefinitionsSettings(t *testing.T) {
	obj, def := bundled1337x(t)
	clients := []*downloadv1.DownloadClient{
		{ObjectMeta: metav1.ObjectMeta{Name: "qbit", Namespace: "media"}, Spec: downloadv1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolTorrent}},
		{ObjectMeta: metav1.ObjectMeta{Name: "eweka", Namespace: "media"}, Spec: downloadv1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolUsenet}},
	}
	srv, _ := settingsServer(t, obj, torrentIndexer(map[string]string{"uploader": "QxR", "mine": "kept"}), clients[0], clients[1])

	rec := get(t, srv, "/settings/edit/indexers/media/leet")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// the section, which fetches itself again when the choice changes
	requireTag(t, body, `data-section="Definition"`, `data-show-when="definition!="`)
	requireTag(t, body, `data-dynamic="definition"`, `hx-get="/settings/indexer-definition"`, `hx-trigger="change from:[name=&#39;definition&#39;]"`)
	// the definition's protocol, which the torrent-only parts follow
	requireTag(t, body, `name="__meta.protocol"`, `value="torrent"`)
	// its address, offered to Base URL
	requireTag(t, body, `name="__fill.baseURL"`, fmt.Sprintf(`value="%s"`, def.Links[0]), `data-fill="baseURL"`)

	// each declared setting an input under its name, the stored value in it
	i := settingIndex(t, def, "uploader")
	requireTag(t, body, fmt.Sprintf(`name="settings.__k.%d"`, i), `value="uploader"`, `type="hidden"`)
	requireTag(t, body, fmt.Sprintf(`name="settings.__v.%d"`, i), `value="QxR"`)
	for _, f := range def.Settings {
		if f.Type == "select" {
			requireTag(t, body, fmt.Sprintf(`name="settings.__v.%d"`, settingIndex(t, def, f.Name)), fmt.Sprintf(`value="%s"`, f.Default))
		}
	}
	// what it does not declare stays, by name
	requireTag(t, body, `name="settings.__k.0"`, `value="mine"`)
	requireTag(t, body, `name="settings.__v.0"`, `value="kept"`)

	// a torrent definition is not offered the usenet client
	requireTag(t, body, `data-slot="select-item" data-templ-value="eweka"`, `data-option-when="generic.protocol!=torrent&amp;__meta.protocol!=torrent"`)
	requireTag(t, body, `data-slot="select-item" data-templ-value="qbit"`, `data-option-when="generic.protocol!=usenet&amp;__meta.protocol!=usenet"`)
	// and the generic upstream's parts are behind the definition choice
	requireTag(t, body, `data-section="Generic Newznab/Torznab"`, `data-show-when="definition="`)
}

func TestTheDefinitionSectionIsFetchedForTheChosenDefinition(t *testing.T) {
	obj, def := bundled1337x(t)
	srv, _ := settingsServer(t, obj, torrentIndexer(map[string]string{"uploader": "QxR"}))

	// a new indexer: the definition's defaults
	rec := get(t, srv, "/settings/indexer-definition?definition=1337x")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html", "the section's content alone")
	requireTag(t, body, `name="__meta.protocol"`, `value="torrent"`)
	i := settingIndex(t, def, "uploader")
	requireTag(t, body, fmt.Sprintf(`name="settings.__k.%d"`, i), `value="uploader"`)
	require.NotContains(t, body, "QxR")
	require.Contains(t, body, def.Name)

	// an existing one: its stored settings fill what the definition declares
	rec = get(t, srv, "/settings/indexer-definition?definition=1337x&__name=leet&__namespace=media")
	requireTag(t, rec.Body.String(), fmt.Sprintf(`name="settings.__v.%d"`, i), `value="QxR"`)

	// none chosen: nothing but the protocol, unset
	rec = get(t, srv, "/settings/indexer-definition?definition=")
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `name="__meta.protocol"`, `value=""`)
	require.NotContains(t, rec.Body.String(), "settings.__k")

	// one no loaded definition has: said, never guessed
	rec = get(t, srv, "/settings/indexer-definition?definition=nope")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "No definition nope")
}

func TestSavingAnIndexerRecordsItsDefinitionSettings(t *testing.T) {
	obj, def := bundled1337x(t)
	srv, c := settingsServer(t, obj, torrentIndexer(map[string]string{"uploader": "QxR", "mine": "kept"}))

	form := url.Values{
		"__name": {"leet"}, "definition": {"1337x"}, "baseURL": {"https://1337x.to/"},
		"settings.__k.0": {"mine"}, "settings.__v.0": {"kept"},
		"__secret.secretRef.cookie": {"session=abc"},
	}
	var changed string
	for _, f := range def.Settings {
		k, v := fmt.Sprintf("settings.__k.%d", settingIndex(t, def, f.Name)), fmt.Sprintf("settings.__v.%d", settingIndex(t, def, f.Name))
		switch f.Type {
		case "text":
			form[k], form[v] = []string{f.Name}, []string{"Team"}
		case "select":
			// as the form posts it untouched: the default
			form[k], form[v] = []string{f.Name}, []string{string(f.Default)}
			if changed == "" {
				for o := range f.Options {
					if o != string(f.Default) {
						form[v], changed = []string{o}, f.Name
						break
					}
				}
			}
		case "checkbox":
			form[k], form[v] = []string{f.Name}, []string{"false", string(f.Default)}
		}
	}
	require.NotEmpty(t, changed)
	rec := post(t, srv, "/settings/edit/indexers/media/leet", form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	var idx indexv1.Indexer
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "leet"}, &idx))
	// the choices made, not a copy of the definition's defaults
	require.Equal(t, "Team", idx.Spec.Settings["uploader"])
	require.Equal(t, "kept", idx.Spec.Settings["mine"])
	require.Contains(t, idx.Spec.Settings, changed)
	require.Len(t, idx.Spec.Settings, 3, "%v", idx.Spec.Settings)
	// the credential in the Secret the indexer now names, under the setting's name
	require.NotNil(t, idx.Spec.SecretRef)
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: idx.Spec.SecretRef.Name}, &s))
	require.Equal(t, map[string]string{"cookie": "session=abc"}, s.StringData)
}

// The Settings page and its forms are accordion areas (owner, 2026-11-16:
// "condense the cards into 3 or 4 columns instead of the existing two";
// "Consider making each section an accordion area"; "The Add button ...
// should be right aligned and follow standard shadcn styling patterns").
func TestTheSettingsPageIsAccordionAreasOfDenseCards(t *testing.T) {
	srv, _ := settingsServer(t, usenetClient())
	rec := get(t, srv, "/settings")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// each kind a card that is a collapsible, closed as the page lands
	requireTag(t, body, `data-settings-kind="downloadclients"`, `id="downloadclients"`, `data-slot="card"`, `data-settings-count="1"`)
	section := body[strings.Index(body, `data-settings-kind="downloadclients"`):]
	section = section[:strings.Index(section, `data-settings-kind="metadataproviders"`)]
	requireTag(t, section, `data-slot="collapsible"`, `data-closed`)
	requireTag(t, section, `data-slot="collapsible-content"`, `hidden`)
	// shadcn's header: the title, the names as its description, and Add in
	// the action slot, which the header's own layout puts at its end
	header := section[strings.Index(section, `data-slot="card-header"`):strings.Index(section, `data-slot="collapsible-content"`)]
	require.Contains(t, header, `data-slot="card-title"`)
	require.Contains(t, header, "Download Clients")
	requireTag(t, header, `data-slot="card-description"`, `data-settings-summary`)
	require.Contains(t, header, "eweka")
	action := header[strings.Index(header, `data-slot="card-action"`):]
	requireTag(t, action, `href="/settings/new/downloadclients"`, `data-action="add"`)
	require.Equal(t, 2, strings.Count(header, `data-templ-controls=`), "the title and the chevron both open the section")
	// three or four cards to a row on a wide screen
	require.Contains(t, section, `xl:grid-cols-3 2xl:grid-cols-4`)
	requireTag(t, section, `data-download-client="media/eweka"`, `data-slot="card"`)
}

func TestAFormsAdvancedSectionsStartClosed(t *testing.T) {
	srv, _ := settingsServer(t)
	rec := get(t, srv, "/settings/new/indexers")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	sectionOf := func(title string) string {
		s := body[strings.Index(body, `data-section="`+title+`"`):]
		return s[:strings.Index(s, `data-slot="collapsible-content"`)+60]
	}
	requireTag(t, sectionOf("Indexer"), `data-slot="collapsible"`, `data-open`)
	requireTag(t, sectionOf("Limits"), `data-slot="collapsible"`, `data-closed`)
	requireTag(t, sectionOf("Limits"), `data-slot="collapsible-content"`, `hidden`)
	requireTag(t, body, `data-section="Limits"`, `data-advanced`)
}
