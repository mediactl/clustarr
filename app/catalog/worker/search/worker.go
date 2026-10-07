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

// Package search is the shared search worker every search lands on: the
// consumer of catalog.SearchTask.v2 on catalog-search-high and
// catalog-search-normal, in the catalog agent.
//
// Since ADR-0019 (§7.4, A4.3) it answers every search -- interactive or
// automatic -- in a clustarr-searches record and writes nothing else. It
// reads the item for its identity and runtime, asks index over
// clustarr.rpc.index.search (or the local release index for an IndexOnly
// task), runs every release through pkg/decision's release rules
// (EvaluateRelease: identity, protocol, availability, size, quality and
// format score, language, sample, a donor's languages), ranks and caps the
// result, and writes SearchRecord. The item-state rules -- the queue, the
// current file and its cutoff, TranscodedFinal, the blocklist -- are the
// manager's, against fresh state (§6.6): the grab planner re-checks an
// automatic answer, the Search controller an interactive one (R24). The
// task carries what the manager decided -- the profile, the enabled
// protocols (P7), the indexer priorities (P9), the release index scope,
// IndexOnly and the donor want -- so the worker reads no DownloadClient,
// no grab entry and no MediaFile.
//
// A task on a Series names a season (Wave S, SeasonSearchable): the worker
// searches the season as one query per indexer, judges the answer in
// season-search identity mode against every episode of the season, and
// answers at the series' season key (agentrecords.SeasonSearchKey) for a
// task the Series planned, or at the Search's UID for an interactive one.
// Every answer's releases carry their scene-mapped coverage and are banded
// approved, rejected, other titles before the record's cap; every
// identity carries the library's namesakes.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchoutcome"
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
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// cacheWarmRetry is how long to wait before looking again for an object a task
// depends on that the worker's informer cache has not seen yet: the manager
// writes the object and then publishes, and nothing orders the agent's
// watch after the publish.
const cacheWarmRetry = 2 * time.Second

// EvaluateFunc is the seam onto pkg/decision.EvaluateRelease. It exists so a
// test can drive Handle's plumbing without also exercising the decision
// engine's own rule table.
type EvaluateFunc func(
	ctx context.Context,
	t decision.Target,
	p quality.Profile,
	cat *catalogue.Catalogue,
	rels []commonv1.ReleaseInfo,
	o decision.Options,
) []decision.Decision

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=searches,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;series,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch

// Worker consumes catalog.SearchTask.v2.
type Worker struct {
	Client    client.Client
	RPC       SearchRPC
	Catalogue *catalogue.Catalogue
	// Index queries index's local release index, for a SearchTask with
	// IndexOnly set. Nil searches such a task live, as before.
	Index IndexQuery
	// Records writes the answer in clustarr-searches, and Answers reads
	// the stored one before a search, so a superseded task is dropped
	// without asking an indexer.
	Records *records.Writer[*schema.SearchRecord]
	Answers *records.Reader[*schema.SearchRecord]
	// Evaluate defaults to decision.EvaluateRelease.
	Evaluate EvaluateFunc
	Clock    clockwork.Clock
	// Topology is the bus topology this process installed; SetupWithManager
	// looks both consumers up in it. Nil means events.Default().
	Topology *events.Topology
	// SceneMaps supplies TheXEM's scene-numbering table for a series (see
	// sceneMappings). Nil means no scene numbering.
	SceneMaps scenemap.Source
}

// NewWorker builds a Worker with the production decision engine and the real
// clock. Records and Answers are the caller's (the agent binds the bucket).
func NewWorker(c client.Client, rpc SearchRPC, cat *catalogue.Catalogue) *Worker {
	return &Worker{
		Client:    c,
		RPC:       rpc,
		Catalogue: cat,
		Evaluate:  decision.EvaluateRelease,
		Clock:     clockwork.NewRealClock(),
	}
}

func (w *Worker) now() time.Time {
	if w.Clock == nil {
		return time.Now().UTC()
	}
	return w.Clock.Now().UTC()
}

func (w *Worker) log(ctx context.Context) *slog.Logger { return logging.FromContext(ctx) }

