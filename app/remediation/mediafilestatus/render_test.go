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

package mediafilestatus_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mediafilestatus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// crdBounds is every bounded string under the MediaFile CRD's status, by
// the renderer's path syntax, conditions excepted.
func crdBounds(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile("../../../config/crd/bases/catalog.clustarr.io_mediafiles.yaml")
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	out := map[string]int{}
	var walk func(s apiextensionsv1.JSONSchemaProps, path string)
	walk = func(s apiextensionsv1.JSONSchemaProps, path string) {
		if path == "conditions" {
			return // metav1.Condition: the message is clamped to k8s.MaxConditionMessage; type and reason are planner constants
		}
		switch s.Type {
		case "string":
			if s.MaxLength != nil {
				out[path] = int(*s.MaxLength)
			}
		case "array":
			walk(*s.Items.Schema, path+"[]")
		case "object":
			for k, p := range s.Properties {
				child := k
				if path != "" {
					child = path + "." + k
				}
				walk(p, child)
			}
		}
	}
	require.Len(t, crd.Spec.Versions, 1)
	walk(crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"], "")
	return out
}

// draftWith is a status whose only string is val at path ("[]" a
// one-element list), built through JSON so every table path is reachable.
func draftWith(t *testing.T, path, val string) *catalogv1alpha1.MediaFileStatus {
	t.Helper()
	root := map[string]any{}
	cur := root
	parts := strings.Split(path, ".")
	for i, p := range parts {
		name, list := strings.CutSuffix(p, "[]")
		last := i == len(parts)-1
		switch {
		case last && list:
			cur[name] = []any{val}
		case last:
			cur[name] = val
		case list:
			next := map[string]any{}
			cur[name] = []any{next}
			cur = next
		default:
			next, ok := cur[name].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[name] = next
			}
			cur = next
		}
	}
	raw, err := json.Marshal(root)
	require.NoError(t, err)
	var st catalogv1alpha1.MediaFileStatus
	require.NoError(t, json.Unmarshal(raw, &st))
	return &st
}

// valueAt reads the string at path from st's JSON; false when it is gone.
func valueAt(t *testing.T, st catalogv1alpha1.MediaFileStatus, path string) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	var cur any
	require.NoError(t, json.Unmarshal(raw, &cur))
	for _, p := range strings.Split(path, ".") {
		name, list := strings.CutSuffix(p, "[]")
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[name]; !ok {
			return "", false
		}
		if list {
			l, ok := cur.([]any)
			if !ok || len(l) == 0 {
				return "", false
			}
			cur = l[0]
		}
	}
	s, ok := cur.(string)
	return s, ok && s != ""
}

