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

package mediafilestatus

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// EventReasonEntryDropped is the reason of the Warning Event the loop
// records on the MediaFile for every Drop.
const EventReasonEntryDropped = "StatusEntryDropped"

// Drop reasons.
const (
	DropTooLong     = "TooLong"
	DropUnprintable = "Unprintable"
	DropDuplicate   = "Duplicate"
)

// Drop is one value Render removed rather than truncate or rewrite.
type Drop struct {
	// Field is the JSON path under status, "[]" marking a list element.
	Field string
	// Reason is DropTooLong, DropUnprintable or DropDuplicate.
	Reason string
	// Bytes is the dropped value's length.
	Bytes int
}

// action is what a bounded string does past its bound.
type action uint8

const (
	keep        action = iota // clamp on a rune boundary (rule 1)
	dropField                 // a file name or path: clear the field
	dropElement               // a sidecar or a language: remove the list element
	dropBlock                 // an output path: remove the enclosing optional block
)

type bound struct {
	max  int
	drop action
}

// bounds is the clamp table: every bounded string under status, by JSON
// path, conditions excepted (conditions renders those).
var bounds = map[string]bound{
	"probeHash": {128, keep},
	"graftTag":  {64, keep},

	"mediaInfo.container":             {64, keep},
	"mediaInfo.videoCodec":            {64, keep},
	"mediaInfo.videoProfile":          {64, keep},
	"mediaInfo.pixelFormat":           {64, keep},
	"mediaInfo.transcodeProfile":      {320, keep},
	"mediaInfo.videoEncoder":          {256, keep},
	"mediaInfo.audio[].codec":         {64, keep},
	"mediaInfo.audio[].profile":       {64, keep},
	"mediaInfo.audio[].language":      {64, keep},
	"mediaInfo.audio[].title":         {256, keep},
	"mediaInfo.audio[].channelLayout": {32, keep},
	"mediaInfo.subtitles[].codec":     {64, keep},
	"mediaInfo.subtitles[].language":  {64, keep},
	"mediaInfo.subtitles[].title":     {256, keep},
	"mediaInfo.chapterList[].title":   {128, keep},

	"sidecars[].path":     {4096, dropElement},
	"sidecars[].name":     {255, dropElement},
	"sidecars[].language": {35, dropElement},

	"naming.expectedPath": {4096, dropField},
	"naming.quality.name": {64, keep},

	"markers.forProbeHash":          {128, keep},
	"markers.message":               {512, keep},
	"markers.analysis.forProbeHash": {128, keep},
	"markers.analysis.message":      {512, keep},

	"subtitles.reason":             {64, keep},
	"subtitles.message":            {1024, keep},
	"subtitles.profile":            {253, keep},
	"subtitles.probeHash":          {128, keep},
	"subtitles.wanted[]":           {64, dropElement},
	"subtitles.items[].langKey":    {64, dropElement},
	"subtitles.items[].provider":   {253, keep},
	"subtitles.items[].subtitleID": {256, keep},
	"subtitles.items[].name":       {255, dropField},
	"subtitles.items[].lastError":  {512, keep},

	"transcode.reason":                   {64, keep},
	"transcode.message":                  {1024, keep},
	"transcode.profile":                  {253, keep},
	"transcode.profileHash":              {64, keep},
	"transcode.probeHash":                {128, keep},
	"transcode.plan.encoder":             {64, keep},
	"transcode.plan.hdrMode":             {64, keep},
	"transcode.plan.planHash":            {64, keep},
	"transcode.plan.dropped[]":           {320, keep},
	"transcode.pool":                     {253, keep},
	"transcode.fallbackReason":           {256, keep},
	"transcode.workerPod":                {253, keep},
	"transcode.result.outputPath":        {4096, dropBlock},
	"transcode.stderrTail":               {1024, keep},
	"transcode.profileTag":               {320, keep},
	"transcode.joinedGraft.donorRelease": {512, keep},
	"transcode.joinedGraft.languages[]":  {35, dropElement},
	"transcode.joinedGraft.probeHash":    {128, keep},
	"transcode.jobRef":                   {253, keep},

	"graft.reason":       {64, keep},
	"graft.message":      {1024, keep},
	"graft.donorRelease": {512, keep},
	"graft.languages[]":  {35, dropElement},
	"graft.probeHash":    {128, keep},
	"graft.jobName":      {253, keep},
	"graft.rateName":     {32, keep},
	"graft.tag":          {64, keep},

	"handledNonces.subtitleSearch":  {63, keep},
	"handledNonces.transcodeRetry":  {63, keep},
	"handledNonces.transcodeCancel": {63, keep},
}

