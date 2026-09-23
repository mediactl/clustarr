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
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/datapath"
	"github.com/mediactl/clustarr/captionarr/providerset"
	"github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// FieldManager is the server-side-apply field manager this worker writes
// SubtitleRequest.status under. captionarr/status.PatchRequest renders its
// complete owned set from [status.RequestWorkerFields].
const FieldManager = k8s.ManagerCaptionarrWorker

const (
	// heartbeatInterval is how often a long fetch sends an in-progress ack.
	// The fetch consumers' AckWait is 90s (pkg/events/topology.go) and a
	// fetch is several provider round trips, each bounded by
	// providerset.DefaultHTTPTimeout, so the worker heartbeats between them
	// rather than ask for an AckWait longer than a pod's termination grace.
	heartbeatInterval = 20 * time.Second

	// probeRetry is spec §6.5's "verify path/size/mtime == probeHash (else
	// Retry(5m))".
	probeRetry = 5 * time.Minute

	// upgradeMargin is Bazarr's upgrade candidacy rule, "score <
	// score_out_of - 3" (research note §8; spec §6.5's 12h upgrade pass):
	// a subtitle within three points of the maximum is not worth
	// upgrading, so it is recorded downloaded rather than upgradable.
	upgradeMargin = 3

	// DefaultSidecarMode is RootFolderSpec.Permissions.FileMode's default,
	// 0664: sidecars sit beside the video in a group-shared library. It is
	// the mode of a sidecar whose video lies under no RootFolder, or under
	// one whose fileMode does not parse ([Worker.sidecarModeFor]).
	DefaultSidecarMode os.FileMode = 0o664

	// maxLastError and maxSubtitleID are the CRD's own limits on
	// items[].lastError and items[].subtitleID.
	maxLastError  = 512
	maxSubtitleID = 256
)

// ProviderSource yields the SubtitleProviders a namespace's fetches may use,
// in priority order. *providerset.Builder is the production implementation.
type ProviderSource interface {
	Build(ctx context.Context, namespace string) ([]providerset.Entry, error)
}

