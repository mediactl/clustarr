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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

func movieFile(name string, labels map[string]string) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media", Labels: labels},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}},
	}
}

func profileAt(name string, created time.Time, spec subtitlev1alpha1.SubtitleProfileSpec) subtitlev1alpha1.SubtitleProfile {
	return subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       spec,
	}
}

func TestEligibleKindOnlyMovieAndEpisode(t *testing.T) {
	assert.True(t, eligibleKind(commonv1.MediaKindMovie))
	assert.True(t, eligibleKind(commonv1.MediaKindEpisode))
	assert.False(t, eligibleKind(commonv1.MediaKindSeries))
	assert.False(t, eligibleKind(commonv1.MediaKindAlbum))
	assert.False(t, eligibleKind(commonv1.MediaKindBook))
	assert.False(t, eligibleKind(commonv1.MediaKindAudiobook))
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

func TestProfileLessOrdersByCreationThenName(t *testing.T) {
	early := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	late := metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	a := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "b", CreationTimestamp: early}}
	b := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "a", CreationTimestamp: late}}
	assert.True(t, profileLess(a, b), "the earlier-created profile wins regardless of name")

	tie1 := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "b", CreationTimestamp: early}}
	tie2 := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "a", CreationTimestamp: early}}
	assert.True(t, profileLess(tie2, tie1), "a creation-timestamp tie falls back to the lexicographically smaller name")
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

func TestSelectFilesPicksTheDeterministicWinnerOnOverlap(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}}

	winner := profileAt("winner", early, subtitlev1alpha1.SubtitleProfileSpec{
		Selector:  sel,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	loser := profileAt("loser", late, subtitlev1alpha1.SubtitleProfileSpec{
		Selector:  sel,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	all := []subtitlev1alpha1.SubtitleProfile{winner, loser}

	mf := movieFile("arrival-2016", map[string]string{"tier": "hd"})
	files := []catalogv1alpha1.MediaFile{*mf}

	matchingWinner, overlapWinner := selectFiles(&winner, all, nil, files)
	require.Len(t, matchingWinner, 1)
	assert.Equal(t, mf.Name, matchingWinner[0].Name)
	assert.False(t, overlapWinner)

	matchingLoser, overlapLoser := selectFiles(&loser, all, nil, files)
	assert.Empty(t, matchingLoser)
	assert.True(t, overlapLoser, "the losing profile must surface the overlap")
}

func TestSelectFilesFallsBackToDefaultOnlyWhenNoSelectorMatches(t *testing.T) {
	def := profileAt("default", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Default:   true,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	selective := profileAt("selective", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}},
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	all := []subtitlev1alpha1.SubtitleProfile{def, selective}

	hdFile := movieFile("hd-movie", map[string]string{"tier": "hd"})
	plainFile := movieFile("plain-movie", nil)
	files := []catalogv1alpha1.MediaFile{*hdFile, *plainFile}

	matchingDefault, _ := selectFiles(&def, all, &def, files)
	require.Len(t, matchingDefault, 1, "the default profile only wins the file the selective profile does not")
	assert.Equal(t, plainFile.Name, matchingDefault[0].Name)

	matchingSelective, _ := selectFiles(&selective, all, &def, files)
	require.Len(t, matchingSelective, 1)
	assert.Equal(t, hdFile.Name, matchingSelective[0].Name)
}

func TestSelectFilesExcludesIneligibleKinds(t *testing.T) {
	def := profileAt("default", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Default:   true,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	book := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "some-book", Namespace: "media"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "some-book"}},
	}
	matching, overlapped := selectFiles(&def, []subtitlev1alpha1.SubtitleProfile{def}, &def, []catalogv1alpha1.MediaFile{*book})
	assert.Empty(t, matching)
	assert.False(t, overlapped)
}
