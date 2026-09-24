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

package status_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/status"
)

// What squasharr owns on TranscodeJob.status -- all of it, since the pools
// report on the results stream instead of writing the object (spec §18.2) --
// restated as data so that a field added to TranscodeJobStatus later cannot
// quietly go unsent by [status.ControllerFields].
var jobControllerOwned = []string{
	"ObservedGeneration", "Phase", "Plan", "JobRef", "Attempts",
	"StartedAt", "FinishedAt", "Message", "Conditions",
	"WorkerPod", "Hardware", "FallbackReason", "NextAttemptAt",
	"Progress", "Result", "StderrTail",
}

// TranscodeProfile has exactly one writer, so there is no split to restate --
// but the field list is still worth asserting complete, so a field added
// later cannot silently go unsent by [status.ProfileFields].
var profileOwned = []string{
	"ObservedGeneration", "Hash", "MatchingFiles", "PendingJobs", "RunningJobs", "Conditions",
}

// fullJobStatus sets every field of TranscodeJobStatus to a distinct non-zero
// value, so that "did the declaration send this?" is answerable by looking at
// whether the apply configuration's pointer is nil. A blank status would make
// every conditional field look unsent and the test would pass whatever the
// declarations did.
func fullJobStatus() transcodev1alpha1.TranscodeJobStatus {
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	jobRef := "arrival-2016-a1b2c3d4"
	vmaf := int32(9542)
	return transcodev1alpha1.TranscodeJobStatus{
		ObservedGeneration: 3,
		Phase:              transcodev1alpha1.TranscodeJobPhaseRunning,
		Plan: &transcodev1alpha1.Plan{
			Encoder: "libx265",
			Mode:    transcodev1alpha1.PlanModeTranscode,
			HDRMode: "hdr10",
			VideoArgs: []string{
				"-c:v", "libx265", "-crf", "22",
			},
			AudioTracks: []transcodev1alpha1.AudioPlan{
				{SourceIndex: 1, Action: transcodev1alpha1.AudioActionEncode, Codec: "aac", BitrateKbps: 128, Default: true},
			},
			SubtitleTracks: []int32{2, 3},
			ArgsHash:       "deadbeef",
		},
		JobRef:         &jobRef,
		Attempts:       1,
		StartedAt:      &at,
		FinishedAt:     &at,
		Message:        "encoding",
		WorkerPod:      "arrival-2016-worker-0",
		Hardware:       transcodev1alpha1.HardwareNVIDIA,
		FallbackReason: "gpuBusy: no free nvidia slot",
		NextAttemptAt:  &at,
		Progress: &transcodev1alpha1.Progress{
			Percent:       42,
			Frame:         1200,
			FPSMilli:      23976,
			SpeedMilli:    1500,
			OutTimeMillis: 60000,
			BitrateKbps:   4500,
			UpdatedAt:     at,
		},
		Result: &transcodev1alpha1.Result{
			OutputPath:            "/data/movies/Arrival (2016)/Arrival.2016.1080p.mkv",
			OutputSizeBytes:       4 << 30,
			OutputToSourcePercent: 45,
			VMAFCentis:            &vmaf,
			MediaInfo:             &commonv1alpha1.MediaInfo{Container: "mkv", VideoCodec: "hevc"},
		},
		StderrTail: "frame=1200 fps=24",
		Conditions: []metav1.Condition{{Type: transcodev1alpha1.TranscodeJobConditionPlanned}},
	}
}

// fullProfileStatus is fullJobStatus's counterpart for TranscodeProfileStatus.
func fullProfileStatus() transcodev1alpha1.TranscodeProfileStatus {
	return transcodev1alpha1.TranscodeProfileStatus{
		ObservedGeneration: 2,
		Hash:               "deadbeefcafe",
		MatchingFiles:      12,
		PendingJobs:        3,
		RunningJobs:        1,
		Conditions:         []metav1.Condition{{Type: transcodev1alpha1.TranscodeProfileConditionReady}},
	}
}

// setFields returns the names of every non-nil field of an apply
// configuration.
func setFields(ac any) []string {
	v := reflect.ValueOf(ac).Elem()
	typ := v.Type()
	var out []string
	for i := range typ.NumField() {
		if !v.Field(i).IsZero() {
			out = append(out, typ.Field(i).Name)
		}
	}
	sort.Strings(out)
	return out
}

// Every field of TranscodeJobStatus must be in jobControllerOwned. Without
// this, a field added to the CRD later would be owned by nobody: no writer
// sends it, so it can never be released, and the defect is invisible until
// someone notices the field is always empty.
func TestEveryTranscodeJobStatusFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(transcodev1alpha1.TranscodeJobStatus{})

	claimed := map[string]int{}
	for _, name := range jobControllerOwned {
		claimed[name]++
	}

	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name],
			"TranscodeJobStatus.%s is claimed %d times by squasharr; it must be exactly once", name, claimed[name])
	}
	for name := range claimed {
		_, ok := typ.FieldByName(name)
		assert.Truef(t, ok, "%s is claimed by squasharr but is not a field of TranscodeJobStatus", name)
	}
	require.Len(t, jobControllerOwned, 16)
	assert.Equal(t, typ.NumField(), len(jobControllerOwned))
}

// Every field of TranscodeProfileStatus must be in profileOwned. There is
// only one writer, but the same "field added later belongs to nobody" hazard
// applies to [status.ProfileFields] forgetting a field, so this asserts the
// list stays complete.
func TestEveryTranscodeProfileStatusFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(transcodev1alpha1.TranscodeProfileStatus{})

	claimed := map[string]int{}
	for _, name := range profileOwned {
		claimed[name]++
	}

	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name], "TranscodeProfileStatus.%s is claimed %d times; want exactly 1", name, claimed[name])
	}
	require.Len(t, profileOwned, 6)
	assert.Equal(t, typ.NumField(), len(profileOwned))
}

// ControllerFields must declare its whole set and nothing else. Conditions is
// the documented exception: the generated WithConditions appends, so seeding
// it would collide with the caller's own set.
func TestControllerFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := setFields(status.ControllerFields(fullJobStatus()))

	want := make([]string, 0, len(jobControllerOwned))
	for _, n := range jobControllerOwned {
		if n == "Conditions" {
			continue
		}
		want = append(want, n)
	}
	sort.Strings(want)

	assert.Equal(t, want, got)
}

// ProfileFields must declare its whole set and nothing else. Conditions is
// the same documented exception as ControllerFields'.
func TestProfileFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := setFields(status.ProfileFields(fullProfileStatus()))

	want := make([]string, 0, len(profileOwned))
	for _, n := range profileOwned {
		if n == "Conditions" {
			continue
		}
		want = append(want, n)
	}
	sort.Strings(want)

	assert.Equal(t, want, got)
}