func (w *Worker) evaluate() EvaluateFunc {
	if w.Evaluate != nil {
		return w.Evaluate
	}
	return decision.EvaluateRelease
}

// schemaSearchTask is the payload the search consumers carry. The
// catalog-search-normal filter still names wantedscan.> (R7: names never
// change), but nothing publishes a WantedScan any more (ADR-0019 §7.3).
var schemaSearchTask = schema.SearchTask{}.Schema()

// Handle implements events.Handler.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "search.Worker.Handle")
	defer span.End()

	switch env.Schema {
	case schemaSearchTask:
		return w.handleSearchTask(ctx, span, m)
	default:
		return events.Discard("unknown payload schema on a search consumer",
			fmt.Errorf("schema=%q subject=%q", env.Schema, m.Subject()))
	}
}

// answer is one search's record in the making: keyed by uid (the task's
// uid) and, for a season task the Series planned, sub
// (schema.SeasonSub).
type answer struct {
	uid, sub string
	item     schema.ItemRef
	task     schema.SearchTask
}

// key is the answer's clustarr-searches key.
func (a answer) key() string {
	if a.sub != "" {
		return events.RecordSubKey(a.uid, a.sub)
	}
	return agentrecords.SearchKey(a.uid)
}

// handleSearchTask answers one search task in a record (§7.4).
func (w *Worker) handleSearchTask(ctx context.Context, span trace.Span, m events.Message) error {
	env := m.Envelope()

	var task schema.SearchTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable SearchTask", err)
	}
	ns, err := namespaceOf(env, task)
	if err != nil {
		return events.Discard("cannot resolve the task's namespace", err)
	}
	ctx = logging.With(ctx, "kind", string(task.MediaRef.Kind), "item", task.MediaRef.Name,
		"namespace", ns, "reason", string(task.Reason))

	// The record's key is the Search's UID for an interactive search, the
	// item's for an automatic one (R13), and the series' season key for a
	// season task the Series planned (Wave S). A task naming none has
	// nowhere to answer.
	a := answer{task: task, item: task.Item}
	switch {
	case task.SearchRef != nil && task.SearchRef.UID != "":
		a.uid = task.SearchRef.UID
		a.item = schema.ItemRef{Kind: "Search", Ref: *task.SearchRef}
		if a.item.Namespace == "" {
			a.item.Namespace = ns
		}
	case task.Item.UID != "":
		a.uid = task.Item.UID
		if SeasonSearchable(task) {
			a.sub = schema.SeasonSub(*task.Season)
		}
	default:
		return events.Discard("the search task names no record key (a v1 task)", nil)
	}
	if w.Records == nil || w.Answers == nil {
		return errors.New("search: no records writer is wired")
	}

	// Superseded: the record already answers this seq or a later one.
	if rec, _, ok, err := w.Answers.Get(ctx, a.key()); err != nil {
		return fmt.Errorf("search: read the record of %s: %w", a.key(), err)
	} else if ok && rec.Seq >= task.Seq {
		w.log(ctx).Debug("search: the task is superseded by its record; dropping it", "seq", task.Seq, "recordSeq", rec.Seq)
		return nil
	}

	// An interactive search takes its indexers and categories from the
	// Search object, so editing the Search before the worker picks the task
	// up does what a user expects.
	var srch *catalogv1alpha1.Search
	if task.SearchRef != nil {
		srch = &catalogv1alpha1.Search{}
		key := client.ObjectKey{Namespace: ns, Name: task.SearchRef.Name}
		if err := w.Client.Get(ctx, key, srch); err != nil {
			if apierrors.IsNotFound(err) {
				// Searches are TTL-deleted; one that is really gone has no
				// one to read the answer.
				return missingObject(m, "Search object no longer exists", err)
			}
			return fmt.Errorf("get Search %s: %w", key, err)
		}
	}

	season := SeasonSearchable(task)
	switch {
	case task.MediaRef.Kind == commonv1.MediaKindSeries && !season:
		return w.fail(ctx, a, "a series search names a season")
	case !season && !Searchable(task.MediaRef.Kind):
		return w.fail(ctx, a, fmt.Sprintf("kind %s is not searchable; search one of its items instead", task.MediaRef.Kind))
	}

	var snap itemSnapshot
	if season {
		snap, err = w.snapshotSeason(ctx, ns, task.MediaRef, *task.Season)
	} else {
		snap, err = w.snapshot(ctx, ns, task.MediaRef)
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			if m.Attempt() > 1 {
				return w.fail(ctx, a, "target no longer exists: "+err.Error())
			}
			return missingObject(m, "target no longer exists", err)
		}
		tracing.RecordError(span, err)
		return err
	}

	profileRef := task.QualityProfile
	if profileRef == "" {
		profileRef = snap.QualityProfileRef
	}
	profile, err := w.resolveProfile(ctx, profileRef)
	if err != nil {
		var de *events.DiscardError
		if errors.As(err, &de) {
			return w.fail(ctx, a, de.Reason+": "+err.Error())
		}
		return err
	}

	if task.Purpose == schema.SearchPurposeAudioDonor {
		// An audio donor search (anime dual-audio spec §6.1): judged on the
		// languages the file lacks, which the manager put in the task.
		if task.Donor == nil || task.Donor.Anchor == "" {
			return w.fail(ctx, a, "the donor search carries no donor want")
		}
		snap.Target.Donor = &decision.Donor{
			Languages: task.Donor.Languages, Anchor: task.Donor.Anchor, Rejected: task.Donor.Rejected,
		}
	}

	req := w.buildRequest(ns, task, snap, srch)
	var resp schema.SearchResponse
	if task.IndexOnly && w.Index != nil {
		if req.Text == "" {
			// The index is searched by text alone, and an empty query
			// matches every release in it. The item has no resolved title
			// yet: answer empty, so its dispatch closes.
			return w.write(ctx, a, schema.SearchResponse{}, nil, decision.Options{})
		}
		resp, err = w.searchIndex(ctx, req)
	} else {
		resp, err = w.RPC.Search(ctx, req)
	}
	if err != nil {
		tracing.RecordError(span, err)
		return err // already an events.RetryError from busSearchRPC, or a plain error -> backoff nak
	}

	opts := decisionOptions(task)
	// Which indexers answered with an id query is known only now (the
	// identity check reads it).
	snap.Target.Identity.IDQueryIndexers = idQueryIndexers(resp.Outcomes)
	rels := releaseInfos(resp.Releases)
	decisions := w.evaluate()(ctx, snap.Target, profile, w.Catalogue, rels, opts)
	recordDecisionMetrics(task.MediaRef.Kind, decisions)
	w.log(ctx).Info("search: decided",
		"releases", len(rels), "approved", countApproved(decisions), "truncated", resp.Truncated)
	warnUnreported(ctx, resp)
	return w.write(ctx, a, resp, decisions, opts)
}

