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

package jobspec

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestLocalPathMapsLogicalDataPathsAndRefusesEverythingElse(t *testing.T) {
	cases := []struct {
		name, dataDir, in, want string
		wantErr                 bool
	}{
		{name: "identity in a pod", dataDir: "/data", in: "/data/media/movies/A (2020)/A.mkv", want: "/data/media/movies/A (2020)/A.mkv"},
		{name: "remapped", dataDir: "/tmp/x", in: "/data/media/movies/A.mkv", want: "/tmp/x/media/movies/A.mkv"},
		{name: "dot-dot cannot escape", dataDir: "/tmp/x", in: "/data/media/../../etc/passwd", wantErr: true},
		{name: "outside /data", dataDir: "/data", in: "/etc/passwd", wantErr: true},
		{name: "prefix is not containment", dataDir: "/data", in: "/database/x.mkv", wantErr: true},
		{name: "relative", dataDir: "/data", in: "media/x.mkv", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LocalPath(tc.dataDir, tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRootFolderForPicksTheDeepestContainingFolderAndNeverAPrefixMatch(t *testing.T) {
	rf := func(name, path string) catalogv1alpha1.RootFolder {
		return catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: catalogv1alpha1.RootFolderSpec{Path: path}}
	}
	folders := []catalogv1alpha1.RootFolder{
		rf("media", "/data/media"),
		rf("movies", "/data/media/movies"),
		rf("movies4k", "/data/media/movies4k"),
	}
	assert.Equal(t, "movies", RootFolderFor(folders, "/data/media/movies/A (2020)/A.mkv").Name)
	assert.Equal(t, "movies4k", RootFolderFor(folders, "/data/media/movies4k/A.mkv").Name)
	assert.Equal(t, "media", RootFolderFor(folders, "/data/media/tv/x.mkv").Name)
	assert.Nil(t, RootFolderFor(folders, "/data/torrents/A.mkv"), "a seeding copy is under no root folder")
	assert.Nil(t, RootFolderFor(folders[1:], "/data/media/movies"), "the folder itself is not inside itself")
}

// ProfileHardware is the job's pinned class, else the profile's; auto with
// no class chosen yet plans for the CPU.
func TestProfileHardwareTakesTheJobsOverride(t *testing.T) {
	spec := transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareNVIDIA}
	assert.Equal(t, transcode.HardwareNVIDIA, ProfileHardware(spec, nil))
	cpu, auto := transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareAuto
	assert.Equal(t, transcode.HardwareCPU, ProfileHardware(spec, &cpu), "TranscodeJob.spec.hardware overrides the profile")
	assert.Equal(t, transcode.HardwareNVIDIA, ProfileHardware(spec, &auto), "auto on the job defers to the profile")
	assert.Equal(t, transcode.HardwareCPU, ProfileHardware(transcodev1alpha1.TranscodeProfileSpec{Hardware: cpu}, &auto),
		"an auto override of a pinned profile keeps its class")
	spec.Hardware = transcodev1alpha1.HardwareAuto
	assert.Equal(t, transcode.HardwareCPU, ProfileHardware(spec, nil), "auto with no class chosen plans for the CPU")
	assert.Equal(t, transcode.HardwareNVIDIA, ProfileHardware(spec, new(transcodev1alpha1.HardwareNVIDIA)), "a chosen class overrides auto")
	// gpu (2026-10-01) is auto that never takes a CPU slot for want of a GPU
	// one: before a class is chosen it plans, and hashes, as auto does.
	spec.Hardware = transcodev1alpha1.HardwareGPU
	assert.Equal(t, transcode.HardwareCPU, ProfileHardware(spec, nil), "gpu with no class chosen plans for the CPU")
	assert.Equal(t, transcode.HardwareIntel, ProfileHardware(spec, new(transcodev1alpha1.HardwareIntel)), "a chosen class overrides gpu")
	gpu := transcodev1alpha1.HardwareGPU
	assert.Equal(t, transcode.HardwareNVIDIA, ProfileHardware(transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareNVIDIA}, &gpu),
		"a gpu override of a pinned profile defers to it, as auto does")
}

// StandardProfile carries every field the standard reads, with its default.
func TestStandardProfileCarriesTheStandardsInputs(t *testing.T) {
	spec := transcodev1alpha1.TranscodeProfileSpec{
		Quality:   ptr.To[int32](30),
		Container: transcodev1alpha1.ContainerMP4,
		Audio:     transcodev1alpha1.AudioSpec{Languages: []string{"en"}},
		Policy: transcodev1alpha1.PolicySpec{
			NeverTranscodeModifiers: []string{"remux"},
			MinDuration:             &metav1.Duration{Duration: 2 * time.Minute},
		},
	}
	got := StandardProfile("hevc", "abc", spec)
	assertNoZeroLeaf(t, reflect.ValueOf(got), "StandardProfile")
	assert.Equal(t, int32(30), got.Quality)
	assert.Equal(t, transcode.ContainerMP4, got.Container)
	assert.Equal(t, 2*time.Minute, got.MinDuration)
	assert.Equal(t, transcodev1alpha1.DefaultQuality, StandardProfile("hevc", "abc", transcodev1alpha1.TranscodeProfileSpec{}).Quality)
}

// policy.replaceSource and policy.recycleBin are pointers so a Go client
// can say false; unset must still mean the CRD default, true, because a spec
// built in Go never passes through the apiserver's defaulting.
func TestPolicyPointersDefaultToTrue(t *testing.T) {
	var unset transcodev1alpha1.PolicySpec
	assert.True(t, ReplaceSource(unset))
	assert.True(t, RecycleBin(unset))

	off := transcodev1alpha1.PolicySpec{ReplaceSource: new(false), RecycleBin: ptr.To(false)}
	assert.False(t, ReplaceSource(off))
	assert.False(t, RecycleBin(off))
}

// The defaults the policy accessors apply to a nil pointer restate the CRD's; this
// holds each to the generated schema, so a changed +kubebuilder:default
// cannot leave a Go-created profile on the old value.
func TestPointerDefaultsMatchTheGeneratedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../../config/crd/bases/transcode.clustarr.io_transcodeprofiles.yaml")
	require.NoError(t, err)
	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	defaultAt := func(path ...string) any {
		t.Helper()
		node := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"]
		for _, p := range append([]string{"spec"}, path...) {
			node = node.(map[string]any)["properties"].(map[string]any)[p]
			require.NotNilf(t, node, "spec.%v is not in the generated CRD", path)
		}
		return node.(map[string]any)["default"]
	}
	for _, path := range [][]string{{"policy", "replaceSource"}, {"policy", "recycleBin"}} {
		assert.Equalf(t, true, defaultAt(path...), "a nil spec.%v reads as true", path)
	}
	d, err := time.ParseDuration(defaultAt("policy", "minDuration").(string))
	require.NoError(t, err)
	assert.Equal(t, DefaultMinDuration, d)
	assert.EqualValues(t, DefaultMaxOutputToSourcePercent, defaultAt("policy", "maxOutputToSourcePercent"))
}

