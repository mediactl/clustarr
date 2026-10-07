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

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// Handler is the clustarr.work.catalog.metadata.<tier>.<mediaKey>
// consumer: the only place that reads a MetadataTask and calls a provider
// through the Registry. It answers in clustarr-item-metadata (ADR-0019
// §7.1): the kind's status.metadata block exactly as the apply it once made
// would have declared it, which the item key (the Artist and Author
// reconcilers for theirs) incorporates under catalog-metadata. It writes no
// Kubernetes object; status.artwork is the item key's, from the artwork
// objects' metadata (A5.2).
type Handler struct {
	Client client.Client

	// Reader reads a series' episodes, uncached (mgr.GetAPIReader()), for
	// their people after a Series refresh.
	Reader client.Reader

	Registry *pkgmetadata.Registry
	Cache    pkgmetadata.Cache

	// Records is the item-metadata records writer, the answer's only path.
	// The artwork fetcher, in this same process, writes its failures
	// through the same writer (records.Writer.Update).
	Records *records.Writer[*schema.ItemMetadataRecord]

	// Extended is the clustarr-metadata-extended bucket: every successful
	// movie or series refresh replaces the item's people and similar titles
	// there (pkg/metadata/extended). Nil writes none.
	Extended events.KV

	Now func() time.Time // nil = time.Now
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}

	env := m.Envelope()
	var task schema.MetadataTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable MetadataTask", err)
	}
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}

	target, err := newTarget(task.MediaRef.Kind)
	if err != nil {
		return events.Discard("unsupported media kind", err)
	}
	key := client.ObjectKey{Namespace: ns, Name: task.MediaRef.Name}

	ctx, span := tracing.Start(ctx, "metadata.Handler.Handle")
	defer span.End()
	// A provider fetch, its enrichment and a series' episode people can
	// outlast the consumer's first BackOff step, which replaces AckWait as
	// the redelivery timer.
	stopHeartbeat := artwork.KeepAlive(ctx, m, artwork.HeartbeatInterval)
	defer stopHeartbeat()

	if err := h.Client.Get(ctx, key, target); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("target object no longer exists", err)
		}
		tracing.RecordError(span, err)
		return fmt.Errorf("metadata: get %s: %w", key, err)
	}
	// The record is keyed by the item the task names. A task for an item
	// since deleted and re-created under the same name answers nothing: the
	// new item's own pass asks again.
	item := task.Item
	if item.UID == "" {
		item = metadatainputs.ItemRef(target)
	} else if item.UID != string(target.GetUID()) {
		// Acked, not dead-lettered: a dead letter would be projected onto
		// the new item, which asked for nothing.
		logging.FromContext(ctx).DebugContext(ctx, "metadata: a task for an item since re-created; nothing to answer",
			"kind", task.MediaRef.Kind, "key", key.String(), "taskUID", item.UID)
		return nil
	}
	inputs := task.Inputs
	if inputs.SchemaVersion == 0 && len(inputs.ProviderIDs) == 0 {
		inputs = metadatainputs.For(target)
	}

	ids, err := externalIDs(target)
	if err != nil {
		return events.Discard("cannot derive external ids", err)
	}
	ck := cacheKey(task.MediaRef.Kind, ids)

	// A forced refresh (RefreshEpoch set, spec §5) never takes the cache
	// hit: the operator asked for what the provider publishes now, which
	// the cached document may lack. The fetch still lands in the cache.
	cacheGet := func(ctx context.Context, key string, out any) (bool, error) {
		if task.RefreshEpoch > 0 || task.Inputs.Force {
			return false, nil
		}
		return h.Cache.Get(ctx, key, out)
	}

	var cached any
	switch target.(type) {
	case *catalogv1alpha1.Movie:
		var v pkgmetadata.Movie
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Series:
		var v pkgmetadata.Series
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Artist:
		var v pkgmetadata.Artist
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Album:
		var v pkgmetadata.Album
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Author:
		var v pkgmetadata.Author
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Book:
		var v pkgmetadata.Book
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Audiobook:
		var v pkgmetadata.Audiobook
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Comic:
		var v pkgmetadata.ComicVolume
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	}

	result := cached
	if result == nil {
		fetchCtx, fetchSpan := tracing.Start(ctx, "metadata.Registry.Lookup")
		v, err := h.Registry.Lookup(fetchCtx, task.MediaRef.Kind, ids)
		if err != nil {
			tracing.RecordError(fetchSpan, err)
			fetchSpan.End()
			verdict := settlement(err)
			var discard *events.DiscardError
			h.answerFailure(ctx, item, inputs, err, !errors.As(verdict, &discard))
			return verdict
		}
		fetchSpan.End()
		// Crosswalk and artwork are folded in before the document is
		// cached, so a cache hit serves them without calling those
		// providers again.
		enrich(ctx, h.Registry, task.MediaRef.Kind, ids, knownExternalIDs(target), v)
		result = v
	}

	// Each kind renders its status.metadata once, from the provider
	// document, exactly as the apply the gateway once made declared it; the
	// record carries that block as JSON and the item key applies it
	// (ADR-0019 §7.1). refreshed is second-precision, what the apiserver
	// keeps, so the record's refreshedAt and the block's are one instant.
	refreshed := now().UTC().Truncate(time.Second)
	var (
		ttl time.Duration
		md  any
	)
	switch v := result.(type) {
	case *pkgmetadata.Movie:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindMovie, movieRefreshState(v, now()), refreshedAt(target))
		// v.IDs, not the outer ids: enrich (above) has already merged in
		// whatever the Resolvers crosswalked, and a ratings provider keyed
		// by a crosswalked id (mdblist by tmdb, omdb by imdb) needs that,
		// not the bare spec id externalIDs(target) returned. v.Ratings is
		// the seed: whatever the primary MovieProvider.Movie fetch already
		// produced (tmdb's mapMovie always sets "tmdb"), so enrichRatings
		// never re-fetches a source this same call already has for free.
		ratings := enrichRatings(ctx, h.Registry, task.MediaRef.Kind, v.IDs, v.Ratings, knownRatings(target))
		// Before the cache write below, so a cached Movie carries the
		// summary and is not fetched again.
		withCollectionSummary(ctx, h.Registry, v.Collection)
		mmd := buildMovieMetadataAC(v, ratings, refreshed)
		if mmd.Collection != nil {
			if id := plexCollection(ctx, h.Registry, v.IDs[pkgmetadata.KeyPlex], knownPlexCollection(target, v.Collection)); id != "" {
				mmd.Collection.WithPlexID(id)
			}
		}
		md = mmd
	case *pkgmetadata.Series:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindSeries, seriesRefreshState(v, now()), refreshedAt(target))
		ratings := enrichRatings(ctx, h.Registry, task.MediaRef.Kind, v.IDs, v.Ratings, knownRatings(target))
		smd := buildSeriesMetadataAC(v, ratings, refreshed)
		withPlexSeasons(smd, plexSeasons(ctx, h.Registry, v.IDs, knownPlexSeasons(target)))
		md = smd
	case *pkgmetadata.Artist:
		// RefreshTTL's MediaKindArtist/MediaKindAlbum branch is a flat 7-day
		// cadence regardless of state (pkg/metadata/refresh.go); Active is
		// passed only to avoid the two magic strings (RefreshStateSearch,
		// RefreshStateCrosswalk) RefreshTTL special-cases ahead of its
		// per-kind switch, not because it carries meaning for this kind.
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindArtist, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md = buildArtistMetadataAC(v, refreshed)
	case *pkgmetadata.Album:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindAlbum, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md = buildAlbumMetadataAC(v, refreshed)
	case *pkgmetadata.Author:
		// MediaKindAuthor/Book/Audiobook are likewise a flat 30-day cadence
		// regardless of state; see the Artist/Album comment above.
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindAuthor, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md = buildAuthorMetadataAC(v, refreshed)
	case *pkgmetadata.Book:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindBook, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md = buildBookMetadataAC(v, refreshed)
	case *pkgmetadata.Audiobook:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindAudiobook, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md = buildAudiobookMetadataAC(v, refreshed)
	case *pkgmetadata.ComicVolume:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindComic, comicRefreshState(v), refreshedAt(target))
		md = buildComicMetadataAC(v, refreshed)
	default:
		return events.Discard("registry returned an unexpected type", fmt.Errorf("%T", result))
	}

	if cached == nil {
		if err := h.Cache.Set(ctx, ck, result, ttl); err != nil {
			// A cache-write failure must not fail the task: the record
			// below is what the item key depends on.
			tracing.RecordError(span, fmt.Errorf("metadata: cache set (non-fatal): %w", err))
		}
	}

	block, err := json.Marshal(md)
	if err != nil {
		return events.Discard("metadata: encode the status.metadata block", err)
	}
	if err := h.answer(ctx, item, inputs, refreshed, block); err != nil {
		if errors.Is(err, records.ErrTooLarge) {
			return events.Discard("metadata: the record is over the bucket's value limit", err)
		}
		tracing.RecordError(span, err)
		return fmt.Errorf("metadata: write the item-metadata record: %w", err)
	}
	h.writeExtended(ctx, task.MediaRef.Kind, target, result)
	if series, ok := target.(*catalogv1alpha1.Series); ok {
		h.refreshEpisodePeople(ctx, series, now(), m.InProgress)
	}
	return nil
}

