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

package fetch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
)

const (
	// HeartbeatInterval is how often a long fetch sends an in-progress ack.
	// A fetch is several provider round trips, each bounded by
	// providerset.DefaultHTTPTimeout, so the worker heartbeats between them
	// rather than ask for an AckWait longer than a pod's termination grace.
	// It is at most a third of the durable's first-delivery deadline,
	// events.AckDeadline(sub, 1), so two heartbeats can be lost before a
	// lapse (NATS research 2026-10-07, S9; held by
	// test/guards.TestHeartbeatsFitTheirDeadline): the fetch consumers'
	// BackOff[0] is 30s, which replaces their 90s AckWait as the deadline
	// (pkg/events/topology.go).
	HeartbeatInterval = 10 * time.Second

	// statRetry is a failed stat's retry (loop spec §6.7.2 step 5).
	statRetry = 5 * time.Minute

	// profileLagRetry is how soon a task whose SubtitleProfile generation
	// is ahead of this agent's cache is tried again (§6.7.2 step 3).
	profileLagRetry = 30 * time.Second

	// DefaultSidecarMode is RootFolderSpec.Permissions.FileMode's default,
	// 0664: a task whose SidecarMode is 0 writes its sidecar with it.
	DefaultSidecarMode os.FileMode = 0o664

	// maxSubtitleID is the CRD's own limit on items[].subtitleID.
	maxSubtitleID = 256
)

// ProviderSource yields the SubtitleProviders a namespace's fetches may use,
// in priority order. *build.Builder (app/caption/providerset/build) is the
// production implementation.
type ProviderSource interface {
	Build(ctx context.Context, namespace string) ([]providerset.Entry, error)
}

// Worker handles clustarr.work.caption.fetch.<priority>.<mediafile-uid>.<langKey>.
// It reads SubtitleProfiles (and, through Providers, SubtitleProviders and
// their Secrets) and answers in clustarr-subtitles; it writes no Kubernetes
// object (loop spec §6.7).
type Worker struct {
	// Client is the agent's cache: SubtitleProfiles only (§6.7.2).
	Client client.Reader

	// Bus publishes SubtitleEvents and carries the
	// clustarr-provider-throttle KV bucket.
	Bus events.Bus

	// Store is clustarr-subtitles, as this pod.
	Store *subtitlestore.Store

	// Providers builds the provider set per task.
	Providers ProviderSource

	// DataDir is where the /data volume is mounted in this process. Empty
	// means /data itself (app/caption/datapath.Local).
	DataDir string

	// MaxDeliver is the fetch consumers' MaxDeliver, for recognising the
	// final delivery. Zero means the default topology's.
	MaxDeliver int

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

// NewWorker builds a Worker with the production clock; writer is the pod
// name the answers carry.
func NewWorker(c client.Reader, bus events.Bus, providers ProviderSource, dataDir, writer string) *Worker {
	return &Worker{Client: c, Bus: bus, Store: subtitlestore.New(bus, writer), Providers: providers, DataDir: dataDir, Clock: time.Now}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

// SetupWithManager subscribes the worker to both fetch consumers,
// caption-fetch-high and caption-fetch-normal, as k8s.EveryReplica
// runnables: fetch workers are never leader-elected -- every replica
// consumes, bounded by the shared KV token bucket rather than by how many
// pods run.
func (w *Worker) SetupWithManager(mgr ctrl.Manager, t events.Topology) error {
	for _, name := range []string{events.ConsumerCaptionFetchHigh, events.ConsumerCaptionFetchNormal} {
		spec, ok := t.Consumer(name)
		if !ok {
			return fmt.Errorf("fetch: consumer %s missing from topology", name)
		}
		if spec.MaxDeliver > 0 && (w.MaxDeliver == 0 || spec.MaxDeliver < w.MaxDeliver) {
			w.MaxDeliver = spec.MaxDeliver
		}
		sub := spec.Subscription()
		if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
			stop, err := w.Bus.Subscribe(ctx, sub, w.Handle)
			if err != nil {
				return fmt.Errorf("fetch: subscribe %s: %w", sub.Durable, err)
			}
			<-ctx.Done()
			stop()
			return nil
		})); err != nil {
			return fmt.Errorf("fetch: add %s runnable: %w", name, err)
		}
	}
	return nil
}

