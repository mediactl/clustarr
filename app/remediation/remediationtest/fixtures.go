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

package remediationtest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// WebDL720 is a WEBDL-720p quality.
var WebDL720 = commonv1.Quality{Name: "WEBDL-720p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution720p, Modifier: commonv1.ModifierNone}

// MustNamespace creates ns, or finds it.
func MustNamespace(t testing.TB, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
}

// ImportarrCreatesMediaFile stands in for the import domain's materialisation
// of a placed file: ImportarrCreatesMediaFileFor a Movie "inception".
func ImportarrCreatesMediaFile(t testing.TB, ctx context.Context, c client.Client, ns, name, path string, size int64, modTime time.Time, q commonv1.Quality) {
	t.Helper()
	ImportarrCreatesMediaFileFor(t, ctx, c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, path, size, modTime, q)
}

// ImportarrCreatesMediaFileFor applies the full MediaFileSpec under
// mediafilespec.FieldManager, the constant import's own writer uses, so the
// split this proves is production's.
func ImportarrCreatesMediaFileFor(t testing.TB, ctx context.Context, c client.Client, ns, name string, mediaRef commonv1.MediaRef, path string, size int64, modTime time.Time, q commonv1.Quality) {
	t.Helper()
	ac := catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(mediaRef).
			WithPath(path).
			WithSizeBytes(size).
			WithModTime(metav1.NewTime(modTime)).
			WithQuality(q).
			WithRevision(commonv1.Revision{Version: 1}).
			WithReleaseType(commonv1.ReleaseTypeSingle).
			WithFormatScore(40).
			WithProfileHash("profile-hash-abc").
			WithOriginal(true),
	)
	if _, err := k8s.Apply(ctx, c, mediafilespec.FieldManager, ac); err != nil {
		t.Fatalf("simulate the import create: %v", err)
	}
}

// MustQualityProfile creates a two-tier video QualityProfile.
func MustQualityProfile(t testing.TB, ctx context.Context, c client.Client, name string) {
	t.Helper()
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers: []catalogv1alpha1.Tier{
				{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
				{Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p"}},
			},
			Cutoff: "Bluray-1080p",
		},
	}
	if err := c.Create(ctx, qp); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create QualityProfile: %v", err)
	}
}

// MustMovie creates a Movie.
func MustMovie(t testing.TB, ctx context.Context, c client.Client, ns, name, qualityProfile string) {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID:            27205,
			QualityProfileRef: qualityProfile,
			RootFolderRef:     "movies",
		},
	}
	if err := c.Create(ctx, m); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create Movie: %v", err)
	}
}

// MustSeries creates a Series.
func MustSeries(t testing.TB, ctx context.Context, c client.Client, ns, name, qualityProfile string) {
	t.Helper()
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID:            153021,
			QualityProfileRef: qualityProfile,
			RootFolderRef:     "tv",
		},
	}
	if err := c.Create(ctx, s); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create Series: %v", err)
	}
}

// MustEpisode creates an Episode of seriesRef.
func MustEpisode(t testing.TB, ctx context.Context, c client.Client, ns, name, seriesRef string, season, episode int32) {
	t.Helper()
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.EpisodeSpec{
			SeriesRef:     seriesRef,
			SeasonNumber:  season,
			EpisodeNumber: episode,
		},
	}
	if err := c.Create(ctx, ep); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create Episode: %v", err)
	}
}

// MustTranscodeProfile creates the cluster-scoped TranscodeProfile name with
// status.hash set as the transcode domain's own controller would.
func MustTranscodeProfile(t testing.TB, ctx context.Context, c client.Client, name, hash string) {
	t.Helper()
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Create(ctx, tp); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create TranscodeProfile: %v", err)
	}
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerTranscode,
		transcodeac.TranscodeProfile(name).WithStatus(transcodeac.TranscodeProfileStatus().WithHash(hash)),
	); err != nil {
		t.Fatalf("set TranscodeProfile status: %v", err)
	}
}

// WriteFile writes contents to dir/name and returns the path.
func WriteFile(t testing.TB, dir, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// FakeProbe is a probe of any file that exists: an h264 1080p mkv with one
// stereo AAC track. A missing file fails as the real prober does.
func FakeProbe(_ context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil, err
	}
	return &commonv1.MediaInfo{
		Container:     "mkv",
		VideoCodec:    "h264",
		Width:         1920,
		Height:        1080,
		RuntimeMillis: 1000,
		Audio:         []commonv1.AudioStream{{Codec: "aac", Channels: 2, ChannelLayout: "stereo"}},
	}, nil, nil
}

// ContainerProbe is FakeProbe reporting container.
func ContainerProbe(container string) func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return func(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		mi, raw, err := FakeProbe(ctx, path)
		if mi != nil {
			mi.Container = container
		}
		return mi, raw, err
	}
}

// ManagesField reports whether entries hold a (manager, subresource) entry.
func ManagesField(entries []metav1.ManagedFieldsEntry, manager, subresource string) bool {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource {
			return true
		}
	}
	return false
}

// FieldManagerNames is each managedFields entry as "manager/subresource".
func FieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Manager+"/"+e.Subresource)
	}
	return out
}