// TestRenderClampsEveryBoundedString (loop spec §2.11.4): the renderer's
// table equals the CRD's maxLength set, and every bounded string, produced
// this pass over its cap with a U+0096 in it, renders within its cap with no
// unapplyable rune -- or, for a path, a file name or a language, is dropped
// and reported, never truncated (§2.11.1 rule 1).
func TestRenderClampsEveryBoundedString(t *testing.T) {
	bounds := mediafilestatus.Bounds()
	require.Equal(t, crdBounds(t), bounds, "the clamp table and the CRD disagree; a new bounded field needs its row")

	drops := map[string]bool{
		"sidecars[].name": true, "sidecars[].language": true,
		"naming.expectedPath": true, "subtitles.items[].name": true, "subtitles.items[].langKey": true,
		"subtitles.wanted[]": true, "transcode.result.outputPath": true,
		"transcode.joinedGraft.languages[]": true, "graft.languages[]": true,
	}
	for path := range drops {
		require.Contains(t, bounds, path, "a dropped field is in the table")
	}
	for path, limit := range bounds {
		val := "\u0096" + strings.Repeat("é", limit+1)
		out, dropped := mediafilestatus.Render(nil, draftWith(t, path, val))
		got, present := valueAt(t, out, path)
		if drops[path] {
			assert.False(t, present, "%s: a path, file name or language is dropped, never truncated", path)
			require.NotEmpty(t, dropped, path)
			assert.Equal(t, path, dropped[0].Field)
			assert.Equal(t, len(val), dropped[0].Bytes, path)
			continue
		}
		require.True(t, present, "%s: a clamped value stays", path)
		assert.LessOrEqual(t, len(got), limit, path)
		assert.True(t, utf8.ValidString(got), path)
		assert.False(t, k8s.HasUnapplyable(got), path)
		assert.True(t, strings.HasPrefix(got, "�"), "%s: the rune is replaced, not cut", path)
		assert.Empty(t, dropped, path)
	}

	// A file name within its bound but carrying an unapplyable rune is dropped too.
	out, dropped := mediafilestatus.Render(nil, draftWith(t, "sidecars[].name", "a\u0096.en.srt"))
	assert.Empty(t, out.Sidecars)
	require.Len(t, dropped, 1)
	assert.Equal(t, mediafilestatus.DropUnprintable, dropped[0].Reason)

	// One past its bound and printable, it is dropped as too long.
	out, dropped = mediafilestatus.Render(nil, draftWith(t, "naming.expectedPath", "/"+strings.Repeat("p", 4096)))
	assert.Empty(t, out.Naming.ExpectedPath)
	require.Len(t, dropped, 1)
	assert.Equal(t, mediafilestatus.DropTooLong, dropped[0].Reason)

	// A 40 KiB condition message renders within k8s.MaxConditionMessage.
	at := metav1.NewTime(time.Date(2026, 10, 6, 12, 0, 0, 999, time.UTC))
	out, _ = mediafilestatus.Render(nil, &catalogv1alpha1.MediaFileStatus{Conditions: []metav1.Condition{{
		Type: catalogv1alpha1.MediaFileConditionProbePlannerError, Status: metav1.ConditionTrue,
		Reason: catalogv1alpha1.MediaFileReasonPlannerPanic, Message: "\u0096" + strings.Repeat("x", 40<<10),
		LastTransitionTime: at,
	}}})
	require.Len(t, out.Conditions, 1)
	assert.LessOrEqual(t, len(out.Conditions[0].Message), k8s.MaxConditionMessage)
	assert.False(t, k8s.HasUnapplyable(out.Conditions[0].Message))
	assert.Zero(t, out.Conditions[0].LastTransitionTime.Nanosecond(), "times are whole seconds")
}

// TestRenderCarriesStoredValuesForward (§2.11.1 rule 5): a value carried
// from the stored status is sent as stored even over a cap release N
// added -- ratcheting admits it, and changing status.mediaInfo would reseed
// Plex -- except for an unapplyable rune, which no apply can carry.
func TestRenderCarriesStoredValuesForward(t *testing.T) {
	stored := &catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
		Audio: []commonv1.AudioStream{{Title: strings.Repeat("a", 300)}},
	}}
	out, _ := mediafilestatus.Render(stored, stored.DeepCopy())
	assert.Len(t, out.MediaInfo.Audio[0].Title, 300)

	draft := stored.DeepCopy()
	draft.MediaInfo.Audio[0].Title = strings.Repeat("b", 300)
	out, _ = mediafilestatus.Render(stored, draft)
	assert.Len(t, out.MediaInfo.Audio[0].Title, 256, "a value produced this pass is clamped")

	stored.MediaInfo.Audio[0].Title = "Commentary \u0096"
	out, _ = mediafilestatus.Render(stored, stored.DeepCopy())
	assert.Equal(t, "Commentary �", out.MediaInfo.Audio[0].Title)

	// A carried condition message is sent as stored too; a new one is clamped.
	long := strings.Repeat("m", 2*k8s.MaxConditionMessage)
	cond := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Old", Message: long}
	stored = &catalogv1alpha1.MediaFileStatus{Conditions: []metav1.Condition{cond}}
	out, _ = mediafilestatus.Render(stored, stored.DeepCopy())
	assert.Len(t, out.Conditions[0].Message, len(long))
	draft = stored.DeepCopy()
	draft.Conditions[0].Reason = "New"
	out, _ = mediafilestatus.Render(stored, draft)
	assert.Len(t, out.Conditions[0].Message, k8s.MaxConditionMessage)
}

