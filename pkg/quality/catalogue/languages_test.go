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

// tagless is every table entry that legitimately carries no ISO-639-1
// code: the three pseudo-languages, and the three rows whose language has
// no two-letter code of its own (Flemish is nl-BE, and the two regional
// variants are pt-BR and es-419 -- BCP-47 region subtags, not languages).
var tagless = map[string]bool{
	"Original": true, "Any": true, "Unknown": true,
	"Flemish": true, "Portuguese (Brazil)": true, "Spanish (Latino)": true,
}

// TestLanguageTableRoundTrips walks every entry of the language table and
// checks name -> ISO-639-1 tag -> name is the identity.
func TestLanguageTableRoundTrips(t *testing.T) {
	langs := catalogue.Languages()
	require.NotEmpty(t, langs)

	seen := map[string]string{}
	for _, l := range langs {
		name, ok := catalogue.LanguageByID(l.ID)
		require.True(t, ok, "id %d must resolve", l.ID)
		require.Equal(t, l.Name, name)

		if l.Tag == "" {
			assert.Truef(t, tagless[l.Name], "%s must carry an ISO-639-1 code", l.Name)
			_, ok := catalogue.LanguageTag(l.Name)
			assert.Falsef(t, ok, "%s has no ISO-639-1 code", l.Name)
			continue
		}
		assert.Falsef(t, tagless[l.Name], "%s is listed as tagless but has tag %q", l.Name, l.Tag)

		prev, dup := seen[l.Tag]
		assert.Falsef(t, dup, "tag %q is claimed by both %s and %s -- a round trip cannot be unambiguous", l.Tag, prev, l.Name)
		seen[l.Tag] = l.Name

		tag, ok := catalogue.LanguageTag(l.Name)
		require.True(t, ok, "%s must have a tag", l.Name)
		require.Equal(t, l.Tag, tag)

		back, ok := catalogue.LanguageName(tag)
		require.True(t, ok, "%s must resolve back to a name", tag)
		require.Equal(t, l.Name, back)
	}
}

// TestLanguageTableCarriesEveryRadarrLanguage pins the full Language.cs set
// (ids -2..57 at the tag languages.go cites), not just the ids the vendored
// custom-format corpus happens to use: QualityProfileSpec.Language is
// validated only as MaxLength=64, so a narrower table would turn a
// previously accepted "es" or "hi" into an Invalid profile.
func TestLanguageTableCarriesEveryRadarrLanguage(t *testing.T) {
	langs := catalogue.Languages()
	assert.Len(t, langs, 60, "Language.cs at v6.4.4.10685 declares 60 entries, ids -2..57")
	for i, l := range langs {
		assert.Equal(t, int32(i-2), l.ID, "the table is in id order with no gaps")
	}

	// The seven ids the vendored corpus's LanguageSpecifications use.
	for id, name := range map[int32]string{
		-2: "Original", 1: "English", 2: "French", 4: "German",
		8: "Japanese", 10: "Chinese", 21: "Korean",
	} {
		got, ok := catalogue.LanguageByID(id)
		require.True(t, ok, "id %d", id)
		assert.Equal(t, name, got)
	}

	// A spread of the newly carried ones, by tag.
	for tag, name := range map[string]string{
		"es": "Spanish", "it": "Italian", "pt": "Portuguese", "ru": "Russian",
		"hi": "Hindi", "ka": "Georgian", "af": "Afrikaans", "no": "Norwegian",
	} {
		got, ok := catalogue.LanguageName(tag)
		require.Truef(t, ok, "%s must resolve", tag)
		assert.Equal(t, name, got)
	}
}

// TestRegionalVariantsResolveToTheirBaseLanguage: "pt-BR" and "es-419" are
// BCP-47 region subtags, so they resolve through the primary subtag to
// Portuguese and Spanish -- Radarr's own "Portuguese (Brazil)" and
// "Spanish (Latino)" rows are separate ids with no ISO-639-1 code.
func TestRegionalVariantsResolveToTheirBaseLanguage(t *testing.T) {
	got, ok := catalogue.LanguageName("pt-BR")
	require.True(t, ok)
	assert.Equal(t, "Portuguese", got)

	got, ok = catalogue.LanguageName("es-419")
	require.True(t, ok)
	assert.Equal(t, "Spanish", got)

	_, ok = catalogue.LanguageTag("Portuguese (Brazil)")
	assert.False(t, ok)
}

func TestLanguageNameAcceptsBCP47AndIsCaseInsensitive(t *testing.T) {
	for _, tag := range []string{"en", "EN", "en-US", "en-GB", "En-Latn-US"} {
		got, ok := catalogue.LanguageName(tag)
		require.True(t, ok, tag)
		assert.Equal(t, "English", got)
	}
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
