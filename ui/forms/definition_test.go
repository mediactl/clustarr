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

package forms_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/ui/forms"
)

// The Indexer form's Definition section is drawn from the chosen
// definition's own settings (owner, 2026-11-16: "The Cardigann definition
// select ... should fill all known settings defined by its definition
// file"). The definitions here are the real ones under test/data.

func definition(t *testing.T, file string) *cardigann.Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "data", "cardigann", file))
	require.NoError(t, err)
	def, err := cardigann.Load(data)
	require.NoError(t, err)
	return def
}

func byName(controls []forms.Control, name string) (forms.Control, bool) {
	for _, c := range controls {
		if c.Name == name {
			return c, true
		}
	}
	return forms.Control{}, false
}

// settingControl finds the input a declared setting posts its value
// through: the control after the hidden one naming the setting.
func settingControl(t *testing.T, controls []forms.Control, setting string) forms.Control {
	t.Helper()
	for i, c := range controls {
		if c.Type == forms.ControlHidden && c.Value == setting && i+1 < len(controls) {
			return controls[i+1]
		}
	}
	require.Failf(t, "no setting", "%q", setting)
	return forms.Control{}
}

func TestADefinitionsSettingsAreInputsOfTheirOwnTypes(t *testing.T) {
	def := definition(t, "1337x.yml")
	stored := map[string]string{"uploader": "QxR", "sort": "seeders", "mine": "kept"}
	controls := forms.DefinitionControls(def, forms.DefinitionInfo{Protocol: "torrent", Privacy: "public"}, stored)

	proto, ok := byName(controls, forms.MetaProtocol)
	require.True(t, ok)
	require.Equal(t, "torrent", proto.Value, "what the torrent-only fields and clients follow")

	fill, ok := byName(controls, "__fill.baseURL")
	require.True(t, ok)
	require.Equal(t, def.Links[0], fill.Value)
	require.Equal(t, "baseURL", fill.Fill)

	// every declared setting that takes a value has an input; the stored
	// value fills it, else the definition's default
	for _, f := range def.Settings {
		switch f.Type {
		case "text":
			c := settingControl(t, controls, f.Name)
			require.Equal(t, forms.ControlText, c.Type, f.Name)
			require.Equal(t, stored[f.Name], c.Value, f.Name)
		case "select":
			c := settingControl(t, controls, f.Name)
			require.Equal(t, forms.ControlSelect, c.Type, f.Name)
			require.Len(t, c.Options, len(f.Options), f.Name)
			want := string(f.Default)
			if v, has := stored[f.Name]; has {
				want = v
			}
			require.Equal(t, want, c.Value, f.Name)
		case "checkbox":
			c := settingControl(t, controls, f.Name)
			require.Equal(t, forms.ControlCheckbox, c.Type, f.Name)
			require.Equal(t, string(f.Default) == "true", c.Checked, f.Name)
		}
	}
	require.Equal(t, "QxR", settingControl(t, controls, "uploader").Value)
	require.Equal(t, "seeders", settingControl(t, controls, "sort").Value)

	// what the definition does not declare is kept, as a free table
	other, ok := byName(controls, "settings")
	require.True(t, ok)
	require.Equal(t, forms.ControlMap, other.Type)
	require.Equal(t, []forms.Pair{{Key: "mine", Value: "kept"}}, other.Pairs)

	// an info setting is a note, its markup gone
	var notes int
	for _, c := range controls {
		if c.Type == forms.ControlNote {
			notes++
			require.NotContains(t, c.Help, "<", c.Label)
		}
	}
	require.GreaterOrEqual(t, notes, 2, "the summary and the definition's own notes")

	// a declared setting's map index never meets the free table's rows
	for _, c := range controls {
		if c.Type == forms.ControlHidden && c.Name != forms.MetaProtocol && c.Fill == "" {
			require.Regexp(t, `^settings\.__k\.1\d{3}$`, c.Name)
		}
	}
}

func TestADefinitionsCredentialsGoToTheSecret(t *testing.T) {
	def := definition(t, "login-form.yml")
	controls := forms.DefinitionControls(def, forms.DefinitionInfo{Protocol: "torrent", Privacy: "private"}, map[string]string{"username": "in-the-spec"})

	for _, name := range []string{"username", "password"} {
		c, ok := byName(controls, "__secret.secretRef."+name)
		require.Truef(t, ok, "%s is a credential", name)
		require.Equal(t, forms.ControlPassword, c.Type)
		require.Empty(t, c.Value, "a credential is never shown")
	}
	// a login can be replaced by a session cookie the definition need not declare
	_, ok := byName(controls, "__secret.secretRef.cookie")
	require.True(t, ok)
	// and a credential is no spec setting
	for _, c := range controls {
		if c.Type == forms.ControlHidden {
			require.NotEqual(t, "username", c.Value)
			require.NotEqual(t, "password", c.Value)
		}
	}
	// A username an indexer keeps in spec.settings has no input of its own
	// now (the credential's posts into the Secret), so it stays in the free
	// table, where a save keeps it: an edit must not lose a login.
	other, ok := byName(controls, "settings")
	require.True(t, ok)
	require.Equal(t, []forms.Pair{{Key: "username", Value: "in-the-spec"}}, other.Pairs)
}

func TestNoDefinitionKeepsTheStoredSettings(t *testing.T) {
	controls := forms.DefinitionControls(nil, forms.DefinitionInfo{}, map[string]string{"a": "1"})
	proto, ok := byName(controls, forms.MetaProtocol)
	require.True(t, ok)
	require.Empty(t, proto.Value)
	other, ok := byName(controls, "settings")
	require.True(t, ok)
	require.Equal(t, []forms.Pair{{Key: "a", Value: "1"}}, other.Pairs)

	controls = forms.DefinitionControls(nil, forms.DefinitionInfo{}, nil)
	require.Len(t, controls, 1, "nothing but the protocol the conditions read")
}

func TestASavedIndexerRecordsChoicesNotTheDefinitionsDefaults(t *testing.T) {
	def := definition(t, "1337x.yml")
	settings := map[string]any{"mine": "kept", "uploader": "QxR"}
	var changed string
	for _, f := range def.Settings {
		switch f.Type {
		case "select":
			settings[f.Name] = string(f.Default)
			if changed == "" {
				for v := range f.Options {
					if v != string(f.Default) {
						settings[f.Name], changed = v, f.Name
						break
					}
				}
			}
		case "checkbox":
			settings[f.Name] = "false"
			if string(f.Default) == "true" {
				settings[f.Name] = "true"
			}
		}
	}
	require.NotEmpty(t, changed, "the fixture declares a select with more than one option")
	forms.PruneDefinitionDefaults(def, settings)
	require.Len(t, settings, 3, "%v", settings)
	require.Contains(t, settings, "mine")
	require.Contains(t, settings, "uploader")
	require.Contains(t, settings, changed)
}
