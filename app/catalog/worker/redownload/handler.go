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

// Package redownload is spec §8.3's last clause: when a Download fails,
// catalogarr frees the item's grab lease and publishes a redownload search.
//
// Until gap fix Y3 nothing did. grabarr published
// clustarr.evt.download.download.failed on the edge and the item's reconciler
// stopped naming the Download in status.activeDownloadRef (a Failed Download
// is terminal, ruling R-5), but schema.SearchReasonRedownload had no producer
// and DownloadSpec.GrabbedBy's "redownload" was never written: a failed item
// read Wanted again and waited for the twelve-hourly wanted sweep.
//
// The Handler consumes events.ConsumerCatalogRedownload and, per failed
// Download:
//
//  1. frees the clustarr-leases keys the Download holds on every item it
//     targeted (grab.FreeLeases: a movie's one key, a season pack's key per
//     episode, and only keys still naming this Download);
//  2. decides whether to search again at all (Redownloads) -- a local fault
//     does not, after Radarr;
//  3. waits briefly for grabarr to blocklist the failed release, so the
//     search cannot pick it again;
//  4. publishes one catalog.SearchTask, reason redownload, per targeted item
//     that still exists and is monitored, on the normal lane (§8.2: "normal =
//     add/redownload"), under a Msg-Id derived from the failed Download's UID
//     and the item, so a redelivered failure never searches twice.
//
// The search worker maps reason redownload onto grabbedBy=redownload
// (search.grabSource), so the grab that follows -- immediate or held by a
// DelayProfile -- records it on the new Download.
//
// It writes no Kubernetes object. activeDownloadRef, which §8.3 also has this
// path clear, is the item reconciler's alone (R-5) and already drops a
// terminal Download.
//
// # Radarr and Sonarr
//
// Radarr's RedownloadFailedDownloadService handles DownloadFailedEvent by
// pushing a MoviesSearchCommand for the movie, unless the user asked to skip
// the redownload or disabled AutoRedownloadFailed
// (src/NzbDrone.Core/Download/RedownloadFailedDownloadService.cs). Clustarr
// has neither setting and always redownloads. MoviesSearchCommand searches
// only a movie that is `Monitored && IsAvailable()` unless a user invoked it
// (IndexerSearch/MoviesSearchService.cs), which is why an unmonitored item
// is skipped here: the decision engine would reject every release for it
// anyway, after spending an indexer query.
//
// A LOCAL fault is not a failed download in Radarr at all. SABnzbd's
// "Unpacking failed, write error or disk is full?" maps to
// DownloadItemStatus.Warning, not Failed (Download/Clients/Sabnzbd/
// Sabnzbd.cs), and qBittorrent's "error" state is a Warning "so failed
// download handling isn't triggered" (Download/Clients/QBittorrent/
// QBittorrent.cs). No DownloadFailedEvent, so no blocklist and no
// redownload: another release would fail on the same full disk. Clustarr's
// diskFull and writeError therefore free the lease and do not search; the
// item reads Wanted again and the wanted sweep searches it on its next pass,
// by when an operator has had the chance to make room. grabarr does not
// blocklist them either (the same ruling, from the other side).
//
// Sonarr's version searches a failed single episode, a failed whole season
// as a SeasonSearchCommand, and a failed multi-episode release episode by
// episode (Sonarr's RedownloadFailedDownloadService.cs). Clustarr's search
// worker searches exactly one item and has no season search, so a failed
// pack is searched episode by episode -- Sonarr's multi-episode branch --
// and each episode's search may still grab a pack if the RSS path offers
// one.
//
// # Registration
//
// app/catalog/run.go's setupQueueWorkers, with the other queue workers:
//
//	h := redownload.NewHandler(mgr.GetClient(), bus)
//	h.Topology = &topo // o.BusTopology()
//	if err := h.SetupWithManager(mgr, bus); err != nil { ... }
package redownload

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=albums;books;audiobooks;issues,verbs=get;list;watch