// policy.minDuration and policy.maxOutputToSourcePercent are pointers
// because their zero means something ("consider every file", "no size
// check") a Go client could not otherwise send: unset reads as the CRD
// default, an explicit zero as zero.
func TestPolicyAccessorsApplyTheDefaultOnlyToUnset(t *testing.T) {
	var unset transcodev1alpha1.PolicySpec
	assert.Equal(t, time.Minute, MinDuration(unset))
	assert.Equal(t, int32(100), MaxOutputToSourcePercent(unset))
	zero := transcodev1alpha1.PolicySpec{MinDuration: &metav1.Duration{}, MaxOutputToSourcePercent: ptr.To[int32](0)}
	assert.Zero(t, MinDuration(zero))
	assert.Zero(t, MaxOutputToSourcePercent(zero))
}

func assertNoZeroLeaf(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	if v.Kind() == reflect.Struct {
		for i := range v.NumField() {
			assertNoZeroLeaf(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
		return
	}
	assert.Falsef(t, v.IsZero(), "%s was not carried over", path)
}

// OutputPath is gap-fix ruling R-11's output location, shared by the
// controller's plan and the worker's write.
func TestOutputPath(t *testing.T) {
	const src = "/data/media/movies/Film (2020)/Film (2020).mkv"
	spec := func(source string, out *string) transcodev1alpha1.TranscodeJobSpec {
		return transcodev1alpha1.TranscodeJobSpec{SourcePath: source, OutputPath: out}
	}
	for _, tc := range []struct {
		name      string
		spec      transcodev1alpha1.TranscodeJobSpec
		container transcodev1alpha1.Container
		replace   bool
		want      string
		wantErr   string
	}{
		{name: "same container replaces in place", spec: spec(src, nil), container: "mkv", replace: true, want: src},
		{name: "an empty container is mkv", spec: spec(src, nil), replace: true, want: src},
		{
			name: "case does not make a change", spec: spec("/data/media/movies/F/F.MKV", nil), container: "mkv", replace: true,
			want: "/data/media/movies/F/F.MKV",
		},
		{
			name: "container change gets the new extension", spec: spec("/data/media/movies/F/F.mp4", nil), container: "mkv", replace: true,
			want: "/data/media/movies/F/F.mkv",
		},
		{
			name: "mkv to mp4", spec: spec(src, nil), container: "mp4", replace: true,
			want: "/data/media/movies/Film (2020)/Film (2020).mp4",
		},
		{
			name: "a kept source takes a version name", spec: spec(src, nil), container: "mkv", replace: false,
			want: "/data/media/movies/Film (2020)/Film (2020) - hevc.mkv",
		},
		{
			name: "a kept source across a container change", spec: spec("/data/media/movies/F/F.avi", nil), container: "mkv", replace: false,
			want: "/data/media/movies/F/F - hevc.mkv",
		},
		{
			name: "spec.outputPath wins", spec: spec(src, new("/data/media/movies/Other/Film.mkv")), container: "mkv", replace: true,
			want: "/data/media/movies/Other/Film.mkv",
		},
		{
			name: "spec.outputPath is cleaned", spec: spec(src, new("/data/media/movies/./Other//Film.mkv")), container: "mkv", replace: false,
			want: "/data/media/movies/Other/Film.mkv",
		},
		{
			name: "spec.outputPath must match the container", spec: spec(src, new("/data/media/x.mp4")), container: "mkv", replace: true,
			wantErr: "extension",
		},
		{
			name: "spec.outputPath must be absolute", spec: spec(src, new("x.mkv")), container: "mkv", replace: true,
			wantErr: "not absolute",
		},
		{
			name: "a kept source cannot be the output", spec: spec(src, new(src)), container: "mkv", replace: false,
			wantErr: "keeps the source",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OutputPath(tc.spec, "hevc", tc.container, tc.replace)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOutputPathIsAlwaysMP4(t *testing.T) {
	out, err := OutputPath(transcodev1alpha1.TranscodeJobSpec{SourcePath: "/data/media/m/F.mkv"}, "p", OutputContainer, true)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/m/F.mp4", out)
}

func TestStandardProfileIgnoresTheContainer(t *testing.T) {
	sp := StandardProfile("p", "h", transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMKV})
	assert.Equal(t, transcode.ContainerMP4, sp.Container)
}

func TestTheV2HashIgnoresTheContainer(t *testing.T) {
	mkv := ProfileHashAt(transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMKV}, 2)
	mp4 := ProfileHashAt(transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMP4}, 2)
	assert.Equal(t, mkv, mp4)
	assert.NotEqual(t, ProfileHashAt(transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMKV}, 1), mkv)
}
