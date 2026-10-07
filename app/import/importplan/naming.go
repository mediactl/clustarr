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

package importplan

import (
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
)

// DonorDir is the folder under a RootFolder that holds audio donors, one
// folder per item UID (anime dual-audio spec §6.2).
const DonorDir = fsops.ClustarrDir + "/donors"

// MediaFileName is the MediaFile a file placed at dest for target is
// materialised as: k8s.ChildName(target, "mediafile", dest).
func MediaFileName(target, dest string) string { return names.ChildName(target, "mediafile", dest) }

// contained reports whether dest is strictly under the root folder.
func contained(dest, root string) bool { return root != "" && fsops.StrictlyUnder(dest, root) }

// TargetString renders an import intent's target in the annotation's
// grammar, "<kind>/<name>[/<key>]", for the summary.
func TargetString(it *lifecycle.ImportIntent) string {
	if it == nil || it.Target == nil {
		return ""
	}
	s := strings.ToLower(it.Target.Kind) + "/" + it.Target.Name
	if it.TargetKey != "" {
		s += "/" + it.TargetKey
	}
	return s
}

// ParseTarget is TargetString's inverse: the inspect task's target
// override (an Episode or Issue for a key), nil for "".
func ParseTarget(s string) *schema.ItemRef {
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	kind, name := parts[0], parts[1]
	if len(parts) == 3 && parts[2] != "" {
		switch kind {
		case string(commonv1.MediaKindSeries):
			return &schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Name: parts[2]}}
		case string(commonv1.MediaKindComic):
			return &schema.ItemRef{Kind: "Issue", Ref: schema.Ref{Name: parts[2]}}
		}
	}
	return &schema.ItemRef{Kind: objectKind(kind), Ref: schema.Ref{Name: name}}
}

// objectKind is a MediaKind as an object Kind ("audiobook" -> "Audiobook").
func objectKind(k string) string {
	if k == "" {
		return ""
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

// importSource is the spec.importedFrom an import of e freezes, its text
// bounded as the MediaFile CRD holds it.
func importSource(e *catalogv1alpha1.DownloadEntry, manual bool, at metav1.Time) catalogv1alpha1.ImportSource {
	return catalogv1alpha1.ImportSource{
		DownloadRef:  e.ID,
		ReleaseTitle: k8s.ClampText(k8s.SanitizeText(e.Release.Title), catalogv1alpha1.MaxReleaseTitleLength),
		IndexerName:  k8s.ClampText(k8s.SanitizeText(e.Release.IndexerName), catalogv1alpha1.MaxIndexerNameLength),
		Protocol:     e.Release.Protocol,
		InfoHash:     strings.ToLower(e.Release.InfoHash),
		ImportedAt:   at,
		Manual:       manual,
	}
}

// mediaRef is the MediaFile spec.mediaRef of a placed file: its target, its
// keys when it covers more than one item, its track.
func mediaRef(target schema.ItemRef, keys []string, track string) commonv1.MediaRef {
	ref := commonv1.MediaRef{Kind: MediaKindOf(target.Kind), Name: target.Name, Track: track}
	if len(keys) > 1 {
		ref.Keys = append([]string(nil), keys...)
	}
	return ref
}

// donorDest is where a donor file goes: <root>/.clustarr/donors/<item
// uid>/<item name><ext>.
func donorDest(root string, item schema.ItemRef, src string) (dir, dest string) {
	dir = filepath.Join(root, DonorDir, item.UID)
	return dir, filepath.Join(dir, item.Name+strings.ToLower(filepath.Ext(src)))
}