// write answers the task: the ranked candidates (at most
// schema.MaxSearchCandidates), every indexer's outcome (at most
// schema.MaxIndexerOutcomes, with the truncation marker when index cut the
// reply), and whether every indexer was paced.
func (w *Worker) write(ctx context.Context, a answer, resp schema.SearchResponse, ds []decision.Decision, opts decision.Options) error {
	outcomes := resp.Outcomes
	if len(outcomes) > schema.MaxIndexerOutcomes {
		outcomes = outcomes[:schema.MaxIndexerOutcomes]
	}
	if resp.Truncated {
		outcomes = searchoutcome.Truncation(append([]schema.SearchOutcome(nil), outcomes...), len(resp.Releases))
	}
	rec := w.record(a)
	rec.QueryMode = queryMode(resp.Outcomes)
	rec.AllPaced = allPaced(resp.Outcomes)
	rec.IndexerOutcomes = outcomes
	rec.Candidates = RankScored(ds, opts, resp.Releases, schema.MaxSearchCandidates)
	return w.store(ctx, a, rec)
}

// fail answers a task the worker could not run at all: no candidates, the
// failure in the header, and -- for an interactive search -- one outcome
// under the worker's reserved name, which the Search controller reports.
// The manager's planner closes the dispatch on it and counts the attempt,
// so a broken item backs off rather than spinning.
func (w *Worker) fail(ctx context.Context, a answer, reason string) error {
	w.log(ctx).Warn("search: the task cannot be answered", "reason", reason)
	rec := w.record(a)
	rec.Failure = clipFailure(reason)
	rec.State = records.StateFailed
	rec.IndexerOutcomes = []schema.SearchOutcome{{
		IndexerName: searchoutcome.WorkerOutcomeName, Status: schema.SearchOutcomeError,
		Error: searchoutcome.TruncateError(reason),
	}}
	return w.store(ctx, a, rec)
}

