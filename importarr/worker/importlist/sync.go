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
	"net/http"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// supportedKinds is every commonv1.MediaKind this task builds catalog items
// for. ImportListSpec.Kinds may also carry album, book, audiobook or comic
// (the CRD's own enum allows them); those are G2's non-video controllers'
// job, not this task's, and are skipped per kind -- never guessed at -- with
// a logged reason, counted by the log line rather than a status field (see
// syncKind's doc comment for why there is no CRD counter for this).
var supportedKinds = map[commonv1.MediaKind]bool{
	commonv1.MediaKindMovie:  true,
	commonv1.MediaKindSeries: true,
}

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
	HTTPClient      *http.Client
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
// to whatever this list previously added that is no longer present.
//
// A kind not in supportedKinds, or a provider that does not cover the
// requested kind at all (pkg/importlist/stevenlu with kind=series, for
// example), is a skip, not an error: it is logged with the reason and
// contributes zero counts, never a speculative or partial sync. Likewise an
// item this task cannot resolve the required external id for (see
// resolveRequiredID) is logged and dropped rather than created with a
// guessed id.
//
// Neither of those two skip reasons has a dedicated ImportListStatus
// counter -- the CRD's four counts (item, added, excluded, removed) are
// exactly the ones design spec §4.2 lists, and adding a fifth is a schema
// change this task does not own. They are tallied in the structured log
// line instead, at "warn" so an operator watching logs can see them without
// a cluster-wide grep.
func syncKind(
	ctx context.Context, deps syncDeps, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind,
) kindResult {
	ctx, span := tracing.Start(ctx, "importlist.syncKind")
	defer span.End()
	log := logging.FromContext(ctx).With("importList", il.Namespace+"/"+il.Name, "kind", kind)

	if !supportedKinds[kind] {
		log.Info("importlist: kind not supported by this task yet (non-video kinds are G2 work); skipping")
		return kindResult{}
	}

	tokenStore := NewSecretTokenStore(deps.Client, il)
	provider, err := BuildProvider(ctx, deps.Client, il, kind, tokenStore, deps.HTTPClient)
	if err != nil {
		if errors.Is(err, ErrUnsupportedProviderKind) {
			log.Info("importlist: provider does not cover this kind; skipping", "reason", err)
			return kindResult{}
		}
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

	existingItems := make([]pkgimportlist.Item, 0, len(existing))
	byKey := make(map[string]StoredItem, len(existing))
	for _, si := range existing {
		existingItems = append(existingItems, si.Item)
		byKey[si.Item.Key()] = si
	}

	newSnapshot := make([]StoredItem, 0, len(included))
	var added int32
	for _, item := range included {
		id, err := resolveRequiredID(ctx, deps, kind, item.ExternalIDs)
		if err != nil {
			log.Warn("importlist: could not resolve the id this kind requires; skipping entry",
				"title", item.Title, "error", err)
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
			continue
		}
		if _, wasKnown := byKey[item.Key()]; !wasKnown {
			added++
		}
		newSnapshot = append(newSnapshot,
			StoredItem{Item: item, ObjectKind: string(kind), ObjectName: objectName, ResolvedID: id})
	}

	decisions, err := pkgimportlist.ApplySyncLevel(
		pkgimportlist.SyncLevel(il.Spec.SyncLevel), included, existingItems)
	if err != nil {
		return kindResult{
			fetched: int32(len(fetched)), added: added, excluded: excludedCount,
			err: fmt.Errorf("apply sync level: %w", err),
		}
	}

	var removed int32
	for _, d := range decisions {
		si, ok := byKey[d.Item.Key()]
		if !ok {
			continue
		}
		if err := applySyncDecision(ctx, deps.Client, il, si, d.Action); err != nil {
			log.Warn("importlist: sync-level action failed", "title", d.Item.Title,
				"action", d.Action, "error", err)
			// Left in place: a snapshot entry this cycle could not act on
			// is remembered again, so the next sync retries the same
			// decision instead of silently forgetting the item.
			newSnapshot = append(newSnapshot, si)
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

	return kindResult{
		fetched: int32(len(fetched)), added: added, excluded: excludedCount, removed: removed,
	}
}

// applySyncDecision turns one pkgimportlist.SyncDecision into a catalog
// write. SyncActionRemove ("remove the catalog item, keep files") and
// SyncActionRemoveAndDelete ("remove the catalog item and its files") are
// deliberately identical here: both delete the Movie or Series object this
// package created. The file-retention distinction between the two is about
// what happens to the underlying MediaFile and the bytes on disk, which is
// catalogarr's and importarr/worker/fileimport's domain, not this list
// sync's -- this task's brief does not specify that deeper behaviour, and
// deleting files this package never wrote would be a guess, which the
// project's own never-guess rule forbids. Both actions are logged as
// "removed" identically; the file-retention gap is a known limitation.
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
	case pkgimportlist.SyncActionRemove, pkgimportlist.SyncActionRemoveAndDelete:
		if si.ObjectKind == string(commonv1.MediaKindSeries) {
			return deleteSeries(ctx, c, il.Namespace, si.ObjectName)
		}
		return deleteMovie(ctx, c, il.Namespace, si.ObjectName)
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