// MaxEventAge is how old a failure may be and still be searched for.
//
// ConsumerCatalogRedownload is a durable on CLUSTARR_EVENTS, whose retention
// is a week, and a new durable delivers from the start of the stream: the
// release that created it would otherwise replay every failure of the past
// seven days as a burst of searches against every indexer's query limit.
// A day is two wanted sweeps; anything older is the sweep's (the item has
// read Wanted since the failure). The lease is still freed.
const MaxEventAge = 24 * time.Hour

// blocklistSettle and blocklistSettleAttempts bound the wait for grabarr to
// blocklist a failed release before searching (awaitBlocklist): up to two
// five-second redeliveries, then the search goes ahead regardless. The
// consumer's MaxDeliver (6) leaves four deliveries for real failures after
// them.
const (
	blocklistSettle         = 5 * time.Second
	blocklistSettleAttempts = 3
)

// queueFullRetry is how long to wait when the catalogarr work stream refuses
// a search. Items already published are deduplicated on the redelivery.
const queueFullRetry = time.Minute

// Handler is the catalogarr-redownload consumer. See the package doc.
type Handler struct {
	// Client reads the failed Download and the items it targeted: the
	// manager's cached client.
	Client client.Client

	// Bus holds clustarr-leases and publishes the searches.
	Bus events.Bus

	// Topology is the bus topology this process installed --
	// k8s.Options.BusTopology(), the value run.go hands
	// k8s.EnsureTopology. Nil means events.Default().
	Topology *events.Topology

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time
}

