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

// Package manager is catalogarr's manager-side registration (spec §4.2.1):
// every catalog reconciler, the two seeding Bootstraps, the wanted sweep,
// the segment planner, and R9's two cluster singletons -- the
// clustarr.io/replay controllers and the artwork Reaper. Wave 5's
// cmd/manager calls Register under the one lease manager.clustarr.io.
package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/delayprofile"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/metadataprovider"
	"github.com/mediactl/clustarr/app/catalog/controller/metadatarefresh"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/overlayprofile"
	"github.com/mediactl/clustarr/app/catalog/controller/qualityprofile"
	"github.com/mediactl/clustarr/app/catalog/controller/rootfolder"
	searchctl "github.com/mediactl/clustarr/app/catalog/controller/search"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/history/replay"
	"github.com/mediactl/clustarr/app/catalog/segmentplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Options is what the catalog manager registration takes: the namespace
// metadataprovider.Bootstrap seeds into and --watch-namespace for the
// wanted sweep, both from the embedded k8s.Options.
type Options struct {
	k8s.Options
}

// Register adds every catalog reconciler and manager-side runnable.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("catalog manager: Register needs the bus")
	}
	if err := registerControllers(mgr, bus, o); err != nil {
		return err
	}
	return registerReplay(mgr, bus)
}