// Worker handles clustarr.work.captionarr.fetch.<priority>.<uid>.<langKey>
// messages. See the package doc for the flow and the registration F-6 does.
type Worker struct {
	// Client is the manager's client: it reads SubtitleRequests,
	// MediaFiles, the catalog owners and SubtitleProfiles, and applies
	// SubtitleRequest.status.
	Client client.Client

	// APIReader re-reads the SubtitleRequest immediately before the status
	// apply. It should be the manager's uncached API reader: the re-read
	// exists to see what another writer did during the search, and an
	// informer that has not caught up yet would hand back the same stale
	// snapshot the re-read is there to replace. Nil falls back to Client.
	APIReader client.Reader

	// Bus publishes SubtitleEvents and carries the
	// clustarr-provider-throttle KV bucket.
	Bus events.Bus

	// Providers builds the provider set per task.
	Providers ProviderSource

	// DataDir is where the /data volume is mounted in this process. Empty
	// means /data itself (captionarr/datapath.Local).
	DataDir string

	// SidecarMode is the file mode of a sidecar whose video lies under no
	// RootFolder. Zero means [DefaultSidecarMode]. A video under a
	// RootFolder gets that folder's spec.permissions.fileMode instead
	// ([Worker.sidecarModeFor]).
	SidecarMode os.FileMode

	// MaxDeliver is the fetch consumers' MaxDeliver, for recognising the
	// final delivery. Zero means the default topology's.
	MaxDeliver int

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

// NewWorker builds a Worker with the production clock.
func NewWorker(c client.Client, apiReader client.Reader, bus events.Bus, providers ProviderSource, dataDir string) *Worker {
	return &Worker{Client: c, APIReader: apiReader, Bus: bus, Providers: providers, DataDir: dataDir, Clock: time.Now}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

func (w *Worker) reader() client.Reader {
	if w.APIReader != nil {
		return w.APIReader
	}
	return w.Client
}

// SetupWithManager subscribes the worker to both fetch consumers,
// captionarr-fetch-high and captionarr-fetch-normal, as
// [k8s.EveryReplica] runnables: fetch workers are never leader-elected
// (spec §6.5) -- every replica consumes, bounded by the shared KV token
// bucket rather than by how many pods run.
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

// task is a decoded FetchTask plus the request it names.
type task struct {
	schema.FetchTask
	key types.NamespacedName
}

// decodeTask decodes a delivery into a task. Any error is a poison message:
// no redelivery of the same bytes can fix it.
func decodeTask(env *events.Envelope) (task, error) {
	if env == nil {
		return task{}, errors.New("fetch: nil envelope")
	}
	var ft schema.FetchTask
	if err := schema.Decode(env.Schema, env.Data, &ft); err != nil {
		return task{}, err
	}
	ns, name := ft.RequestRef.Namespace, ft.RequestRef.Name
	if keyNS, keyName, ok := strings.Cut(env.Key, "/"); ok {
		ns, name = cmp.Or(ns, keyNS), cmp.Or(name, keyName)
	}
	if ns == "" || name == "" {
		return task{}, fmt.Errorf("fetch: no <namespace>/<name> in requestRef %+v or key %q", ft.RequestRef, env.Key)
	}
	if _, _, _, err := subtitles.ParseLangKey(subtitles.LangKey(ft.LangKey)); err != nil {
		return task{}, err
	}
	return task{FetchTask: ft, key: types.NamespacedName{Namespace: ns, Name: name}}, nil
}

// Handle implements events.Handler.
//
// Settlement follows importarr/worker/fileimport: nil acks, events.Discard
// dead-letters a poison message at once, and any other error is redelivered
// on the consumer's backoff until MaxDeliver dead-letters it. A search that
// finds nothing, or finds every provider throttled, is a result -- recorded
// on the item and acked -- not a failure to redeliver.
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
	ctx = logging.With(ctx, "namespace", t.key.Namespace, "subtitleRequest", t.key.Name, "langKey", t.LangKey)

	if err := w.handle(ctx, m, t); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

func (w *Worker) handle(ctx context.Context, m events.Message, t task) error {
	log := logging.FromContext(ctx)

	var req subtitlev1alpha1.SubtitleRequest
	if err := w.Client.Get(ctx, t.key, &req); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("fetch: the subtitle request no longer exists; nothing to fetch")
			return nil
		}
		return fmt.Errorf("fetch: get subtitle request: %w", err)
	}
	if t.RequestRef.UID != "" && string(req.UID) != t.RequestRef.UID {
		log.Info("fetch: the subtitle request was replaced since this task was published", "taskUID", t.RequestRef.UID)
		return nil
	}
	if !status.LiveItemKeys(req.Status).Has(t.LangKey) {
		// Rule 3 of the item-liveness protocol (status.IsLive): the
		// controller no longer schedules this language, so the want was
		// withdrawn. Record nothing -- the worker never creates an item.
		log.Info("fetch: the controller no longer wants this language; nothing to fetch")
		return nil
	}

	var mf catalogv1alpha1.MediaFile
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Spec.MediaFileRef}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("fetch: the media file no longer exists; nothing to fetch", "mediaFile", req.Spec.MediaFileRef)
			return nil
		}
		return fmt.Errorf("fetch: get media file: %w", err)
	}
	kind := mf.Spec.MediaRef.Kind
	if kind != commonv1.MediaKindMovie && kind != commonv1.MediaKindEpisode {
		return events.Discard("fetch: media file is not a video kind",
			fmt.Errorf("media file %s/%s is a %q", mf.Namespace, mf.Name, kind))
	}
	outOf := int32(subtitles.MaxScore[kind]) //nolint:gosec // 180 or 360

	profile, err := w.resolveProfile(ctx, req.Spec.ProfileRef, mf.Labels)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if profile == nil {
		log.Info("fetch: no subtitle profile applies to this request any more; the controller's replan supersedes the task")
		return nil
	}
	wnt, ok := wantFor(profile, req.Spec.Languages, t.LangKey)
	if !ok {
		log.Info("fetch: the profile no longer wants this language; the controller's replan supersedes the task", "profile", profile.Name)
		return nil
	}

	flt, err := compileFilters(profile.Spec)
	if err != nil {
		msg := truncate(fmt.Sprintf("subtitle profile %s is invalid: %v", profile.Name, err), maxLastError)
		return w.recordFailure(ctx, &req, t.LangKey, outOf, msg)
	}

	local, err := datapath.Local(w.DataDir, mf.Spec.Path)
	if err != nil {
		return events.Discard("fetch: media file path is not on the data volume", err)
	}
	st, err := os.Stat(local)
	if err != nil {
		return w.transient(ctx, m, &req, t.LangKey, outOf, fmt.Errorf("stat media file %s: %w", mf.Spec.Path, err), probeRetry)
	}
	live := mediainfo.ProbeHash(mf.Spec.Path, st.Size(), st.ModTime())
	if planned := cmp.Or(t.ProbeHash, req.Status.ProbeHash); planned != "" && live != planned {
		if mf.Status.ProbeHash == live {
			log.Info("fetch: the media file changed and was re-probed after this task was planned; the replan supersedes it")
			return nil
		}
		return w.transient(ctx, m, &req, t.LangKey, outOf,
			fmt.Errorf("media file %s no longer matches the probe the request was planned against", mf.Spec.Path), probeRetry)
	}

	q, err := w.buildQuery(ctx, &mf, local, st.Size(), wnt.key)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	mode, err := w.sidecarModeFor(ctx, &mf)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	entries, err := w.Providers.Build(ctx, req.Namespace)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	entries = providerset.Order(entries, profile.Spec.Providers)

	cur := findItem(req.Status.Items, t.LangKey)
	onDisk := hasSubtitle(cur)

	var info commonv1.MediaInfo
	if mf.Status.MediaInfo != nil {
		info = *mf.Status.MediaInfo
	}
	src := providerset.FileSource{
		Path: local, Info: info,
		IgnoreASS: profile.Spec.Embedded.IgnoreASS, SkipCommentary: profile.Spec.Embedded.SkipCommentaryOrDefault(),
	}
	provs, skipped := eligible(entries, src, kind, wnt, q, profile.Spec.Embedded.ExtractOrDefault() && mf.Status.MediaInfo != nil)

	plan := searchPlan{
		kind:      kind,
		query:     q,
		want:      wnt,
		filters:   flt,
		threshold: minScore(kind, profile.Spec.MinScorePercent, req.Spec.MinScoreOverride, t.MinScore, cur, onDisk),
		providers: provs,
		skipped:   skipped,
		mods:      mods(profile.Spec.Mods),
		toSRT:     !profile.Spec.OriginalFormat,
		hiExt:     string(profile.Spec.HIExtension),
		mediaPath: mf.Spec.Path,
		mode:      mode,
	}
	out, err := w.search(ctx, m, plan)
	if err != nil {
		var we *writeError
		if errors.As(err, &we) {
			return w.transient(ctx, m, &req, t.LangKey, outOf, err, 0)
		}
		return fmt.Errorf("fetch: %w", err)
	}

	return w.finish(ctx, &req, t.LangKey, plan, out, onDisk, cur, outOf, profile.Spec.Upgrade.EnabledOrDefault())
}

