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

package rollup

import (
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// This file builds the two catalog domain events the item reconcilers
// produce -- spec §5's clustarr.evt.catalog.<kind>.<added|updated|deleted>
// and clustarr.evt.catalog.mediafile.<imported|replaced|deleted> -- which the
// history sink (app/catalog/history) turns into Events on the item. Until the
// gap-fix wave nothing published either subject, so an item's history showed
// only its grabs. The builders are pure (no bus, no clock) so each
// reconciler keeps the one side effect, the publish, where it decides to
// make it.
//
// Every envelope id is a function of the edge it announces, never of the
// wall clock: a reconcile that publishes and then fails its status apply
// re-observes the same edge and republishes the same id, which the EVENTS
// stream's duplicate window absorbs. That is why each caller publishes
// BEFORE the apply that records the edge, the same order grabarr's
// DownloadEvent producer uses: publishing after it would lose the event on
// exactly that crash.

// ItemFields is what an ItemEvent says about the item beyond its identity.
type ItemFields struct {
	Title     string
	Year      int32
	IDs       map[string]string
	Monitored bool
	AddSource *commonv1.AddSource
}

// ItemEvent builds the publication announcing that item (of kind) was added,
// updated or deleted. The envelope id is "<uid>:added", "<uid>:deleted" or
// "<uid>:updated:<generation>": an item is added and deleted once, and
// updated once per spec generation.
func ItemEvent(item client.Object, kind commonv1.MediaKind, action string, f ItemFields, at time.Time) (string, *events.Envelope, error) {
	evt := schema.ItemEvent{
		Media:     commonv1.MediaRef{Kind: kind, Name: item.GetName()},
		Ref:       schema.Ref{Namespace: item.GetNamespace(), Name: item.GetName(), UID: string(item.GetUID())},
		Action:    action,
		Title:     f.Title,
		Year:      f.Year,
		IDs:       f.IDs,
		Monitored: f.Monitored,
		AddSource: f.AddSource,
		At:        at,
	}
	name, data, err := schema.Encode(evt)
	if err != nil {
		return "", nil, err
	}
	id := string(item.GetUID()) + ":" + action
	if action == events.ActionUpdated {
		id += ":" + strconv.FormatInt(item.GetGeneration(), 10)
	}
	return events.CatalogItemSubject(string(kind), action, string(item.GetUID())), &events.Envelope{
		ID:     id,
		Type:   "catalog.ItemEvent",
		Schema: name,
		Source: "catalogarr@" + version.String(),
		Key:    item.GetNamespace() + "/" + item.GetName(),
		Time:   at,
		Data:   data,
	}, nil
}

// ItemAction reports which ItemEvent, if any, a reconcile of item is the
// first to observe: added on the first reconcile (the one that has not yet
// recorded status.addOptionsApplied), updated on the first reconcile of a
// new spec generation, and "" otherwise. deleted is the delete path's own
// decision and never comes from here.
func ItemAction(generation, observedGeneration int64, addOptionsApplied bool) string {
	switch {
	case !addOptionsApplied:
		return events.ActionAdded
	case observedGeneration != 0 && generation != observedGeneration:
		return events.ActionUpdated
	default:
		return ""
	}
}

// FileTransition reports the MediaFileEvent a reconcile observes when the
// file backing an item moves from oldRef (the item's status.fileRef as last
// recorded) to mf (the file rollup.PickMediaFile chose now): imported when
// the item gains its first file, replaced when a different file takes over
// (Sonarr and Radarr call this an upgrade), deleted when the file is gone,
// and "" when nothing changed. file is the MediaFile the event is about.
func FileTransition(oldRef *string, mf *catalogv1alpha1.MediaFile) (action, file string) {
	switch {
	case oldRef == nil && mf == nil:
		return "", ""
	case oldRef == nil:
		return events.ActionImported, mf.Name
	case mf == nil:
		return events.ActionDeleted, *oldRef
	case *oldRef != mf.Name:
		return events.ActionReplaced, mf.Name
	default:
		return "", ""
	}
}

// MediaFileEvent builds the publication for one FileTransition of item. mf
// is the file now backing the item, nil for deleted -- which is why the
// subject's <uid> is the ITEM's, the same choice CatalogReleaseSubject makes
// with its <target-uid>: the reconciler that observes a deletion no longer
// has the deleted MediaFile, or its UID, to name. One subject per item also
// lets a consumer follow one item's whole file history with one filter.
//
// The envelope id is "<item-uid>:mediafile:<action>:<file>", so the same
// edge always dedups and two different files never collide.
func MediaFileEvent(item client.Object, media commonv1.MediaRef, action, file string, mf *catalogv1alpha1.MediaFile, at time.Time) (string, *events.Envelope, error) {
	evt := schema.MediaFileEvent{
		Media:  commonv1.MediaRef{Kind: media.Kind, Name: media.Name},
		Action: action,
		At:     at,
	}
	switch action {
	case events.ActionReplaced:
		evt.Reason = schema.MediaFileReasonUpgrade
	case events.ActionDeleted:
		// Nothing here knows WHY the file went: the rescan deletes a
		// MediaFile whose file is missing from disk, a user deletes one by
		// hand, and an upgrade's importer deletes the file it replaced.
		// Leaving the reason empty is honest; guessing missingFromDisk is not.
	}
	if mf != nil {
		evt.ImportedPath = mf.Spec.Path
		evt.SizeBytes = mf.Spec.SizeBytes
		evt.Quality = mf.Spec.Quality
		if src := mf.Spec.ImportedFrom; src != nil {
			if src.Manual && action == events.ActionImported {
				evt.Reason = schema.MediaFileReasonManual
			}
			if src.DownloadRef != "" {
				evt.DownloadRef = &schema.Ref{Namespace: mf.Namespace, Name: src.DownloadRef}
			}
		}
	}
	name, data, err := schema.Encode(evt)
	if err != nil {
		return "", nil, err
	}
	return events.CatalogMediaFileSubject(action, string(item.GetUID())), &events.Envelope{
		ID:     string(item.GetUID()) + ":mediafile:" + action + ":" + file,
		Type:   "catalog.MediaFileEvent",
		Schema: name,
		Source: "catalogarr@" + version.String(),
		Key:    item.GetNamespace() + "/" + item.GetName(),
		Time:   at,
		Data:   data,
	}, nil
}

// Transitioned reports whether setting condType to (status, reason) is a
// change from prev, the conditions the object carried before this
// reconcile. The item reconcilers emit a Kubernetes Event only on such an
// edge: a steady state -- a RootFolder still missing, a profile still
// unresolved -- persists on every reconcile, and an Event per reconcile
// would bury the one that said when it started.
func Transitioned(prev []metav1.Condition, condType string, status metav1.ConditionStatus, reason string) bool {
	for i := range prev {
		if prev[i].Type == condType {
			return prev[i].Status != status || prev[i].Reason != reason
		}
	}
	return true
}