// decodeTask decodes a v2 FetchTask. Any error is a poison message: no
// redelivery of the same bytes can fix it.
func decodeTask(env *events.Envelope) (schema.FetchTask, error) {
	if env == nil {
		return schema.FetchTask{}, errors.New("fetch: nil envelope")
	}
	var t schema.FetchTask
	if err := schema.Decode(env.Schema, env.Data, &t); err != nil {
		return schema.FetchTask{}, err
	}
	switch {
	case t.MediaFile.UID == "":
		return schema.FetchTask{}, errors.New("fetch: the task names no MediaFile UID")
	case t.Path == "":
		return schema.FetchTask{}, errors.New("fetch: the task names no path")
	case t.Seq <= 0:
		return schema.FetchTask{}, fmt.Errorf("fetch: the task's seq %d is not positive", t.Seq)
	}
	if _, _, _, err := subtitles.ParseLangKey(subtitles.LangKey(t.LangKey)); err != nil {
		return schema.FetchTask{}, err
	}
	return t, nil
}

// Handle implements events.Handler.
//
// Settlement: nil acks, events.Discard dead-letters a poison message at
// once, and any other error is redelivered on the consumer's backoff until
// MaxDeliver dead-letters it. A search that finds nothing, or finds every
// provider throttled, is an answer -- recorded in the task's record and
// acked -- not a failure to redeliver.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "fetch.Worker.Handle")
	defer span.End()
	t, err := decodeTask(env)
	if err != nil {
		tracing.RecordError(span, err)
		return events.Discard("fetch: malformed FetchTask", err)
	}
	ctx = logging.With(ctx, "namespace", t.MediaFile.Namespace, "mediaFile", t.MediaFile.Name, "langKey", t.LangKey, "seq", t.Seq)
	if err := w.handle(ctx, m, t); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// handle is §6.7.2 in order: the record fences the task, the profile is
// read at the task's generation, the video must still be the planned file,
// then the provider walk and the answer.
func (w *Worker) handle(ctx context.Context, m events.Message, t schema.FetchTask) error {
	if sup, err := w.Store.Superseded(ctx, t); err != nil {
		return fmt.Errorf("fetch: read the record: %w", err)
	} else if sup {
		logging.FromContext(ctx).Info("fetch: a newer request, a withdrawal or an answer supersedes this task")
		return nil
	}
	kind := t.Query.Kind
	if kind != commonv1.MediaKindMovie && kind != commonv1.MediaKindEpisode {
		return events.Discard("fetch: the task is not for a video kind", fmt.Errorf("kind %q", kind))
	}
	outOf := int32(subtitles.MaxScore[kind]) //nolint:gosec // 180 or 360

	var profile subtitlev1alpha1.SubtitleProfile
	switch err := w.Client.Get(ctx, client.ObjectKey{Name: t.Profile}, &profile); {
	case apierrors.IsNotFound(err):
		return w.answer(ctx, t, dropped(outOf, fmt.Sprintf("SubtitleProfile %s is gone", t.Profile)))
	case err != nil:
		return fmt.Errorf("fetch: get SubtitleProfile %s: %w", t.Profile, err)
	}
	wnt, ok := wantFor(&profile, t.LangKey)
	switch {
	case !ok:
		return w.answer(ctx, t, dropped(outOf, fmt.Sprintf("SubtitleProfile %s no longer has %s", profile.Name, t.LangKey)))
	case profile.Generation < t.ProfileGeneration:
		return events.Retry(profileLagRetry, fmt.Errorf("SubtitleProfile %s is at generation %d here, the task at %d",
			profile.Name, profile.Generation, t.ProfileGeneration))
	case profile.Generation > t.ProfileGeneration:
		return w.answer(ctx, t, dropped(outOf, fmt.Sprintf("SubtitleProfile %s changed since planned", profile.Name)))
	}
	flt, err := compileFilters(profile.Spec)
	if err != nil { // the profile reconciler marks it Invalid too (§2.13)
		return w.answer(ctx, t, verdict(schema.SubtitleVerdictFailed, outOf,
			fmt.Sprintf("subtitle profile %s is invalid: %v", profile.Name, err)))
	}
	local, err := datapath.Local(w.DataDir, t.Path)
	if err != nil {
		return events.Discard("fetch: media file path is not on the data volume", err)
	}
	st, err := os.Stat(local)
	if err != nil {
		return w.transient(ctx, m, t, outOf, fmt.Errorf("stat %s: %w", t.Path, err), statRetry)
	}
	if mediainfo.ProbeHash(t.Path, st.Size(), st.ModTime()) != t.ProbeHash {
		return w.answer(ctx, t, dropped(outOf, "file changed since planned"))
	}
	entries, err := w.Providers.Build(ctx, t.MediaFile.Namespace)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	entries = providerset.Order(entries, profile.Spec.Providers)
	q := buildQuery(ctx, t, local, st.Size(), wnt.key)
	src := providerset.FileSource{
		Path: local, Info: commonv1.MediaInfo{Subtitles: t.Extract},
		IgnoreASS: profile.Spec.Embedded.IgnoreASS, SkipCommentary: profile.Spec.Embedded.SkipCommentaryOrDefault(),
	}
	provs, skipped := eligible(entries, src, kind, wnt, q, profile.Spec.Embedded.ExtractOrDefault() && len(t.Extract) > 0)
	plan := searchPlan{
		kind: kind, query: q, want: wnt, filters: flt, threshold: int(t.MinScore),
		providers: provs, skipped: skipped, mods: mods(profile.Spec.Mods), toSRT: !profile.Spec.OriginalFormat,
		hiExt: string(profile.Spec.HIExtension), mediaPath: t.Path, probeHash: t.ProbeHash,
		mode: cmp.Or(os.FileMode(t.SidecarMode), DefaultSidecarMode),
	}
	out, err := w.search(ctx, m, plan)
	if err != nil {
		var moved *movedError
		if errors.As(err, &moved) {
			return w.answer(ctx, t, dropped(outOf, moved.Error()))
		}
		var we *writeError
		if errors.As(err, &we) {
			return w.transient(ctx, m, t, outOf, err, 0)
		}
		return fmt.Errorf("fetch: %w", err)
	}
	return w.finish(ctx, t, plan, out, outOf)
}