// ManagedFieldPaths decodes the (manager, subresource) entry's FieldsV1;
// nil when there is no such entry.
func ManagedFieldPaths(entries []metav1.ManagedFieldsEntry, manager, subresource string) map[string]any {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource && e.FieldsV1 != nil {
			var m map[string]any
			if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

// SpecFieldNames is the bare field names a ManagedFieldPaths result's
// "f:spec" claims.
func SpecFieldNames(fields map[string]any) map[string]bool {
	out := map[string]bool{}
	spec, ok := fields["f:spec"].(map[string]any)
	if !ok {
		return out
	}
	for k := range spec {
		if k == "." {
			continue
		}
		out[strings.TrimPrefix(k, "f:")] = true
	}
	return out
}

// Goroutines is the process's goroutine count.
func Goroutines() int { return runtime.NumGoroutine() }

// FullStatus is a MediaFileStatus with every field set to a distinct
// non-zero value: strings "v-<json name>", numbers their field index plus
// one, booleans true, pointers allocated and filled two levels down, slices
// of one element, and the closed set of conditions (§2.8). A field a later
// wave adds is filled the day it lands.
func FullStatus(t testing.TB) *catalogv1alpha1.MediaFileStatus {
	t.Helper()
	s := &catalogv1alpha1.MediaFileStatus{}
	fill(reflect.ValueOf(s).Elem(), 2)
	now := metav1.Now()
	s.Conditions = nil
	for _, typ := range ConditionTypes() {
		s.Conditions = append(s.Conditions, metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: "R", Message: typ, LastTransitionTime: now})
	}
	return s
}

// ConditionTypes is the MediaFile's closed set of condition types.
func ConditionTypes() []string {
	return []string{
		catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileConditionReady,
		catalogv1alpha1.ConditionNamingCurrent, k8s.ConditionDeadLettered,
		catalogv1alpha1.MediaFileConditionProbePlannerError, catalogv1alpha1.MediaFileConditionNamingPlannerError,
		catalogv1alpha1.MediaFileConditionTranscodePlannerError, catalogv1alpha1.MediaFileConditionGraftPlannerError,
		catalogv1alpha1.MediaFileConditionSubtitlesPlannerError, catalogv1alpha1.MediaFileConditionMarkersPlannerError,
	}
}

// StatusKeys are s's set fields: each top-level JSON key but conditions and
// handledNonces, each handledNonces leaf as "handledNonces.<leaf>", and each
// condition as "condition:<type>", sorted.
func StatusKeys(t testing.TB, s *catalogv1alpha1.MediaFileStatus) []string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	var out []string
	for k, v := range m {
		switch k {
		case "conditions":
		case "handledNonces":
			if leaves, ok := v.(map[string]any); ok {
				for leaf := range leaves {
					out = append(out, "handledNonces."+leaf)
				}
			}
		default:
			out = append(out, k)
		}
	}
	for _, c := range s.Conditions {
		out = append(out, "condition:"+c.Type)
	}
	slices.Sort(out)
	return out
}

func fill(v reflect.Value, depth int) {
	for i := range v.NumField() {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("v-" + jsonName(v.Type().Field(i)))
		case reflect.Int, reflect.Int32, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Uint, reflect.Uint32, reflect.Uint64:
			f.SetUint(uint64(i + 1))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Pointer:
			if depth == 0 {
				continue
			}
			p := reflect.New(f.Type().Elem())
			switch {
			case p.Elem().Type() == reflect.TypeFor[metav1.Time]():
				p.Elem().Set(reflect.ValueOf(metav1.Now()))
			case p.Elem().Kind() == reflect.Struct:
				fill(p.Elem(), depth-1)
			case p.Elem().Kind() == reflect.String:
				p.Elem().SetString("v-" + jsonName(v.Type().Field(i)))
			case p.Elem().Kind() == reflect.Bool:
				p.Elem().SetBool(true)
			case p.Elem().CanInt():
				p.Elem().SetInt(int64(i + 1))
			}
			f.Set(p)
		case reflect.Slice:
			e := reflect.New(f.Type().Elem()).Elem()
			switch {
			case e.Kind() == reflect.Struct && depth > 0:
				fill(e, depth-1)
			case e.Kind() == reflect.String:
				e.SetString("v-" + jsonName(v.Type().Field(i)))
			}
			f.Set(reflect.Append(reflect.MakeSlice(f.Type(), 0, 1), e))
		case reflect.Map:
			if f.Type().Key().Kind() == reflect.String && f.Type().Elem().Kind() == reflect.String {
				m := reflect.MakeMap(f.Type())
				m.SetMapIndex(reflect.ValueOf("k").Convert(f.Type().Key()), reflect.ValueOf("v-"+jsonName(v.Type().Field(i))).Convert(f.Type().Elem()))
				f.Set(m)
			}
		case reflect.Struct:
			if f.Type() == reflect.TypeFor[metav1.Time]() {
				f.Set(reflect.ValueOf(metav1.Now()))
			} else if depth > 0 {
				fill(f, depth-1)
			}
		}
	}
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}