// maxDropped is status.transcode.plan.dropped's MaxItems (loop spec §2.5).
const maxDropped = 8

// capDropped keeps a planner's dropped-subtitle names within MaxItems: past
// eight, the first seven stay and the eighth reads "and N more" (§2.5, D44),
// so the apiserver never refuses the list; the bounds table then clamps
// each name to 320 bytes on a rune boundary.
func capDropped(st *catalogv1alpha1.MediaFileStatus) {
	if st.Transcode == nil || st.Transcode.Plan == nil || len(st.Transcode.Plan.Dropped) <= maxDropped {
		return
	}
	d := st.Transcode.Plan.Dropped
	st.Transcode.Plan.Dropped = append(slices.Clone(d[:maxDropped-1]), fmt.Sprintf("and %d more", len(d)-(maxDropped-1)))
}

// Bounds returns the clamp table, JSON path to MaxLength in bytes.
func Bounds() map[string]int {
	out := make(map[string]int, len(bounds))
	for p, b := range bounds {
		out[p] = b.max
	}
	return out
}

// Render returns draft made safe to apply, and every value it dropped.
// stored is the status the planners read (nil for a file never written).
// A top-level field of draft equal to stored's is carried forward: its
// strings are sent as stored, over a cap release N added or not (rule 5;
// ratcheting admits an unchanged value, and it is judged per top-level
// field because ratcheting does not correlate an atomic list's items), and
// only an Unapplyable rune in one is replaced. Every other string is
// sanitised and bounded by the table. Duplicate sidecar paths and condition
// types are dropped, the lists the spec sorts are sorted, and times are cut
// to whole seconds.
func Render(stored, draft *catalogv1alpha1.MediaFileStatus) (catalogv1alpha1.MediaFileStatus, []Drop) {
	var out, prev catalogv1alpha1.MediaFileStatus
	if draft != nil {
		draft.DeepCopyInto(&out)
	}
	if stored != nil {
		prev = *stored
	}
	capDropped(&out)
	r := &renderer{}
	ov, pv := reflect.ValueOf(&out).Elem(), reflect.ValueOf(&prev).Elem()
	for i := range ov.NumField() {
		name := jsonName(ov.Type().Field(i))
		if name == "" || name == "conditions" {
			continue
		}
		carried := equality.Semantic.DeepEqual(ov.Field(i).Interface(), pv.Field(i).Interface())
		r.walk(ov.Field(i), name, carried)
	}
	out.Conditions = r.conditions(out.Conditions, prev.Conditions)
	out.Sidecars = r.uniqueSidecars(out.Sidecars)
	sortLists(&out)
	return out, r.drops
}

type renderer struct{ drops []Drop }

var timeType = reflect.TypeFor[metav1.Time]()

// walk renders v, found at path, and returns what its parent must do.
func (r *renderer) walk(v reflect.Value, path string, carried bool) action {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return keep
		}
		switch a := r.walk(v.Elem(), path, carried); a {
		case dropBlock:
			v.Set(reflect.Zero(v.Type()))
		case dropElement:
			return a
		}
		return keep
	case reflect.Struct:
		if v.Type() == timeType {
			if t := v.Interface().(metav1.Time); !t.IsZero() {
				v.Set(reflect.ValueOf(t.Rfc3339Copy()))
			}
			return keep
		}
		for i := range v.NumField() {
			name := jsonName(v.Type().Field(i))
			if name == "" {
				continue
			}
			if a := r.walk(v.Field(i), path+"."+name, carried); a != keep {
				return a
			}
		}
		return keep
	case reflect.Slice:
		if v.IsNil() {
			return keep
		}
		kept := reflect.MakeSlice(v.Type(), 0, v.Len())
		for i := range v.Len() {
			switch a := r.walk(v.Index(i), path+"[]", carried); a {
			case dropElement:
				continue
			case dropBlock:
				return a
			}
			kept = reflect.Append(kept, v.Index(i))
		}
		v.Set(kept)
		return keep
	case reflect.String:
		return r.text(v, path, carried)
	default:
		return keep
	}
}