// writeExtended replaces the item's extended-metadata document. Like the
// cache write, a failure is recorded and never fails the task: the record is
// what the rest of the system depends on, and the next refresh rewrites
// the document.
func (h *Handler) writeExtended(ctx context.Context, kind commonv1.MediaKind, target client.Object, result any) {
	if h.Extended == nil {
		return
	}
	var doc extended.Doc
	switch v := result.(type) {
	case *pkgmetadata.Movie:
		doc = extended.FromPeople(v.People, v.Similar)
		doc.Collection = extended.FromCollection(v.Collection)
	case *pkgmetadata.Series:
		doc = extended.FromPeople(v.People, nil)
	default:
		return
	}
	b, err := extended.Encode(doc)
	if err == nil {
		_, err = h.Extended.Put(ctx, extended.Key(kind, target.GetUID()), b)
	}
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "metadata: write the extended document (non-fatal)",
			"kind", kind, "name", target.GetName(), "error", err)
	}
}

// answer writes the item's metadata record (ADR-0019 §7.1): the next Seq,
// the inputs the task asked for, the refresh time and the block, carrying
// the fetcher's artwork failures forward. The writer re-reads before its
// compare-and-swap.
func (h *Handler) answer(ctx context.Context, item schema.ItemRef, inputs schema.MetadataInputs,
	refreshed time.Time, block json.RawMessage,
) error {
	if h.Records == nil {
		return errors.New("metadata: no item-metadata records writer")
	}
	_, err := h.Records.Update(ctx, agentrecords.ItemMetadataKey(item.UID), func(cur *schema.ItemMetadataRecord, ok bool) (*schema.ItemMetadataRecord, bool) {
		next := &schema.ItemMetadataRecord{Inputs: inputs, RefreshedAt: refreshed, SchemaVersion: pkgmetadata.SchemaVersion, Metadata: block}
		ref := item
		next.Item = &ref
		next.State = records.StateAnswered
		if ok {
			next.Seq = cur.Seq + 1
			next.ArtworkFailures = cur.ArtworkFailures
		} else {
			next.Seq = 1
		}
		return next, true
	})
	return err
}

// answerFailure records a provider failure in the item's record (ADR-0019
// §7.1): the header's Failure and Transient, the inputs asked for, and
// whatever document the record held, kept, so a failure never empties what
// the item already incorporated. A write failure is logged: the task's own
// settlement is what retries.
func (h *Handler) answerFailure(ctx context.Context, item schema.ItemRef, inputs schema.MetadataInputs, cause error, transient bool) {
	if h.Records == nil {
		return
	}
	_, err := h.Records.Update(ctx, agentrecords.ItemMetadataKey(item.UID), func(cur *schema.ItemMetadataRecord, ok bool) (*schema.ItemMetadataRecord, bool) {
		next := &schema.ItemMetadataRecord{Inputs: inputs}
		if ok {
			*next = *cur
			next.Seq = cur.Seq + 1
		} else {
			next.Seq = 1
		}
		ref := item
		next.Item = &ref
		next.Inputs = inputs
		next.State = records.StateFailed
		next.Failure, next.Transient = cause.Error(), transient
		next.Failures++
		return next, true
	})
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "metadata: record a provider failure (non-fatal)",
			"kind", item.Kind, "name", item.Name, "error", err)
	}
}
