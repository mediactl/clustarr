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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestNonVideoProfilesScoreNoCustomFormats is the carried defect
// "quality.FromCRD scores every catalogue custom format -- all TRaSH video
// data -- against music, book, audiobook and comic profiles alike". A music
// release has no language token, so against an English-original item it
// matched language-not-original (a NEGATED "contains Original") and every
// built-in music profile scored it -10000.
func TestNonVideoProfilesScoreNoCustomFormats(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	profiles, errs := quality.BuiltinProfiles(cat)
	require.Empty(t, errs)

	for _, name := range []string{"music-lossless", "music-standard", "ebook", "audiobook", "comic"} {
		assert.Empty(t, profiles[name].Scores, "%s: a non-video profile resolves no custom formats", name)
	}
	assert.NotEmpty(t, profiles["hd-bluray-web"].Scores, "a video profile keeps its formats")

	rel, err := release.ParseKind("Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", common.MediaKindAlbum)
	require.NoError(t, err)
	ic := catalogue.ItemContext{OriginalLanguageName: "English", ReleaseType: common.ReleaseTypeAlbum}
	score, matched := profiles["music-lossless"].Score(t.Context(), cat, rel, ic)
	assert.Zero(t, score)
	assert.Empty(t, matched, "no video format may be reported as matched on a music release")
}

func TestFromCRDReportsCustomFormatSettingsOnANonVideoProfile(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	p := &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind:           catalogv1alpha1.ProfileMediaKindMusic,
		Tiers:               []catalogv1alpha1.Tier{{Name: "FLAC", Qualities: []string{"FLAC"}}},
		Cutoff:              "FLAC",
		FormatScores:        []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 10}},
		EnabledFormatGroups: []string{catalogv1alpha1.FormatGroupStreamingBoost},
		MinFormatScore:      1,
	}}
	prof, errs := quality.FromCRD(p, cat)
	require.Len(t, errs, 3, "formatScores, enabledFormatGroups and an unreachable minFormatScore are each reported")
	assert.Empty(t, prof.Scores)

	p.Spec.FormatScores, p.Spec.EnabledFormatGroups, p.Spec.MinFormatScore = nil, nil, 0
	_, errs = quality.FromCRD(p, cat)
	assert.Empty(t, errs, "a plain music profile is valid")
}

// TestParsedNonVideoReleasesLandOnTheirLadders runs pkg/release's
// upstream-named qualities through the built-in profiles: each is allowed,
// and the cutoff verdicts are the upstream apps' own (Readarr's eBook cutoff
// MOBI, Spoken cutoff MP3; Lidarr's Lossless cutoff FLAC, which ALAC, APE
// and WavPack tie with).
func TestParsedNonVideoReleasesLandOnTheirLadders(t *testing.T) {
	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)

	tests := []struct {
		profile, title string
		kind           common.MediaKind
		cutoffMet      bool
	}{
		{"music-lossless", "Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", common.MediaKindAlbum, true},
		{"music-lossless", "Pink Floyd - The Dark Side of the Moon (1973) [ALAC]", common.MediaKindAlbum, true},
		{"music-lossless", "Pink Floyd - The Dark Side of the Moon (1973) [APE]", common.MediaKindAlbum, true},
		{"music-lossless", "Pink Floyd - The Dark Side of the Moon (1973) [FLAC 24bit]", common.MediaKindAlbum, true},
		{"music-lossless", "Pink Floyd - The Dark Side of the Moon (1973) [MP3 192]", common.MediaKindAlbum, false},
		{"music-lossless", "Pink Floyd - The Dark Side of the Moon (1973) [MP3 128]", common.MediaKindAlbum, false},
		{"music-standard", "Pink Floyd - The Dark Side of the Moon (1973) [MP3 192]", common.MediaKindAlbum, true},
		{"music-standard", "Pink Floyd - The Dark Side of the Moon (1973) [MP3 128]", common.MediaKindAlbum, false},
		{"ebook", "Andy Weir - Project Hail Mary (2021) [EPUB]", common.MediaKindBook, true},
		{"ebook", "Andy Weir - Project Hail Mary (2021) [PDF]", common.MediaKindBook, false},
		{"audiobook", "Andy Weir - Project Hail Mary (Unabridged) [M4B 64kbps]", common.MediaKindAudiobook, true},
		{"audiobook", "Andy Weir - Project Hail Mary (Unabridged) [AAX]", common.MediaKindAudiobook, false},
		{"comic", "Saga 001 (2012) (Digital) (Zone-Empire).cbz", common.MediaKindIssue, true},
		{"comic", "Saga 001 (2012) (Digital) (Zone-Empire).cbr", common.MediaKindIssue, false},
	}
	for _, tt := range tests {
		rel, err := release.ParseKind(tt.title, tt.kind)
		require.NoError(t, err, tt.title)
		p := profiles[tt.profile]
		assert.True(t, p.Allowed(rel.Quality), "%s: %s (%s) must be on the ladder", tt.profile, tt.title, rel.Quality.Name)
		assert.Equal(t, tt.cutoffMet, p.CutoffMet(rel.Quality), "%s: CutoffMet(%s)", tt.profile, rel.Quality.Name)
	}

	// An audio release is an audio quality, which the ebook ladder refuses
	// rather than misplaces.
	rel, err := release.ParseKind("Andy Weir - Project Hail Mary (2021) [MP3]", common.MediaKindBook)
	require.NoError(t, err)
	assert.False(t, profiles["ebook"].Allowed(rel.Quality))
}

// A non-video quality's identity is its name, so a revision upgrade needs the
// same name: a PROPER ALAC does not replace a FLAC it merely ties with.
func TestNonVideoRevisionUpgradeNeedsTheSameQuality(t *testing.T) {
	flac, ok := quality.Lookup("music", "FLAC")
	require.True(t, ok)
	p := quality.Profile{MediaKind: "music", Tiers: [][]quality.Definition{{flac}}, UpgradeAllowed: true, CutoffFormatScore: 10000}

	current := quality.Candidate{Quality: common.Quality{Name: "FLAC"}, Revision: common.Revision{Version: 1}}
	proper := common.Revision{Version: 2}
	assert.Equal(t, quality.Upgrade, p.UpgradeDecision(current, quality.Candidate{Quality: common.Quality{Name: "FLAC"}, Revision: proper}))
	assert.NotEqual(t, quality.Upgrade, p.UpgradeDecision(current, quality.Candidate{Quality: common.Quality{Name: "ALAC"}, Revision: proper}))
}

func TestLookupResolvesLidarrNamesOnTheMusicLadder(t *testing.T) {
	for name, tier := range map[string]string{
		"MP3-64": "Trash", "MP3-128": "Poor", "OGG Vorbis Q5": "Poor",
		"ALAC": "FLAC", "APE": "FLAC", "WavPack": "FLAC",
		"FLAC 24bit": "24bit Lossless", "ALAC 24bit": "24bit Lossless",
	} {
		def, ok := quality.Lookup("music", name)
		require.True(t, ok, name)
		assert.Equal(t, tier, def.Name, name)
	}
	// The lossy middle is deliberately unmapped: see nonVideoDefinitions.
	for _, name := range []string{"MP3-256", "MP3-320", "MP3-VBR-V0", "AAC-256"} {
		_, ok := quality.Lookup("music", name)
		assert.False(t, ok, name)
	}
}
