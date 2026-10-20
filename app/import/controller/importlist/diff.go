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

package importlist

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// syncOutcome is one snapshot's diff, every kind combined: what status
// reports for the sync (R17: the counts are the controller's, computed
// here, and the snapshot's SyncedAt is status.lastSyncAt).
type syncOutcome struct {
	// SyncedAt is when the import agent finished the sync, in seconds.
	SyncedAt time.Time
	// Error joins the agent's fetch errors and every kind's diff error.
	Error string
	// NeedsToken is the snapshot's: Trakt refused the list's token.
	NeedsToken bool

	Fetched, Added, Excluded, Removed int32
}

// kindResult is one kind's contribution to a syncOutcome.
type kindResult struct {
	fetched, added, excluded, removed int32
	err                               error
}

// diff applies one complete snapshot (ADR-0019 §7.6): for every kind both
// the snapshot and spec.kinds name, diffKind. A kind spec.kinds names that
// the snapshot lacks failed its fetch (its error is the snapshot's), and is
// left as it is: a failed fetch is never an emptied list.
func (r *Reconciler) diff(ctx context.Context, il *catalogv1alpha1.ImportList, snap schema.ListSnapshot) syncOutcome {
	ctx, span := tracing.Start(ctx, "importlist.diff")
	defer span.End()

	out := syncOutcome{SyncedAt: snap.SyncedAt.UTC().Truncate(time.Second), NeedsToken: snap.NeedsToken}
	var errs []error
	if snap.Error != "" {
		errs = append(errs, errors.New(snap.Error))
	}
	for _, ks := range snap.Kinds {
		if !slices.Contains(il.Spec.Kinds, ks.Kind) {
			continue
		}
		kr := r.diffKind(ctx, il, ks, out.SyncedAt)
		out.Fetched += kr.fetched
		out.Added += kr.added
		out.Excluded += kr.excluded
		out.Removed += kr.removed
		if kr.err != nil {
			tracing.RecordError(span, kr.err)
			errs = append(errs, fmt.Errorf("%s: %w", ks.Kind, kr.err))
		}
	}
	if len(errs) > 0 {
		out.Error = errors.Join(errs...).Error()
	}
	return out
}

// listedItem is one snapshot entry as the diff reads it: the provider's
// item and the import agent's resolution of the id its kind is keyed on.
type listedItem struct {
	item       pkgimportlist.Item
	resolvedID int64
	unresolved bool
}