// findItem returns a copy of items' entry for langKey, or nil.
func findItem(items []subtitlev1alpha1.SubtitleItem, langKey string) *subtitlev1alpha1.SubtitleItem {
	for i := range items {
		if items[i].LangKey == langKey {
			return items[i].DeepCopy()
		}
	}
	return nil
}

// hasSubtitle reports whether an item already has a subtitle on disk that
// catalogarr projects into MediaFile.status.sidecars
// (catalogarr/controller/mediafile/sidecars.go: downloaded or upgradable,
// with a path). Such an item is only ever REPLACED by a better one: a search
// that finds nothing better leaves it exactly as it is.
func hasSubtitle(it *subtitlev1alpha1.SubtitleItem) bool {
	return it != nil && it.Path != "" &&
		(it.State == subtitlev1alpha1.SubtitleItemDownloaded || it.State == subtitlev1alpha1.SubtitleItemUpgradable)
}

// minScore is the absolute score a candidate must reach: the profile's
// percentage for the kind (or the request's override) of MaxScore, raised
// to the task's own minScore (the controller's upgrade pass sends
// score+1), and -- when a subtitle is already on disk -- to one point more
// than it, so an upgrade can only ever replace it with something better.
func minScore(kind commonv1.MediaKind, pct subtitlev1alpha1.ScorePct, override *int32, taskMin int32,
	cur *subtitlev1alpha1.SubtitleItem, onDisk bool,
) int {
	p := pct.Movie
	if kind == commonv1.MediaKindEpisode {
		p = pct.Episode
	}
	if override != nil {
		p = *override
	}
	th := max(subtitles.MinScore(kind, int(p)), int(taskMin))
	if onDisk {
		th = max(th, int(cur.Score)+1)
	}
	return th
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
			skipped = append(skipped, e.Name+": embedded extraction is off or the file is not probed yet")
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
