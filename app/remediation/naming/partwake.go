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

package naming

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s/rowcache"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// audiobookPartWakes is S12/MediaFile, the sibling arm of S1 for an
// audiobook's parts (2026-11-10 design D7, D11): a part's {PartNumber} is
// its place among its Audiobook's MediaFiles (mediafile.LoadNaming's
// audiobookPartNumber), so a part appearing or going, moving to another
// audiobook, or changing the key it orders by renumbers the others -- which
// nothing else woke: S1 enqueues the changed file alone, the item arm its
// Audiobook, and an informer resync wakes nothing (fileUpdateWakes), so
// after the import's one materialisation pass a part a rescan added or a
// MediaFile removed left every other part's status.naming, and under
// renameFiles its name, at the last plan until some unrelated wake. The
// siblings are enqueued by key through the item index at
// handler.LowPriority (no file is decoded; the changed file is S1's), of
// the audiobook the file now names and the one it named before. The
// initial list is skipped: every file wakes itself at the start.
func audiobookPartWakes(c client.Reader) handler.TypedEventHandler[client.Object, remediation.Key] {
	return handler.TypedFuncs[client.Object, remediation.Key]{
		CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			if e.IsInInitialList {
				return
			}
			wakeAudiobookSiblings(ctx, c, q, partOf(e.Object), nil)
		},
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			old, nw := partOf(e.ObjectOld), partOf(e.ObjectNew)
			if !partOrderMoved(old, nw) {
				return
			}
			wakeAudiobookSiblings(ctx, c, q, nw, old)
		},
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			wakeAudiobookSiblings(ctx, c, q, partOf(e.Object), nil)
		},
	}
}

// partOf is o as a MediaFile, nil for anything else.
func partOf(o client.Object) *catalogv1alpha1.MediaFile {
	mf, _ := o.(*catalogv1alpha1.MediaFile)
	return mf
}

// isAudiobookPart: the file is attributed to an audiobook.
func isAudiobookPart(mf *catalogv1alpha1.MediaFile) bool {
	return mf != nil && mf.Spec.MediaRef.Kind == commonv1.MediaKindAudiobook && mf.Spec.MediaRef.Name != ""
}

// partOrderMoved reports whether an update moved what catalogctx.PartNumbers
// orders a part by, or the audiobook it is a part of: spec.mediaRef (kind or
// name), spec.partIndex, or spec.path for a part that orders by its path --
// an unnumbered one (partIndex 0), before or after -- where a numbered
// part's path breaks only a tie with an equal key. A file that was and is
// no audiobook part moves nothing.
func partOrderMoved(old, nw *catalogv1alpha1.MediaFile) bool {
	if old == nil || nw == nil || (!isAudiobookPart(old) && !isAudiobookPart(nw)) {
		return false
	}
	switch {
	case old.Spec.MediaRef.Kind != nw.Spec.MediaRef.Kind, old.Spec.MediaRef.Name != nw.Spec.MediaRef.Name:
		return true
	case old.Spec.PartIndex != nw.Spec.PartIndex:
		return true
	case old.Spec.Path != nw.Spec.Path && (old.Spec.PartIndex == 0 || nw.Spec.PartIndex == 0):
		return true
	}
	return false
}

// wakeAudiobookSiblings enqueues, at handler.LowPriority, every other part
// of the audiobook file names and of the one old named (nil for a create or
// a delete), each once; file itself is left to S1.
func wakeAudiobookSiblings(ctx context.Context, c client.Reader, q workqueue.TypedRateLimitingInterface[remediation.Key], file, old *catalogv1alpha1.MediaFile) {
	if file == nil {
		return
	}
	seen := map[remediation.Key]bool{}
	var keys []remediation.Key
	for _, f := range []*catalogv1alpha1.MediaFile{file, old} {
		if !isAudiobookPart(f) {
			continue
		}
		nns, err := rowcache.Keys(ctx, c, &catalogv1alpha1.MediaFileList{},
			client.InNamespace(f.Namespace), client.MatchingFields{mfindex.Item: mfindex.ItemKey(commonv1.MediaKindAudiobook, f.Spec.MediaRef.Name)})
		if err != nil {
			logging.FromContext(ctx).Warn("naming: list an audiobook's parts to renumber them", "audiobook", f.Spec.MediaRef.Name, "error", err)
			continue
		}
		for _, nn := range nns {
			k := remediation.Key{Kind: remediation.KindMediaFile, Namespace: nn.Namespace, Name: nn.Name}
			if nn.Name == file.Name || seen[k] {
				continue
			}
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return
	}
	if pq, ok := q.(priorityqueue.PriorityQueue[remediation.Key]); ok {
		pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(handler.LowPriority)}, keys...)
		return
	}
	for _, k := range keys {
		q.Add(k)
	}
}