// diffKind applies one kind of a snapshot: refuse a kind the list cannot
// yield (P72), dedupe (P73), drop excluded entries (P74, failing open),
// automaticAdd off records entries as listed only (P75), leave an item
// another source added (P76), keep an unresolvable id known (P77), create
// or update from the list's defaults keeping a classified series' profile
// and type (P78), act on what fell off the list per spec.syncLevel (P79)
// unless another list still has it (P80), and delete with UID
// preconditions, recycling files through a task under removeAndDelete
// (W28, W29, P81). The remembered items (importliststate.ItemsKey) are this
// controller's alone (R17).
//
// A kind with no catalog writer fails with importliststate.ErrNoCatalogWriter,
// reaching status.lastError, never a log line alone.
func (r *Reconciler) diffKind(
	ctx context.Context, il *catalogv1alpha1.ImportList, ks schema.ListKindSnapshot, syncedAt time.Time,
) kindResult {
	ctx, span := tracing.Start(ctx, "importlist.diffKind")
	defer span.End()
	kind := commonv1.MediaKind(ks.Kind)
	log := logging.FromContext(ctx).With("kind", kind)
	ctx = logging.NewContext(ctx, log)
	fetched := ks.Fetched

	if !importliststate.CanYield(il.Spec, kind) {
		return kindResult{err: fmt.Errorf("%w: a %s list yields only %v, not %s",
			importliststate.ErrKindNotYieldable, importliststate.ProviderName(il.Spec), importliststate.YieldableKinds(il.Spec), kind)}
	}
	if !importliststate.HasCatalogWriter(kind) {
		return kindResult{fetched: fetched, err: fmt.Errorf(
			"%w: the %s list returned %d %s items, and import lists create only movie and series items",
			importliststate.ErrNoCatalogWriter, importliststate.ProviderName(il.Spec), fetched, kind)}
	}

	listed := make([]pkgimportlist.Item, 0, len(ks.Items))
	byListedKey := make(map[string]listedItem, len(ks.Items))
	for _, li := range ks.Items {
		item := pkgimportlist.Item{
			Title: li.Title, Year: li.Year,
			ExternalIDs: importliststate.FromMetadataExternalIDs(metadata.ExternalIDs(li.IDs)),
		}
		listed = append(listed, item)
		if _, seen := byListedKey[item.Key()]; !seen {
			byListedKey[item.Key()] = listedItem{item: item, resolvedID: li.ResolvedID, unresolved: li.Unresolved}
		}
	}

	kv := r.snapshots()
	exclusions := r.Bus.KV(events.BucketImportExclusions)
	existing, rev, err := importliststate.LoadItems(ctx, kv, il.Namespace, il.Name, string(kind))
	if err != nil {
		return kindResult{fetched: fetched, err: fmt.Errorf("load remembered items: %w", err)}
	}

	deduped := pkgimportlist.Dedupe(listed)
	included := make([]pkgimportlist.Item, 0, len(deduped))
	var excludedCount int32
	for _, item := range deduped {
		blocked, entry, err := isExcluded(ctx, exclusions, item.ExternalIDs)
		if err != nil {
			log.Warn("importlist: exclusion lookup failed; treating as not excluded", "error", err)
		}
		if blocked {
			excludedCount++
			log.Debug("importlist: entry excluded", "title", item.Title, "importExclusion", entry)
			continue
		}
		included = append(included, item)
	}

	// Only what this list itself added is subject to spec.syncLevel. An
	// entry it merely listed (automaticAdd off) was never its to add, so it
	// is never its to unmonitor or remove either.
	existingItems := make([]pkgimportlist.Item, 0, len(existing))
	byKey := make(map[string]importliststate.StoredItem, len(existing))
	for _, si := range existing {
		if !si.ListedOnly {
			existingItems = append(existingItems, si.Item)
		}
		byKey[si.Item.Key()] = si
	}
	autoAdd := il.Spec.AutomaticAdd == nil || *il.Spec.AutomaticAdd

	remembered := make([]importliststate.StoredItem, 0, len(included))
	// keepKnown carries a previously added item forward when this diff could
	// not resolve or re-apply it. It is still on the list, so ApplySyncLevel
	// makes no decision about it; without this it would silently drop out
	// of the remembered items, and a later diff would never act on it once
	// it really did fall off.
	keepKnown := func(item pkgimportlist.Item) {
		if prev, ok := byKey[item.Key()]; ok {
			remembered = append(remembered, prev)
		}
	}
	var library map[int64]libraryItem
	if autoAdd {
		library, err = libraryByID(ctx, r.Client, il.Namespace, il.Name, kind)
		if err != nil {
			return kindResult{fetched: fetched, excluded: excludedCount, err: err}
		}
	}
	var added int32
	for _, item := range included {
		lr := byListedKey[item.Key()]
		if lr.unresolved || lr.resolvedID <= 0 {
			log.Warn("importlist: the agent could not resolve the id this kind requires; skipping entry",
				"title", item.Title)
			keepKnown(item)
			continue
		}
		id := lr.resolvedID

		prev, wasKnown := byKey[item.Key()]
		if !autoAdd {
			// Radarr's ProcessMovieReport returns before adding anything
			// when the list's EnableAuto is off, but the list is still
			// fetched and its movies still recorded (SyncMoviesForList), so
			// they count as listed when CleanLibrary looks for movies no
			// list has. The remembered item is that record: the entry is
			// remembered as listed only, under the name it would have, so
			// listedElsewhere sees it; and an item this list added before
			// automaticAdd was turned off stays its own.
			entry := importliststate.StoredItem{
				Item: item, ObjectKind: string(kind), ObjectName: catalogName(kind, item.Title, id),
				ResolvedID: id, ListedOnly: true,
			}
			if wasKnown && !prev.ListedOnly {
				entry = prev
			}
			remembered = append(remembered, entry)
			continue
		}

		objectName := catalogName(kind, item.Title, id)
		if held, ok := library[id]; ok {
			if held.listRef != il.Name {
				// Added by hand, by a library scan or by another list: not
				// this list's to change or remove. Radarr skips a list movie
				// already in the library. Remembered as listed, so another
				// list's syncLevel still sees it wanted.
				log.Debug("importlist: already in the library, added elsewhere; leaving it",
					"title", item.Title, "object", held.name, "addedBy", held.listRef)
				remembered = append(remembered, importliststate.StoredItem{
					Item: item, ObjectKind: string(kind), ObjectName: held.name,
					ResolvedID: id, ListedOnly: true,
				})
				continue
			}
			objectName = held.name
		}
		switch kind {
		case commonv1.MediaKindMovie:
			_, err = applyMovie(ctx, r.Client, r.apiReader(), il.Namespace, il.Name, objectName, item, il.Spec.Defaults, id)
		case commonv1.MediaKindSeries:
			_, err = applySeries(ctx, r.Client, r.apiReader(), il.Namespace, il.Name, objectName, item, il.Spec.Defaults, id)
		}
		if err != nil {
			log.Warn("importlist: could not apply catalog item; skipping entry",
				"title", item.Title, "error", err)
			keepKnown(item)
			continue
		}
		if !wasKnown || prev.ListedOnly {
			added++
		}
		remembered = append(remembered,
			importliststate.StoredItem{Item: item, ObjectKind: string(kind), ObjectName: objectName, ResolvedID: id})
	}
	if !autoAdd {
		log.Info("importlist: spec.automaticAdd is false; entries recorded as listed, nothing added",
			"listed", len(included))
	}

	decisions, err := pkgimportlist.ApplySyncLevel(
		pkgimportlist.SyncLevel(il.Spec.SyncLevel), included, existingItems)
	if err != nil {
		return kindResult{
			fetched: fetched, added: added, excluded: excludedCount,
			err: fmt.Errorf("apply sync level: %w", err),
		}
	}

	var (
		removed    int32
		actionErrs []error
	)
	for _, d := range decisions {
		si, ok := byKey[d.Item.Key()]
		if !ok {
			continue
		}
		if d.Action != pkgimportlist.SyncActionLog {
			other, err := r.listedElsewhere(ctx, kv, il, si)
			if err != nil {
				log.Warn("importlist: could not check the other lists; retrying at the next sync",
					"title", d.Item.Title, "error", err)
				remembered = append(remembered, si)
				actionErrs = append(actionErrs, fmt.Errorf("%s: %w", si.ObjectName, err))
				continue
			}
			if other != "" {
				// Design spec §8.7 applies syncLevel to items absent from
				// every enabled list. No longer remembered by this list, the
				// item is the other list's to act on once it falls off
				// there too.
				log.Info("importlist: entry fell off this list but another list still has it; leaving it",
					"title", d.Item.Title, "object", si.ObjectName, "importList", other)
				continue
			}
		}
		acted, err := r.applySyncDecision(ctx, il, si, d.Action, syncedAt)
		if err != nil {
			log.Warn("importlist: sync-level action failed", "title", d.Item.Title,
				"action", d.Action, "error", err)
			// Remembered again, so the next snapshot's diff retries the same
			// decision instead of silently forgetting the item. The failure
			// is also this kind's error, so it reaches status.
			remembered = append(remembered, si)
			actionErrs = append(actionErrs, fmt.Errorf("%s %s: %w", d.Action, si.ObjectName, err))
			continue
		}
		if acted {
			removed++
		}
	}

	if err := importliststate.SaveItems(ctx, kv, il.Namespace, il.Name, string(kind), rev, remembered); err != nil {
		if !errors.Is(err, importliststate.ErrItemsChangedConcurrently) {
			log.Warn("importlist: could not save remembered items", "error", err)
		}
		// Not fatal to this diff's own result: the catalog writes above are
		// already durable. See SaveItems' doc comment.
	}

	r.publishSynced(ctx, il, kind, int(fetched), int(added), int(removed), len(deduped)-len(included))

	res := kindResult{fetched: fetched, added: added, excluded: excludedCount, removed: removed}
	if len(actionErrs) > 0 {
		res.err = fmt.Errorf("sync level %s: %w", il.Spec.SyncLevel, errors.Join(actionErrs...))
	}
	return res
}

