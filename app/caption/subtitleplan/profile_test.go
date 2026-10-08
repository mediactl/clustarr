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

package subtitleplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// These tests moved here from app/caption/controller/subtitleprofile with
// the selection they test (ADR-0016 F5.1): the loop's planner decides which
// profile owns a file, and the profile controller only counts and flags.

func profileAt(name string, created time.Time, spec subtitlev1alpha1.SubtitleProfileSpec) subtitlev1alpha1.SubtitleProfile {
	return subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       spec,
	}
}

func TestEligibleKindOnlyMovieAndEpisode(t *testing.T) {
	assert.True(t, EligibleKind(commonv1.MediaKindMovie))
	assert.True(t, EligibleKind(commonv1.MediaKindEpisode))
	assert.False(t, EligibleKind(commonv1.MediaKindSeries))
	assert.False(t, EligibleKind(commonv1.MediaKindAlbum))
	assert.False(t, EligibleKind(commonv1.MediaKindBook))
	assert.False(t, EligibleKind(commonv1.MediaKindAudiobook))
}

func TestProfileLessOrdersByCreationThenName(t *testing.T) {
	early := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	late := metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	a := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "b", CreationTimestamp: early}}
	b := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "a", CreationTimestamp: late}}
	assert.True(t, ProfileLess(a, b), "the earlier-created profile wins regardless of name")

	tie1 := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "b", CreationTimestamp: early}}
	tie2 := &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: "a", CreationTimestamp: early}}
	assert.True(t, ProfileLess(tie2, tie1), "a creation-timestamp tie falls back to the lexicographically smaller name")
}

// Two profiles whose selectors both match a file: the earlier-created one
// wins it, whichever way the list is ordered. (Was
// TestSelectFilesPicksTheDeterministicWinnerOnOverlap over the profile
// controller's selectFiles; the loser's Overlap condition is the
// controller's, TestOverlapIsTrueWhenAnotherProfileWinsAFileThisOneSelects.)
func TestWinningProfilePicksTheDeterministicWinnerOnOverlap(t *testing.T) {
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
	hd := map[string]string{"tier": "hd"}

	for _, all := range [][]subtitlev1alpha1.SubtitleProfile{{winner, loser}, {loser, winner}} {
		got := WinningProfile(hd, all)
		require.NotNil(t, got)
		assert.Equal(t, "winner", got.Name)
	}
	assert.True(t, SelectorMatches(&loser, hd), "the loser selects the file too: that is the overlap")
}

// The default profile wins only a file no selector matches; a selector that
// matches outranks the cluster default. (Was
// TestSelectFilesFallsBackToDefaultOnlyWhenNoSelectorMatches.)
func TestWinningProfileFallsBackToDefaultOnlyWhenNoSelectorMatches(t *testing.T) {
	def := profileAt("default", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Default:   true,
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	selective := profileAt("selective", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}},
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
	})
	all := []subtitlev1alpha1.SubtitleProfile{def, selective}

	got := WinningProfile(map[string]string{"tier": "hd"}, all)
	require.NotNil(t, got)
	assert.Equal(t, "selective", got.Name, "the default profile only wins the file the selective profile does not")

	got = WinningProfile(nil, all)
	require.NotNil(t, got)
	assert.Equal(t, "default", got.Name)

	assert.Nil(t, WinningProfile(nil, []subtitlev1alpha1.SubtitleProfile{selective}),
		"no selector match and no default: no profile owns the file")
}