// record is a's record header.
func (w *Worker) record(a answer) *schema.SearchRecord {
	now := w.now()
	item := a.item
	rec := &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &item, Sub: a.sub, Seq: a.task.Seq, State: records.StateAnswered, AnsweredAt: &now,
		},
		Task: a.uid, Purpose: a.task.Purpose, FinishedAt: now,
	}
	if a.task.Season != nil {
		season := *a.task.Season
		rec.Season = &season
	}
	if a.task.SearchRef == nil {
		// An automatic answer is the container's grab input too (R7); an
		// interactive one is the Search's alone.
		rec.Container = a.task.Container
	}
	return rec
}

// store writes rec; a record above rec's seq already stored is no error.
func (w *Worker) store(ctx context.Context, a answer, rec *schema.SearchRecord) error {
	if _, err := w.Records.Write(ctx, a.key(), rec); err != nil {
		if errors.Is(err, records.ErrTooLarge) {
			// Too many candidates for the bucket's value cap: keep the best
			// half and say so, rather than lose the answer.
			rec.Candidates = rec.Candidates[:len(rec.Candidates)/2]
			if _, err2 := w.Records.Write(ctx, a.key(), rec); err2 == nil {
				return nil
			}
		}
		return fmt.Errorf("search: write the record of %s: %w", a.key(), err)
	}
	return nil
}

func clipFailure(s string) string {
	if len(s) <= schema.MaxRecordFailure {
		return s
	}
	return s[:schema.MaxRecordFailure]
}

// queryMode is the record's query mode: an id query when any indexer used
// one, else the first mode an outcome names.
func queryMode(outs []schema.SearchOutcome) schema.SearchQueryMode {
	var first schema.SearchQueryMode
	for _, o := range outs {
		if o.QueryMode == schema.SearchQueryModeID {
			return o.QueryMode
		}
		if first == "" {
			first = o.QueryMode
		}
	}
	return first
}

// decisionOptions are pkg/decision's options from the task: the protocols
// the manager found an enabled DownloadClient for (P7) -- both, for a
// user-invoked search in a namespace with none, so the person sees what is
// out there -- and the indexer priorities (P9's finding: every indexer
// ranked at 25 before).
func decisionOptions(task schema.SearchTask) decision.Options {
	o := decision.Options{UserInvoked: task.UserInvoked, ProtocolsEnabled: map[string]bool{}, IndexerPriority: map[string]int{}}
	for _, p := range task.Protocols {
		o.ProtocolsEnabled[string(p)] = true
	}
	if len(task.Protocols) == 0 && task.UserInvoked {
		o.ProtocolsEnabled[string(commonv1.ProtocolTorrent)] = true
		o.ProtocolsEnabled[string(commonv1.ProtocolUsenet)] = true
	}
	for name, p := range task.IndexerPriorities {
		o.IndexerPriority[name] = int(p)
	}
	return o
}

// Searchable reports whether the search worker can search for an item of
// kind: one it can snapshot, identify and decide. Every other kind a
// SearchTask can name is a container whose items are searched instead.
func Searchable(kind commonv1.MediaKind) bool {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode,
		commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindIssue:
		return true
	default:
		return false
	}
}