// text renders one string: a path, file name or language past its bound or
// carrying an Unapplyable rune is dropped (rules 1 and 3; never rewritten
// into a name that does not exist); any other string is sanitised and, when
// produced this pass, clamped.
func (r *renderer) text(v reflect.Value, path string, carried bool) action {
	s := v.String()
	b, bounded := bounds[path]
	if bounded && b.drop != keep {
		reason := ""
		switch {
		case k8s.HasUnapplyable(s):
			reason = DropUnprintable
		case len(s) > b.max && !carried:
			reason = DropTooLong
		}
		if reason == "" {
			return keep
		}
		r.drops = append(r.drops, Drop{Field: path, Reason: reason, Bytes: len(s)})
		if b.drop == dropField {
			v.SetString("")
			return keep
		}
		return b.drop
	}
	out := k8s.SanitizeText(s)
	if bounded && !carried {
		out = k8s.ClampText(out, b.max)
	}
	if out != s {
		v.SetString(out)
	}
	return keep
}

// conditions renders the conditions (rule 2): one per type, the message
// sanitised and, unless carried unchanged, clamped to k8s.MaxConditionMessage.
func (r *renderer) conditions(draft, stored []metav1.Condition) []metav1.Condition {
	if draft == nil {
		return nil
	}
	out := make([]metav1.Condition, 0, len(draft))
	seen := make(map[string]bool, len(draft))
	for _, c := range draft {
		if seen[c.Type] {
			r.drops = append(r.drops, Drop{Field: "conditions[].type", Reason: DropDuplicate, Bytes: len(c.Type)})
			continue
		}
		seen[c.Type] = true
		old := k8s.FindCondition(stored, c.Type)
		carried := old != nil && equality.Semantic.DeepEqual(*old, c)
		c.Reason = k8s.SanitizeText(c.Reason)
		c.Message = k8s.SanitizeText(c.Message)
		if !carried {
			c.Message = k8s.ClampText(c.Message, k8s.MaxConditionMessage)
		}
		if !c.LastTransitionTime.IsZero() {
			c.LastTransitionTime = c.LastTransitionTime.Rfc3339Copy()
		}
		out = append(out, c)
	}
	return out
}

// uniqueSidecars keeps the first sidecar per path, the list's map key.
func (r *renderer) uniqueSidecars(in []catalogv1alpha1.Sidecar) []catalogv1alpha1.Sidecar {
	if in == nil {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s.Path] {
			r.drops = append(r.drops, Drop{Field: "sidecars[].path", Reason: DropDuplicate, Bytes: len(s.Path)})
			continue
		}
		seen[s.Path] = true
		out = append(out, s)
	}
	return out
}

// sortLists sorts what the spec sorts (rule 4): conditions by type,
// sidecars by name then path (§2.7), subtitles' wanted and items by langKey
// (§2.4). Stream lists and segments keep the order their producer gave.
func sortLists(st *catalogv1alpha1.MediaFileStatus) {
	sort.SliceStable(st.Conditions, func(i, j int) bool { return st.Conditions[i].Type < st.Conditions[j].Type })
	sort.SliceStable(st.Sidecars, func(i, j int) bool {
		a, b := st.Sidecars[i], st.Sidecars[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Path < b.Path
	})
	if s := st.Subtitles; s != nil {
		sort.Strings(s.Wanted)
		sort.SliceStable(s.Items, func(i, j int) bool { return s.Items[i].LangKey < s.Items[j].LangKey })
	}
}

// jsonName is f's JSON name, "" for a field the API does not serialise.
func jsonName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}
