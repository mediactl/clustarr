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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/status"
)

// TranscodeProfile has exactly one writer, so there is no split to restate --
// but the field list is still worth asserting complete, so a field added
// later cannot silently go unsent by [status.ProfileFields].
var profileOwned = []string{
	"ObservedGeneration", "Hash", "MatchingFiles", "PendingJobs", "RunningJobs", "Conditions", "EncoderLimits",
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
		EncoderLimits:      []transcodev1alpha1.EncoderLimit{{Class: transcodev1alpha1.HardwareNVIDIA, Node: "n1"}},
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
	require.Len(t, profileOwned, 7)
	assert.Equal(t, typ.NumField(), len(profileOwned))
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

// ProfileFields re-sends every leaf of every encoder limit, nvdec included:
// one the seed dropped would be released by the next apply that did not
// re-measure, and the profile would stop showing what its nodes decode.
func TestProfileFieldsResendsEveryEncoderLimitLeaf(t *testing.T) {
	st := fullProfileStatus()
	st.EncoderLimits = []transcodev1alpha1.EncoderLimit{{
		Class: transcodev1alpha1.HardwareNVIDIA, Node: "n1",
		NVDEC: []string{"h264:8", "hevc:10"}, Healthy: new(false), Message: "no /dev/nvidia0",
	}}
	ac := status.ProfileFields(st)
	require.Len(t, ac.EncoderLimits, 1)
	assert.Equal(t, []string{"Class", "Healthy", "Message", "NVDEC", "Node"}, setFields(&ac.EncoderLimits[0]))
	assert.Equal(t, []string{"h264:8", "hevc:10"}, ac.EncoderLimits[0].NVDEC)
}
