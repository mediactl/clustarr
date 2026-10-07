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

package cardigann_test

import (
	"testing"

	yaml "github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func TestScalarUnmarshalsEveryPrimitiveKind(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want cardigann.Scalar
	}{
		{"string", `v: hello`, "hello"},
		{"quoted number looking string", `v: "0"`, "0"},
		{"bare int", `v: 0`, "0"},
		{"bare float", `v: 1.5`, "1.5"},
		{"bare bool true", `v: true`, "true"},
		{"bare bool false", `v: false`, "false"},
		{"null", "v:", ""},
		// The number as written: Prowlarr keeps the text, and goccy reads
		// these as YAML 1.1 numbers (nyaasi's category ids, 0_0 and 1_2).
		{"underscored int", `v: 0_0`, "0_0"},
		{"underscored id", `v: 1_2`, "1_2"},
		{"leading zero", `v: 01`, "01"},
		{"float with a zero fraction", `v: 1.0`, "1.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc struct{ V cardigann.Scalar }
			require.NoError(t, yaml.Unmarshal([]byte(tc.yaml), &doc))
			assert.Equal(t, tc.want, doc.V)
		})
	}
}

func TestScalarMapKeepsOptionKeysAsWritten(t *testing.T) {
	var doc struct{ Options cardigann.ScalarMap }
	require.NoError(t, yaml.Unmarshal([]byte("options:\n  0_0: All categories\n  1_2: Anime - English-translated\n  2: Trusted only\n"), &doc))
	assert.Equal(t, cardigann.ScalarMap{"0_0": "All categories", "1_2": "Anime - English-translated", "2": "Trusted only"}, doc.Options)

	var none struct{ Options cardigann.ScalarMap }
	require.NoError(t, yaml.Unmarshal([]byte("options:"), &none))
	assert.Nil(t, none.Options)
}

func TestScalarListAcceptsBareScalarOrList(t *testing.T) {
	var single struct{ Args cardigann.ScalarList }
	require.NoError(t, yaml.Unmarshal([]byte(`args: "htt MMM. d"`), &single))
	assert.Equal(t, cardigann.ScalarList{"htt MMM. d"}, single.Args)

	var list struct{ Args cardigann.ScalarList }
	require.NoError(t, yaml.Unmarshal([]byte(`args: ["-", " "]`), &list))
	assert.Equal(t, cardigann.ScalarList{"-", " "}, list.Args)

	var mixed struct{ Args cardigann.ScalarList }
	require.NoError(t, yaml.Unmarshal([]byte(`args: ["/", 3]`), &mixed))
	assert.Equal(t, cardigann.ScalarList{"/", "3"}, mixed.Args)
}

func TestLoadParsesBothBundledDefinitions(t *testing.T) {
	for _, name := range []string{"1337x.yml", "0dayfiles-api.yml"} {
		t.Run(name, func(t *testing.T) {
			data := readTestdata(t, name)
			def, err := cardigann.Load(data)
			require.NoError(t, err)
			assert.NotEmpty(t, def.ID)
			assert.NotEmpty(t, def.Search.Fields)
		})
	}
}
