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
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
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

// TestValidateAndLoadRejectMalformedInputWithoutPanicking is the
// plan-mandated (docs/superpowers/plans, Global Constraints: "Malformed
// input never panics") malformed-input coverage for this package's two
// entry points that take a raw, un-decoded byte slice straight off the
// wire (an IndexerDefinition.spec.yaml a cluster admin could hand-author
// badly): empty, whitespace-only, truncated (an unterminated quoted
// scalar — a realistic copy-paste accident) and outright binary garbage.
// Every case must produce a non-nil error from both functions and must
// never panic.
func TestValidateAndLoadRejectMalformedInputWithoutPanicking(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte("")},
		{"nil", nil},
		{"whitespace only", []byte("   \n\t\n")},
		{"truncated unterminated quoted scalar", []byte("id: 'unterminated string\nname: x")},
		{"truncated flow mapping", []byte("id: x\ncaps: {categories: {")},
		{"binary garbage", []byte{0x00, 0xFF, 0x02, 0x80, 0x81, '\n', 'x', ':', '{', '}', '['}},
		{"garbage with stray brackets", []byte("\x00\x01\x02not yaml at all: [[[{{{")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var validateErr, loadErr error
			require.NotPanics(t, func() {
				validateErr = cardigann.Validate(tc.data)
			})
			assert.Error(t, validateErr)

			require.NotPanics(t, func() {
				_, loadErr = cardigann.Load(tc.data)
			})
			assert.Error(t, loadErr)
		})
	}
}

// TestSchemaErrorNamesNoWorkingDirectory: the embedded schema used to be
// registered under the bare name "schema-v11.json", which jsonschema/v6
// resolves against the process working directory -- so a validation
// failure rendered as file:///<cwd>/schema-v11.json#..., leaking the
// binary's working directory into Indexer and IndexerDefinition conditions
// for a file that is //go:embed-ed and not on disk at all.
func TestSchemaErrorNamesNoWorkingDirectory(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	verr := cardigann.Validate([]byte("id: x\nname: x\n"))
	require.Error(t, verr)
	assert.NotContains(t, verr.Error(), "file://")
	assert.NotContains(t, verr.Error(), wd)
	assert.Contains(t, verr.Error(), cardigann.SchemaURL)
}

// TestSchemaErrorIsBounded: jsonschema/v6 renders every failed branch of
// every oneOf, so a large invalid definition produced an error over three
// times its own size -- 105,599 bytes for a 73 KB file, past the 32,768-byte
// maxLength on conditions[].message. Over that the apply is rejected, not
// truncated: the condition explaining the invalid definition would itself
// fail to write, and the reconcile would error-loop on a terminal state.
func TestSchemaErrorIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("id: big\nname: big\ndescription: d\nlanguage: en-US\ntype: public\nencoding: UTF-8\n")
	b.WriteString("links: [\"https://example.org/\"]\n")
	b.WriteString("caps: {categorymappings: [{id: 1, cat: Movies}], modes: {search: [q]}}\n")
	b.WriteString("search:\n  path: /\n  rows: {selector: tr}\n  fields:\n")
	for i := range 3000 {
		// Every field name is illegal, and every value is the wrong type,
		// so each one fails every patternProperties branch.
		fmt.Fprintf(&b, "    bogus%d: [%d]\n", i, i)
	}
	data := []byte(b.String())

	verr := cardigann.Validate(data)
	require.Error(t, verr)
	assert.LessOrEqual(t, len(verr.Error()), cardigann.MaxSchemaErrorBytes+len("cardigann: schema validation: "),
		"a schema error must fit a condition message")
	assert.True(t, utf8.ValidString(verr.Error()))

	// The full detail is still reachable for a caller that wants it.
	var ve *jsonschema.ValidationError
	require.ErrorAs(t, verr, &ve)
	assert.Greater(t, len(ve.Error()), cardigann.MaxSchemaErrorBytes, "the fixture must actually exceed the bound")
	assert.ErrorIs(t, verr, cardigann.ErrInvalidDefinition)

	_, lerr := cardigann.Load(data)
	require.Error(t, lerr)
	assert.LessOrEqual(t, len(lerr.Error()), cardigann.MaxSchemaErrorBytes+len("cardigann: schema validation: "))
}
