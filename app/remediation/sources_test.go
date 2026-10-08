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

package remediation

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// S1's update arm (§3.3): a generation change, a watched annotation and a
// user's label wake the file; the seven labels the loop writes and a
// status-only change do not (the self-trigger rule). An intent annotation or
// a user label wakes it at PriorityUser.
func TestS1WakesOnTheWatchedChanges(t *testing.T) {
	base := func() *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{
			Name: "heat-abc", Namespace: "media", Generation: 3,
			Labels:      map[string]string{catalogv1alpha1.LabelKind: "movie", catalogv1alpha1.LabelVideoCodec: "h264"},
			Annotations: map[string]string{catalogv1alpha1.AnnotationSubtitleSearch: "1759600000"},
		}}
	}
	for _, tc := range []struct {
		name       string
		mutate     func(*catalogv1alpha1.MediaFile)
		wake, user bool
	}{
		{"nothing", func(*catalogv1alpha1.MediaFile) {}, false, false},
		{"generation", func(m *catalogv1alpha1.MediaFile) { m.Generation++ }, true, false},
		{"observed fingerprint", func(m *catalogv1alpha1.MediaFile) {
			m.Annotations["catalog.clustarr.io/observed-fingerprint"] = "123:456"
		}, true, false},
		{"dead-lettered", func(m *catalogv1alpha1.MediaFile) {
			m.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.probe.high.x@2026-10-06T12:00:00Z"
		}, true, false},
		{"replay", func(m *catalogv1alpha1.MediaFile) { m.Annotations[history.AnnotationReplay] = "42" }, true, false},
		{"transcode retry nonce", func(m *catalogv1alpha1.MediaFile) {
			m.Annotations[catalogv1alpha1.AnnotationTranscodeRetry] = "1759700000"
		}, true, true},
		{"transcode hardware", func(m *catalogv1alpha1.MediaFile) {
			m.Annotations[catalogv1alpha1.AnnotationTranscodeHardware] = "cpu"
		}, true, true},
		{"subtitle search nonce removed", func(m *catalogv1alpha1.MediaFile) {
			delete(m.Annotations, catalogv1alpha1.AnnotationSubtitleSearch)
		}, true, true},
		{"a user's label", func(m *catalogv1alpha1.MediaFile) { m.Labels["clustarr.io/transcode-profile"] = "hevc" }, true, true},
		{"a user's label removed", func(m *catalogv1alpha1.MediaFile) {
			m.Labels["clustarr.io/transcode-profile"] = "hevc"
		}, true, true},
		{"a loop label", func(m *catalogv1alpha1.MediaFile) { m.Labels[catalogv1alpha1.LabelVideoCodec] = "hevc" }, false, false},
		{"a loop label added", func(m *catalogv1alpha1.MediaFile) { m.Labels[catalogv1alpha1.LabelHdr] = "hdr10" }, false, false},
		{"status only", func(m *catalogv1alpha1.MediaFile) { m.Status.ProbeHash = "h2" }, false, false},
		{"an unrelated annotation", func(m *catalogv1alpha1.MediaFile) { m.Annotations["example.com/note"] = "x" }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, nw := base(), base()
			tc.mutate(nw)
			if tc.name == "a user's label removed" {
				old, nw = nw, old
			}
			wake, user := fileUpdateWakes(old, nw)
			assert.Equal(t, tc.wake, wake, "wake")
			assert.Equal(t, tc.user, user, "user priority")
		})
	}
}

// TestLoopLabelKeysAreWhatMirrorLabelsWrites: the labels S1 reads as the
// loop's own (and View.Labels overlays) are exactly what the main apply
// writes, so a label the loop adds never wakes the file and a user's label
// is never mistaken for the loop's.
func TestLoopLabelKeysAreWhatMirrorLabelsWrites(t *testing.T) {
	q := commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierRemux}
	mi := &commonv1.MediaInfo{VideoCodec: "hevc", Hdr: commonv1.HdrFormatHDR10}
	got := mediafile.MirrorLabels(commonv1.MediaKindMovie, q, mi, true)
	assert.Equal(t, slices.Sorted(slices.Values(mediafile.LoopLabelKeys)), slices.Sorted(maps.Keys(got)))
	for k := range got {
		assert.False(t, userLabelsDiffer(map[string]string{}, map[string]string{k: "x"}), "%s reads as a user's label", k)
	}
}

func TestSubtitleProfileSignature(t *testing.T) {
	mf := func(profile string, phase catalogv1alpha1.SubtitlesPhase) *catalogv1alpha1.MediaFile {
		m := &catalogv1alpha1.MediaFile{}
		if profile != "" {
			m.Status.Subtitles = &catalogv1alpha1.SubtitlesStatus{Profile: profile, Phase: phase}
		}
		return m
	}
	assert.Equal(t, subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseWanted)),
		subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseWanted)))
	assert.NotEqual(t, subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseWanted)),
		subtitleProfileSignature(mf("q", catalogv1alpha1.SubtitlesPhaseWanted)), "a new winner")
	assert.NotEqual(t, subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseWanted)),
		subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseSatisfied)), "a new phase")
	assert.NotEqual(t, subtitleProfileSignature(mf("", "")), subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseWanted)))
	other := mf("p", catalogv1alpha1.SubtitlesPhaseWanted)
	other.Status.Subtitles.Wanted = []string{"en"}
	assert.Equal(t, subtitleProfileSignature(mf("p", catalogv1alpha1.SubtitlesPhaseWanted)), subtitleProfileSignature(other),
		"what the slim reconciler does not count moves nothing")
	assert.Empty(t, subtitleProfileSignature(&catalogv1alpha1.Movie{}), "not a MediaFile")
}
