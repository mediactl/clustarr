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

package catalogue_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// TestLanguageTableRoundTrips walks every entry of the language table and
// checks name -> ISO-639-1 tag -> name is the identity. "Original" is the
// one entry with no tag: it is Radarr's pseudo-language (id -2), not a
// language, so it has no ISO code to round-trip through.
func TestLanguageTableRoundTrips(t *testing.T) {
	langs := catalogue.Languages()
	require.NotEmpty(t, langs)

	for _, l := range langs {
		name, ok := catalogue.LanguageByID(l.ID)
		require.True(t, ok, "id %d must resolve", l.ID)
		require.Equal(t, l.Name, name)

		if l.Tag == "" {
			assert.Equal(t, "Original", l.Name, "only Original may lack an ISO-639-1 code")
			_, ok := catalogue.LanguageTag(l.Name)
			assert.False(t, ok, "Original has no ISO-639-1 code")
			continue
		}

		tag, ok := catalogue.LanguageTag(l.Name)
		require.True(t, ok, "%s must have a tag", l.Name)
		require.Equal(t, l.Tag, tag)

		back, ok := catalogue.LanguageName(tag)
		require.True(t, ok, "%s must resolve back to a name", tag)
		require.Equal(t, l.Name, back)
	}
}

func TestLanguageTableCoversTheCorpusIDs(t *testing.T) {
	want := map[int32]string{
		-2: "Original", 1: "English", 2: "French", 4: "German",
		8: "Japanese", 10: "Chinese", 21: "Korean",
	}
	assert.Len(t, catalogue.Languages(), len(want))
	for id, name := range want {
		got, ok := catalogue.LanguageByID(id)
		require.True(t, ok, "id %d", id)
		assert.Equal(t, name, got)
	}
}

func TestLanguageNameAcceptsBCP47AndIsCaseInsensitive(t *testing.T) {
	for _, tag := range []string{"en", "EN", "en-US", "en-GB", "En-Latn-US"} {
		got, ok := catalogue.LanguageName(tag)
		require.True(t, ok, tag)
		assert.Equal(t, "English", got)
	}
	got, ok := catalogue.LanguageName("pt-BR")
	assert.False(t, ok, "Portuguese is not in the table yet")
	assert.Empty(t, got)

	for _, tag := range []string{"", "-", "zzz", "english"} {
		_, ok := catalogue.LanguageName(tag)
		assert.False(t, ok, "%q must not resolve", tag)
	}
}

func TestLanguageTagIsCaseInsensitive(t *testing.T) {
	tag, ok := catalogue.LanguageTag("japanese")
	require.True(t, ok)
	assert.Equal(t, "ja", tag)

	_, ok = catalogue.LanguageTag("Klingon")
	assert.False(t, ok)
}
