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

package subtitleprofile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

func profileAt(name string, created time.Time, spec subtitlev1alpha1.SubtitleProfileSpec) subtitlev1alpha1.SubtitleProfile {
	return subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       spec,
	}
}

func TestCanonicalKeyMatchesFormatLangKeyPrecedence(t *testing.T) {
	// Plain language: no suffix.
	assert.Equal(t, "en", string(canonicalKey(subtitlev1alpha1.LanguageItem{Language: "en"})))
	// Forced: :forced suffix regardless of HI.
	assert.Equal(t, "en:forced", string(canonicalKey(subtitlev1alpha1.LanguageItem{
		Language: "en", Forced: true, HI: subtitlev1alpha1.HIPolicyRequired,
	})))
	// HI required, not forced: :hi suffix.
	assert.Equal(t, "pt-BR:hi", string(canonicalKey(subtitlev1alpha1.LanguageItem{
		Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyRequired,
	})))
	// HI prefer/excluded never add a suffix on their own -- only "required" does.
	assert.Equal(t, "es", string(canonicalKey(subtitlev1alpha1.LanguageItem{
		Language: "es", HI: subtitlev1alpha1.HIPolicyPrefer,
	})))
	assert.Equal(t, "es", string(canonicalKey(subtitlev1alpha1.LanguageItem{
		Language: "es", HI: subtitlev1alpha1.HIPolicyExcluded,
	})))
	// Forced wins over hi when both would apply.
	assert.Equal(t, "en:forced", string(canonicalKey(subtitlev1alpha1.LanguageItem{
		Language: "en", Forced: true, HI: subtitlev1alpha1.HIPolicyRequired,
	})))
}

func TestWantedKeysPreservesSpecOrder(t *testing.T) {
	spec := subtitlev1alpha1.SubtitleProfileSpec{
		Languages: []subtitlev1alpha1.LanguageItem{
			{Key: "en", Language: "en"},
			{Key: "en:forced", Language: "en", Forced: true},
			{Key: "pt-BR:hi", Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyRequired},
		},
	}
	assert.Equal(t, []string{"en", "en:forced", "pt-BR:hi"}, wantedKeys(spec))
}

func TestWantedKeysNilForNoLanguages(t *testing.T) {
	assert.Nil(t, wantedKeys(subtitlev1alpha1.SubtitleProfileSpec{}))
}

func TestValidateProfileFlagsTheNewerOfTwoDefaults(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	spec := subtitlev1alpha1.SubtitleProfileSpec{
		Default:   true,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	}
	first := profileAt("first", early, spec)
	second := profileAt("second", late, spec)
	all := []subtitlev1alpha1.SubtitleProfile{first, second}

	invalid, _, _ := validateProfile(&first, all)
	assert.False(t, invalid, "the earlier-created default profile is not the one flagged")

	invalid, reason, msg := validateProfile(&second, all)
	require.True(t, invalid)
	assert.Equal(t, ReasonDuplicateDefault, reason)
	assert.Contains(t, msg, "first")
}

func TestValidateProfileFlagsAKeyMismatch(t *testing.T) {
	spec := subtitlev1alpha1.SubtitleProfileSpec{
		Languages: []subtitlev1alpha1.LanguageItem{
			{Key: "en", Language: "en"},
			{Key: "pt-BR", Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyRequired}, // should be "pt-BR:hi"
		},
	}
	p := profileAt("mismatched", time.Now(), spec)
	invalid, reason, msg := validateProfile(&p, []subtitlev1alpha1.SubtitleProfile{p})
	require.True(t, invalid)
	assert.Equal(t, ReasonKeyMismatch, reason)
	assert.Contains(t, msg, "pt-BR:hi")
}

func TestValidateProfileAcceptsCorrectlyDerivedKeys(t *testing.T) {
	spec := subtitlev1alpha1.SubtitleProfileSpec{
		Languages: []subtitlev1alpha1.LanguageItem{
			{Key: "en", Language: "en"},
			{Key: "en:forced", Language: "en", Forced: true},
			{Key: "pt-BR:hi", Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyRequired},
		},
	}
	p := profileAt("valid", time.Now(), spec)
	invalid, _, _ := validateProfile(&p, []subtitlev1alpha1.SubtitleProfile{p})
	assert.False(t, invalid)
}