// TestRenderSortsAndDedupes (§2.11.1 rule 4): lists the spec sorts are
// sorted, and a duplicate map-list key -- which the apiserver refuses whole
// -- is dropped.
func TestRenderSortsAndDedupes(t *testing.T) {
	out, dropped := mediafilestatus.Render(nil, &catalogv1alpha1.MediaFileStatus{
		Sidecars: []catalogv1alpha1.Sidecar{
			{Name: "b.en.srt", Language: "en"}, {Name: "a.en.srt", Language: "en"}, {Name: "a.en.srt", Language: "en", Forced: true},
		},
		Subtitles: &catalogv1alpha1.SubtitlesStatus{
			Phase: catalogv1alpha1.SubtitlesPhaseWanted, Wanted: []string{"fr", "en"},
			Items: []catalogv1alpha1.SubtitleItemStatus{{LangKey: "fr"}, {LangKey: "en"}},
		},
		Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Probed"},
			{Type: "Probed", Status: metav1.ConditionTrue, Reason: "Probed"},
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Again"},
		},
	})
	require.Len(t, out.Sidecars, 2)
	assert.Equal(t, "a.en.srt", out.Sidecars[0].Name)
	assert.False(t, out.Sidecars[0].Forced, "the first sidecar of a name is kept")
	assert.Equal(t, "b.en.srt", out.Sidecars[1].Name)
	assert.Equal(t, []string{"en", "fr"}, out.Subtitles.Wanted)
	assert.Equal(t, "en", out.Subtitles.Items[0].LangKey)
	assert.Equal(t, "fr", out.Subtitles.Items[1].LangKey)
	require.Len(t, out.Conditions, 2)
	assert.Equal(t, "Probed", out.Conditions[0].Type)
	assert.Equal(t, "Ready", out.Conditions[1].Type)
	assert.Equal(t, metav1.ConditionTrue, out.Conditions[1].Status, "the first condition of a type is kept")
	require.Len(t, dropped, 2, "one duplicate sidecar name, one duplicate condition type")
	fields := []string{dropped[0].Field, dropped[1].Field}
	assert.ElementsMatch(t, []string{"sidecars[].name", "conditions[].type"}, fields)
	for _, d := range dropped {
		assert.Equal(t, mediafilestatus.DropDuplicate, d.Reason)
	}
}

// TestRenderCapsTheDroppedSubtitles (loop spec §2.5, D44): past eight names
// the first seven stay and the eighth reads "and N more", so the apiserver
// never refuses status.transcode.plan.dropped at its MaxItems; each name is
// clamped to 320 bytes on a rune boundary, never dropped.
func TestRenderCapsTheDroppedSubtitles(t *testing.T) {
	names := make([]string, 11)
	for i := range names {
		names[i] = fmt.Sprintf("subtitle %d (dvb_teletext): a codec the MP4 standard does not carry", i)
	}
	names[0] = strings.Repeat("é", 200) // 400 bytes
	plan := func(d []string) *catalogv1alpha1.MediaFileStatus {
		return &catalogv1alpha1.MediaFileStatus{Transcode: &catalogv1alpha1.TranscodeState{
			Phase: catalogv1alpha1.TranscodePhasePlanned,
			Plan:  &catalogv1alpha1.TranscodePlan{Mode: catalogv1alpha1.TranscodeModeTranscode, PlanHash: "p1", Dropped: d},
		}}
	}
	out, dropped := mediafilestatus.Render(nil, plan(names))
	assert.Empty(t, dropped)
	got := out.Transcode.Plan.Dropped
	require.Len(t, got, 8)
	assert.Equal(t, names[1:7], got[1:7])
	assert.Equal(t, "and 4 more", got[7])
	assert.LessOrEqual(t, len(got[0]), 320)
	assert.NotEmpty(t, got[0])
	assert.True(t, utf8.ValidString(got[0]))
	assert.Len(t, names, 11, "the draft is not modified")

	eight := slices.Clone(names[1:9])
	out, _ = mediafilestatus.Render(nil, plan(eight))
	assert.Equal(t, eight, out.Transcode.Plan.Dropped, "eight names fit as they are")
}
