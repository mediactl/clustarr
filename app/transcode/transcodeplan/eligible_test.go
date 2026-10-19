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

package transcodeplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
)

// TestEligibleFollowsTheWinningProfile is §5.11's membership rule over the
// one selection rule (§2.13): the ProfileLess-least selector match over the
// view's labels, else the default; an Invalid winner plans nothing; a file
// that is held (unprobed, a probe pending, a standalone graft) is Pending,
// one that is done or unmanaged is not eligible at all.
func TestEligibleFollowsTheWinningProfile(t *testing.T) {
	hevc := newProfile("hevc", true, t0, nil)
	hevc.Spec.Priority = 7
	anime := newProfile("anime", false, t0.Add(time.Hour), map[string]string{"catalog.clustarr.io/genre": "anime"})
	all := []transcodev1alpha1.TranscodeProfile{hevc, anime}

	got := Eligible(eligibleFile(probed("ph-1")), all)
	require.True(t, got.Eligible, got.Why)
	assert.Equal(t, "hevc", got.Profile.Name)
	assert.Equal(t, jobspec.ProfileHash(hevc.Spec), got.Hash)
	assert.Equal(t, "hevc@"+got.Hash, got.Tag)
	assert.Equal(t, tagOf(hevc), got.Tag)
	assert.Empty(t, got.Pending)
	assert.Equal(t, int32(7), got.Priority, "the winning profile's spec.priority")

	pinned := eligibleFile(probed("ph-1"))
	pinned.Labels = map[string]string{"catalog.clustarr.io/genre": "anime"} // v.Labels(), never mf.Labels
	assert.Equal(t, "anime", Eligible(pinned, all).Profile.Name, "a selector match outranks the default")
	cached := eligibleFile(probed("ph-1"))
	cached.MF.Labels = map[string]string{"catalog.clustarr.io/genre": "anime"}
	cached.Labels = map[string]string{"catalog.clustarr.io/genre": "drama"}
	assert.Equal(t, "hevc", Eligible(cached, all).Profile.Name, "the view's labels win over the cached object's")

	urgent := eligibleFile(probed("ph-1"))
	urgent.Priority = new(int32(100))
	assert.Equal(t, int32(100), Eligible(urgent, all).Priority, "the file's priority intent outranks the profile's")

	// A block of the winning profile that records no transcode -- in the
	// window, or a verdict -- carries the tag it was planned under: the
	// file stays eligible (the window and the verdict rules decide).
	for _, ph := range []catalogv1alpha1.TranscodePhase{
		catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned, catalogv1alpha1.TranscodePhaseQueued,
		catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping,
		catalogv1alpha1.TranscodePhaseSkipped, catalogv1alpha1.TranscodePhaseFailed,
	} {
		f := eligibleFile(probed("ph-1"))
		f.Status.Transcode = &catalogv1alpha1.TranscodeState{Phase: ph, Profile: "hevc", ProfileTag: tagOf(hevc)}
		got := Eligible(f, all)
		assert.True(t, got.Eligible, "a %s block records no transcode: %s", ph, got.Why)
	}

	// Another tool's HEVC encode (Tdarr's QSV MKVs) is final but still
	// eligible: the standard remuxes it into the MP4 layout, and never
	// encodes it a second time (owner, 2026-10-15).
	elsewhere := eligibleFile(probed("ph-1"))
	elsewhere.Status.MediaInfo.VideoEncoder = "Lavc61.19.100 hevc_qsv"
	got = Eligible(elsewhere, all)
	assert.True(t, got.Eligible, "another tool's HEVC is remuxed: %s", got.Why)

	// So is transcode's own from before the MP4 layout, tagged in Matroska.
	own := eligibleFile(probed("ph-1"))
	own.Status.MediaInfo.TranscodeProfile, own.Status.MediaInfo.Container = "hevc@0ld", "mkv"
	got = Eligible(own, all)
	assert.True(t, got.Eligible, "a tagged MKV is remuxed: %s", got.Why)

	for _, tc := range []struct {
		name    string
		mutate  func(*File)
		profs   []transcodev1alpha1.TranscodeProfile
		pending string
	}{
		{name: "an audio file", mutate: func(f *File) { f.MF.Spec.MediaRef.Kind = commonv1.MediaKindAlbum }},
		{name: "its item is gone", mutate: func(f *File) { f.ItemExists = false }},
		{name: "never probed", mutate: func(f *File) { f.Status = &catalogv1alpha1.MediaFileStatus{}; f.ProbeCurrent = false }, pending: ReasonUnprobed},
		{name: "a probe of new bytes is pending", mutate: func(f *File) { f.ProbeCurrent = false }, pending: ReasonProbePending},
		{name: "a standalone graft rewrites it", mutate: func(f *File) { f.StandaloneGraft = true }, pending: ReasonGrafting},
		{name: "an upgrade of its item is downloading", mutate: func(f *File) { f.UpgradeDownloading = true }, pending: ReasonUpgradeDownloading},
		{name: "transcode swapped it in", mutate: func(f *File) { f.MF.Spec.Original = new(false) }},
		{name: "it carries CLUSTARR_PROFILE in an MP4", mutate: func(f *File) {
			// HEVC: a remux that kept H.264 is the one exception
			// (TestARemuxThatKeptH264IsEligibleOnceH264IsNoLongerKept).
			f.Status.MediaInfo.TranscodeProfile, f.Status.MediaInfo.Container = "hevc@0ld", "mp4"
			f.Status.MediaInfo.VideoCodec = "hevc"
		}},
		{name: "a kept copy was recorded under any hash", mutate: func(f *File) {
			f.Status.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, ProfileTag: "hevc@0ld"}
		}},
		{name: "a legacy block records a transcode", mutate: func(f *File) {
			f.Status.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: "hevc@0ld", Compliant: true}
		}},
		{name: "no profile at all", profs: []transcodev1alpha1.TranscodeProfile{}},
		{name: "its winner is an Invalid duplicate default", mutate: func(f *File) { f.Labels = map[string]string{"tier": "uhd"} },
			profs: []transcodev1alpha1.TranscodeProfile{
				newProfile("aa-default", true, t0, nil),
				newProfile("zz-default", true, t0.Add(time.Hour), map[string]string{"tier": "uhd"}), // wins by selector, Invalid as the newer default
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := eligibleFile(probed("ph-1"))
			if tc.mutate != nil {
				tc.mutate(&f)
			}
			profs := all
			if tc.profs != nil {
				profs = tc.profs
			}
			got := Eligible(f, profs)
			assert.False(t, got.Eligible)
			assert.NotEmpty(t, got.Why)
			assert.Equal(t, tc.pending, got.Pending)
		})
	}
}
