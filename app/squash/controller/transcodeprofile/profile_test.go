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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// renderSpec returns a spec with every leaf of the standard's inputs and of
// the kept policy set to a non-zero value, so changing any one of them is a
// meaningful change.
func renderSpec() transcodev1alpha1.TranscodeProfileSpec {
	return transcodev1alpha1.TranscodeProfileSpec{
		Quality:   ptr.To[int32](30),
		Container: transcodev1alpha1.ContainerMKV,
		Hardware:  transcodev1alpha1.HardwareCPU,
		Audio:     transcodev1alpha1.AudioSpec{Languages: []string{"eng"}},
		Policy: transcodev1alpha1.PolicySpec{
			NeverTranscodeModifiers:  []string{"remux", "brdisk"},
			MinDuration:              &metav1.Duration{Duration: time.Minute},
			MaxOutputToSourcePercent: ptr.To[int32](100),
			ReplaceSource:            ptr.To(true), RecycleBin: ptr.To(true),
		},
	}
}

// standardInputs are the leaves the standard reads (worker.StandardProfile):
// a change to any of them moves status.hash.
var standardInputs = map[string]bool{
	"Quality": true, "Container": true, "Audio.Languages": true, "Policy.NeverTranscodeModifiers": true,
}

// operationalFields are the TranscodeProfileSpec fields too structured to
// walk leaf by leaf, each with a change to prove it leaves the hash: they
// decide where, when and whether an encode runs, never what it writes.
var operationalFields = map[string]func(*transcodev1alpha1.TranscodeProfileSpec){
	"Default":  func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Default = true },
	"Selector": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Selector = &metav1.LabelSelector{} },
	"Hardware": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Hardware = transcodev1alpha1.HardwareNVIDIA },
	"Resources": func(s *transcodev1alpha1.TranscodeProfileSpec) {
		s.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")}
	},
	"GPU":           func(s *transcodev1alpha1.TranscodeProfileSpec) { s.GPU = &transcodev1alpha1.GPUSpec{Count: 2} },
	"Scratch":       func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Scratch = resource.MustParse("50Gi") },
	"Priority":      func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Priority = 99 },
	"MaxConcurrent": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.MaxConcurrent = 3 },
	"ActiveDeadline": func(s *transcodev1alpha1.TranscodeProfileSpec) {
		s.ActiveDeadline = metav1.Duration{Duration: time.Hour}
	},
	"TTLSecondsAfterFinished": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.TTLSecondsAfterFinished = 60 },
	"Chunking":                func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Chunking = &transcodev1alpha1.ChunkSpec{} },
}

// TestTheHashCoversOnlyTheStandardsInputs: status.hash is the tag the
// worker writes and the name jobs take, so it moves exactly when what the
// standard writes would: quality, container, audio languages, the
// never-transcode modifiers, and standard.Version. Every other field --
// scheduling, hardware, the kept policy -- leaves it. The walk is over the
// CRD type, so a field added to TranscodeProfileSpec must be placed in one
// set or the other.
func TestTheHashCoversOnlyTheStandardsInputs(t *testing.T) {
	base := renderSpec()
	baseHash := profileHash(base)

	specType := reflect.TypeOf(base)
	seen := map[string]bool{}
	for i := range specType.NumField() {
		field := specType.Field(i)
		if change, ok := operationalFields[field.Name]; ok {
			s := renderSpec()
			change(&s)
			assert.Equalf(t, baseHash, profileHash(s), "%s is operational; it must not change status.hash", field.Name)
			continue
		}
		for _, leaf := range leafPaths(field.Type, []int{i}, field.Name) {
			s := renderSpec()
			v := reflect.ValueOf(&s).Elem().FieldByIndex(leaf.index)
			require.Falsef(t, v.IsZero(), "renderSpec leaves %s zero; set it so changing it is meaningful", leaf.name)
			mutate(t, v, leaf.name)
			if standardInputs[leaf.name] {
				seen[leaf.name] = true
				assert.NotEqualf(t, baseHash, profileHash(s), "%s is a standard input; changing it must change status.hash", leaf.name)
			} else {
				assert.Equalf(t, baseHash, profileHash(s), "%s is not a standard input; it must not change status.hash", leaf.name)
			}
		}
	}
	assert.Equal(t, standardInputs, seen, "the walk reached every standard input")
	for name := range operationalFields {
		_, ok := specType.FieldByName(name)
		require.Truef(t, ok, "operationalFields names %s, which TranscodeProfileSpec no longer has", name)
	}

	assert.NotEqual(t, baseHash, worker.ProfileHashAt(base, standard.Version+1), "a new standard.Version moves every hash")
	assert.Equal(t, baseHash, worker.ProfileHashAt(base, standard.Version))
}

// Unset and the default are the same profile, so they are the same hash: a
// profile a Go client created (nil quality, empty container) and one the
// apiserver defaulted must not be told apart.
func TestTheHashReadsUnsetFieldsAsTheirDefaults(t *testing.T) {
	set := renderSpec()
	set.Quality = ptr.To(transcodev1alpha1.DefaultQuality)
	unset := set
	unset.Quality = nil
	unset.Container = ""
	assert.Equal(t, profileHash(set), profileHash(unset))
}

type leaf struct {
	index []int
	name  string
}

