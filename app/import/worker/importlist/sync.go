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
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// kindResult is one kind's contribution to a sync's overall Result.
type kindResult struct {
	fetched, added, excluded, removed int32
	err                               error
}

// syncDeps bundles what syncKind needs beyond the ImportList and kind
// themselves, so tests can substitute an HTTP test server and an in-memory
// bus without a real cluster or the network.
type syncDeps struct {
	Client          client.Client
	Bus             events.Bus
	Providers       ProviderOptions
	MetadataTimeout time.Duration
	Clock           func() time.Time
}

func (d syncDeps) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

// syncKind fetches, dedupes, drops excluded entries, resolves the id each
// catalog kind requires, and creates or updates the resulting Movie or
// Series items for one (ImportList, kind) pair, then applies spec.syncLevel
// to whatever this list previously added that is no longer present. With
// spec.automaticAdd false it does all of that except the create or update:
// the entries are recorded as listed and nothing is added (Radarr).
//
// A kind the provider cannot yield (CanYield; gap-fix ruling R-10) fails
// with ErrKindNotYieldable, and a yieldable kind with no catalog writer
// fails with ErrNoCatalogWriter once the provider has answered: both reach
// status.lastError through the Result, never a log line alone. An item this
// task cannot resolve the required external id for (see resolveRequiredID)
// is logged and dropped rather than created with a guessed id.
func syncKind(
	ctx context.Context, deps syncDeps, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind,
) kindResult {
	ctx, span := tracing.Start(ctx, "importlist.syncKind")
	defer span.End()
	log := logging.FromContext(ctx).With("importList", il.Namespace+"/"+il.Name, "kind", kind)

	if !CanYield(il.Spec, kind) {
		return kindResult{err: fmt.Errorf("%w: a %s list yields only %v, not %s",
			ErrKindNotYieldable, ProviderName(il.Spec), YieldableKinds(il.Spec), kind)}
	}

	tokenStore := NewSecretTokenStore(deps.Client, il)
	provider, err := BuildProvider(ctx, deps.Client, il, kind, tokenStore, deps.Providers)
	if err != nil {
		return kindResult{err: fmt.Errorf("build provider: %w", err)}
	}

	kv := deps.Bus.KV(events.BucketImportList)
	exclusions := deps.Bus.KV(events.BucketImportExclusions)
	existing, rev, err := LoadItems(ctx, kv, il.Namespace, il.Name, string(kind))
	if err != nil {
		return kindResult{err: fmt.Errorf("load remembered items: %w", err)}
	}

	// The slow, networked call. Everything before this reads only the
	// ImportList's spec and a Secret/ConfigMap; everything after it is what
	// CLAUDE.md's "lost update" note calls the re-Get window -- LoadItems'
	// revision, captured above, is what SaveItems' compare-and-swap
	// verifies is still current before this sync's writes land (see
	// SaveItems' doc comment).
	fetched, err := provider.Fetch(ctx)
	if err != nil {
		tracing.RecordError(span, err)
		return kindResult{err: fmt.Errorf("fetch: %w", err)}
	}
	if !hasCatalogWriter(kind) {
		return kindResult{fetched: int32(len(fetched)), err: fmt.Errorf(
			"%w: the %s list returned %d %s items, and import lists create only movie and series items",
			ErrNoCatalogWriter, ProviderName(il.Spec), len(fetched), kind)}
	}

	deduped := pkgimportlist.Dedupe(fetched)
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
	byKey := make(map[string]StoredItem, len(existing))
	for _, si := range existing {
		if !si.ListedOnly {
			existingItems = append(existingItems, si.Item)
		}
		byKey[si.Item.Key()] = si
	}
	autoAdd := il.Spec.AutomaticAdd == nil || *il.Spec.AutomaticAdd

	newSnapshot := make([]StoredItem, 0, len(included))
	// keepKnown carries a previously added item forward when this cycle
	// could not re-resolve or re-apply it. It is still on the list, so
	// ApplySyncLevel makes no decision about it; without this it would
	// silently drop out of the snapshot, and a later sync would never act
	// on it once it really did fall off.
	keepKnown := func(item pkgimportlist.Item) {
		if prev, ok := byKey[item.Key()]; ok {
			newSnapshot = append(newSnapshot, prev)
		}
	}
	var added int32
	for _, item := range included {
		id, err := resolveRequiredID(ctx, deps, kind, item.ExternalIDs)
		if err != nil {
			log.Warn("importlist: could not resolve the id this kind requires; skipping entry",
				"title", item.Title, "error", err)
			keepKnown(item)
			continue
		}

		prev, wasKnown := byKey[item.Key()]
		if !autoAdd {
			// Radarr's ProcessMovieReport returns before adding anything
			// when the list's EnableAuto is off, but the list is still
			// fetched and its movies still recorded (SyncMoviesForList), so
			// they count as listed when CleanLibrary looks for movies no
			// list has. Here the snapshot is that record: the entry is
			// remembered as listed only, under the name it would have, so
			// listedElsewhere sees it; and an item this list added before
			// automaticAdd was turned off stays its own.
			entry := StoredItem{
				Item: item, ObjectKind: string(kind), ObjectName: catalogName(kind, item.Title, id),
				ResolvedID: id, ListedOnly: true,
			}
			if wasKnown && !prev.ListedOnly {
				entry = prev
			}
			newSnapshot = append(newSnapshot, entry)
			continue
		}

		var objectName string
		switch kind {
		case commonv1.MediaKindMovie:
			objectName, err = applyMovie(ctx, deps.Client, il.Namespace, il.Name, item, il.Spec.Defaults, id)
		case commonv1.MediaKindSeries:
			objectName, err = applySeries(ctx, deps.Client, il.Namespace, il.Name, item, il.Spec.Defaults, id)
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
		newSnapshot = append(newSnapshot,
			StoredItem{Item: item, ObjectKind: string(kind), ObjectName: objectName, ResolvedID: id})
	}
	if !autoAdd {
		log.Info("importlist: spec.automaticAdd is false; entries recorded as listed, nothing added",
			"listed", len(included))
	}

	decisions, err := pkgimportlist.ApplySyncLevel(
		pkgimportlist.SyncLevel(il.Spec.SyncLevel), included, existingItems)
	if err != nil {
		return kindResult{
			fetched: int32(len(fetched)), added: added, excluded: excludedCount,
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
			other, err := listedElsewhere(ctx, deps.Client, kv, il, si)
			if err != nil {
				log.Warn("importlist: could not check the other lists; retrying next sync",
					"title", d.Item.Title, "error", err)
				newSnapshot = append(newSnapshot, si)
				actionErrs = append(actionErrs, fmt.Errorf("%s: %w", si.ObjectName, err))
				continue
			}
			if other != "" {
				// Design spec §8.7 applies syncLevel to items absent from
				// every enabled list. Dropped from this list's snapshot,
				// the item is the other list's to act on once it falls
				// off there too.
				log.Info("importlist: entry fell off this list but another list still has it; leaving it",
					"title", d.Item.Title, "object", si.ObjectName, "importList", other)
				continue
			}
		}
		if err := applySyncDecision(ctx, deps.Client, il, si, d.Action); err != nil {
			log.Warn("importlist: sync-level action failed", "title", d.Item.Title,
				"action", d.Action, "error", err)
			// Left in place: a snapshot entry this cycle could not act on
			// is remembered again, so the next sync retries the same
			// decision instead of silently forgetting the item. The
			// failure is also this kind's error, so it reaches status.
			newSnapshot = append(newSnapshot, si)
			actionErrs = append(actionErrs, fmt.Errorf("%s %s: %w", d.Action, si.ObjectName, err))
			continue
		}
		if d.Action != pkgimportlist.SyncActionLog {
			removed++
		}
	}

	if err := SaveItems(ctx, kv, il.Namespace, il.Name, string(kind), rev, newSnapshot); err != nil {
		if !errors.Is(err, ErrItemsChangedConcurrently) {
			log.Warn("importlist: could not save remembered items", "error", err)
		}
		// Not fatal to this sync's own result: the catalog writes above are
		// already durable. See SaveItems' doc comment.
	}

	publishSynced(ctx, deps, il, kind, len(fetched), int(added), int(removed), len(deduped)-len(included))

	res := kindResult{
		fetched: int32(len(fetched)), added: added, excluded: excludedCount, removed: removed,
	}
	if len(actionErrs) > 0 {
		res.err = fmt.Errorf("sync level %s: %w", il.Spec.SyncLevel, errors.Join(actionErrs...))
	}
	return res
}

// listedElsewhere returns the name of another enabled ImportList in il's
// namespace whose last sync still remembers si's catalog object, or "" when
// none does. Two lists naming the same film share one Movie (the name hashes
// the TMDB id), so without this a list dropping it would unmonitor, remove
// or -- under removeAndDelete -- recycle the files of something a second
// list still wants.
func listedElsewhere(
	ctx context.Context, c client.Client, kv events.KV, il *catalogv1alpha1.ImportList, si StoredItem,
) (string, error) {
	var lists catalogv1alpha1.ImportListList
	if err := c.List(ctx, &lists, client.InNamespace(il.Namespace)); err != nil {
		return "", fmt.Errorf("list import lists: %w", err)
	}
	for i := range lists.Items {
		other := &lists.Items[i]
		if other.Name == il.Name || k8s.IsDeleting(other) ||
			(other.Spec.Enabled != nil && !*other.Spec.Enabled) ||
			!slices.Contains(other.Spec.Kinds, si.ObjectKind) {
			continue
		}
		items, _, err := LoadItems(ctx, kv, other.Namespace, other.Name, si.ObjectKind)
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
// write. SyncActionRemoveAndDelete recycles the item's files and deletes
// their MediaFiles before the item (removeWithFiles, delete.go).
//
// SyncActionRemove ("remove the catalog item, keep files") deletes only the
// Movie or Series, and leaves every file and every MediaFile record where
// it is. Radarr also drops the MovieFile rows (MediaFileService handles
// MoviesDeletedEvent with DeleteForMovies), but Radarr's disk scan only
// ever scans movies it already has (DiskScanService.Scan(Movie)); Clustarr's
// library rescan instead adopts every unrecorded file under a root folder
// and creates the item it belongs to. The MediaFile record is what marks a
// file as accounted for -- the rescan re-observes a recorded file and never
// creates an item for it -- so deleting it here would be exactly what lets
// the next rescan undo the removal. Kept, it also re-attaches by itself if
// a list adds the item back: the item's name is deterministic, which is
// Radarr's re-added movie finding its file again. An ImportExclusion is not
// written either: Radarr's list clean-up calls DeleteMovie(id, false),
// whose addImportListExclusion defaults to false, so a movie that comes
// back onto a list is added again.
func applySyncDecision(
	ctx context.Context, c client.Client, il *catalogv1alpha1.ImportList, si StoredItem, action pkgimportlist.SyncAction,
) error {
	switch action {
	case pkgimportlist.SyncActionLog:
		logging.FromContext(ctx).Info("importlist: entry fell off the list (syncLevel=logOnly)",
			"title", si.Item.Title, "object", si.ObjectName)
		return nil
	case pkgimportlist.SyncActionUnmonitor:
		if si.ObjectKind == string(commonv1.MediaKindSeries) {
			return unmonitorSeries(ctx, c, il.Namespace, il.Name, si, il.Spec.Defaults)
		}
		return unmonitorMovie(ctx, c, il.Namespace, il.Name, si, il.Spec.Defaults)
	case pkgimportlist.SyncActionRemove:
		if si.ObjectKind == string(commonv1.MediaKindSeries) {
			return deleteSeries(ctx, c, il.Namespace, si.ObjectName)
		}
		return deleteMovie(ctx, c, il.Namespace, si.ObjectName)
	case pkgimportlist.SyncActionRemoveAndDelete:
		return removeWithFiles(ctx, c, il.Namespace, si)
	default:
		return fmt.Errorf("importlist: unknown sync action %q", action)
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

// resolveRequiredID returns the external id kind's spec identifies items
// by: TMDB for movie, TVDB for series. If ids already carries it, no RPC is
// made. Otherwise it asks the metadata gateway's resolve verb to fill the
// gap from whatever ids ARE present, the same rpc.catalogarr.metadata.resolve
// call importarr/worker/rescan.resolveIMDb makes.
func resolveRequiredID(
	ctx context.Context, deps syncDeps, kind commonv1.MediaKind, ids pkgimportlist.ExternalIDs,
) (int64, error) {
	key := metadataKeyFor(kind)
	if direct := directID(ids, kind); direct != "" {
		return strconv.ParseInt(direct, 10, 64)
	}
	if deps.Bus == nil {
		return 0, fmt.Errorf("no bus to resolve a %s id with", key)
	}
	timeout := deps.MetadataTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	rpcCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := schema.MetadataRequest{Kind: kind, IDs: ToMetadataExternalIDs(ids)}
	var resp schema.MetadataResponse
	if err := deps.Bus.Request(rpcCtx, events.RPCMetadataResolve, req, &resp); err != nil {
		return 0, fmt.Errorf("resolve %s id: %w", key, err)
	}
	if resp.Error != "" {
		return 0, fmt.Errorf("resolve %s id: %s", key, resp.Error)
	}
	raw := resp.IDs[key]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("gateway returned no usable %s id (got %q)", key, raw)
	}
	return id, nil
}

func metadataKeyFor(kind commonv1.MediaKind) string {
	if kind == commonv1.MediaKindSeries {
		return "tvdb"
	}
	return "tmdb"
}

func directID(ids pkgimportlist.ExternalIDs, kind commonv1.MediaKind) string {
	if kind == commonv1.MediaKindSeries {
		return ids.TVDB
	}
	return ids.TMDB
}

// publishSynced fires the catalog.ImportListSynced history event (spec §5's
// clustarr.evt.catalog.importlist.synced.<uid>) for one kind's sync. It is
// best-effort: a publish failure is logged, not returned, since the sync's
// own catalog writes already landed and the history sink (task G1-4) is a
// secondary audit trail, not a correctness dependency of this worker.
func publishSynced(
	ctx context.Context, deps syncDeps, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind,
	fetched, added, removed, skipped int,
) {
	evt := schema.ImportListSynced{
		ListRef: schema.Ref{Namespace: il.Namespace, Name: il.Name, UID: string(il.UID)},
		Fetched: int32(fetched), Added: int32(added), Removed: int32(removed), Skipped: int32(skipped),
		At: deps.now(),
	}
	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		logging.FromContext(ctx).Warn("importlist: encode history event failed", "error", err)
		return
	}
	env := &events.Envelope{
		ID:     events.MsgIDForObject(string(il.UID), il.Generation, "synced-"+string(kind)+"-"+deps.now().Format(time.RFC3339)),
		Type:   "catalog.ImportListSynced",
		Schema: schemaName,
		Source: "importarr",
		Key:    il.Namespace + "/" + il.Name,
		Time:   deps.now(),
		Data:   data,
	}
	if _, err := deps.Bus.Publish(ctx, events.CatalogImportListSyncedSubject(string(il.UID)), env); err != nil {
		logging.FromContext(ctx).Warn("importlist: publish history event failed", "error", err)
	}
}
