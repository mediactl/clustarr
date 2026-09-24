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

package crdcheck

import (
	"go/ast"
	"strconv"
	"strings"
	"testing"

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// statusList is one list-typed field reachable from a CRD's status.
type statusList struct {
	key      string // "<group>.<Type>.<Field>"
	pos      string
	goType   string
	via      string // the status root it was first reached from
	maxItems string // "" when uncapped
}

// statusRoots returns every status type to walk: the type of each root
// kind's Status field, plus any struct whose name ends in Status, so a status
// type reachable only through another kind's embedding is not missed.
func (m *apiModel) statusRoots() []*apiType {
	seen := map[*apiType]bool{}
	var out []*apiType
	add := func(at *apiType) {
		if at != nil && !seen[at] {
			seen[at] = true
			out = append(out, at)
		}
	}
	for _, at := range m.sortedTypes() {
		if _, isRoot := markerValue(at.markers, "kubebuilder:object:root"); isRoot {
			for _, f := range structFields(at) {
				if fieldName(f, at) == "Status" {
					add(m.resolveLocal(unpointer(f.Type), at))
				}
			}
		}
	}
	for _, at := range m.sortedTypes() {
		if _, ok := at.spec.Type.(*ast.StructType); ok && strings.HasSuffix(at.name, "Status") {
			add(at)
		}
	}
	return out
}

func unpointer(expr ast.Expr) ast.Expr {
	if s, ok := expr.(*ast.StarExpr); ok {
		return s.X
	}
	return expr
}

// statusLists walks every status root, through pointers, lists, maps and
// nested and embedded structs across api/ packages, and returns each
// list-typed field once, with its MaxItems when it has one. externals names
// the out-of-tree types it could not look inside.
func (m *apiModel) statusLists() (lists []statusList, externals map[string]string) {
	externals = map[string]string{}
	visited := map[*apiType]bool{}
	var walkType func(at *apiType, root string)
	var walkExpr func(expr ast.Expr, from *apiType, root, where string)

	walkExpr = func(expr ast.Expr, from *apiType, root, where string) {
		switch e := expr.(type) {
		case *ast.StarExpr:
			walkExpr(e.X, from, root, where)
			return
		case *ast.ArrayType:
			walkExpr(e.Elt, from, root, where)
			return
		case *ast.MapType:
			walkExpr(e.Value, from, root, where)
			return
		case *ast.Ident:
			if _, basic := basicKinds[e.Name]; basic {
				return
			}
		}
		if at := m.resolveLocal(expr, from); at != nil {
			walkType(at, root)
			return
		}
		if name := externalName(expr, from); name != "" {
			if _, ok := externals[name]; !ok {
				externals[name] = where
			}
		}
	}

	walkType = func(at *apiType, root string) {
		if visited[at] {
			return
		}
		visited[at] = true
		for _, f := range structFields(at) {
			if parseJSONTag(f).skip {
				continue
			}
			key := at.qualified() + "." + fieldName(f, at)
			if arr, ok := unpointer(f.Type).(*ast.ArrayType); ok && !isByteSlice(arr) {
				markers := append(markersOf(f.Doc), markersOf(f.Comment)...)
				maxItems, _ := markerValue(markers, "kubebuilder:validation:MaxItems")
				lists = append(lists, statusList{
					key:      key,
					pos:      m.position(f.Pos()),
					goType:   exprString(f.Type, at),
					via:      root,
					maxItems: maxItems,
				})
			}
			walkExpr(f.Type, at, root, key)
		}
		// A named non-struct type (type Foo []Bar) carries its list on the
		// type, not a field.
		if _, isStruct := at.spec.Type.(*ast.StructType); !isStruct {
			if arr, ok := at.spec.Type.(*ast.ArrayType); ok && !isByteSlice(arr) {
				maxItems, _ := markerValue(at.markers, "kubebuilder:validation:MaxItems")
				lists = append(lists, statusList{
					key:      at.qualified(),
					pos:      m.position(at.spec.Pos()),
					goType:   exprString(at.spec.Type, at),
					via:      root,
					maxItems: maxItems,
				})
			}
			walkExpr(at.spec.Type, at, root, at.qualified())
		}
	}

	for _, root := range m.statusRoots() {
		walkType(root, root.qualified())
	}
	return lists, externals
}

// isByteSlice reports a []byte, which JSON renders as a base64 string, not a
// list.
func isByteSlice(arr *ast.ArrayType) bool {
	id, ok := arr.Elt.(*ast.Ident)
	return ok && (id.Name == "byte" || id.Name == "uint8")
}

// listFreeExternals are the out-of-tree types a status may carry that the
// walker cannot look inside, each checked by hand to hold no list. A new one
// fails TestEveryStatusListIsCapped until someone looks: corev1's
// ResourceRequirements, for one, carries a Claims list no marker here can cap.
var listFreeExternals = map[string]bool{
	"k8s.io/apimachinery/pkg/apis/meta/v1.Time":      true,
	"k8s.io/apimachinery/pkg/apis/meta/v1.Condition": true,
	"k8s.io/apimachinery/pkg/apis/meta/v1.Duration":  true,
	"k8s.io/apimachinery/pkg/api/resource.Quantity":  true,
}

// TestEveryStatusListIsCapped is the CLAUDE.md invariant "cap every status
// list with +kubebuilder:validation:MaxItems -- unbounded lists in status are
// how operators melt etcd", held mechanically. Both transcode Conditions gaps
// were found by hand; when G4-0 walked for the rest there were twelve more
// (eight Conditions, four lists in ReleaseInfo). It follows every
// status type through pointers, lists, maps and nested and embedded structs,
// across api/ packages, and fails on any list without a MaxItems.
//
// A cap is half the contract: a writer that exceeds it has its WHOLE apply
// rejected. Whoever adds a cap here owns truncating to it where the list is
// written (pkg/download.MaxStatusFiles, app/import/worker/fileimport's caps,
// pkg/mediainfo.MaxStreamsPerKind).
func TestEveryStatusListIsCapped(t *testing.T) {
	m := loadAPIModel(t)
	lists, externals := m.statusLists()
	// A floor, so a walk that silently stopped recursing cannot pass.
	if len(lists) < 60 {
		t.Fatalf("the walker reached only %d lists from status types; it has stopped recursing", len(lists))
	}
	for _, l := range lists {
		if l.maxItems == "" {
			t.Errorf("%s (%s): %s is reachable from %s but has no +kubebuilder:validation:MaxItems. "+
				"Cap it at a value its domain justifies, and truncate to it wherever it is written",
				l.key, l.pos, l.goType, l.via)
		}
	}
	for name, where := range externals {
		if !listFreeExternals[name] {
			t.Errorf("%s reaches %s, a type outside api/ this walker cannot look inside. "+
				"Check it holds no list (or cap what it holds some other way), then add it to listFreeExternals",
				where, name)
		}
	}
}

// TestWriterCapsMatchTheirMarkers holds the exported writer-side caps to the
// MaxItems they restate. A writer truncating to a stale constant is as broken
// as one not truncating at all: past the marker, the apply is rejected whole.
func TestWriterCapsMatchTheirMarkers(t *testing.T) {
	m := loadAPIModel(t)
	lists, _ := m.statusLists()
	maxItems := map[string]string{}
	for _, l := range lists {
		maxItems[l.key] = l.maxItems
	}
	for key, want := range map[string]int{
		"download.DownloadStatus.Files": download.MaxStatusFiles,
		"common.MediaInfo.Audio":        mediainfo.MaxStreamsPerKind,
		"common.MediaInfo.Subtitles":    mediainfo.MaxStreamsPerKind,
	} {
		got, ok := maxItems[key]
		if !ok {
			t.Errorf("%s is no longer reachable from any status type; update this table", key)
			continue
		}
		if got != strconv.Itoa(want) {
			t.Errorf("%s: MaxItems=%s, but its writer truncates at %d", key, got, want)
		}
	}
}

// TestStatusListWalkerReachesNestedLists pins the walker on the shapes it
// must see through, so the guard above is not trusted only on what api/
// holds today.
func TestStatusListWalkerReachesNestedLists(t *testing.T) {
	m := modelFromSource(t, `package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type Thing struct {
	Spec   ThingSpec   `+"`json:\"spec\"`"+`
	Status ThingState  `+"`json:\"status\"`"+`
}

type ThingSpec struct {
	Unrelated []string `+"`json:\"unrelated\"`"+`
}

// Not named *Status: reachable only through Thing's Status field.
type ThingState struct {
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `+"`json:\"conditions\"`"+`
	Top        []string           `+"`json:\"top\"`"+`
	Ptr        *Inner             `+"`json:\"ptr\"`"+`
	// +kubebuilder:validation:MaxItems=4
	Items   []Item           `+"`json:\"items\"`"+`
	ByKey   map[string]Inner `+"`json:\"byKey\"`"+`
	Named   Names            `+"`json:\"named\"`"+`
	Inner   `+"`json:\",inline\"`"+`
	Blob    []byte           `+"`json:\"blob\"`"+`
}

type Inner struct {
	Deep []int32 `+"`json:\"deep\"`"+`
}

type Item struct {
	Sub []string `+"`json:\"sub\"`"+`
}

// +kubebuilder:validation:MaxItems=3
type Names []string
`)
	lists, _ := m.statusLists()
	got := map[string]string{}
	for _, l := range lists {
		got[l.key] = l.maxItems
	}
	want := map[string]string{
		"synthetic.ThingState.Conditions": "8",
		"synthetic.ThingState.Top":        "",
		"synthetic.ThingState.Items":      "4",
		"synthetic.Inner.Deep":            "",
		"synthetic.Item.Sub":              "",
		"synthetic.Names":                 "3",
	}
	for key, max := range want {
		have, ok := got[key]
		if !ok {
			t.Errorf("%s was not reached", key)
			continue
		}
		if have != max {
			t.Errorf("%s: MaxItems %q, want %q", key, have, max)
		}
	}
	if _, ok := got["synthetic.ThingSpec.Unrelated"]; ok {
		t.Error("a spec list was walked as if it were status")
	}
	if _, ok := got["synthetic.ThingState.Blob"]; ok {
		t.Error("a []byte, which JSON renders as a string, was walked as a list")
	}
}
