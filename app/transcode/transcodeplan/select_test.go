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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
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
	"github.com/mediactl/clustarr/app/transcode/jobspec"
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
			KeepCodecs:               []string{"h264"},
			MinDuration:              &metav1.Duration{Duration: time.Minute},
			MaxOutputToSourcePercent: ptr.To[int32](100),
			ReplaceSource:            new(true), RecycleBin: ptr.To(true),
		},
	}
}

// standardInputs are the leaves the standard reads (jobspec.StandardProfile)
// and the size limit that decides whether its output is kept: a change to
// any of them moves status.hash, so a file a terminal job skipped or failed
// under the old values is planned again (a new job name) under the new.
// Container is not one since standard.Version 2: every transcode writes MP4
// (jobspec.OutputContainer).
var standardInputs = map[string]bool{
	"Quality": true, "Audio.Languages": true, "Policy.NeverTranscodeModifiers": true, "Policy.KeepCodecs": true,
	"Policy.MinDuration": true, "Policy.MaxOutputToSourcePercent": true,
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
	baseHash := jobspec.ProfileHash(base)

	specType := reflect.TypeOf(base)
	seen := map[string]bool{}
	for i := range specType.NumField() {
		field := specType.Field(i)
		if change, ok := operationalFields[field.Name]; ok {
			s := renderSpec()
			change(&s)
			assert.Equalf(t, baseHash, jobspec.ProfileHash(s), "%s is operational; it must not change status.hash", field.Name)
			continue
		}
		for _, leaf := range leafPaths(field.Type, []int{i}, field.Name) {
			s := renderSpec()
			v := reflect.ValueOf(&s).Elem().FieldByIndex(leaf.index)
			require.Falsef(t, v.IsZero(), "renderSpec leaves %s zero; set it so changing it is meaningful", leaf.name)
			mutate(t, v, leaf.name)
			if standardInputs[leaf.name] {
				seen[leaf.name] = true
				assert.NotEqualf(t, baseHash, jobspec.ProfileHash(s), "%s is a standard input; changing it must change status.hash", leaf.name)
			} else {
				assert.Equalf(t, baseHash, jobspec.ProfileHash(s), "%s is not a standard input; it must not change status.hash", leaf.name)
			}
		}
	}
	assert.Equal(t, standardInputs, seen, "the walk reached every standard input")
	for name := range operationalFields {
		_, ok := specType.FieldByName(name)
		require.Truef(t, ok, "operationalFields names %s, which TranscodeProfileSpec no longer has", name)
	}

	assert.NotEqual(t, baseHash, jobspec.ProfileHashAt(base, standard.Version+1), "a new standard.Version moves every hash")
	assert.Equal(t, baseHash, jobspec.ProfileHashAt(base, standard.Version))
}

// The two lists are sets -- a language is kept or not, a modifier skips or
// not -- so neither their order nor nil versus empty is a different
// profile. The canonical order is descending, which leaves the CRD default
// modifiers {"remux","brdisk"} as they are, and with them every profile
// hash already stored and tagged.
func TestTheHashTreatsItsListsAsSets(t *testing.T) {
	a, b := renderSpec(), renderSpec()
	a.Audio.Languages, b.Audio.Languages = []string{"eng", "fre"}, []string{"fre", "eng", "eng"}
	a.Policy.NeverTranscodeModifiers, b.Policy.NeverTranscodeModifiers = []string{"remux", "brdisk"}, []string{"brdisk", "remux"}
	assert.Equal(t, jobspec.ProfileHash(a), jobspec.ProfileHash(b), "order and duplicates are not a different profile")

	a.Audio.Languages, b.Audio.Languages = nil, []string{}
	assert.Equal(t, jobspec.ProfileHash(a), jobspec.ProfileHash(b), "an empty list is an unset one")

	// policy.keepCodecs: HEVC is kept whether it is listed or not.
	a.Policy.KeepCodecs, b.Policy.KeepCodecs = []string{"hevc", "h264"}, []string{"h264", "hevc", "h264"}
	assert.Equal(t, jobspec.ProfileHash(a), jobspec.ProfileHash(b))
	a.Policy.KeepCodecs, b.Policy.KeepCodecs = []string{"hevc"}, nil
	assert.Equal(t, jobspec.ProfileHash(a), jobspec.ProfileHash(b), "listing hevc keeps what is always kept")
	b.Policy.KeepCodecs = []string{"h264"}
	assert.NotEqual(t, jobspec.ProfileHash(a), jobspec.ProfileHash(b), "keeping h264 is a different standard")

	stable := renderSpec()
	stable.Policy.NeverTranscodeModifiers = []string{"remux", "brdisk"}
	stable.Policy.KeepCodecs = nil // a profile that keeps only HEVC hashes in the shape it always had
	before := sha256Hex(t, struct {
		Version                  int
		Quality                  int32
		Container                transcodev1alpha1.Container
		Languages                []string
		NeverTranscodeModifiers  []string
		MinDuration              time.Duration
		MaxOutputToSourcePercent int32
	}{standard.Version, 30, jobspec.OutputContainer, []string{"eng"}, []string{"remux", "brdisk"}, time.Minute, 100})
	assert.Equal(t, before, jobspec.ProfileHash(stable), "the hash's shape is unchanged: the container field holds the MP4 standard's")
}