// NewHandler returns a Handler over c and bus.
func NewHandler(c client.Client, bus events.Bus) *Handler {
	return &Handler{Client: c, Bus: bus}
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Subscription is events.ConsumerCatalogRedownload from the topology, so the
// tuning lives in one place.
func (h *Handler) Subscription() events.Subscription {
	topo := events.Default()
	if h.Topology != nil {
		topo = *h.Topology
	}
	spec, ok := topo.Consumer(events.ConsumerCatalogRedownload)
	if !ok {
		// A zero Subscription fails Subscription.Validate loudly at
		// Subscribe time rather than consuming nothing.
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager subscribes the consumer on every replica
// (k8s.EveryReplica): the durable consumer, not the leader lease, is what
// keeps two replicas off one failure.
func (h *Handler) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, h.Subscription(), h.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe redownload: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// LocalFailure reports whether reason blames the machine rather than the
// release: the disk filled, or a write failed. See the package doc for why
// such a failure is not searched again.
func LocalFailure(reason downloadv1alpha1.DownloadFailureReason) bool {
	switch reason {
	case downloadv1alpha1.DownloadFailureDiskFull, downloadv1alpha1.DownloadFailureWriteError:
		return true
	}
	return false
}

// Redownloads reports whether a download event with action and reason leads
// to a redownload search. A blocklisted Download always does -- someone
// blamed its release, which is Radarr's mark-as-failed -- and a failed one
// does unless the fault was local.
func Redownloads(action string, reason downloadv1alpha1.DownloadFailureReason) bool {
	switch action {
	case events.ActionBlocklisted:
		return true
	case events.ActionFailed:
		return !LocalFailure(reason)
	}
	return false
}

// MsgID is the Nats-Msg-Id of the redownload search for item after the
// Download ref failed: "<download-uid>:redownload:<kind>/<name>". It is a
// function of the failed Download and the item alone, so a redelivered
// failure event -- or the blocklisted event that follows a failed one --
// publishes a duplicate the work stream's one-hour window absorbs. A ref
// with no UID falls back to its namespace/name, which is as deterministic.
func MsgID(ref schema.Ref, item commonv1.MediaRef) string {
	id := ref.UID
	if id == "" {
		id = ref.Namespace + "/" + ref.Name
	}
	return fmt.Sprintf("%s:redownload:%s/%s", id, item.Kind, item.Name)
}

// Handle frees the failed Download's leases and publishes its redownload
// searches. Every step is idempotent, so a redelivery re-runs all of it.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "redownload.Handler.Handle")
	defer span.End()

	var evt schema.DownloadEvent
	if err := schema.Decode(env.Schema, env.Data, &evt); err != nil {
		return events.Discard("redownload: undecodable DownloadEvent", err)
	}
	if evt.Action != events.ActionFailed && evt.Action != events.ActionBlocklisted {
		// The consumer filters these two; anything else is not ours.
		return nil
	}
	ns := evt.DownloadRef.Namespace
	if ns == "" {
		ns, _, _ = strings.Cut(env.Key, "/")
	}
	if ns == "" || evt.DownloadRef.Name == "" {
		return events.Discard("redownload: the event names no namespaced Download",
			fmt.Errorf("downloadRef=%+v key=%q", evt.DownloadRef, env.Key))
	}
	reason := downloadv1alpha1.DownloadFailureReason(evt.Reason)
	ctx = logging.With(ctx, "namespace", ns, "download", evt.DownloadRef.Name,
		"action", evt.Action, "reason", evt.Reason)
	log := logging.FromContext(ctx)

	items, err := grab.StatusTargets(evt.Media, evt.Media.Keys)
	if err != nil {
		// Nothing the grab path grabs, so no lease to free and no search
		// that could lead anywhere. No redelivery changes that.
		log.Warn("redownload: the failed Download targets nothing the grab path grabs; ignoring",
			"kind", string(evt.Media.Kind), "item", evt.Media.Name, "error", err)
		return nil
	}

	freed, err := grab.FreeLeases(ctx, h.Bus.KV(events.BucketLeases), ns, evt.Media, evt.DownloadRef.Name)
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}

	if !Redownloads(evt.Action, reason) {
		log.Info("redownload: a local fault is not the release's; freed the lease, not searching again",
			"freed", len(freed))
		return nil
	}
	if !evt.At.IsZero() && h.now().Sub(evt.At) > MaxEventAge {
		log.Info("redownload: the failure is older than the redownload window; the wanted sweep owns the item",
			"at", evt.At, "freed", len(freed))
		return nil
	}
	if err := h.awaitBlocklist(ctx, m, ns, evt, reason); err != nil {
		return err
	}

	published := 0
	for _, item := range items {
		ok, err := h.searchable(ctx, ns, item)
		if err != nil {
			tracing.RecordError(span, err)
			return err
		}
		if !ok {
			continue
		}
		if err := h.publishSearch(ctx, ns, evt.DownloadRef, item); err != nil {
			if errors.Is(err, events.ErrQueueFull) {
				return events.Retry(queueFullRetry, err)
			}
			tracing.RecordError(span, err)
			return err
		}
		published++
	}
	log.Info("redownload: freed the lease and searched again", "freed", len(freed), "searches", published)
	return nil
}

// awaitBlocklist holds a failed event's search until grabarr has blocklisted
// the failed release, so the search does not pick that release again.
//
// The race is real: grabarr publishes failed on the Failed edge and applies
// the download.clustarr.io/blocklisted label after, while the search worker
// reads the blocklist from the label. A search that wins it ranks the failed
// release first again; the grab then refuses it (a terminal Download already
// has its deterministic name) and the item is left with nothing until the
// wanted sweep.
//
// So a failed event whose reason grabarr blocklists is retried, a few
// seconds at a time, until the Download carries the label, is Blocklisted,
// is being deleted, or is gone -- and after blocklistSettleAttempts
// deliveries it searches anyway, because a blocklist that never comes (an
// older grabarr) must not cost the redownload. A blocklisted event needs no
// wait: its label is what produced it. An unknown reason is not blocklisted
// by grabarr, so it does not wait either.
func (h *Handler) awaitBlocklist(ctx context.Context, m events.Message, ns string,
	evt schema.DownloadEvent, reason downloadv1alpha1.DownloadFailureReason,
) error {
	if evt.Action != events.ActionFailed || reason == "" || reason == downloadv1alpha1.DownloadFailureNone {
		return nil
	}
	var dl downloadv1alpha1.Download
	err := h.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: evt.DownloadRef.Name}, &dl)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("redownload: get Download %s/%s: %w", ns, evt.DownloadRef.Name, err)
	}
	if evt.DownloadRef.UID != "" && string(dl.UID) != evt.DownloadRef.UID {
		// Another Download under the same name; the failed one is gone.
		return nil
	}
	if dl.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue ||
		dl.Status.Phase == downloadv1alpha1.DownloadPhaseBlocklisted ||
		!dl.DeletionTimestamp.IsZero() {
		return nil
	}
	if m.Attempt() < blocklistSettleAttempts {
		return events.Retry(blocklistSettle,
			fmt.Errorf("redownload: waiting for grabarr to blocklist %s/%s before searching", ns, dl.Name))
	}
	logging.FromContext(ctx).Warn("redownload: grabarr has not blocklisted the failed release; searching anyway, "+
		"and the search may pick the same release again", "attempt", m.Attempt())
	return nil
}

