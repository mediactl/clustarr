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
	"github.com/mediactl/clustarr/pkg/k8s"
)

// renderSpec returns a spec with every leaf of every render-relevant field
// set to a non-zero value, so that changing any one of them is a change a
// correct converter must carry into the hash.
func renderSpec() transcodev1alpha1.TranscodeProfileSpec {
	return transcodev1alpha1.TranscodeProfileSpec{
		Container: transcodev1alpha1.ContainerMKV,
		Hardware:  transcodev1alpha1.HardwareCPU,
		Video: transcodev1alpha1.VideoSpec{
			Codec: "hevc", PixelFormat: "yuv420p10le", Profile: "main10",
			CRF:    transcodev1alpha1.CRFTable{SD: 21, HD: 22, UHD: 23, HDROffset: ptr.To[int32](-1)},
			Preset: "slow", Tune: ptr.To("grain"),
			KeyintFactor: 10, BFrames: 8, Refs: 4, RCLookahead: 40, AQMode: 3,
			MaxRateKbps: ptr.To[int32](20000), BufSizeKbps: ptr.To[int32](40000),
			ExtraX265Params: map[string]string{"no-sao": "1"},
			NVENC:           transcodev1alpha1.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24, Multipass: "fullres", BRefMode: "middle"},
			QSV:             transcodev1alpha1.QSVSpec{GlobalQuality: 22, Preset: "veryslow", LookAheadDepth: 40},
		},
		Audio: transcodev1alpha1.AudioSpec{
			Codec: "aac", BitratePerChannelKbps: 64, KeepOriginal: transcodev1alpha1.KeepOriginalAtmos,
			Languages: []string{"eng"}, DropCommentary: ptr.To(true), StereoCompatTrack: true,
		},
		Subtitles: transcodev1alpha1.SubSpec{CopyText: ptr.To(true), CopyBitmap: ptr.To(true), CopyAttachments: ptr.To(true)},
		HDR:       transcodev1alpha1.HDRSpec{HDR10Plus: transcodev1alpha1.HDR10PlusDrop, DolbyVision: transcodev1alpha1.DolbyVisionPassthrough},
		Policy: transcodev1alpha1.PolicySpec{
			SkipIfCompliant: ptr.To(true), RemuxOnlyWhenVideoCompliant: ptr.To(true),
			NeverTranscodeModifiers:  []string{"remux", "brdisk"},
			MinDuration:              &metav1.Duration{Duration: time.Minute},
			MaxOutputToSourcePercent: ptr.To[int32](100),
			ReplaceSource:            ptr.To(true), RecycleBin: ptr.To(true),
		},
		Verify: transcodev1alpha1.VerifySpec{PacketCount: ptr.To(true), FullDecode: true, VMAFMinCentis: ptr.To[int32](9000)},
	}
}

// schedulingFields are the TranscodeProfileSpec fields that legitimately do
// NOT reach status.hash, each with a change to prove it. They decide where
// and when an encode runs and how it is admitted -- never a byte of what it
// writes -- so an edit to any of them must not re-transcode the library.
// pkg/transcode.ProfileSpec omits exactly these, by its own doc comment.
var schedulingFields = map[string]func(*transcodev1alpha1.TranscodeProfileSpec){
	"Default":  func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Default = true },
	"Selector": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Selector = &metav1.LabelSelector{} },
	"Resources": func(s *transcodev1alpha1.TranscodeProfileSpec) {
		s.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")}
	},
	"GPU":      func(s *transcodev1alpha1.TranscodeProfileSpec) { s.GPU = &transcodev1alpha1.GPUSpec{Count: 2} },
	"Scratch":  func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Scratch = resource.MustParse("50Gi") },
	"Priority": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Priority = 99 },
	// MaxConcurrent is admission-only (per-profile concurrency, X1 item 8).
	"MaxConcurrent": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.MaxConcurrent = 3 },
	"ActiveDeadline": func(s *transcodev1alpha1.TranscodeProfileSpec) {
		s.ActiveDeadline = metav1.Duration{Duration: time.Hour}
	},
	"TTLSecondsAfterFinished": func(s *transcodev1alpha1.TranscodeProfileSpec) { s.TTLSecondsAfterFinished = 60 },
	"Chunking":                func(s *transcodev1alpha1.TranscodeProfileSpec) { s.Chunking = &transcodev1alpha1.ChunkSpec{} },
}

