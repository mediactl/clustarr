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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
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
)

// matchRetry is how long Handle waits before re-reading the catalog after a
// failed List. It is short because the read is against the manager's cache and
// a failure there is a blip, and because CLUSTARR_RELEASES is a high-volume
// stream whose consumer holds only 256 in flight.
const matchRetry = 5 * time.Second

// grabRetry is how long Handle waits after a failed grab, matching the grab
// worker's own figure.
const grabRetry = 10 * time.Second

// EvaluateFunc is pkg/decision.Evaluate's signature, injectable for tests.
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
	Bus    events.Bus

	// Reader is an uncached reader -- manager.GetAPIReader() -- handed to
	// the grab path (grab.Deps.Reader), whose double-grab guard reads the
	// item's Downloads live. Through the cache a Download another path
	// created milliseconds earlier can be missed, and the RSS grab is the
	// likeliest racer of all: it fires the moment an indexer publishes a
	// release a search may just have grabbed. Nil falls back to Client, the
	// cache, which is only right in a test with no second writer.
	Reader client.Reader

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

	// Evaluate defaults to decision.Evaluate.
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
	return decision.Evaluate
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;mediafiles;delayprofiles;qualityprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch

// Handler is the catalogarr-rss-matcher consumer.
type Handler struct {
	Deps Deps
}

// NewHandler returns a Handler over d.
func NewHandler(d Deps) *Handler { return &Handler{Deps: d} }

// Subscription is the catalogarr-rss-matcher durable consumer from §5's
// table: the whole clustarr.rel.> firehose, AckWait 30s, MaxDeliver 6,
// BackOff 1s/5s/30s/2m/10m, MaxAckPending 256. It is read from the topology
// the process installed (Deps.Topology) rather than restated, so the tuning
// lives in one place and the consumer subscribed to is the one created.
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

// SetupWithManager registers the subscription as a manager.Runnable so it
// starts with the manager and drains on shutdown. See the package doc for the
// full registration the wiring task performs, including IndexFields.
//
// It is a k8s.EveryReplica rather than a manager.RunnableFunc: §3 runs the
// queue workers on every replica, and a bare RunnableFunc has no
// NeedLeaderElection method, so controller-runtime puts it behind the leader
// lease (see k8s.EveryReplica). Before Task C12a's review that meant exactly
// one replica consumed the release firehose, however many were scaled up.
func (h *Handler) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, h.Subscription(), h.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe rss-matcher: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle runs §8.7's per-release evaluation: match, decide, and hand an
// approved release to the same grab path a search uses.
//
// Most releases match nothing. That is the expected case on a firehose, and it
// acknowledges immediately without a single write.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	// Extract before Start, so this span continues indexarr's RSS poll rather
	// than beginning a new trace per release.
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "rssmatcher.Handler.Handle")
	defer span.End()

	var rel schema.Release
	if err := schema.Decode(env.Schema, env.Data, &rel); err != nil {
		return events.Discard("rssmatcher: malformed Release", err)
	}
	// Every catalogarr consumer splits the envelope key on "/" for its
	// namespace, and indexarr must follow the same convention on
	// clustarr.rel.> -- "<namespace>/<indexerName>". Guessing a namespace
	// instead would fan one indexer's releases across the whole cluster.
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("rssmatcher: envelope key is not <namespace>/<indexerName>",
			fmt.Errorf("key=%q", env.Key))
	}
	ctx = logging.With(ctx, "kind", string(rel.Kind), "namespace", ns, "indexer", rel.Info.IndexerRef)
	log := logging.FromContext(ctx)

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
	log.Debug("rssmatcher: release matched", "targets", len(targets))

	now := h.Deps.now()
	// One List of the namespace's blocklist, shared by every matched item:
	// the same loader the search worker uses, so the two paths agree on
	// what is blocklisted. A failed read retries rather than deciding as if
	// nothing were blocklisted -- nothing after this re-checks.
	blocklist, err := search.LoadBlocklist(ctx, h.Deps.Client, ns, now)
	if err != nil {
		return events.Retry(matchRetry, err)
	}
	for _, ref := range targets {
		if err := h.decideOne(ctx, ns, ref, rel, blocklist.Contains, scenes, now); err != nil {
			return err
		}
	}
	return nil
}

