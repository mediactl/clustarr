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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

// scalarPtr is a one-line test helper used across this package's _test.go
// files whenever a SelectorBlock.Default/Text needs a *cardigann.Scalar.
func scalarPtr(s cardigann.Scalar) *cardigann.Scalar { return &s }

func TestDocSelectAndTextAcrossAllThreeResponseTypes(t *testing.T) {
	html := []byte(`<table><tr><td class="title">Some.Movie.2024</td><td class="size">4.2 GB</td></tr></table>`)
	jsonBody := []byte(`{"data":[{"attributes":{"name":"Some.Movie.2024","size":4508876800}}]}`)
	xmlBody := []byte(`<item><title>Some.Movie.2024</title><size>4508876800</size></item>`)

	htmlDoc, err := cardigann.ParseDoc(cardigann.ResponseHTML, html)
	require.NoError(t, err)
	row, ok := htmlDoc.Select("td.title")
	require.True(t, ok)
	text, ok := row.Text("")
	require.True(t, ok)
	assert.Equal(t, "Some.Movie.2024", text)

	jsonDoc, err := cardigann.ParseDoc(cardigann.ResponseJSON, jsonBody)
	require.NoError(t, err)
	rows := jsonDoc.Rows("data")
	require.Len(t, rows, 1)
	nameDoc, ok := rows[0].Select("attributes")
	require.True(t, ok)
	name, ok := nameDoc.Select("name")
	require.True(t, ok)
	nameText, ok := name.Text("")
	require.True(t, ok)
	assert.Equal(t, "Some.Movie.2024", nameText)

	xmlDoc, err := cardigann.ParseDoc(cardigann.ResponseXML, xmlBody)
	require.NoError(t, err)
	titleNode, ok := xmlDoc.Select("//title")
	require.True(t, ok)
	titleText, ok := titleNode.Text("")
	require.True(t, ok)
	assert.Equal(t, "Some.Movie.2024", titleText)
}

func TestSelectorBlockExtractText(t *testing.T) {
	def := &cardigann.SelectorBlock{Text: scalarPtr("0")}
	tc := &cardigann.TemplateContext{}
	val, ok, err := def.Extract(context.Background(), cardigann.Doc{}, tc)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "0", val)
}

func TestSelectorBlockExtractCaseMapWithWildcardFallback(t *testing.T) {
	def := &cardigann.SelectorBlock{
		Selector: "freeleech",
		Case:     map[string]cardigann.Scalar{"100%": "0", "0%": "1", "*": "0"},
	}
	doc, err := cardigann.ParseDoc(cardigann.ResponseJSON, []byte(`{"freeleech":"100%"}`))
	require.NoError(t, err)
	val, ok, err := def.Extract(context.Background(), doc, &cardigann.TemplateContext{})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "0", val)
}

func TestSelectorBlockExtractOptionalMissingReturnsFalse(t *testing.T) {
	def := &cardigann.SelectorBlock{Selector: "does-not-exist", Optional: true}
	doc, err := cardigann.ParseDoc(cardigann.ResponseHTML, []byte(`<html></html>`))
	require.NoError(t, err)
	_, ok, err := def.Extract(context.Background(), doc, &cardigann.TemplateContext{})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSelectorBlockExtractDefaultWhenOptionalAndMissing(t *testing.T) {
	d := cardigann.Scalar("1.0")
	def := &cardigann.SelectorBlock{Selector: "does-not-exist", Optional: true, Default: &d}
	doc, err := cardigann.ParseDoc(cardigann.ResponseHTML, []byte(`<html></html>`))
	require.NoError(t, err)
	val, ok, err := def.Extract(context.Background(), doc, &cardigann.TemplateContext{})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "1.0", val)
}

func TestSelectorBlockExtractRemoveStripsNestedElementFirst(t *testing.T) {
	def := &cardigann.SelectorBlock{Selector: "td", Remove: "span"}
	// Wrapped in <table><tr>...</tr></table>, unlike the brief's bare
	// snippet: verified against golang.org/x/net/html (goquery's parser),
	// a standalone <td> outside a table/row context is dropped entirely
	// by HTML5 tree construction's foster-parenting rules (doc.Find("td")
	// matches zero elements), so the bare fixture could never exercise
	// this selector. A real Cardigann response body always has its table
	// markup intact; this fixture now does too.
	doc, err := cardigann.ParseDoc(cardigann.ResponseHTML, []byte(`<table><tr><td>4.2 GB<span class="extra">ignore me</span></td></tr></table>`))
	require.NoError(t, err)
	val, ok, err := def.Extract(context.Background(), doc, &cardigann.TemplateContext{})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "4.2 GB", val)
}
