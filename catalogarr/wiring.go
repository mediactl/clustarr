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

package catalogarr

import (
	"context"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/delayprofile"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// The MediaFile controller's RBAC lives here rather than in
// catalogarr/controller/mediafile, which was being rewritten by a concurrent
// task while Task C12a ran and was therefore closed to edits. controller-gen
// folds every marker in RBAC_DIRS into the one clustarr-manager-role
// regardless of which package carries it, so the generated Role is identical
// either way -- but the markers belong next to the controller that needs
// them, and moving them there is a one-line follow-up.
//
// The Events group is "" and not events.k8s.io on purpose: the MediaFile
// reconciler takes a k8s.io/client-go/tools/record.EventRecorder, which is
// what the deprecated mgr.GetEventRecorderFor returns, and that writes CORE/v1
// Events. See setupControllers for the split across this tree.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// resolveQualityProfile turns a QualityProfile name into a resolved
// quality.Profile. It is the production ResolveProfile for
// catalogarr/worker/grab.Sink.
//
// QualityProfile is cluster-scoped (qualityprofile_types.go), so the lookup
// takes no namespace.
func resolveQualityProfile(ctx context.Context, c client.Client, name string, cat *catalogue.Catalogue) (quality.Profile, error) {
	var qp catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return quality.Profile{}, fmt.Errorf("catalogarr: quality profile %q not found", name)
		}
		return quality.Profile{}, err
	}
	profile, errs := quality.FromCRD(&qp, cat)
	if len(errs) > 0 {
		return quality.Profile{}, fmt.Errorf("catalogarr: resolve quality profile %q: %w", name, errs[0])
	}
	return profile, nil
}

// resolveDelayProfile runs §8.2's resolution order (item ref -> tag match ->
// lowest order) over the namespace's DelayProfiles, through the delayprofile
// controller's own pure Resolve. It is the production ResolveDelay for
// catalogarr/worker/grab.Sink.
//
// A namespace with no catch-all profile yields ErrNoMatch, which is "no
// delay", not a failure: the chart installs a catch-all, and an operator who
// removed it meant grabs to be immediate. This matches what the RSS matcher
// does with the same situation, deliberately -- §8.7 requires an RSS hit and
// a search hit to take the same delay/lease/grab path.
func resolveDelayProfile(ctx context.Context, c client.Client, ns string, ref *string, tags []string) (catalogv1alpha1.DelayProfileSpec, error) {
	var list catalogv1alpha1.DelayProfileList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return catalogv1alpha1.DelayProfileSpec{}, fmt.Errorf("catalogarr: list delay profiles: %w", err)
	}
	dp, err := delayprofile.Resolve(ref, tags, list.Items)
	if err != nil {
		logging.FromContext(ctx).Debug("catalogarr: no delay profile applies; grabbing without a delay",
			"namespace", ns, "reason", err)
		return catalogv1alpha1.DelayProfileSpec{}, nil
	}
	return dp.Spec, nil
}

// workerIndexes are every field index the catalogarr queue workers read,
// paired with the object kind they are registered on.
//
// They exist as data, not just as a sequence of calls, because
// [assertWorkerIndexes] has to prove at startup that each one actually
// reached the manager's cache.
var workerIndexes = []struct {
	name string
	// list returns an empty list of the kind the index is registered on.
	list func() client.ObjectList
}{
	// catalogarr/worker/search's three Download indexes: the two halves of
	// the live blocklist and the per-target queue.
	{search.IndexBlocklistInfoHash, func() client.ObjectList { return &downloadv1alpha1.DownloadList{} }},
	{search.IndexBlocklistTitle, func() client.ObjectList { return &downloadv1alpha1.DownloadList{} }},
	{search.IndexDownloadTarget, func() client.ObjectList { return &downloadv1alpha1.DownloadList{} }},

	// catalogarr/worker/rssmatcher's five matching indexes -- §6.1's
	// "informer-backed in-memory map".
	{rssmatcher.IndexMovieTmdbID, func() client.ObjectList { return &catalogv1alpha1.MovieList{} }},
	{rssmatcher.IndexMovieTitleYear, func() client.ObjectList { return &catalogv1alpha1.MovieList{} }},
	{rssmatcher.IndexSeriesTvdbID, func() client.ObjectList { return &catalogv1alpha1.SeriesList{} }},
	{rssmatcher.IndexSeriesTitleYear, func() client.ObjectList { return &catalogv1alpha1.SeriesList{} }},
	{rssmatcher.IndexEpisodeSeriesSeason, func() client.ObjectList { return &catalogv1alpha1.EpisodeList{} }},
}

// registerWorkerIndexes registers every index in [workerIndexes], once, on
// one manager.
//
// It is deliberately NOT a side effect of whichever worker happens to be
// enabled. catalogarr/worker/rssmatcher reads the blocklist and the queue
// through catalogarr/worker/search's three Download indexes, and if they are
// absent its lookups degrade to "not blocklisted, empty queue" with a WARNING
// rather than an error -- so a wiring mistake leaves the RSS path quietly
// grabbing releases the operator has blocklisted, for as long as nobody reads
// the logs. Registering both sets here, from one call, is what makes the
// dependency an ordering fact instead of a coincidence; [assertWorkerIndexes]
// is what proves it held.
//
// A field index name is global to a manager's cache and registering one twice
// is an error, so this must be the only caller of either function.
func registerWorkerIndexes(ctx context.Context, mgr manager.Manager) error {
	if err := search.RegisterDownloadIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("catalogarr: register Download indexes: %w", err)
	}
	if err := rssmatcher.IndexFields(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("catalogarr: register RSS matcher indexes: %w", err)
	}
	return nil
}

// assertWorkerIndexes adds a Runnable that, once the caches have synced,
// issues one cached List per entry in [workerIndexes] and fails the manager
// if any of them is missing.
//
// This is the startup assertion the degraded path needs. controller-runtime's
// cache answers a client.MatchingFields lookup for an unregistered index with
// "no index with name <n> has been registered", and every caller in the RSS
// path swallows that error by design (an unreadable blocklist must not stop
// the firehose). So the only moment the difference between "live" and
// "silently inert" is observable is here, at startup, before any of it
// matters.
//
// It costs eight empty, cache-served Lists once per process.
func assertWorkerIndexes(mgr manager.Manager) error {
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			// The manager is shutting down; nothing to assert.
			return nil
		}
		c := mgr.GetClient()
		for _, idx := range workerIndexes {
			list := idx.list()
			if err := c.List(ctx, list, client.MatchingFields{idx.name: "startup-probe"}); err != nil {
				return fmt.Errorf(
					"catalogarr: field index %q is not registered on this manager, so the queue workers "+
						"would run degraded -- an unregistered blocklist index reads as not blocklisted: %w",
					idx.name, err)
			}
		}
		logging.FromContext(ctx).Info("catalogarr: worker field indexes are live", "indexes", len(workerIndexes))
		<-ctx.Done()
		return nil
	}))
}

// defaultHTTPClient is the outbound client the MetadataProvider reconciler
// probes with. It is a named value rather than http.DefaultClient so a
// misbehaving provider cannot hang a reconcile for the manager's whole
// five-minute ReconciliationTimeout.
var defaultHTTPClient = &http.Client{Timeout: metadataProbeTimeout}

// metadataProbeTimeout bounds one MetadataProvider credential probe.
const metadataProbeTimeout = 30 * time.Second
