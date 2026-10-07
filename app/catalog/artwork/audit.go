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

package artwork

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

const (
	// DefaultAuditPace is Reaper.Pace when unset: the audit publishes only
	// while the target consumer lags fewer tasks than this (artwork design
	// §B.5 as amended 2026-10-07). After a bucket loss the audit is the whole
	// library, and CLUSTARR_WORK_CATALOGARR is a few MiB of discard-oldest
	// memory on single-node NATS, where queueing by the thousand silently
	// drops the neighbours' work (CLAUDE.md).
	DefaultAuditPace = 256

	// DefaultAuditCheck is Reaper.Check when unset: how often the bucket's
	// creation time is compared, one STREAM.INFO each.
	DefaultAuditCheck = time.Minute

	// DefaultAuditBacklog is Reaper.Backlog when unset: how often a sweep
	// runs while the last audit left tasks unpublished.
	DefaultAuditBacklog = 10 * time.Minute

	// lagReadEvery is how many publishes the audit makes on one read of a
	// consumer's lag before it reads again.
	lagReadEvery = 50

	// metaVersionNone labels ArtworkObjects for an object stored before
	// object metadata existed.
	metaVersionNone = "none"
)

// The audit's reasons, in the Msg-Id token and the ArtworkAuditTasksTotal
// label.
const (
	auditMissing = "missing"
	auditDigest  = "digest"
	auditMeta    = "meta"
)

// LagReader reads one durable's backlog (split §9.0): the audit publishes only
// while its consumer's lag is below Pace. events.StreamAdmin is one.
type LagReader interface {
	ConsumerState(ctx context.Context, stream, durable string) (events.ConsumerState, error)
}

// auditKinds are the eight kinds with status.artwork, in the order the audit
// visits them.
var auditKinds = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAlbum,
	commonv1.MediaKindAuthor, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
}

func (r *Reaper) auditing() bool { return r.Cache != nil && r.Publisher != nil }

func (r *Reaper) pace() int {
	if r.Pace > 0 {
		return r.Pace
	}
	return DefaultAuditPace
}

func (r *Reaper) checkEvery() time.Duration {
	if r.Check > 0 {
		return r.Check
	}
	return DefaultAuditCheck
}

func (r *Reaper) backlogEvery() time.Duration {
	if r.Backlog > 0 {
		return r.Backlog
	}
	return DefaultAuditBacklog
}

// pacer gates one consumer's publishes on its lag: it reads the lag, allows
// publishes while lag plus what it published since stays below pace, and
// reads again every lagReadEvery publishes.
type pacer struct {
	lag     LagReader
	durable string
	pace    int

	read  bool
	base  uint64 // the lag at the last read
	since int    // publishes since the last read
	full  bool   // stopped for this audit
}

// allow reports whether one more task may be published. A lag that cannot be
// read stops the pacer, with the error, rather than publish blind.
func (p *pacer) allow(ctx context.Context) (bool, error) {
	if p.lag == nil {
		return true, nil
	}
	if p.full {
		return false, nil
	}
	if !p.read || p.since >= lagReadEvery {
		st, err := p.lag.ConsumerState(ctx, events.StreamWorkCatalog, p.durable)
		if err != nil {
			p.full = true
			return false, fmt.Errorf("artwork: audit pacing, read %s's lag: %w", p.durable, err)
		}
		p.read, p.base, p.since = true, st.Lag(), 0
	}
	if p.base+uint64(p.since) >= uint64(p.pace) {
		p.full = true
		return false, nil
	}
	return true, nil
}

