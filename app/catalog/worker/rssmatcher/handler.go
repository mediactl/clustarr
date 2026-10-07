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

package rssmatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/version"
)

// matchRetry is how long Handle waits before re-reading the catalog after a
// failed List. It is short because the read is against the agent's cache and
// a failure there is a blip, and because CLUSTARR_RELEASES is a high-volume
// stream whose consumer holds only 256 in flight.
const matchRetry = 5 * time.Second

// sendRetry is how long Handle waits after a candidate it could not send.
const sendRetry = 10 * time.Second

// EvaluateFunc is pkg/decision.EvaluateRelease's signature, injectable for
// tests.
type EvaluateFunc func(
	ctx context.Context,
	t decision.Target,
	p quality.Profile,
	cat *catalogue.Catalogue,
	rels []commonv1.ReleaseInfo,
	o decision.Options,
) []decision.Decision

// Deps is everything the handler needs from the process around it.
type Deps struct {
	Client client.Client
	// Bus publishes the candidates.
	Bus events.Bus

	// Topology is the bus topology this process installed
	// (k8s.Options.BusTopology()); Subscription looks the consumer up in it.
	// Nil means events.Default().
	Topology *events.Topology

	// SceneMaps supplies TheXEM's scene-numbering table for a series, read
	// through the same code the search worker uses (search.SceneMappings),
	// so an RSS decision and a search decision read a scene number the same
	// way. Nil reads every release number literally.
	SceneMaps scenemap.Source

	// Catalogue is the resolved TRaSH custom-format corpus. Nil means
	// catalogue.LoadedCatalogue().
	Catalogue *catalogue.Catalogue

	// Evaluate defaults to decision.EvaluateRelease.
	Evaluate EvaluateFunc

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) catalogue() *catalogue.Catalogue {
	if d.Catalogue != nil {
		return d.Catalogue
	}
	return catalogue.LoadedCatalogue()
}

func (d Deps) evaluate() EvaluateFunc {
	if d.Evaluate != nil {
		return d.Evaluate
	}
	return decision.EvaluateRelease
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;qualityprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch

// Handler is the catalog-rss-matcher consumer.
type Handler struct {
	Deps Deps
}

// NewHandler returns a Handler over d.
func NewHandler(d Deps) *Handler { return &Handler{Deps: d} }

// Subscription is the catalog-rss-matcher durable consumer from §5's
// table, read from the topology the process installed (Deps.Topology).
func (h *Handler) Subscription() events.Subscription {
	topo := events.Default()
	if h.Deps.Topology != nil {
		topo = *h.Deps.Topology
	}
	spec, ok := topo.Consumer(events.ConsumerCatalogRSSMatcher)
	if !ok {
		// Unreachable with the default topology, which carries the consumer.
		// A zero Subscription fails Validate loudly at Subscribe time rather
		// than silently consuming nothing.
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager registers the subscription as a k8s.EveryReplica
// manager.Runnable, so every replica consumes the firehose.
func (h *Handler) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, h.Subscription(), h.Handle)
		if err != nil {
			return fmt.Errorf("catalog: subscribe rss-matcher: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle matches one firehose release to monitored items, evaluates it
// against each with the release rules, and sends each approved match to
// its owner as a Candidate (ADR-0019 §6.6 step 1). It writes nothing else:
// the owner's grab planner re-checks the item state, delays, narrows a pack
// and grabs.
//
// Most releases match nothing. That is the expected case on a firehose, and
// it acknowledges immediately.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	// Extract before Start, so this span continues index's RSS poll rather
	// than beginning a new trace per release.
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "rssmatcher.Handler.Handle")
	defer span.End()

	var rel schema.Release
	if err := schema.Decode(env.Schema, env.Data, &rel); err != nil {
		return events.Discard("rssmatcher: malformed Release", err)
	}
	// Every catalog consumer splits the envelope key on "/" for its
	// namespace -- "<namespace>/<indexerName>" on clustarr.rel.>.
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("rssmatcher: envelope key is not <namespace>/<indexerName>",
			fmt.Errorf("key=%q", env.Key))
	}
	ctx = logging.With(ctx, "kind", string(rel.Kind), "namespace", ns, "indexer", rel.Info.IndexerRef)

	// One scene-table read per series per message, shared by the match and
	// by every decision it leads to.
	scenes := h.sceneMemo()
	targets, err := matchWith(ctx, h.Deps.Client, ns, rel, scenes)
	if err != nil {
		return events.Retry(matchRetry, err)
	}
	if len(targets) == 0 {
		return nil
	}
	logging.FromContext(ctx).Debug("rssmatcher: release matched", "targets", len(targets))

	now := h.Deps.now()
	for _, ref := range targets {
		if err := h.decideOne(ctx, ns, ref, rel, scenes, now); err != nil {
			return err
		}
	}
	return nil
}

// decideOne evaluates the release against one matched item and, if the
// release rules approve it, sends it to the item's owner.
func (h *Handler) decideOne(ctx context.Context, ns string, ref commonv1.MediaRef, rel schema.Release, scenes sceneLookup, now time.Time) error {
	log := logging.FromContext(ctx).With("item", ref.Name)

	st, err := resolve(ctx, h.Deps.Client, ns, ref, now, scenes)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The item was deleted between the index lookup and this read.
			log.Debug("rssmatcher: matched item no longer exists")
			return nil
		}
		if errors.Is(err, errUnsupportedKind) {
			return nil
		}
		return events.Retry(matchRetry, err)
	}
	// The monitored pre-filter (P22) bounds the work; the owner re-checks.
	if !st.monitored || st.owner.UID == "" {
		return nil
	}

	profile, err := resolveQualityProfile(ctx, h.Deps.Client, st.qualityProfile, h.Deps.catalogue())
	if err != nil {
		// A missing or invalid profile is a configuration problem: skipping
		// this item and acknowledging keeps the firehose flowing.
		log.Warn("rssmatcher: cannot resolve the quality profile; skipping this item", "error", err)
		return nil
	}

	input := decision.Target{
		Kind:                ref.Kind,
		Key:                 events.MediaKey(string(ref.Kind), ns, ref.Name),
		Monitored:           st.monitored,
		Available:           st.available,
		RuntimeMinutes:      st.runtimeMinutes,
		OriginalLanguageTag: st.originalLanguageTag,
		// No IDQueryIndexers: a firehose release was not found by any query,
		// so nothing vouches for it but its own ids and title.
		Identity: st.identity,
	}
	decisions := h.Deps.evaluate()(ctx, input, profile, h.Deps.catalogue(), []commonv1.ReleaseInfo{rel.Info}, releaseOptions())
	if len(decisions) == 0 || !decisions[0].Approved {
		recordRejections(ref.Kind, decisions)
		return nil
	}
	if err := h.send(ctx, st, rel, decisions[0], now); err != nil {
		return events.Retry(sendRetry, err)
	}
	return nil
}

