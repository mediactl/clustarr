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
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Handler is the clustarr.work.catalogarr.metadata.<tier>.<mediaKey>
// consumer: the only place that reads a MetadataTask, calls a provider
// through the Registry, and patches status.metadata back -- and ONLY
// status.metadata (§3's single-writer rule; §8.1).
type Handler struct {
	Client   client.Client
	Registry *pkgmetadata.Registry
	Cache    pkgmetadata.Cache
	Now      func() time.Time // nil = time.Now
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

	if err := h.Client.Get(ctx, key, target); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("target object no longer exists", err)
		}
		tracing.RecordError(span, err)
		return fmt.Errorf("metadata: get %s: %w", key, err)
	}

	ids, err := externalIDs(target)
	if err != nil {
		return events.Discard("cannot derive external ids", err)
	}
	ck := cacheKey(task.MediaRef.Kind, ids)

	var cached any
	switch target.(type) {
	case *catalogv1alpha1.Movie:
		var v pkgmetadata.Movie
		if hit, err := h.Cache.Get(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Series:
		var v pkgmetadata.Series
		if hit, err := h.Cache.Get(ctx, ck, &v); err != nil {
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
			return settlement(err)
		}
		fetchSpan.End()
		result = v
	}

	var ac k8s.ApplyConfiguration
	var ttl time.Duration
	switch v := result.(type) {
	case *pkgmetadata.Movie:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindMovie, movieRefreshState(v, now()), refreshedAt(target))
		ac = catalogac.Movie(key.Name, key.Namespace).WithStatus(
			catalogac.MovieStatus().WithMetadata(buildMovieMetadataAC(v, now())))
	case *pkgmetadata.Series:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindSeries, seriesRefreshState(v, now()), refreshedAt(target))
		ac = catalogac.Series(key.Name, key.Namespace).WithStatus(
			catalogac.SeriesStatus().WithMetadata(buildSeriesMetadataAC(v, now())))
	default:
		return events.Discard("registry returned an unexpected type", fmt.Errorf("%T", result))
	}

	if cached == nil {
		if err := h.Cache.Set(ctx, ck, result, ttl); err != nil {
			// A cache-write failure must not fail the task: the status
			// write below is what §8.1 actually depends on.
			tracing.RecordError(span, fmt.Errorf("metadata: cache set (non-fatal): %w", err))
		}
	}

	// k8s.ManagerCatalogarrMetadata, not ManagerCatalogarrWorker: this apply
	// carries status.metadata and nothing else, and server-side apply
	// releases every field its manager owns and this apply omits. While the
	// gateway and the grab path shared catalogarr-worker, each refresh
	// deleted the grab path's status.activeDownloadRef and
	// status.pendingGrab -- taking a delayed item out of Phase=Delayed back
	// to Wanted, where the wanted cron re-searched an item that already had a
	// grab scheduled -- and each grab deleted the metadata written here.
	if _, err := k8s.PatchStatus(ctx, h.Client, k8s.ManagerCatalogarrMetadata, ac); err != nil {
		tracing.RecordError(span, err)
		return fmt.Errorf("metadata: patch status.metadata: %w", err)
	}
	return nil
}