// audit compares every item's recorded artwork with the bucket listing
// (artwork design §B.5 as amended 2026-10-07) and publishes the repair or
// backfill each needs: an original that is missing, carries another digest
// or carries metadata below events.ArtworkMetaVersion gets one
// ArtworkFetchTask per item (its first reason); a Movie's or Series' overlay
// likewise gets a RenderOverlay task while status.overlay is set. Msg-Ids
// carry "audit-<reason>-<gen>", gen being the bucket's creation time, so a
// quiet bucket publishes nothing twice and a re-created one is audited
// afresh. A Put and the status apply that records it are not atomic, so an
// audit between them may publish a task for an item already being written;
// the task then finds it current and costs a read.
//
// It reports whether tasks were left unpublished (the pacing stopped it),
// and every error it met.
func (r *Reaper) audit(ctx context.Context, objects []events.ObjectInfo, gen string) (backlog bool, err error) {
	byName := make(map[string]events.ObjectInfo, len(objects))
	for _, o := range objects {
		byName[o.Name] = o
	}
	fetch := &pacer{lag: r.Lag, durable: events.ConsumerCatalogArtworkFetch, pace: r.pace()}
	render := &pacer{lag: r.Lag, durable: events.ConsumerCatalogArtworkRender, pace: r.pace()}
	check := func(name, recorded string) string {
		info, ok := byName[name]
		switch {
		case !ok:
			return auditMissing
		case info.Digest != recorded:
			return auditDigest
		case !MetaCurrent(info):
			return auditMeta
		}
		return ""
	}

	var errs []error
	publish := func(p *pacer, variant, reason string, send func() error) {
		ok, err := p.allow(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		if !ok {
			backlog = true
			return
		}
		if err := send(); err != nil {
			errs = append(errs, err)
			if errors.Is(err, events.ErrQueueFull) {
				p.full, backlog = true, true
			}
			return
		}
		p.since++
		metrics.ArtworkAuditTasksTotal.WithLabelValues(variant, reason).Inc()
	}

	for _, kind := range auditKinds {
		list, err := NewList(kind)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := r.Cache.List(ctx, list); err != nil {
			errs = append(errs, fmt.Errorf("artwork: audit, list %s: %w", kind, err))
			continue
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			errs = append(errs, fmt.Errorf("artwork: audit, read the %s list: %w", kind, err))
			continue
		}
		for _, ro := range items {
			obj, ok := ro.(client.Object)
			if !ok {
				continue
			}
			it, err := ItemOf(obj)
			if err != nil {
				continue
			}
			reason := ""
			for _, e := range it.Entries {
				if !KnownType(e.Type) {
					continue
				}
				name := events.ArtworkKey(kind, obj.GetUID(), string(e.Type), events.ArtworkVariantOriginal)
				if reason = check(name, e.Digest); reason != "" {
					break
				}
			}
			if reason != "" {
				token := "audit-" + reason + "-" + gen
				publish(fetch, events.ArtworkVariantOriginal, reason, func() error {
					return PublishFetchFor(ctx, r.Publisher, obj, kind, token)
				})
			}
			if ov := overlayOf(obj); ov != nil {
				name := events.ArtworkKey(kind, obj.GetUID(), string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOverlay)
				if reason := check(name, ov.Digest); reason != "" {
					token := "audit-" + reason + "-" + gen
					publish(render, events.ArtworkVariantOverlay, reason, func() error {
						return PublishRender(ctx, r.Publisher, obj, kind, token)
					})
				}
			}
		}
	}
	if backlog {
		logging.FromContext(ctx).Info("artwork: audit paced against consumer lag; the rest waits for the backlog tick")
	}
	return backlog, errors.Join(errs...)
}

// overlayOf is status.overlay of a Movie or Series, nil for any other kind.
func overlayOf(obj client.Object) *catalogv1alpha1.OverlayEntry {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		return o.Status.Overlay
	case *catalogv1alpha1.Series:
		return o.Status.Overlay
	}
	return nil
}

// countObjects sets ArtworkObjects from a bucket listing: the backfill's
// progress, by variant and metadata version.
func countObjects(objects []events.ObjectInfo) {
	counts := map[[2]string]int{}
	for _, o := range objects {
		variant := o.Name[strings.LastIndex(o.Name, "/")+1:]
		if variant != events.ArtworkVariantOriginal && variant != events.ArtworkVariantOverlay {
			continue
		}
		v := o.Metadata[events.ArtworkMetaKeyVersion]
		if v == "" {
			v = metaVersionNone
		}
		counts[[2]string{variant, v}]++
	}
	metrics.ArtworkObjects.Reset()
	for k, n := range counts {
		metrics.ArtworkObjects.WithLabelValues(k[0], k[1]).Set(float64(n))
	}
}