// send publishes one approved match to its owner's candidate intake, Msg-Id
// rss/<owner uid>/<sha1(indexer:guid)[:16]>, so a redelivered release is
// absorbed.
func (h *Handler) send(ctx context.Context, st resolveState, rel schema.Release, d decision.Decision, now time.Time) error {
	scored := search.RankScored([]decision.Decision{d}, decision.Options{}, nil, 1)
	if len(scored) == 0 {
		return nil
	}
	sr := scored[0]
	sr.Blocked = blockFor(rel, st.scope)
	c := schema.Candidate{
		Owner: st.owner, Targets: st.targets, Origin: schema.CandidateOriginRSS,
		GrabbedBy: commonv1.GrabSourceRSS, Release: sr, At: now.UTC(),
	}
	name, data, err := schema.Encode(c)
	if err != nil {
		return err
	}
	id := events.MsgIDForCandidate(events.CandidateMsgOriginRSS, st.owner.UID, rel.Info.IndexerRef, rel.Info.GUID)
	env := &events.Envelope{
		ID: id, Type: "catalog.Candidate", Schema: name, Source: "catalog-agent@" + version.String(),
		Key: st.owner.Namespace + "/" + st.owner.Name, Time: now.UTC(), Data: data,
	}
	tracing.Inject(ctx, env)
	if _, err := h.Deps.Bus.Publish(ctx, events.IntakeCandidateSubject(st.owner.Kind, st.owner.UID), env, events.WithMsgID(id)); err != nil {
		return fmt.Errorf("rssmatcher: send the candidate: %w", err)
	}
	metrics.SearchDecisionsTotal.WithLabelValues(string(rel.Kind), "candidate", "rss").Inc()
	return nil
}

// blockFor is the firehose release's block for the owner's scope or every
// item (ADR-0019 §6.14), nil when none blocks it.
func blockFor(rel schema.Release, scope string) *schema.ReleaseBlock {
	for i := range rel.Blocks {
		if b := rel.Blocks[i]; b.Scope == schema.BlockScopeGlobal || (scope != "" && b.Scope == scope) {
			return &b
		}
	}
	return nil
}

// sceneMemo returns a sceneLookup over Deps.SceneMaps that asks at most once
// per series for the life of one message. Nil when no source is wired.
func (h *Handler) sceneMemo() sceneLookup {
	if h.Deps.SceneMaps == nil {
		return nil
	}
	memo := map[int64][]decision.SceneMapping{}
	return func(ctx context.Context, tvdbID int64) []decision.SceneMapping {
		if rows, ok := memo[tvdbID]; ok {
			return rows
		}
		rows := search.SceneMappings(ctx, h.Deps.SceneMaps, tvdbID)
		memo[tvdbID] = rows
		return rows
	}
}

// recordRejections reports why a matched release was turned down, in
// metrics and nowhere else. The labels are kind, decision and reason -- all
// bounded -- and never the release title or the item name.
func recordRejections(kind commonv1.MediaKind, ds []decision.Decision) {
	for _, d := range ds {
		if d.Approved {
			continue
		}
		reason := string(commonv1.RejectionPermanent)
		if d.TemporarilyRejected {
			reason = string(commonv1.RejectionTemporary)
		}
		metrics.SearchDecisionsTotal.WithLabelValues(string(kind), "rejected", reason).Inc()
	}
}