// decideOne evaluates the release against one matched item and, if it is
// approved, hands it to grab.Decide -- the same entry point the search
// worker's sink uses, which is what makes §8.7's "the same delay/lease/grab
// path" literally true.
func (h *Handler) decideOne(
	ctx context.Context,
	ns string,
	ref commonv1.MediaRef,
	rel schema.Release,
	blocklist func(infohash, title string) bool,
	scenes sceneLookup,
	now time.Time,
) error {
	log := logging.FromContext(ctx).With("item", ref.Name)

	st, err := resolve(ctx, h.Deps.Client, ns, ref, now, scenes)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The item was deleted between the index lookup and this read.
			log.Debug("rssmatcher: matched item no longer exists")
			return nil
		}
		if errors.Is(err, grab.ErrUnsupportedKind) {
			return nil
		}
		return events.Retry(matchRetry, err)
	}
	if !st.monitored {
		return nil
	}

	profile, err := resolveQualityProfile(ctx, h.Deps.Client, st.qualityProfile, h.Deps.catalogue())
	if err != nil {
		// A missing or invalid profile is a configuration problem, not a
		// transient one: retrying it six times and dead-lettering would bury
		// one release per indexer row. Skipping this item and acknowledging
		// keeps the firehose flowing; the next RSS row for the same item
		// retries it for free once the profile is fixed.
		log.Warn("rssmatcher: cannot resolve the quality profile; skipping this item", "error", err)
		return nil
	}
	delaySpec, err := resolveDelayProfile(ctx, h.Deps.Client, ns, st.delayProfileRef, st.tags)
	if err != nil {
		return events.Retry(matchRetry, err)
	}

	input := decision.Target{
		Kind:                ref.Kind,
		Key:                 events.MediaKey(string(ref.Kind), ns, ref.Name),
		Monitored:           st.monitored,
		Available:           st.available,
		RuntimeMinutes:      st.runtimeMinutes,
		OriginalLanguageTag: st.originalLanguageTag,
		Current:             st.currentFile,
		Queue:               queueFor(ctx, h.Deps.Client, ns, ref),
		Blocklist:           blocklist,
		// No IDQueryIndexers: a firehose release was not found by any query,
		// so nothing vouches for it but its own ids and title.
		Identity: st.identity,
	}
	opts := decisionOptions(ctx, h.Deps.Client, ns, delaySpec, rel)

	decisions := h.Deps.evaluate()(ctx, input, profile, h.Deps.catalogue(), []commonv1.ReleaseInfo{rel.Info}, opts)
	if len(decisions) == 0 || !decisions[0].Approved {
		recordRejections(ref.Kind, decisions)
		return nil
	}

	approved := decisions[0]
	keys := ref.Keys
	if ref.Kind == commonv1.MediaKindSeries {
		keys = wantedKeys(profile, approved.Release, st.episodes, st.episodeFiles)
		if len(keys) == 0 {
			// Every episode the pack covers already has a file this release
			// would not improve on: nothing here is wanted.
			log.Debug("rssmatcher: pack approved, but no episode it covers wants it")
			return nil
		}
	}
	if err := grab.Decide(ctx,
		grab.Deps{Client: h.Deps.Client, Reader: h.Deps.Reader, Bus: h.Deps.Bus, Now: h.Deps.Now},
		profile,
		delaySpec,
		grab.Approved{
			Namespace: ns,
			Target:    commonv1.MediaRef{Kind: ref.Kind, Name: ref.Name},
			Keys:      keys,
			// Decision.Release already carries the resolved FormatScore and
			// MatchedFormats -- pkg/decision.Evaluate writes both onto it
			// before scoring -- so there is nothing to fold back on.
			Release:   approved.Release,
			GrabbedBy: downloadv1alpha1.GrabSourceRSS,
		}); err != nil {
		if errors.Is(err, grab.ErrDuplicateGrab) {
			// Another path got there first. Acknowledge: redelivering would
			// only lose the same race again, and the grab path has already
			// cleared status.pendingGrab so the item does not strand at
			// Delayed.
			log.Debug("rssmatcher: already grabbed elsewhere")
			return nil
		}
		if errors.Is(err, grab.ErrUnsupportedKind) {
			return nil
		}
		return events.Retry(grabRetry, err)
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

// wantedKeys narrows a pack's episodes to those that want the approved
// release: an episode with no file, or whose file the release is an upgrade
// of under profile (quality.Profile.UpgradeDecision, the rule the decision
// engine applies to a single episode's current file) -- and never one whose
// file is transcoded, which is final however the qualities compare. files is
// each episode's current file, read from its MediaFile (resolveState's
// episodeFiles), so the comparison carries the file's revision: a PROPER of
// the quality on disk is an upgrade of it.
//
// The matcher resolves a season pack to every monitored episode of the
// season, including ones already at their cutoff, and grabarr downloads only
// the files of the Download's spec.target.keys. Handing the grab every
// episode made a pack for one missing episode download the whole season.
// Sonarr, with no file selection, refuses such a pack outright
// (UpgradeDiskSpecification rejects a release when any episode it covers
// already has an equal or better file); Clustarr can take just the episodes
// that want it, which is what the keys are for.
func wantedKeys(profile quality.Profile, rel commonv1.ReleaseInfo, eps []*catalogv1alpha1.Episode, files map[string]*decision.Current) []string {
	candidate := quality.Candidate{Quality: rel.Quality, Revision: rel.Revision, FormatScore: int(rel.FormatScore)}
	keys := make([]string, 0, len(eps))
	for _, ep := range eps {
		if cur := files[ep.Name]; cur != nil {
			if cur.Transcoded {
				// A transcoded file is final (CLAUDE.md, "Transcoding"):
				// the single-episode path rejects it as TranscodedFinal,
				// and a pack must not reach it by the back door.
				continue
			}
			current := quality.Candidate{Quality: cur.Quality, Revision: cur.Revision, FormatScore: cur.FormatScore}
			if profile.UpgradeDecision(current, candidate) != quality.Upgrade {
				continue
			}
		}
		keys = append(keys, ep.Name)
	}
	return keys
}

// recordRejections reports why a matched release was turned down. The labels
// are kind, decision and reason -- all bounded -- and never the release title
// or the item name.
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