// searchable reports whether item should be searched again: it still exists,
// is not being deleted, and is monitored. Radarr searches only a monitored
// movie on an automatic trigger; see the package doc.
func (h *Handler) searchable(ctx context.Context, ns string, item commonv1.MediaRef) (bool, error) {
	obj, monitored, err := newItem(item.Kind)
	if err != nil {
		// Unreachable: item came from grab.StatusTargets.
		return false, err
	}
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: item.Name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			logging.FromContext(ctx).Info("redownload: the item is gone; nothing to search for",
				"kind", string(item.Kind), "item", item.Name)
			return false, nil
		}
		return false, fmt.Errorf("redownload: get %s %s/%s: %w", item.Kind, ns, item.Name, err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return false, nil
	}
	if !ptr.Deref(monitored(), true) {
		logging.FromContext(ctx).Info("redownload: the item is not monitored; not searching",
			"kind", string(item.Kind), "item", item.Name)
		return false, nil
	}
	return true, nil
}

// newItem returns an empty object of a grab status-target kind and a reader
// of its spec.monitored once filled.
func newItem(kind commonv1.MediaKind) (client.Object, func() *bool, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		o := &catalogv1alpha1.Movie{}
		return o, func() *bool { return o.Spec.Monitored }, nil
	case commonv1.MediaKindEpisode:
		o := &catalogv1alpha1.Episode{}
		return o, func() *bool { return o.Spec.Monitored }, nil
	case commonv1.MediaKindAlbum:
		o := &catalogv1alpha1.Album{}
		return o, func() *bool { return o.Spec.Monitored }, nil
	case commonv1.MediaKindBook:
		o := &catalogv1alpha1.Book{}
		return o, func() *bool { return o.Spec.Monitored }, nil
	case commonv1.MediaKindAudiobook:
		o := &catalogv1alpha1.Audiobook{}
		return o, func() *bool { return o.Spec.Monitored }, nil
	case commonv1.MediaKindIssue:
		o := &catalogv1alpha1.Issue{}
		return o, func() *bool { return o.Spec.Monitored }, nil
	}
	return nil, nil, fmt.Errorf("%w: %s", grab.ErrUnsupportedKind, kind)
}

// publishSearch enqueues one item's redownload search at the normal tier,
// shaped as every other SearchTask producer shapes it (the envelope key names
// the item, the subject carries its media key).
func (h *Handler) publishSearch(ctx context.Context, ns string, failed schema.Ref, item commonv1.MediaRef) error {
	schemaName, data, err := schema.Encode(schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: item.Kind, Name: item.Name},
		Reason:   schema.SearchReasonRedownload,
	})
	if err != nil {
		return events.Discard("redownload: encode SearchTask", err)
	}
	env := &events.Envelope{
		ID:     MsgID(failed, item),
		Type:   "catalog.SearchTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		Key:    ns + "/" + item.Name,
		Time:   h.now().UTC(),
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := h.Bus.Publish(ctx, events.WorkSearchSubject(events.PriorityNormal, grab.MediaKey(ns, item)), env); err != nil {
		return fmt.Errorf("redownload: publish the search for %s %s/%s: %w", item.Kind, ns, item.Name, err)
	}
	return nil
}