func sha256Hex(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Unset and the default are the same profile, so they are the same hash: a
// profile a Go client created (nil quality, empty container) and one the
// apiserver defaulted must not be told apart.
func TestTheHashReadsUnsetFieldsAsTheirDefaults(t *testing.T) {
	set := renderSpec()
	set.Quality = new(transcodev1alpha1.DefaultQuality)
	set.Policy.MinDuration = &metav1.Duration{Duration: jobspec.DefaultMinDuration}
	set.Policy.MaxOutputToSourcePercent = ptr.To[int32](jobspec.DefaultMaxOutputToSourcePercent)
	unset := set
	unset.Quality = nil
	unset.Container = ""
	unset.Policy.MinDuration, unset.Policy.MaxOutputToSourcePercent = nil, nil
	assert.Equal(t, jobspec.ProfileHash(set), jobspec.ProfileHash(unset))
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

func TestProfileTagMatchesTheMediafileControllerConvention(t *testing.T) {
	// This exact "<name>@<hash>" shape is also rendered by
	// app/catalog/controller/mediafile.transcodeProfileTag and by
	// pkg/transcode.Plan's own PlanResult.Tags["CLUSTARR_PROFILE"]. All three
	// must agree byte for byte.
	assert.Equal(t, "hevc-1080p@deadbeef", Tag("hevc-1080p", "deadbeef"))
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
	assert.True(t, ProfileLess(&older, &newer), "an earlier creation timestamp wins regardless of name")
	assert.False(t, ProfileLess(&newer, &older))

	tie1 := profileAt("a-profile", t0, transcodev1alpha1.TranscodeProfileSpec{})
	tie2 := profileAt("b-profile", t0, transcodev1alpha1.TranscodeProfileSpec{})
	assert.True(t, ProfileLess(&tie1, &tie2), "a tied timestamp falls back to the lexicographically smaller name")
}

func TestValidateProfileFlagsTheNewerOfTwoDefaults(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := profileAt("first", t0, transcodev1alpha1.TranscodeProfileSpec{Default: true})
	second := profileAt("second", t0.Add(time.Minute), transcodev1alpha1.TranscodeProfileSpec{Default: true})
	all := []transcodev1alpha1.TranscodeProfile{first, second}

	invalid, _, _ := ValidateProfile(&first, all)
	assert.False(t, invalid, "the earlier-created default profile must not be invalidated")

	invalid, reason, msg := ValidateProfile(&second, all)
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

	winnerMatching, winnerOverlap := SelectFiles(&hd, profiles, nil, files)
	require.Len(t, winnerMatching, 1, "the earlier-created profile must win the overlapping file")
	assert.Equal(t, "arrival-2016", winnerMatching[0].Name)
	assert.False(t, winnerOverlap, "the winner itself never reports overlap")

	loserMatching, loserOverlap := SelectFiles(&uhd, profiles, nil, files)
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
	def0 := DefaultWinner(profiles)

	files := []catalogv1alpha1.MediaFile{
		movieFile("hd-movie", map[string]string{"tier": "hd"}),
		movieFile("unlabeled-movie", nil),
	}

	defMatching, _ := SelectFiles(&def, profiles, def0, files)
	require.Len(t, defMatching, 1, "the default must only pick up the file no selector claims")
	assert.Equal(t, "unlabeled-movie", defMatching[0].Name)

	selMatching, _ := SelectFiles(&selective, profiles, def0, files)
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
	matching, overlapped := SelectFiles(&def, []transcodev1alpha1.TranscodeProfile{def}, &def, []catalogv1alpha1.MediaFile{book})
	assert.Empty(t, matching, "a book MediaFile must never be selected, even by the default profile")
	assert.False(t, overlapped)
}

func TestAlreadyTranscodedAndProbed(t *testing.T) {
	mf := movieFile("arrival-2016", nil)
	assert.False(t, Probed(&mf), "an unprobed file is not ready for a transcode")
	assert.False(t, AlreadyTranscoded(&mf, "hevc"))

	mf.Status.ProbeHash = "abc123"
	mf.Status.MediaInfo = &commonv1.MediaInfo{}
	assert.True(t, Probed(&mf))
	assert.False(t, AlreadyTranscoded(&mf, "hevc"), "an untouched original is not transcoded")

	// catalog's record of a transcode by this profile counts under any
	// hash: an edit or a new standard.Version redoes nothing. Another
	// profile's record on this (untouched) source does not.
	mf.Status.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: "hevc@deadbeef"}
	assert.True(t, AlreadyTranscoded(&mf, "hevc"))
	assert.False(t, AlreadyTranscoded(&mf, "hevc-4k"), "another profile's derived copy is not this profile's")

	// The probe's record of the file's own tag counts whoever wrote it: the
	// bytes are a transcode.
	mf.Status.Transcode = nil
	mf.Status.MediaInfo.TranscodeProfile = "other@0123"
	assert.True(t, AlreadyTranscoded(&mf, "hevc"))

	// So does a swap catalog incorporated (spec.original false).
	mf.Status.MediaInfo.TranscodeProfile = ""
	mf.Spec.Original = new(false)
	assert.True(t, AlreadyTranscoded(&mf, "hevc"))
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
	items := map[ItemKey]bool{
		{Kind: commonv1.MediaKindMovie, Namespace: "media", Name: "kept-movie"}:     true,
		{Kind: commonv1.MediaKindEpisode, Namespace: "media", Name: "kept-episode"}: true,
		// An Episode by the movie's name must not keep the movie's file.
		{Kind: commonv1.MediaKindEpisode, Namespace: "media", Name: "orphan-movie"}: true,
	}

	var got []string
	for _, mf := range ManagedFiles(files, items) {
		got = append(got, mf.Namespace+"/"+mf.Name)
	}
	assert.Equal(t, []string{"media/kept-movie", "media/kept-episode", "media/a-book"}, got)
}

// TestATranscodeFromElsewhereIsNotDoneHere: a file another tool re-encoded
// to HEVC (Tdarr's QSV output) is final -- MediaFile.Transcoded, so never
// upgraded -- but not done: a profile still remuxes it into the MP4 layout,
// and the standard never encodes it a second time (owner, 2026-10-15).
// Only transcode's own output, tagged, is done.
func TestATranscodeFromElsewhereIsNotDoneHere(t *testing.T) {
	mf := movieFile("127-hours-2010", nil)
	mf.Status.ProbeHash = "abc123"
	mf.Status.MediaInfo = &commonv1.MediaInfo{VideoCodec: "hevc", VideoEncoder: "Lavc61.3.100 hevc_qsv"}
	assert.True(t, mf.Transcoded(), "final: never upgraded")
	assert.False(t, AlreadyTranscoded(&mf, "hevc"), "but remuxed by a profile")
	mf.Status.MediaInfo.TranscodeProfile = "hevc@0ld"
	assert.True(t, AlreadyTranscoded(&mf, "hevc"), "transcode's own output is done")
}

// clustarr's own transcode from before the MP4 layout -- HEVC in Matroska,
// its probe reading the CLUSTARR_PROFILE tag -- is final but not done: the
// standard remuxes it into the MP4 layout, the tag keeping it from being
// encoded again (owner, 2026-10-15). In an MP4 it is done; so is a swap
// whose probe lacks the tag, which the standard could not tell from an
// original; and so is a kept copy's record on its untouched source.
func TestClustarrsOwnMKVTranscodeIsNotDone(t *testing.T) {
	mf := movieFile("arrival-2016", nil)
	mf.Spec.Path = "/data/movies/Arrival (2016)/Arrival (2016).mkv"
	mf.Status.ProbeHash = "abc123"
	mf.Status.MediaInfo = &commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc", TranscodeProfile: "hevc@0ld"}
	assert.True(t, mf.Transcoded(), "final: never upgraded")
	assert.False(t, AlreadyTranscoded(&mf, "hevc"), "remuxed into the MP4 layout")
	mf.Spec.Original = new(false)
	assert.False(t, AlreadyTranscoded(&mf, "hevc"), "a swap catalog incorporated alike")
	mf.Status.MediaInfo.Container = ""
	assert.False(t, AlreadyTranscoded(&mf, "hevc"), "an unrecorded container is read off the name")

	mf.Status.MediaInfo.Container = "mp4"
	assert.True(t, AlreadyTranscoded(&mf, "hevc"), "in the MP4 layout it is done")

	mf.Status.MediaInfo.Container, mf.Status.MediaInfo.TranscodeProfile = "mkv", ""
	assert.True(t, AlreadyTranscoded(&mf, "hevc"), "untagged, nothing would keep the standard from encoding it")

	mf.Spec.Original = nil
	mf.Status.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, ProfileTag: "hevc@deadbeef"}
	assert.True(t, AlreadyTranscoded(&mf, "hevc"), "a kept copy's record on its source")
}
