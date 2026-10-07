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

// Package manager is the remediation loop's manager-side registration (split
// spec §4.2.1's contract; loop spec §3.1): the loop, its indexes, its
// planners and actuators, under the manager's one lease.
package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/history/replay"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/import/mediafilespec/specwrite"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/remediation"
	artworkstage "github.com/mediactl/clustarr/app/remediation/artwork"
	"github.com/mediactl/clustarr/app/remediation/downloads"
	grabstage "github.com/mediactl/clustarr/app/remediation/grab"
	"github.com/mediactl/clustarr/app/remediation/graft"
	markersplanner "github.com/mediactl/clustarr/app/remediation/markers"
	metadatastage "github.com/mediactl/clustarr/app/remediation/metadata"
	"github.com/mediactl/clustarr/app/remediation/naming"
	"github.com/mediactl/clustarr/app/remediation/probe"
	"github.com/mediactl/clustarr/app/remediation/rename"
	replayactuator "github.com/mediactl/clustarr/app/remediation/replay"
	searchstage "github.com/mediactl/clustarr/app/remediation/search"
	"github.com/mediactl/clustarr/app/remediation/subtitles"
	"github.com/mediactl/clustarr/app/remediation/transcode"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/probestore"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
)

// Options is what the loop's registration takes.
type Options struct {
	k8s.Options
	DataDir             string
	Concurrency         int // --remediation-concurrency
	BulkWritesPerSecond int // --remediation-bulk-writes-per-second
	IOWorkers           int // --remediation-io-workers

	// The manager's admission and intake planes (ADR-0019 §5.4, §8.1),
	// handed to the item stages (A3.3): the dispatch ledger, the candidate
	// inbox, the advisory intake's wakes and its delivery book. nil leaves
	// the item path without stages' admission, S30 or delivery wakes.
	Dispatch      *dispatch.Ledger
	Inbox         *intake.Inbox
	DeliveryWakes <-chan history.Target
	Book          *dispatch.DeliveryBook

	// Artwork is the manager's objindex over clustarr-artwork (ADR-0019
	// §7.1-§7.2, D13): the metadata stage renders status.artwork from it,
	// the artwork stages plan fetches and overlays against it, and its
	// changes wake the items (S26). Nil runs no artwork stage and keeps
	// every stored status.artwork.
	Artwork *objindex.Index
	// The search planner's inputs (ADR-0019 §7.3, §5.4): wantedcron's
	// sweep, the first-time live donor budget it refills, and the
	// live-search pace per namespace (--search-live-per-minute; 0 is the
	// default rate).
	WantedWakes         <-chan schema.ItemRef
	DonorBudget         *searchplan.DonorBudget
	SearchLivePerMinute int

	// Clock is the loop's pass clock; nil is time.Now (§5.17's fake-clock
	// envtests drive View.Now through it).
	Clock func() time.Time

	// Ledger is the transcode admission ledger the loop shares with
	// transcode-admission (loop spec §5.5): internal/cli/manager builds it
	// once and hands both the same one. Required.
	Ledger *admission.Ledger

	// SubtitleProfileWake is the slim SubtitleProfile reconciler's
	// subtitleProfileWake (loop spec §2.13): the loop calls it when a file's
	// subtitles profile or phase changes. internal/cli/manager hands the
	// caption manager the same Wake.
	SubtitleProfileWake func()

	// GraftPool is the cpu pool's pod template a standalone graft Job runs
	// on (loop spec §4.12): the transcode manager's pool configuration
	// (transcodemanager.PoolConfig), so a graft pod is the pool pod's twin.
	GraftPool pool.Config
}

// Deps is what Planners, Actuators and Stages build from.
type Deps struct {
	Env     *remediation.Env
	Options Options
	// Manager is the controller manager, for the stages that need its
	// clients and recorders.
	Manager ctrl.Manager
	// CaptionBacklog is §6.4.6's gate the subtitles planner reads; nil
	// keeps every scheduled subtitle dispatch waiting.
	CaptionBacklog *remediation.CaptionBacklog
}