// dropped is an answer that searched nothing (§6.7.2): the loop plans
// again.
func dropped(outOf int32, msg string) schema.SubtitleRecord {
	return verdict(schema.SubtitleVerdictDropped, outOf, msg)
}

// verdict is an answer with no subtitle written.
func verdict(v schema.SubtitleVerdict, outOf int32, msg string) schema.SubtitleRecord {
	return schema.SubtitleRecord{Verdict: v, ScoreOutOf: outOf, LastError: truncate(msg, schema.MaxSubtitleLastError)}
}

func mods(in []subtitlev1alpha1.SubtitleMod) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		out = append(out, string(m))
	}
	return out
}

// eligibleProvider is one provider this task will ask, with the client to
// ask it through.
type eligibleProvider struct {
	entry  providerset.Entry
	client subtitles.Provider
}

// eligible narrows the priority-ordered provider set to those that can serve
// this task, keeping the order: embedded extraction switched on for a local
// provider, the provider's own spec.languages, whether it can identify the
// item at all ([searchable]), and then its client's capabilities -- the
// media kind, forced-search support and the languages it has codes for.
// Every SubtitleProvider is judged on its own: two of one type -- two
// accounts -- are both eligible and both searched ([Worker.search]). Until
// gap-fix X11b a pkg/subtitles.Registry keyed by provider type skipped the
// second, so a second account never answered and never lent its quota.
func eligible(entries []providerset.Entry, src providerset.FileSource, kind commonv1.MediaKind, w want,
	q subtitles.Query, extract bool,
) ([]eligibleProvider, []string) {
	var (
		out     []eligibleProvider
		skipped []string
	)
	for _, e := range entries {
		switch {
		case e.Local() && !extract:
			skipped = append(skipped, e.Name+": embedded extraction is off or the task names no embedded track")
			continue
		case !e.Serves(w.tags()):
			skipped = append(skipped, e.Name+": spec.languages excludes "+w.lang)
			continue
		case !searchable(e.Type, kind, q):
			skipped = append(skipped, e.Name+": nothing to identify this item by (no id or hash it searches on)")
			continue
		}
		c := e.Provider(src)
		caps := c.Capabilities()
		servesKind := (kind == commonv1.MediaKindMovie && caps.Movies) || (kind == commonv1.MediaKindEpisode && caps.Episodes)
		switch {
		case !servesKind:
			skipped = append(skipped, e.Name+": does not serve "+string(kind)+" subtitles")
		case w.forced && !caps.ForcedSearch:
			skipped = append(skipped, e.Name+": cannot search forced subtitles")
		case caps.Languages != nil && !anyLang(caps.Languages, w.tags()):
			skipped = append(skipped, e.Name+": does not serve "+w.lang)
		default:
			out = append(out, eligibleProvider{entry: e, client: c})
		}
	}
	return out, skipped
}

func anyLang(serves func(string) bool, tags []string) bool {
	for _, t := range tags {
		if serves(t) {
			return true
		}
	}
	return false
}
