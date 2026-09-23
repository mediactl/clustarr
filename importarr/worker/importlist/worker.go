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
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// defaultMetadataTimeout bounds one id-resolve RPC, matching
// importarr/worker/rescan's own default for the same call.
const defaultMetadataTimeout = 10 * time.Second

// Worker handles clustarr.work.importarr.list.* messages: one message is
// one ImportList's sync, across every catalog kind its spec.kinds names.
// See this package's doc comment for how importarr/run.go registers it.
//
// The worker is never the writer of ImportList.status: k8s.ManagerImportarr
// (the controller's field manager) owns it in full, per that constant's own
// doc comment, and this worker instead checkpoints its result to a
// clustarr-progress key (see Result and ResultKey) that the controller
// reads -- the same split importarr/worker/rescan uses for LibraryScan --
// and then stamps [AnnotationSyncedAt] so the controller reads it now.
type Worker struct {
	// Client reads the ImportList, its Secret/ConfigMap, and creates or
	// updates the Movie and Series items a sync produces.
	Client client.Client

	// Bus carries the result checkpoint, the exclusion and remembered-item
	// KV buckets, the id-resolve RPC and the history event.
	Bus events.Bus

	// HTTPClient is used by every provider that makes its own HTTP calls.
	// Nil means http.DefaultClient.
	HTTPClient *http.Client

	// TraktBaseURL and PlexBaseURL override the Trakt API and Plex Discover
	// hosts (ProviderOptions). Empty means production.
	TraktBaseURL string
	PlexBaseURL  string

	// MetadataTimeout bounds one id-resolve RPC. Zero means
	// defaultMetadataTimeout.
	MetadataTimeout time.Duration

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

// The import-list worker's RBAC. It creates and updates Movie and Series
// spec, reads Secrets and ConfigMaps for provider credentials and CSV
// content, and writes the owned Trakt token Secret. It patches one
// annotation on the ImportList itself ([AnnotationSyncedAt]) but never
// importlists/status (the controller's alone), and never writes
// MovieStatus/SeriesStatus, both of which catalogarr owns in full. Under
// syncLevel removeAndDelete it reads the item's Episodes and RootFolder and
// deletes the MediaFiles whose files it recycled.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importlists,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get

// NewWorker builds a Worker with the production clock, timeout and HTTP
// client.
func NewWorker(c client.Client, bus events.Bus) *Worker {
	return &Worker{
		Client: c, Bus: bus, HTTPClient: http.DefaultClient,
		MetadataTimeout: defaultMetadataTimeout, Clock: time.Now,
	}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

func (w *Worker) deps() syncDeps {
	return syncDeps{
		Client: w.Client, Bus: w.Bus,
		Providers: ProviderOptions{
			HTTPClient: w.HTTPClient, TraktBaseURL: w.TraktBaseURL, PlexBaseURL: w.PlexBaseURL,
		},
		MetadataTimeout: w.MetadataTimeout, Clock: w.Clock,
	}
}

// Handle implements events.Handler.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "importlist.Worker.Handle")
	defer span.End()

	if env == nil {
		return events.Discard("import-list task has no envelope", errors.New("importlist: nil envelope"))
	}

	var task schema.ListTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("importlist: undecodable ListTask", err)
	}

	ns, name, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		ns, name = task.ListRef.Namespace, task.ListRef.Name
	}
	if name == "" {
		return events.Discard("importlist: no list name in envelope key or task",
			fmt.Errorf("key=%q listRef=%+v", env.Key, task.ListRef))
	}

	log := logging.FromContext(ctx).With("namespace", ns, "importList", name)
	ctx = logging.NewContext(ctx, log)

	var il catalogv1alpha1.ImportList
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &il); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("importlist: list no longer exists", err)
		}
		return fmt.Errorf("importlist: get list %s/%s: %w", ns, name, err)
	}
	if task.ListRef.UID != "" && string(il.UID) != task.ListRef.UID {
		return events.Discard("importlist: list was replaced", fmt.Errorf(
			"importlist: list %s/%s has uid %s, task was published for %s",
			ns, name, il.UID, task.ListRef.UID))
	}
	if k8s.IsDeleting(&il) {
		log.Debug("importlist: list is being deleted; skipping")
		return nil
	}
	if il.Spec.Enabled != nil && !*il.Spec.Enabled {
		log.Debug("importlist: list is disabled; skipping")
		return nil
	}

	result := w.syncAllKinds(ctx, &il)

	kv := w.Bus.KV(events.BucketProgress)
	data, err := result.Encode()
	if err != nil {
		return fmt.Errorf("importlist: encode result: %w", err)
	}
	if _, err := kv.Put(ctx, ResultKey(string(il.UID)), data); err != nil {
		return fmt.Errorf("importlist: checkpoint result: %w", err)
	}
	if err := StampSynced(ctx, w.Client, &il, result.SyncedAt); err != nil {
		// The checkpoint is durable; only the nudge failed. The controller
		// still projects it at its next scheduled reconcile, so this is
		// not worth redoing the whole sync over.
		log.Warn("importlist: could not stamp the list; status catches up at the next scheduled sync",
			"error", err)
	}

	if result.Error != "" {
		log.Warn("importlist: sync finished with errors", "error", result.Error,
			"fetched", result.Fetched, "added", result.Added,
			"excluded", result.Excluded, "removed", result.Removed)
	} else {
		log.Info("importlist: sync finished", "fetched", result.Fetched, "added", result.Added,
			"excluded", result.Excluded, "removed", result.Removed)
	}
	return nil
}

// AnnotationSyncedAt is stamped on an ImportList by the worker when a sync
// finishes, with the checkpointed Result's SyncedAt (RFC 3339, nanoseconds).
// It is how the ImportList controller learns a sync completed: its For()
// predicate passes a change to this value, so the new Result is projected
// into status as soon as it lands rather than at the next scheduled sync
// (nextSyncAt, up to a day away). The value is only a signal; the Result in
// the clustarr-progress bucket stays the source of what status says.
const AnnotationSyncedAt = "catalog.clustarr.io/importlist-synced-at"

// StampSynced applies [AnnotationSyncedAt] to il under [FieldManager], the
// one field that manager owns on an ImportList, so every apply is its
// complete declaration there. It never touches spec or status.
func StampSynced(ctx context.Context, c client.Client, il *catalogv1alpha1.ImportList, at time.Time) error {
	ac := catalogac.ImportList(il.Name, il.Namespace).
		WithAnnotations(map[string]string{AnnotationSyncedAt: at.UTC().Format(time.RFC3339Nano)})
	_, err := k8s.Apply(ctx, c, FieldManager, ac)
	return err
}

// syncAllKinds runs syncKind for every kind il.Spec.Kinds names and
// aggregates the outcome into one Result.
func (w *Worker) syncAllKinds(ctx context.Context, il *catalogv1alpha1.ImportList) Result {
	deps := w.deps()
	var (
		fetched, added, excluded, removed int32
		errs                              []error
	)
	for _, k := range il.Spec.Kinds {
		kr := syncKind(ctx, deps, il, commonv1.MediaKind(k))
		fetched += kr.fetched
		added += kr.added
		excluded += kr.excluded
		removed += kr.removed
		if kr.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, kr.err))
		}
	}

	r := Result{
		SyncedAt: w.now(), Fetched: fetched, Added: added, Excluded: excluded, Removed: removed,
	}
	if len(errs) > 0 {
		r.Error = errors.Join(errs...).Error()
	}
	return r
}