// allPaced reports whether outs is non-empty and every outcome is a paced
// skip: index sent no query at all, so the search is no attempt (P6). An
// index-only search reports no outcomes and counts.
func allPaced(outs []schema.SearchOutcome) bool {
	if len(outs) == 0 {
		return false
	}
	for _, o := range outs {
		if o.Status != schema.SearchOutcomeSkipped || o.Error != schema.SkipReasonPaced {
			return false
		}
	}
	return true
}

// missingObject settles a NotFound on an object this task depends on: retry
// once to ride out an informer that has not caught up, then dead-letter.
func missingObject(m events.Message, reason string, err error) error {
	if m.Attempt() <= 1 {
		return events.Retry(cacheWarmRetry, err)
	}
	return events.Discard(reason, err)
}

// namespaceOf resolves the namespace the task's objects live in: the
// envelope key ("<namespace>/<name>"), else the task's item or Search.
func namespaceOf(env *events.Envelope, task schema.SearchTask) (string, error) {
	if ns, _, ok := strings.Cut(env.Key, "/"); ok && ns != "" {
		return ns, nil
	}
	if task.Item.Namespace != "" {
		return task.Item.Namespace, nil
	}
	if task.SearchRef != nil && task.SearchRef.Namespace != "" {
		return task.SearchRef.Namespace, nil
	}
	return "", fmt.Errorf("envelope key %q is not <namespace>/<name> and the task carries no namespaced ref", env.Key)
}

// resolveProfile loads and resolves the QualityProfile the task names.
// QualityProfile is cluster-scoped, so the lookup carries no namespace.
func (w *Worker) resolveProfile(ctx context.Context, ref string) (quality.Profile, error) {
	if ref == "" {
		return quality.Profile{}, events.Discard("item has no qualityProfileRef",
			errors.New("a search cannot be decided without a profile"))
	}
	var qp catalogv1alpha1.QualityProfile
	if err := w.Client.Get(ctx, client.ObjectKey{Name: ref}, &qp); err != nil {
		// Not a Discard: a profile can be created after the item that
		// references it, and the subscription's backoff is the right place
		// to wait for that.
		return quality.Profile{}, fmt.Errorf("get QualityProfile %s: %w", ref, err)
	}
	profile, ferrs := quality.FromCRD(&qp, w.Catalogue)
	if len(ferrs) > 0 {
		return quality.Profile{}, events.Discard("invalid QualityProfile", errors.Join(ferrs...))
	}
	return profile, nil
}

// buildRequest renders the RPC request, letting an interactive Search
// override the per-kind indexer and category defaults with its own spec.
// The RPC limit is always schema.MaxSearchReleases, never Search.spec.limit
// (which caps what is kept, not what is fetched). The release index's block
// scope and the protocols come from the task.
func (w *Worker) buildRequest(ns string, task schema.SearchTask, snap itemSnapshot, srch *catalogv1alpha1.Search) schema.SearchRequest {
	var indexerRefs []schema.Ref
	var categories []int32
	if srch != nil {
		categories = srch.Spec.Categories
		for _, name := range srch.Spec.IndexerRefs {
			indexerRefs = append(indexerRefs, schema.Ref{Namespace: srch.Namespace, Name: name})
		}
	}
	kind := task.MediaRef.Kind
	if SeasonSearchable(task) {
		// A season is a tv-search: the series' tvdb id and the season,
		// no episode (series search design §5.2).
		kind = commonv1.MediaKindEpisode
	}
	req := BuildSearchRequest(ns, kind, snap.IDs, schema.MaxSearchReleases,
		task.UserInvoked, indexerRefs, categories)
	req.Protocols = task.Protocols
	switch {
	case task.Scope != "":
		req.Scope = task.Scope
	case snap.Owner.UID != "":
		// Every index answer carries block state for this scope or the
		// global one (ADR-0019 §6.14).
		req.Scope = schema.BlockScopeOf(snap.Owner)
	}
	return req
}

// releaseInfos projects the RPC reply onto what pkg/decision consumes;
// PublishedAt passes through as the indexer reported it, absence included.
func releaseInfos(rels []schema.Release) []commonv1.ReleaseInfo {
	out := make([]commonv1.ReleaseInfo, 0, len(rels))
	for _, r := range rels {
		out = append(out, r.Info)
	}
	return out
}

