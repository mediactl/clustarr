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
	"strconv"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// snapshot fetches every kind il.Spec.Kinds names and returns the listed
// set (ADR-0019 §7.6). A kind whose fetch failed is left out of Kinds, with
// its error joined into Error, so the controller never reads a failed fetch
// as "everything fell off the list"; a kind the provider cannot yield is
// left out silently, since the controller refuses it from the spec itself
// (R-10). A Trakt token Trakt refuses sets NeedsToken.
//
// The agent does no diffing, dedupe, exclusion or library lookup: those are
// the controller's (P72-P81). It only resolves the id each movie and series
// is keyed on (P77's resolution, through the metadata gateway), since that
// is provider I/O.
func (w *Worker) snapshot(ctx context.Context, il *catalogv1alpha1.ImportList) schema.ListSnapshot {
	ctx, span := tracing.Start(ctx, "importlist.Worker.snapshot")
	defer span.End()

	snap := schema.ListSnapshot{
		List: schema.ItemRef{Kind: "ImportList", Ref: schema.Ref{
			Namespace: il.Namespace, Name: il.Name, UID: string(il.UID),
		}},
	}
	tokens := ReadOnlyTokens{Reader: importliststate.NewTokenReader(w.reader(), il)}
	opts := ProviderOptions{HTTPClient: w.HTTPClient, TraktBaseURL: w.TraktBaseURL, PlexBaseURL: w.PlexBaseURL}
	var errs []error
	for _, k := range il.Spec.Kinds {
		kind := commonv1.MediaKind(k)
		if !importliststate.CanYield(il.Spec, kind) {
			continue
		}
		ks, needsToken, err := w.snapshotKind(ctx, il, kind, tokens, opts)
		if needsToken {
			snap.NeedsToken = true
		}
		if err != nil {
			tracing.RecordError(span, err)
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			continue
		}
		snap.Kinds = append(snap.Kinds, ks)
	}
	if len(errs) > 0 {
		snap.Error = errors.Join(errs...).Error()
	}
	// Seconds, in UTC: status.lastSyncAt, which the controller compares a
	// snapshot's SyncedAt against, round-trips at that precision.
	snap.SyncedAt = w.now().UTC().Truncate(time.Second)
	return snap
}

// snapshotKind fetches one kind and resolves its items' ids. needsToken is
// true when Trakt refused the list's token.
func (w *Worker) snapshotKind(
	ctx context.Context, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind,
	tokens pkgimportlist.TokenStore, opts ProviderOptions,
) (ks schema.ListKindSnapshot, needsToken bool, err error) {
	log := logging.FromContext(ctx).With("kind", kind)
	provider, err := BuildProvider(ctx, w.reader(), il, kind, tokens, opts)
	if err != nil {
		return schema.ListKindSnapshot{}, false, fmt.Errorf("build provider: %w", err)
	}
	fetched, err := provider.Fetch(ctx)
	if err != nil {
		if il.Spec.Trakt != nil && tokenRefused(err) {
			return schema.ListKindSnapshot{}, true, fmt.Errorf("trakt refused the list's token: %w", err)
		}
		return schema.ListKindSnapshot{}, false, fmt.Errorf("fetch: %w", err)
	}

	ks = schema.ListKindSnapshot{Kind: string(kind), Fetched: int32(len(fetched)), Items: make([]schema.ListedItem, 0, len(fetched))}
	type resolution struct {
		id  int64
		err error
	}
	resolved := map[string]resolution{}
	for _, item := range fetched {
		li := schema.ListedItem{
			IDs: map[string]string(importliststate.ToMetadataExternalIDs(item.ExternalIDs)), Title: item.Title, Year: item.Year,
		}
		if len(li.IDs) == 0 {
			li.IDs = nil
		}
		if kind == commonv1.MediaKindMovie || kind == commonv1.MediaKindSeries {
			r, ok := resolved[item.Key()]
			if !ok {
				r.id, r.err = w.resolveRequiredID(ctx, kind, item.ExternalIDs)
				resolved[item.Key()] = r
				if r.err != nil {
					log.Warn("importlist: could not resolve the id this kind requires",
						"title", item.Title, "error", r.err)
				}
			}
			if r.err != nil {
				li.Unresolved = true
			} else {
				li.ResolvedID = r.id
			}
		}
		ks.Items = append(ks.Items, li)
	}
	return ks, false, nil
}

// resolveRequiredID returns the external id kind's spec identifies items
// by: TMDB for movie, TVDB for series. If ids already carries it, no RPC is
// made. Otherwise it asks the metadata gateway's resolve verb to fill the
// gap from whatever ids ARE present, the same rpc.catalog.metadata.resolve
// call app/import/worker/rescan.resolveIMDb makes.
func (w *Worker) resolveRequiredID(
	ctx context.Context, kind commonv1.MediaKind, ids pkgimportlist.ExternalIDs,
) (int64, error) {
	key := metadataKeyFor(kind)
	if direct := directID(ids, kind); direct != "" {
		id, err := strconv.ParseInt(direct, 10, 64)
		if err != nil || id <= 0 {
			return 0, fmt.Errorf("the list's %s id %q is not a number", key, direct)
		}
		return id, nil
	}
	if w.Bus == nil {
		return 0, fmt.Errorf("no bus to resolve a %s id with", key)
	}
	timeout := w.MetadataTimeout
	if timeout <= 0 {
		timeout = defaultMetadataTimeout
	}
	rpcCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := schema.MetadataRequest{Kind: kind, IDs: ToMetadataExternalIDs(ids)}
	var resp schema.MetadataResponse
	if err := w.Bus.Request(rpcCtx, events.RPCMetadataResolve, req, &resp); err != nil {
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
