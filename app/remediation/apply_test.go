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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestStatusApplyIsLossless: the one apply declares every field the status
// holds (§3.7 "The status is complete by construction"), so no branch,
// early return or planner failure can release one. Every field of
// MediaFileStatus is filled by reflection, so a field a later wave adds is
// covered the day it lands.
func TestStatusApplyIsLossless(t *testing.T) {
	s := fullStatus(t)
	ac, err := statusApply("heat-abc", "media", s)
	require.NoError(t, err)
	assert.Equal(t, "heat-abc", *ac.GetName())
	assert.Equal(t, "media", *ac.GetNamespace())
	want, err := json.Marshal(s)
	require.NoError(t, err)
	got, err := json.Marshal(ac.Status)
	require.NoError(t, err)
	var w, g map[string]any
	require.NoError(t, json.Unmarshal(want, &w))
	require.NoError(t, json.Unmarshal(got, &g))
	assert.Equal(t, w, g)
	for _, f := range reflect.VisibleFields(reflect.TypeFor[catalogv1alpha1.MediaFileStatus]()) {
		name := jsonName(f)
		assert.Contains(t, g, name, "statusApply dropped %s", name)
	}
}

// normalize makes "nothing changed" exact: nil and empty alike, lists sorted
// by their key, so a planner's re-render in another order applies nothing.
func TestNormalizeTreatsNilAndEmptyAlikeAndSortsLists(t *testing.T) {
	now := metav1.Now()
	a := &catalogv1alpha1.MediaFileStatus{
		Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: now},
			{Type: "Probed", Status: metav1.ConditionTrue, Reason: "Probed", LastTransitionTime: now},
		},
		Sidecars: []catalogv1alpha1.Sidecar{{Name: "b.en.srt", Language: "en"}, {Name: "a.fr.srt", Language: "fr"}},
		Subtitles: &catalogv1alpha1.SubtitlesStatus{
			Items:  []catalogv1alpha1.SubtitleItemStatus{{LangKey: "fr"}, {LangKey: "en"}},
			Wanted: []string{"fr", "en"},
		},
	}
	b := a.DeepCopy()
	b.Conditions[0], b.Conditions[1] = b.Conditions[1], b.Conditions[0]
	b.Sidecars[0], b.Sidecars[1] = b.Sidecars[1], b.Sidecars[0]
	b.Subtitles.Items[0], b.Subtitles.Items[1] = b.Subtitles.Items[1], b.Subtitles.Items[0]
	b.Subtitles.Wanted[0], b.Subtitles.Wanted[1] = b.Subtitles.Wanted[1], b.Subtitles.Wanted[0]
	assert.Equal(t, normalize(a), normalize(b))
	assert.Equal(t, "a.fr.srt", normalize(a).Sidecars[0].Name)
	assert.Equal(t, "Probed", normalize(a).Conditions[0].Type)
	assert.Equal(t, normalize(&catalogv1alpha1.MediaFileStatus{Sidecars: []catalogv1alpha1.Sidecar{}, Conditions: []metav1.Condition{}}),
		normalize(&catalogv1alpha1.MediaFileStatus{}))
	assert.Equal(t,
		normalize(&catalogv1alpha1.MediaFileStatus{Subtitles: &catalogv1alpha1.SubtitlesStatus{Items: []catalogv1alpha1.SubtitleItemStatus{}, Wanted: []string{}}}),
		normalize(&catalogv1alpha1.MediaFileStatus{Subtitles: &catalogv1alpha1.SubtitlesStatus{}}))
	assert.Len(t, a.Sidecars, 2, "normalize changed its argument")
	assert.Equal(t, "b.en.srt", a.Sidecars[0].Name, "normalize sorted its argument in place")
}

// fullStatus is a MediaFileStatus with every field set to a distinct
// non-zero value: strings "v-<json name>", numbers their field index plus
// one, booleans true, pointers allocated and filled two levels down, slices
// of one element, and ten conditions of the closed set (§2.8). It is
// remediationtest.FullStatus's twin: this package's internal tests cannot
// import remediationtest, which imports this package.
func fullStatus(t *testing.T) *catalogv1alpha1.MediaFileStatus {
	t.Helper()
	s := &catalogv1alpha1.MediaFileStatus{}
	fill(reflect.ValueOf(s).Elem(), 2)
	now := metav1.Now()
	s.Conditions = nil
	for _, typ := range []string{"Probed", "Ready", "NamingCurrent", "DeadLettered",
		catalogv1alpha1.MediaFileConditionProbePlannerError, catalogv1alpha1.MediaFileConditionNamingPlannerError,
		catalogv1alpha1.MediaFileConditionTranscodePlannerError, catalogv1alpha1.MediaFileConditionGraftPlannerError,
		catalogv1alpha1.MediaFileConditionSubtitlesPlannerError, catalogv1alpha1.MediaFileConditionMarkersPlannerError} {
		s.Conditions = append(s.Conditions, metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: "R", Message: typ, LastTransitionTime: now})
	}
	return s
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
