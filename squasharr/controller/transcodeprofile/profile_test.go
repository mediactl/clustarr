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

package transcodeprofile

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestToProfileSpecConvertsEveryField(t *testing.T) {
	maxRate, bufSize := int32(20000), int32(40000)
	spec := transcodev1alpha1.TranscodeProfileSpec{
		Container: transcodev1alpha1.ContainerMKV,
		Hardware:  transcodev1alpha1.HardwareNVIDIA,
		Video: transcodev1alpha1.VideoSpec{
			Codec:       "hevc",
			PixelFormat: "yuv420p10le",
			Profile:     "main10",
			CRF:         transcodev1alpha1.CRFTable{SD: 21, HD: 22, UHD: 23, HDROffset: -1},
			Preset:      "slow",
			MaxRateKbps: &maxRate,
			BufSizeKbps: &bufSize,
			NVENC:       transcodev1alpha1.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24},
		},
		Audio: transcodev1alpha1.AudioSpec{
			Codec:                 "aac",
			BitratePerChannelKbps: 64,
			KeepOriginal:          transcodev1alpha1.KeepOriginalAtmos,
			DropCommentary:        true,
		},
		HDR: transcodev1alpha1.HDRSpec{
			HDR10Plus:   transcodev1alpha1.HDR10PlusDrop,
			DolbyVision: transcodev1alpha1.DolbyVisionPassthrough,
		},
		Policy: transcodev1alpha1.PolicySpec{
			SkipIfCompliant: true,
			MinDuration:     metav1.Duration{Duration: 90 * time.Second},
		},
	}

	got := toProfileSpec(spec)
	assert.EqualValues(t, "mkv", got.Container)
	assert.EqualValues(t, "nvidia", got.Hardware)
	assert.Equal(t, "hevc", got.Video.Codec)
	assert.EqualValues(t, -1, got.Video.CRF.HDROffset)
	assert.Equal(t, &maxRate, got.Video.MaxRateKbps)
	assert.Equal(t, &bufSize, got.Video.BufSizeKbps)
	assert.EqualValues(t, "p6", got.Video.NVENC.Preset)
	assert.EqualValues(t, "atmos", got.Audio.KeepOriginal)
	assert.True(t, got.Audio.DropCommentary)
	assert.EqualValues(t, "passthrough", got.HDR.DolbyVision)
	// The one non-trivial conversion: metav1.Duration wraps time.Duration
	// under a different field name (.Duration); everything else in this
	// struct is a same-named, same-typed (mod package qualifier) copy.
	assert.Equal(t, 90*time.Second, got.Policy.MinDuration)
}

func TestTranscodeJobNameIsDeterministicOnFileAndHash(t *testing.T) {
	a := transcodeJobName("arrival-2016", "deadbeefcafe0102")
	b := transcodeJobName("arrival-2016", "deadbeefcafe0102")
	require.Equal(t, a, b, "the same (file, hash) pair must render the same name")
	assert.Equal(t, "arrival-2016-deadbeef", a, "only the first 8 hex characters of the hash are kept")

	// A different hash -- the only thing that changes when a profile is
	// edited -- must render a different name, which is what makes a profile
	// edit create a NEW TranscodeJob instead of mutating the CEL-immutable
	// spec of the old one.
	c := transcodeJobName("arrival-2016", "0102cafedeadbeef")
	assert.NotEqual(t, a, c)
}

func TestTranscodeJobNameTruncatesOnlyWhenOverLimit(t *testing.T) {
	short := transcodeJobName("short-name", "01234567890123456789")
	assert.LessOrEqual(t, len(short), k8s.MaxNameLength)
	assert.True(t, strings.HasPrefix(short, "short-name-"), "a name well under the limit is never trimmed")

	long := strings.Repeat("x", 300)
	got := transcodeJobName(long, "01234567")
	assert.LessOrEqual(t, len(got), k8s.MaxNameLength)
	assert.True(t, strings.HasSuffix(got, "-01234567"), "the hash suffix is never the part that is trimmed")
}

func TestProfileTagMatchesTheMediafileControllerConvention(t *testing.T) {
	// This exact "<name>@<hash>" shape is also rendered by
	// catalogarr/controller/mediafile.transcodeProfileTag and by
	// pkg/transcode.Plan's own PlanResult.Tags["CLUSTARR_PROFILE"]. All three
	// must agree byte for byte.
	assert.Equal(t, "hevc-1080p@deadbeef", profileTag("hevc-1080p", "deadbeef"))
}

func profileAt(name string, created time.Time, spec transcodev1alpha1.TranscodeProfileSpec) transcodev1alpha1.TranscodeProfile {
	return transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       spec,
	}
}

func TestProfileLessOrdersByCreationThenName(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	older := profileAt("z-profile", t0, transcodev1alpha1.TranscodeProfileSpec{})
	newer := profileAt("a-profile", t0.Add(time.Hour), transcodev1alpha1.TranscodeProfileSpec{})
	assert.True(t, profileLess(&older, &newer), "an earlier creation timestamp wins regardless of name")
	assert.False(t, profileLess(&newer, &older))

	tie1 := profileAt("a-profile", t0, transcodev1alpha1.TranscodeProfileSpec{})
	tie2 := profileAt("b-profile", t0, transcodev1alpha1.TranscodeProfileSpec{})
	assert.True(t, profileLess(&tie1, &tie2), "a tied timestamp falls back to the lexicographically smaller name")
}