// registerReplay registers the clustarr.io/replay handler (design §5) behind
// the lease (R9): a metadata-only controller per annotatable kind that reads
// a dead letter back off CLUSTARR_DLQ and republishes it. Without a
// JetStream DLQ reader nothing is registered.
func registerReplay(mgr ctrl.Manager, bus events.Bus) error {
	reader, replayable := history.DLQReaderFor(bus)
	if !replayable {
		return nil
	}
	if err := replay.NewReplayer(replay.ReplayDeps{
		Client: mgr.GetClient(), Bus: bus, DLQ: reader, Recorder: mgr.GetEventRecorder("clustarr-replay"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: dlq replay: %w", err)
	}
	return nil
}

// metadataProbeClient is the outbound client the MetadataProvider reconciler
// probes with: a named value, not http.DefaultClient, so a misbehaving
// provider cannot hang a reconcile for the manager's whole
// ReconciliationTimeout.
var metadataProbeClient = &http.Client{Timeout: metadataProbeTimeout}

// metadataProbeTimeout bounds one MetadataProvider credential probe.
const metadataProbeTimeout = 30 * time.Second

// registerControllers registers every catalog reconciler (§6.1, §16 M1 and M6),
// plus the wantedcron sweep, which is a manager.Runnable rather than a
// reconciler because it reconciles nothing -- only a clock.
//
// The seven non-video reconcilers (plan tasks G2-2 and G2-3 built them, G2-5
// wires them) sat in their own packages, tested and registered nowhere, until
// this function named them: every Artist, Album, Author, Book, Audiobook,
// Comic and Issue a user created was accepted by the apiserver and then
// never reconciled -- no metadata task, no fan-out, no phase -- with nothing
// in the logs to say so.
//
// The importer, importlist and importexclusion controllers are NOT here:
// amendment §A1.2/§A1.3 moved them to importarr, whose run.go registers them.
// Building them here would break the MediaFile single-writer split (importarr
// owns what it observed, catalogarr owns what it decided).
//
// One recorder convention: every reconciler here takes a
// k8s.io/client-go/tools/events.EventRecorder from mgr.GetEventRecorder, and
// that writes events.k8s.io/v1 -- the API §13 asks for. The deprecated
// mgr.GetEventRecorderFor, which returns a
// k8s.io/client-go/tools/record.EventRecorder and writes core/v1 Events, is
// gone from this tree, so no package needs a groups="" events rule and the
// generated Role grants events only in events.k8s.io.
//
// One core-group events rule survives and must: controller-runtime's own
// leader election still calls GetEventRecorderFor internally
// (pkg/leaderelection/leader_election.go) and writes core/v1 Events for
// acquire/renew. That rule lives in the hand-written leader-election Role
// (config/rbac/leader_election_role.yaml and the chart's copy), not in the
// marker-generated manager ClusterRole, so deleting it because "nothing
// writes core events any more" would break leader election.
//
// The tree arrived at one convention the hard way, and the lesson outlives
// the split. Until Task C12a the wave 1 controllers (movie, series, episode,
// mediafile, search, and importarr's rootfolderschedule) declared
// events.k8s.io while still writing core/v1 through the deprecated recorder,
// so the generated Role granted a group nobody wrote and omitted the one they
// did: on a real cluster every one of their events would have been denied,
// and no envtest could see it, because envtest does not enforce RBAC. C12a
// made the markers honest by moving them back to ""; this task moved the
// recorders instead, and moved each marker in the same commit. The rule that
// falls out of both: the RBAC marker and the recorder type are one change. A
// marker that disagrees with the recorder beside it passes every test in this
// repo and fails only in production.
func registerControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	c := mgr.GetClient()
	scheme := mgr.GetScheme()

	if err := (&movie.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorder("movie"),
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: movie: %w", err)
	}

	if err := (&series.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorder("series"),
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: series: %w", err)
	}

	if err := (&episode.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorder("episode"),
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: episode: %w", err)
	}

	if err := registerNonVideoControllers(mgr, bus); err != nil {
		return err
	}

	if err := rootfolder.NewReconciler(c, mgr.GetEventRecorder("rootfolder")).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: rootfolder: %w", err)
	}

	cat := catalogue.LoadedCatalogue()
	if err := qualityprofile.NewReconciler(c, cat,
		mgr.GetEventRecorder("qualityprofile")).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: qualityprofile: %w", err)
	}

	// The 13 built-in TRaSH profiles. Without this the reconciler has nothing
	// to reconcile on a fresh cluster: every Movie's and Series'
	// spec.qualityProfileRef resolves to "not found", so the grab Sink and the
	// RSS matcher both refuse every release and the whole decision path is
	// inert while looking healthy. Leader-elected by its own
	// NeedLeaderElection -- seeding is a cluster singleton, not per replica.
	if err := mgr.Add(&qualityprofile.Bootstrap{Client: c, Catalogue: cat}); err != nil {
		return fmt.Errorf("catalogarr: qualityprofile bootstrap: %w", err)
	}

	if err := delayprofile.NewReconciler(c, mgr.GetEventRecorder("delayprofile")).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: delayprofile: %w", err)
	}

	if err := metadataprovider.NewReconciler(c, mgr.GetEventRecorder("metadataprovider"),
		metadataProbeClient).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: metadataprovider: %w", err)
	}

	// The providers that need no key (MusicBrainz, Open Library, Cover Art
	// Archive and the rest), so music, book and anime metadata work on a
	// fresh install. Create-only; leader-elected like the profile seed.
	if err := mgr.Add(&metadataprovider.Bootstrap{Client: c, Namespace: o.Namespace}); err != nil {
		return fmt.Errorf("catalogarr: metadataprovider bootstrap: %w", err)
	}

	if err := searchctl.NewReconciler(c, bus,
		mgr.GetEventRecorder("search"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: search: %w", err)
	}

	// Spec §C.4: which items each OverlayProfile badges, and the render
	// tasks a selection change calls for. The renderer they reach is the
	// catalog agent domain's (app/catalog/agent/catalog).
	if err := (&overlayprofile.Reconciler{Client: c, Bus: bus}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: overlayprofile: %w", err)
	}

	// The operator's forced metadata refresh (clustarr.io/refresh-metadata):
	// one metadata-only controller per kind with metadata of its own, beside
	// the reconcilers that publish the scheduled refreshes.
	if err := metadatarefresh.NewRefresher(metadatarefresh.RefreshDeps{
		Client: c, Bus: bus, Recorder: mgr.GetEventRecorder("metadata-refresh"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: metadata refresh: %w", err)
	}

	// §6.1's twelve-hourly missing/cutoff-unmet sweep. It is leader-elected
	// (see Runnable.NeedLeaderElection), so a sweep fires once per cluster
	// rather than once per replica.
	if err := (&wantedcron.Runnable{
		Client:     c,
		Bus:        bus,
		Schedule:   wantedcron.TwelveHourly(),
		Namespaces: o.WatchNamespaces,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: wantedcron: %w", err)
	}

	// R9 (spec §5.13): the orphan sweep is a cluster singleton behind this
	// role's lease. It used to run in the metadata gateway's process, which
	// is pinned to one replica by its Deployment, not by a lease. The
	// 30-minute grace (artwork.DefaultReapGrace) covers the gateway's Puts
	// from another process.
	//
	// It also purges the orphaned chunks two racing Puts leak, in the
	// artwork and fingerprint buckets (Admin), and audits status.artwork and
	// status.overlay, read from this manager's cache, against the bucket,
	// publishing paced fetch and render tasks for what is missing,
	// mismatched or metadata-stale (artwork design §B.5 as amended
	// 2026-10-07). A bus that is not a StreamAdmin publishes unpaced; one
	// that is not an ObjectStoreAdmin purges nothing.
	reaper := &artwork.Reaper{
		Store:     bus.ObjectStore(events.BucketArtwork),
		Client:    mgr.GetAPIReader(),
		Cache:     mgr.GetClient(),
		Publisher: bus,
	}
	if lag, ok := bus.(events.StreamAdmin); ok {
		reaper.Lag = lag
	}
	if admin, ok := bus.(events.ObjectStoreAdmin); ok {
		reaper.Admin = admin
	}
	if err := mgr.Add(reaper); err != nil {
		return fmt.Errorf("catalogarr: add the artwork reaper: %w", err)
	}

	// The segment analysis planner reads a season's episodes and files
	// through this role's cache and its field indexes (segmentplan.Planner).
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := segmentplan.Setup(ctx, segmentplan.Options{Bus: bus, Reader: c})
		if err != nil {
			return fmt.Errorf("catalogarr: segment planner: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("catalogarr: add the segment planner: %w", err)
	}

	// The clustarr-segments sweep (loop spec §4.11): the bucket's TTL is 0,
	// so a dead file's record is deleted here, once a day, behind the lease.
	// It reads mfindex.UID, which the remediation step registers in this
	// same manager.
	sweeper := &segmentplan.Sweeper{KV: bus.KV(events.BucketSegments), Reader: c}
	if err := mgr.Add(k8s.LeaderOnly(sweeper.Run)); err != nil {
		return fmt.Errorf("catalogarr: segments sweeper: %w", err)
	}

	return nil
}

// registerNonVideoControllers registers the seven non-video reconcilers (§4.2,
// §16 M6), each as its package's doc.go prescribes.
//
// Three parents fan out children through the metadata gateway's
// rpc.catalogarr.metadata.lookup: Artist -> Album, Author -> Book and
// Comic -> Issue. So each of those three needs the bus for its
// Request as well as for publishing its own MetadataTask; with a nil Bus
// every reconcile panics into RecoverPanic before it lists a single child.
// Album, Book and Audiobook are metadata targets of their own and publish
// their own MetadataTasks, so they need it too. Issue fetches no metadata
// of its own -- Comic's fan-out writes its provider fields under
// k8s.ManagerCatalogarrFanout (app/catalog/controller/issue's doc.go) -- but
// it publishes its catalog item events like every other kind, and a nil Bus
// publishes nothing, so it gets the bus as well.
//
// Every recorder is named after its kind, the convention movie and series
// set, so `kubectl get events` attributes each Event to the controller that
// raised it.
func registerNonVideoControllers(mgr ctrl.Manager, bus events.Bus) error {
	c := mgr.GetClient()
	scheme := mgr.GetScheme()

	if err := (&artist.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("artist"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: artist: %w", err)
	}
	if err := (&album.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("album"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: album: %w", err)
	}
	if err := (&author.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("author"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: author: %w", err)
	}
	// Book covers both shapes book_types.go allows: fanned out from an
	// Author, and standalone (no spec.authorRef).
	if err := (&book.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("book"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: book: %w", err)
	}
	if err := (&audiobook.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("audiobook"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: audiobook: %w", err)
	}
	if err := (&comic.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("comic"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: comic: %w", err)
	}
	if err := (&issue.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("issue"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: issue: %w", err)
	}
	return nil
}
