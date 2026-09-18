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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func TestEmbeddedSchemaMatchesTheTestdataCopy(t *testing.T) {
	want := readTestdata(t, "schema-v11.json")
	assert.Equal(t, string(want), string(cardigann.EmbeddedSchemaForTest()))
}

func TestValidateAcceptsBothBundledDefinitions(t *testing.T) {
	for _, name := range []string{"1337x.yml", "0dayfiles-api.yml"} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, cardigann.Validate(readTestdata(t, name)))
		})
	}
}

func TestValidateRejectsAMissingRequiredField(t *testing.T) {
	broken := []byte("id: x\nname: x\n") // missing description, encoding, language, links, caps, search, type
	err := cardigann.Validate(broken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cardigann: schema validation")
}

func TestLoadRejectsWhatValidateRejects(t *testing.T) {
	_, err := cardigann.Load([]byte("id: x\n"))
	require.Error(t, err)
}