// status.hash names every TranscodeJob and is the CLUSTARR_PROFILE tag
// catalogarr compares, so it is what makes a profile edit re-transcode: a
// field the hash does not see is a field whose edit silently does nothing
// to the library. E-1 computed it through its own converter while the
// worker executed another; this pins the one converter left.
//
// The walk is over the CRD type, not pkg/transcode's mirror: a field added
// to TranscodeProfileSpec and forgotten in worker.ProfileSpec (or missing
// from transcode.ProfileSpec altogether) fails here by its path. Every
// top-level field must be walked or listed in schedulingFields, so a new
// field cannot slip past uncategorised.
func TestStatusHashChangesWithEveryRenderField(t *testing.T) {
	base := renderSpec()
	baseHash := profileHash(base)

	specType := reflect.TypeOf(base)
	var walked int
	for i := range specType.NumField() {
		field := specType.Field(i)
		if change, ok := schedulingFields[field.Name]; ok {
			s := renderSpec()
			change(&s)
			assert.Equalf(t, baseHash, profileHash(s),
				"%s is a scheduling field; changing it must not change status.hash (it would re-transcode the library)", field.Name)
			continue
		}
		for _, leaf := range leafPaths(field.Type, []int{i}, field.Name) {
			s := renderSpec()
			v := reflect.ValueOf(&s).Elem().FieldByIndex(leaf.index)
			require.Falsef(t, v.IsZero(), "renderSpec leaves %s zero; set it so changing it is meaningful", leaf.name)
			mutate(t, v, leaf.name)
			assert.NotEqualf(t, baseHash, profileHash(s),
				"changing %s does not change status.hash: worker.ProfileSpec (or pkg/transcode.ProfileSpec) drops it, "+
					"so editing it would never re-transcode anything", leaf.name)
			walked++
		}
	}
	// A floor, so a walk that silently stopped recursing cannot pass.
	require.GreaterOrEqual(t, walked, 45, "the leaf walk visited only %d fields", walked)

	for name := range schedulingFields {
		_, ok := specType.FieldByName(name)
		require.Truef(t, ok, "schedulingFields names %s, which TranscodeProfileSpec no longer has", name)
	}
}

// Unset and the CRD default are the same policy, so they are the same hash:
// a profile created by a Go client (nil) and one the apiserver defaulted
// must not be told apart, or every Go-created profile would re-transcode the
// moment kubectl touched it. renderSpec sets every one of these pointers to
// its CRD default, so clearing them all must leave the hash unchanged.
func TestStatusHashTreatsUnsetPolicyPointersAsTheirDefault(t *testing.T) {
	defaulted := renderSpec()
	unset := renderSpec()
	unset.Audio.DropCommentary = nil
	unset.Subtitles.CopyText, unset.Subtitles.CopyBitmap, unset.Subtitles.CopyAttachments = nil, nil, nil
	unset.Policy.SkipIfCompliant, unset.Policy.RemuxOnlyWhenVideoCompliant = nil, nil
	unset.Policy.MinDuration, unset.Policy.MaxOutputToSourcePercent = nil, nil
	unset.Policy.ReplaceSource, unset.Policy.RecycleBin = nil, nil
	unset.Verify.PacketCount = nil
	unset.Video.CRF.HDROffset = nil
	assert.Equal(t, profileHash(defaulted), profileHash(unset))

	noHDROffset := renderSpec()
	noHDROffset.Video.CRF.HDROffset = ptr.To[int32](0)
	assert.NotEqual(t, profileHash(defaulted), profileHash(noHDROffset), "an explicit 0 hdrOffset is no offset; it is not the -1 default")

	off := renderSpec()
	off.Policy.RecycleBin = ptr.To(false)
	assert.NotEqual(t, profileHash(defaulted), profileHash(off))

	zero := renderSpec()
	zero.Policy.MaxOutputToSourcePercent = ptr.To[int32](0)
	assert.NotEqual(t, profileHash(defaulted), profileHash(zero), "an explicit 0 disables the size check; it is not the default")
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

	// The probe's record of the file's own tag counts too: a file with only
	// that -- an earlier install's output a rescan found -- is already
	// transcoded. Either record counts: a replaceSource=false job records
	// its profile on a source whose own probe tag is another's.
	mf.Status.MediaInfo.TranscodeProfile = "hevc@newhash"
	assert.True(t, alreadyTranscoded(&mf, "hevc@newhash"), "the probe's tag counts")
	assert.True(t, alreadyTranscoded(&mf, "hevc@deadbeef"), "the incorporated transcode's tag still counts")
	assert.False(t, alreadyTranscoded(&mf, "hevc@third"))
	mf.Status.Transcode = nil
	assert.True(t, alreadyTranscoded(&mf, "hevc@newhash"), "the probe's tag alone counts")
	assert.False(t, alreadyTranscoded(&mf, "hevc@deadbeef"))
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