// Register adds the loop, its indexes and its sources: the file path's
// planners and actuators, and the item path's six item kinds (§3.12).
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("remediation manager: Register needs the bus")
	}
	if o.Ledger == nil {
		return errors.New("remediation manager: Register needs the transcode admission ledger transcode-admission shares")
	}
	if err := remediation.RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("remediation: indexes: %w", err)
	}
	rates := records.DefaultRates()
	if o.SearchLivePerMinute > 0 {
		rates[records.PaceSearch] = o.SearchLivePerMinute
	}
	env := &remediation.Env{
		Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: bus,
		IO:    remediation.NewIOExecutor(max(o.IOWorkers, 1)),
		Pacer: records.NewPacer(rates, time.Now), DataDir: o.DataDir,
	}
	if o.Dispatch != nil {
		// catalog-search-normal's outstanding searches, rebuilt from the
		// items' searchDispatch after a leader change (A4.2 step 4).
		o.Dispatch.Register(searchstage.LedgerSource{})
	}
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return errors.New("remediation: the subtitle backlog gate needs a bus with a StreamAdmin")
	}
	backlog := remediation.NewCaptionBacklog(admin, time.Now)
	if err := mgr.Add(backlog); err != nil {
		return fmt.Errorf("remediation: subtitle backlog gate: %w", err)
	}
	d := Deps{Env: env, Options: o, Manager: mgr, CaptionBacklog: backlog}
	r, err := remediation.NewReconciler(mgr.GetClient(), env, mgr.GetEventRecorder(remediation.ControllerName),
		remediation.Config{Concurrency: o.Concurrency, BulkWritesPerSecond: o.BulkWritesPerSecond},
		Planners(d), Actuators(mgr, d)...)
	if err != nil {
		return err
	}
	r.SubtitleProfileWake = o.SubtitleProfileWake
	r.Ledger = o.Ledger
	if o.Clock != nil {
		r.Clock = o.Clock
	}
	// mgr.GetEventRecorder returns controller-runtime's recorder.EventRecorder,
	// which embeds the events.k8s.io EventRecorder the item reconcilers hold.
	recorder := func(name string) k8sevents.EventRecorder { return mgr.GetEventRecorder(name) }
	r.Items = remediation.ItemReconciler{
		Items:    items(mgr.GetClient(), mgr.GetScheme(), recorder, bus),
		Stages:   Stages(d),
		Env:      env,
		Dispatch: o.Dispatch, Book: o.Book, Inbox: o.Inbox, DeliveryWakes: o.DeliveryWakes,
		WantedWakes: o.WantedWakes,
		Client:      mgr.GetClient(),
		Recorder:    recorder,
		// The import materialisation's MediaFile spec writer (A3.8): the
		// manager-only specwrite, under import-worker.
		SpecWriter: specWriter,
	}
	return r.SetupWithManager(mgr)
}

// specWriter applies a placed file's MediaFile spec through
// specwrite.ApplyFacts, the one render of import-worker's set.
func specWriter(ctx context.Context, c client.Client, e remediation.ApplyMediaFileSpec) error {
	_, err := specwrite.ApplyFacts(ctx, c, e.Namespace, e.Name, e.RV, e.Ref, e.Path, e.SizeBytes, e.ModTime, e.Frozen)
	return err
}

// Stages is every item stage the loop runs around the kinds' rollups
// (ADR-0019 A3.3, ruling R5): A3.5 adds the downloads stage, A4 grab and
// search, A5 metadata, artwork and overlay. The loop orders them by
// remediation.StageOrder.
func Stages(d Deps) []remediation.ItemStage {
	if d.Manager == nil {
		return nil
	}
	recorder := func(name string) k8sevents.EventRecorder { return d.Manager.GetEventRecorder(name) }
	stages := []remediation.ItemStage{
		// The grab decision is the owner key's (ADR-0019 §6.6, A4.1).
		grabstage.New(grabstage.Options{Reader: d.Manager.GetClient(), Bus: d.Env.Bus, Inbox: d.Options.Inbox}),
		downloads.New(downloads.Options{
			Reader: d.Manager.GetClient(), APIReader: d.Manager.GetAPIReader(), Bus: d.Env.Bus,
			Dispatch: d.Options.Dispatch, Book: d.Options.Book, Recorder: recorder,
		}),
		// The search planner (ADR-0019 §7.3, A4.2).
		searchstage.New(searchstage.Options{
			Reader: d.Manager.GetClient(), Bus: d.Env.Bus,
			Dispatch: d.Options.Dispatch, Book: d.Options.Book, Budget: d.Options.DonorBudget,
		}),
	}
	meta := metadatastage.Options{Bus: d.Env.Bus}
	if idx := d.Options.Artwork; idx != nil {
		meta.Artwork = func(obj client.Object) ([]catalogv1alpha1.ArtworkEntry, bool) {
			return catalogartwork.Entries(idx, obj)
		}
	}
	stages = append(stages, metadatastage.New(meta))
	if d.Options.Artwork != nil {
		art := artworkstage.Options{Bus: d.Env.Bus, Index: d.Options.Artwork, Dispatch: d.Options.Dispatch}
		stages = append(stages, artworkstage.New(art), artworkstage.NewOverlay(art))
	}
	return stages
}