func TestValidateProfileFlagsTheNewerOfTwoDefaults(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := profileAt("first", t0, transcodev1alpha1.TranscodeProfileSpec{Default: true})
	second := profileAt("second", t0.Add(time.Minute), transcodev1alpha1.TranscodeProfileSpec{Default: true})
	all := []transcodev1alpha1.TranscodeProfile{first, second}

	invalid, _, _ := validateProfile(&first, all)
	assert.False(t, invalid, "the earlier-created default profile must not be invalidated")

	invalid, reason, msg := validateProfile(&second, all)
	assert.True(t, invalid, "the later-created default profile must be invalidated")
	assert.Equal(t, ReasonDuplicateDefault, reason)
	assert.Contains(t, msg, "first")
}

// TestValidateProfileDoesNotFlagDolbyVisionPassthroughWithoutVBV is the
// negative proof for profile.go's validateProfile doc comment: HDRSpec's own
// CRD default is DolbyVision=passthrough with no default VBV values, so if
// this shape were still treated as profile-wide Invalid, a profile created
// with a bare spec (every real profile that does not explicitly override
// HDR) would be Invalid from the moment it is created and would never
// create a TranscodeJob for any file. An envtest caught this the first time
// (TestReconcileCreatesJobsIdempotently failed with a Invalid/
// DolbyVisionVBVRequired condition on a plain Default:true profile) -- this
// unit test pins the fix so it cannot silently come back.
func TestValidateProfileDoesNotFlagDolbyVisionPassthroughWithoutVBV(t *testing.T) {
	tp := transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "dv-default-shape"},
		Spec: transcodev1alpha1.TranscodeProfileSpec{
			HDR: transcodev1alpha1.HDRSpec{DolbyVision: transcodev1alpha1.DolbyVisionPassthrough},
		},
	}
	invalid, _, _ := validateProfile(&tp, []transcodev1alpha1.TranscodeProfile{tp})
	assert.False(t, invalid, "hdr.dolbyVision=passthrough without VBV is a per-file Plan() outcome (R1's Skipped phase), not a profile-wide defect")
}

func movieFile(name string, labels map[string]string) catalogv1alpha1.MediaFile {
	return catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}},
	}
}

func TestSelectFilesPicksTheDeterministicWinnerOnOverlap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hd := profileAt("hd", t0, transcodev1alpha1.TranscodeProfileSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}},
	})
	uhd := profileAt("uhd", t0.Add(time.Minute), transcodev1alpha1.TranscodeProfileSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}}, // deliberately overlapping
	})
	profiles := []transcodev1alpha1.TranscodeProfile{hd, uhd}
	files := []catalogv1alpha1.MediaFile{movieFile("arrival-2016", map[string]string{"tier": "hd"})}

	winnerMatching, winnerOverlap := selectFiles(&hd, profiles, nil, files)
	require.Len(t, winnerMatching, 1, "the earlier-created profile must win the overlapping file")
	assert.Equal(t, "arrival-2016", winnerMatching[0].Name)
	assert.False(t, winnerOverlap, "the winner itself never reports overlap")

	loserMatching, loserOverlap := selectFiles(&uhd, profiles, nil, files)
	assert.Empty(t, loserMatching, "the losing profile must create no job for a file it does not win")
	assert.True(t, loserOverlap, "the losing profile must surface that its own selector matched a file it lost")
}

func TestSelectFilesFallsBackToDefaultOnlyWhenNoSelectorMatches(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	def := profileAt("default", t0, transcodev1alpha1.TranscodeProfileSpec{Default: true})
	selective := profileAt("hd-only", t0, transcodev1alpha1.TranscodeProfileSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}},
	})
	profiles := []transcodev1alpha1.TranscodeProfile{def, selective}
	def0 := defaultWinner(profiles)

	files := []catalogv1alpha1.MediaFile{
		movieFile("hd-movie", map[string]string{"tier": "hd"}),
		movieFile("unlabeled-movie", nil),
	}

	defMatching, _ := selectFiles(&def, profiles, def0, files)
	require.Len(t, defMatching, 1, "the default must only pick up the file no selector claims")
	assert.Equal(t, "unlabeled-movie", defMatching[0].Name)

	selMatching, _ := selectFiles(&selective, profiles, def0, files)
	require.Len(t, selMatching, 1)
	assert.Equal(t, "hd-movie", selMatching[0].Name)
}

func TestSelectFilesExcludesIneligibleKinds(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	def := profileAt("default", t0, transcodev1alpha1.TranscodeProfileSpec{Default: true})
	book := catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "a-book"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "a-book"}},
	}
	matching, overlapped := selectFiles(&def, []transcodev1alpha1.TranscodeProfile{def}, &def, []catalogv1alpha1.MediaFile{book})
	assert.Empty(t, matching, "a book MediaFile must never be selected, even by the default profile")
	assert.False(t, overlapped)
}

func TestAlreadyTranscodedAndProbed(t *testing.T) {
	mf := movieFile("arrival-2016", nil)
	assert.False(t, probed(&mf), "an unprobed file is not ready for a TranscodeJob")
	assert.False(t, alreadyTranscoded(&mf, "hevc@deadbeef"))

	mf.Status.ProbeHash = "abc123"
	mf.Status.MediaInfo = &commonv1.MediaInfo{}
	assert.True(t, probed(&mf))

	mf.Status.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: "hevc@deadbeef"}
	assert.True(t, alreadyTranscoded(&mf, "hevc@deadbeef"))
	assert.False(t, alreadyTranscoded(&mf, "hevc@newhash"), "a stale (pre-edit) tag must not count as already transcoded")
}