// leafPaths lists every non-struct field reachable from t through struct
// fields, as FieldByIndex paths. metav1.Duration is a struct and so is
// walked into, reaching its time.Duration.
func leafPaths(t reflect.Type, prefix []int, name string) []leaf {
	if t.Kind() != reflect.Struct {
		return []leaf{{index: prefix, name: name}}
	}
	var out []leaf
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		out = append(out, leafPaths(f.Type, append(append([]int(nil), prefix...), i), name+"."+f.Name)...)
	}
	return out
}

// mutate changes v, which is non-zero, to a different value of its type.
func mutate(t *testing.T, v reflect.Value, name string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		p.Elem().Set(v.Elem())
		mutate(t, p.Elem(), name)
		v.Set(p)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		for _, k := range v.MapKeys() {
			m.SetMapIndex(k, v.MapIndex(k))
		}
		m.SetMapIndex(reflect.ValueOf("changed").Convert(v.Type().Key()), reflect.ValueOf("1").Convert(v.Type().Elem()))
		v.Set(m)
	case reflect.Slice:
		v.Set(reflect.Append(v, reflect.ValueOf("changed").Convert(v.Type().Elem())))
	case reflect.Struct:
		// Reached only through a pointer (leafPaths walks into a struct
		// value): *metav1.Duration. Changing its first field changes it.
		mutate(t, v.Field(0), name+"."+v.Type().Field(0).Name)
	default:
		t.Fatalf("%s has kind %s, which this test does not know how to change; teach mutate", name, v.Kind())
	}
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
	// app/catalog/controller/mediafile.transcodeProfileTag and by
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
	assert.False(t, alreadyTranscoded(&mf, "hevc"))

	mf.Status.ProbeHash = "abc123"
	mf.Status.MediaInfo = &commonv1.MediaInfo{}
	assert.True(t, probed(&mf))
	assert.False(t, alreadyTranscoded(&mf, "hevc"), "an untouched original is not transcoded")

	// catalogarr's record of a transcode by this profile counts under any
	// hash: an edit or a new standard.Version redoes nothing. Another
	// profile's record on this (untouched) source does not.
	mf.Status.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: "hevc@deadbeef"}
	assert.True(t, alreadyTranscoded(&mf, "hevc"))
	assert.False(t, alreadyTranscoded(&mf, "hevc-4k"), "another profile's derived copy is not this profile's")

	// The probe's record of the file's own tag counts whoever wrote it: the
	// bytes are a transcode.
	mf.Status.Transcode = nil
	mf.Status.MediaInfo.TranscodeProfile = "other@0123"
	assert.True(t, alreadyTranscoded(&mf, "hevc"))

	// So does a swap catalogarr incorporated (spec.original false).
	mf.Status.MediaInfo.TranscodeProfile = ""
	mf.Spec.Original = ptr.To(false)
	assert.True(t, alreadyTranscoded(&mf, "hevc"))
}

// A file whose Movie or Episode is gone -- an import list's removeAndKeep
// keeps the file and its record but deletes the item -- is no longer a
// candidate; one whose item exists is, and a kind managedFiles does not
// judge (a book) passes through for eligibleKind to drop.
func TestManagedFilesDropsFilesWhoseItemIsGone(t *testing.T) {
	file := func(ns, name string, kind commonv1.MediaKind) catalogv1alpha1.MediaFile {
		return catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}},
		}
	}
	files := []catalogv1alpha1.MediaFile{
		file("media", "kept-movie", commonv1.MediaKindMovie),
		file("media", "orphan-movie", commonv1.MediaKindMovie),
		file("media", "kept-episode", commonv1.MediaKindEpisode),
		file("media", "orphan-episode", commonv1.MediaKindEpisode),
		file("other", "kept-movie", commonv1.MediaKindMovie), // same name, other namespace: no item there
		file("media", "a-book", commonv1.MediaKindBook),
	}
	items := map[itemKey]bool{
		{kind: commonv1.MediaKindMovie, namespace: "media", name: "kept-movie"}:     true,
		{kind: commonv1.MediaKindEpisode, namespace: "media", name: "kept-episode"}: true,
		// An Episode by the movie's name must not keep the movie's file.
		{kind: commonv1.MediaKindEpisode, namespace: "media", name: "orphan-movie"}: true,
	}

	var got []string
	for _, mf := range managedFiles(files, items) {
		got = append(got, mf.Namespace+"/"+mf.Name)
	}
	assert.Equal(t, []string{"media/kept-movie", "media/kept-episode", "media/a-book"}, got)
}

// TestAlreadyTranscodedCountsATranscodeFromElsewhere: a file another tool
// re-encoded to HEVC (Tdarr's QSV output) is transcoded and final, so no
// profile queues it again.
func TestAlreadyTranscodedCountsATranscodeFromElsewhere(t *testing.T) {
	mf := movieFile("127-hours-2010", nil)
	mf.Status.ProbeHash = "abc123"
	mf.Status.MediaInfo = &commonv1.MediaInfo{VideoCodec: "hevc", VideoEncoder: "Lavc61.3.100 hevc_qsv"}
	assert.True(t, alreadyTranscoded(&mf, "hevc"))
	mf.Status.MediaInfo.VideoEncoder = "Lavc61.3.100 libx264"
	assert.False(t, alreadyTranscoded(&mf, "hevc"), "an H.264 release made with ffmpeg is not a transcode")
}