// Planners is every planner the loop binds; the loop sorts them by Order.
func Planners(d Deps) []remediation.Bound {
	probes := probestore.New(d.Env.Bus, probestore.WithErrors(func(op string) {
		metrics.RecordErrorsTotal.WithLabelValues("probe", op).Inc()
	}))
	subs := subtitles.Options{Bus: d.Env.Bus, Store: subtitlestore.New(d.Env.Bus, ""), DataDir: d.Env.DataDir}
	if d.CaptionBacklog != nil {
		subs.BacklogOpen = d.CaptionBacklog.Open
	}
	return []remediation.Bound{
		remediation.Bind[mediafile.ProbeInput](probe.New(probe.Options{Bus: d.Env.Bus, Probes: probes})),
		// The transcode planner (loop spec §5.8-§5.11).
		remediation.Bind[transcode.In](transcode.New(transcodeOptions(d))),
		// The graft planner (loop spec §4.12, §5.13): after transcode, whose
		// dispatch decides a join.
		remediation.Bind[graft.In](graft.New(d.Env.Bus, graft.Options{Pool: d.Options.GraftPool, Ledger: graftLedger(d)})),
		remediation.Bind[naming.Input](naming.New(naming.Options{})),
		// The subtitles planner (loop spec §6.4); its adapter is also the
		// Withdrawer of the withdrawals it emits.
		remediation.Bind[subtitles.In](subtitles.New(subs)),
		remediation.Bind[markersplanner.Input](markersplanner.New(markersplanner.Options{Bus: d.Env.Bus})),
	}
}

// Actuators is every actuator; the loop orders them, replay before rename
// (§3.4 step 10). Replay needs a bus that reads dead letters back.
func Actuators(mgr ctrl.Manager, d Deps) []remediation.Actuator {
	acts := []remediation.Actuator{rename.New(rename.Options{Recorder: mgr.GetEventRecorder("rename")})}
	if reader, ok := history.DLQReaderFor(d.Env.Bus); ok {
		acts = append(acts, replayactuator.New(replay.NewReplayer(replay.ReplayDeps{
			Client: mgr.GetClient(), Bus: d.Env.Bus, DLQ: reader, Recorder: mgr.GetEventRecorder("clustarr-replay"),
		})))
	}
	return acts
}

// graftLedger is the ledger the graft planner takes its graft-slot grants
// from; nil (no ledger) grants none.
func graftLedger(d Deps) transcodeplan.Admission {
	if d.Options.Ledger == nil {
		return nil
	}
	return d.Options.Ledger
}

// transcodeOptions is the transcode planner's: the ledger it takes grants
// from (through transcodeplan.Admission) and S22, admission's wake of the
// loop's queue.
func transcodeOptions(d Deps) transcode.Options {
	o := transcode.Options{
		Bus:     d.Env.Bus,
		Records: task.Requests(d.Env.Bus.KV(events.BucketTranscodes)), Leases: d.Env.Bus.KV(events.BucketTranscodeLeases),
	}
	if admin, ok := d.Env.Bus.(events.StreamAdmin); ok {
		o.Admin = admin
		o.Withdrawal = task.Withdrawal{Records: o.Records, Leases: o.Leases, Admin: admin}
	}
	if l := d.Options.Ledger; l != nil {
		o.Ledger = l
		o.LoopSource = admission.LoopSource(l, func(nn types.NamespacedName) remediation.Key {
			return remediation.Key{Kind: remediation.KindMediaFile, Namespace: nn.Namespace, Name: nn.Name}
		})
	}
	return o
}