// listedElsewhere returns the name of another enabled ImportList in il's
// namespace whose remembered items still name si's catalog object, or ""
// when none does. Two lists naming the same film share one Movie (the
// second finds the first's by TMDB id and records it as listed), so without
// this a list dropping it would unmonitor, remove or -- under
// removeAndDelete -- recycle the files of something a second list still
// wants.
func (r *Reconciler) listedElsewhere(
	ctx context.Context, kv events.KV, il *catalogv1alpha1.ImportList, si importliststate.StoredItem,
) (string, error) {
	var lists catalogv1alpha1.ImportListList
	if err := r.Client.List(ctx, &lists, client.InNamespace(il.Namespace)); err != nil {
		return "", fmt.Errorf("list import lists: %w", err)
	}
	for i := range lists.Items {
		other := &lists.Items[i]
		if other.Name == il.Name || k8s.IsDeleting(other) ||
			(other.Spec.Enabled != nil && !*other.Spec.Enabled) ||
			!slices.Contains(other.Spec.Kinds, si.ObjectKind) {
			continue
		}
		items, _, err := importliststate.LoadItems(ctx, kv, other.Namespace, other.Name, si.ObjectKind)
		if err != nil {
			return "", err
		}
		for _, o := range items {
			if o.ObjectName == si.ObjectName {
				return other.Name, nil
			}
		}
	}
	return "", nil
}