func countApproved(ds []decision.Decision) int {
	n := 0
	for _, d := range ds {
		if d.Approved {
			n++
		}
	}
	return n
}

// recordDecisionMetrics counts verdicts by kind and outcome. The reason label
// is the RejectionType, never the rejection message.
func recordDecisionMetrics(kind commonv1.MediaKind, ds []decision.Decision) {
	for _, d := range ds {
		if d.Approved {
			metrics.SearchDecisionsTotal.WithLabelValues(string(kind), "approved", "none").Inc()
			continue
		}
		reason := string(commonv1.RejectionPermanent)
		if d.TemporarilyRejected {
			reason = string(commonv1.RejectionTemporary)
		}
		metrics.SearchDecisionsTotal.WithLabelValues(string(kind), "rejected", reason).Inc()
	}
}

// warnUnreported logs a truncated reply and indexers that reported no name.
func warnUnreported(ctx context.Context, resp schema.SearchResponse) {
	log := logging.FromContext(ctx)
	if resp.Truncated {
		log.Warn("search: index truncated the reply; releases beyond the cap were never decided",
			"cap", schema.MaxSearchReleases, "releases", len(resp.Releases))
	}
	for i, o := range resp.Outcomes {
		if searchoutcome.Name(o) != "" {
			continue
		}
		log.Warn("search: an indexer outcome carried no indexer name",
			"position", i, "status", string(o.Status), "releases", o.Releases, "err", o.Error)
	}
}

// topology is the bus topology the consumers are looked up in.
func (w *Worker) topology() events.Topology {
	if w.Topology != nil {
		return *w.Topology
	}
	return events.Default()
}

// SetupWithManager subscribes both search consumers, each a k8s.EveryReplica
// runnable: the work queue itself is what stops two replicas doing the same
// task.
func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	if w.Index == nil {
		// The sweep's repeat searches query the release index over the same
		// bus (SearchTask.IndexOnly).
		w.Index = NewBusIndexQuery(bus)
	}
	topo := w.topology()
	for _, name := range []string{events.ConsumerCatalogSearchHigh, events.ConsumerCatalogSearchNorm} {
		spec, ok := topo.Consumer(name)
		if !ok {
			return fmt.Errorf("no consumer spec named %s in the bus topology", name)
		}
		sub := spec.Subscription()
		if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
			stop, err := bus.Subscribe(ctx, sub, w.Handle)
			if err != nil {
				return fmt.Errorf("subscribe %s: %w", sub.Durable, err)
			}
			defer stop()
			<-ctx.Done()
			return nil
		})); err != nil {
			return fmt.Errorf("add %s consumer: %w", name, err)
		}
	}
	return nil
}

// indexQueryLimit is how many of the release index's matches an index-only
// search reads: the newest, as relindex orders them.
const indexQueryLimit = 100

// searchIndex answers a search from index's local release index instead
// of the indexers (SearchTask.IndexOnly): the same text a live text search
// sends, narrowed to the request's categories and, when it names exactly
// one, its protocol.
func (w *Worker) searchIndex(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error) {
	q := schema.QueryRequest{Text: req.Text, Limit: indexQueryLimit, Filters: map[string]string{}, Scope: req.Scope}
	if len(req.Categories) > 0 {
		cats := make([]string, 0, len(req.Categories))
		for _, c := range req.Categories {
			cats = append(cats, strconv.Itoa(int(c)))
		}
		q.Filters["category"] = strings.Join(cats, ",")
	}
	if len(req.Protocols) == 1 {
		q.Filters["protocol"] = string(req.Protocols[0])
	}
	resp, err := w.Index.Query(ctx, q)
	if err != nil {
		return schema.SearchResponse{}, err
	}
	if resp.Error != "" {
		return schema.SearchResponse{}, fmt.Errorf("search: release index query: %s", resp.Error)
	}
	return schema.SearchResponse{Releases: resp.Releases, Truncated: resp.Total > int64(len(resp.Releases))}, nil
}