// applySyncDecision turns one pkgimportlist.SyncDecision into a catalog
// write: unmonitor (re-apply with monitored false), remove (delete the item
// with a UID precondition, keeping its files), or remove and delete
// (removeWithFiles). acted is true when the item was changed; an item that
// is gone, or that this list no longer added (spec.source names another
// list or none), is forgotten without a write, since a list writes only the
// items it added itself.
func (r *Reconciler) applySyncDecision(
	ctx context.Context, il *catalogv1alpha1.ImportList, si importliststate.StoredItem,
	action pkgimportlist.SyncAction, syncedAt time.Time,
) (acted bool, err error) {
	log := logging.FromContext(ctx)
	if action == pkgimportlist.SyncActionLog {
		log.Info("importlist: entry fell off the list (syncLevel=logOnly)",
			"title", si.Item.Title, "object", si.ObjectName)
		return false, nil
	}
	item, rootFolder, source, ok, err := r.liveItem(ctx, il.Namespace, si)
	if err != nil {
		return false, err
	}
	if !ok {
		log.Info("importlist: an item that fell off the list is already gone", "object", si.ObjectName)
		return false, nil
	}
	if !addedBy(source, il.Name) {
		log.Info("importlist: an item that fell off the list is no longer this list's; leaving it",
			"object", si.ObjectName)
		return false, nil
	}
	switch action {
	case pkgimportlist.SyncActionUnmonitor:
		return true, r.unmonitor(ctx, il, si)
	case pkgimportlist.SyncActionRemove:
		return true, r.remove(ctx, item)
	case pkgimportlist.SyncActionRemoveAndDelete:
		return true, r.removeWithFiles(ctx, il, item, rootFolder, syncedAt)
	default:
		return false, fmt.Errorf("importlist: unknown sync action %q", action)
	}
}

// isExcluded checks every recognised external id ids carries against the
// clustarr-import-exclusions bucket, the same lookup contract
// events.ExclusionEntry documents. It returns the first match found; ids
// with no recognised id at all (only a bare title+year Key()) can never be
// excluded, since ImportExclusion requires at least one external id.
func isExcluded(ctx context.Context, kv events.KV, ids pkgimportlist.ExternalIDs) (bool, events.ExclusionEntry, error) {
	pairs := []struct{ provider, value string }{
		{catalogv1alpha1.ExclusionIDKeyTMDB, ids.TMDB},
		{catalogv1alpha1.ExclusionIDKeyIMDB, ids.IMDb},
		{catalogv1alpha1.ExclusionIDKeyTVDB, ids.TVDB},
		{catalogv1alpha1.ExclusionIDKeyMusicBrainz, ids.MusicBrainz},
		{catalogv1alpha1.ExclusionIDKeyASIN, ids.ASIN},
		{catalogv1alpha1.ExclusionIDKeyComicVine, ids.ComicVine},
	}
	for _, p := range pairs {
		if p.value == "" {
			continue
		}
		entry, err := kv.Get(ctx, events.ExclusionKey(p.provider, p.value))
		switch {
		case err == nil:
			decoded, derr := events.DecodeExclusionEntry(entry.Value)
			if derr != nil {
				return true, events.ExclusionEntry{}, derr
			}
			return true, decoded, nil
		case errors.Is(err, events.ErrKeyNotFound):
			continue
		default:
			return false, events.ExclusionEntry{}, err
		}
	}
	return false, events.ExclusionEntry{}, nil
}

// publishSynced fires the catalog.ImportListSynced history event (spec §5's
// clustarr.evt.catalog.importlist.synced.<uid>) for one kind's diff. It is
// best-effort: a publish failure is logged, not returned, since the diff's
// own catalog writes already landed and the history sink is a secondary
// audit trail, not a correctness dependency of this controller.
func (r *Reconciler) publishSynced(
	ctx context.Context, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind,
	fetched, added, removed, skipped int,
) {
	now := r.now()
	evt := schema.ImportListSynced{
		ListRef: schema.Ref{Namespace: il.Namespace, Name: il.Name, UID: string(il.UID)},
		Fetched: int32(fetched), Added: int32(added), Removed: int32(removed), Skipped: int32(skipped),
		At: now,
	}
	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		logging.FromContext(ctx).Warn("importlist: encode history event failed", "error", err)
		return
	}
	env := &events.Envelope{
		ID:     events.MsgIDForObject(string(il.UID), il.Generation, "synced-"+string(kind)+"-"+now.Format(time.RFC3339)),
		Type:   "catalog.ImportListSynced",
		Schema: schemaName,
		Source: "import",
		Key:    il.Namespace + "/" + il.Name,
		Time:   now,
		Data:   data,
	}
	if _, err := r.Bus.Publish(ctx, events.CatalogImportListSyncedSubject(string(il.UID)), env); err != nil {
		logging.FromContext(ctx).Warn("importlist: publish history event failed", "error", err)
	}
}
