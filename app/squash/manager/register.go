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

// Package manager is squasharr's manager-side registration (spec §4.2.1):
// the TranscodeProfile and TranscodeJob reconcilers, the leader-only results
// consumer that shares the TranscodeJob status CAS path, and the AudioGraft
// controller, which runs graft and donor-reduce Jobs on the cpu pool's pod
// template (the spec's drift note; main 6e44ba35 added the reduce Job, and the
// TranscodeJob dispatcher attaches a waiting graft to the task it dispatches,
// audiograft.JoinTask). The pool pods run cmd/squasharr-worker, which becomes
// cmd/transcode in Wave 5.
package manager

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/squash/controller/audiograft"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	"github.com/mediactl/clustarr/app/squash/controller/transcodeprofile"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Options is what the squash manager registration takes.
type Options struct {
	k8s.Options
	Slots             map[string]int32 // the per-hardware admission budget (--slots)
	DataDir           string           // the logical /data root the pools mount
	WorkerImage       string           // the image every pool and graft Job runs
	DataClaimName     string           // the RWX claim the pools mount at DataDir
	IntelRenderGroups []int64          // supplementalGroups for Intel pools
	NodeLabelNVIDIA   string           // the label marking an NVIDIA node
	NodeLabelIntel    string           // the label marking an Intel GPU node
	JobWindow         int              // most non-terminal TranscodeJobs per profile
	JobRetention      time.Duration    // how long a Succeeded job outlives its re-probe
	GraftConcurrency  int              // most audio grafts running at once (--graft-concurrency)
	Logging           logging.Options  // rendered onto the pool workers' args
	Tracing           tracing.Options  // likewise
}

// Register adds the three reconcilers and the results consumer (§6.4, §16
// M4): the TranscodeProfile controller, which hashes profiles and creates a
// TranscodeJob per file a profile wins; the TranscodeJob controller, which
// plans, admits against the --slots budget and dispatches each job's task to
// its pool over bus, and whose results consumer turns the pool workers'
// status events on squasharr-transcode-results into TranscodeJob status
// (spec 2026-09-23 §18.2); and the AudioGraft controller.
//
// The TranscodeJob reconciler reads TranscodeJobs through
// mgr.GetAPIReader(), never the cache: its status writes are conditional on
// the resourceVersion they read, and admission counts the jobs it
// dispatched a moment ago; a cache one event behind would conflict on the
// first and admit past the budget on the second (ADR-0005).
//
// bus carries the tasks, the results and the controller's
// clustarr.evt.transcode.job.* history events (§5); Leases is the bucket
// withdrawal writes its cancel markers to. Register refuses a nil bus before
// it touches mgr.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("squash manager: Register needs the bus")
	}
	profiles := transcodeprofile.NewReconciler(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorder("transcodeprofile"))
	profiles.Window, profiles.Retention = o.JobWindow, o.JobRetention
	profiles.Progress = bus.KV(events.BucketProgress)
	if err := profiles.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: transcodeprofile: %w", err)
	}
	rec := &transcodejob.Reconciler{
		Client:   mgr.GetClient(),
		Reader:   mgr.GetAPIReader(),
		Slots:    o.Slots,
		Pool:     poolConfig(o),
		Recorder: mgr.GetEventRecorder("transcodejob"),
		Bus:      bus,
		Leases:   bus.KV(events.BucketTranscodeLeases),
	}
	// natsbus and membus both implement events.StreamAdmin; the comma-ok
	// form only keeps a bus that does not (a narrower test double) from
	// panicking a real run.
	if admin, ok := bus.(events.StreamAdmin); ok {
		rec.Admin = admin
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: transcodejob: %w", err)
	}
	if err := mgr.Add(rec.ResultsConsumer()); err != nil {
		return fmt.Errorf("squasharr: transcode results consumer: %w", err)
	}
	grafts := &audiograft.Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Pool: poolConfig(o), Concurrency: o.GraftConcurrency,
	}
	if err := grafts.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: audiograft: %w", err)
	}
	return nil
}

// poolConfig is what the TranscodeJob controller renders every pool Job
// with, from this controller's own options and environment. It is a
// function of o alone so a test can hold each option to the pool it reaches
// (TestPoolConfigCarriesTheControllerOptions): several of them -- the render
// groups, the claim -- have legal empty values that only fail on a real
// node.
func poolConfig(o Options) pool.Config {
	return pool.Config{
		Namespace:         o.Namespace,
		Image:             o.WorkerImage,
		DataClaimName:     o.DataClaimName,
		DataDir:           o.DataDir,
		IntelRenderGroups: o.IntelRenderGroups,
		// §18.5: the labels a GPU class's nodes carry, which admission
		// reads to choose an auto job's class and the class's pools are
		// held to.
		NodeLabelNVIDIA: o.NodeLabelNVIDIA,
		NodeLabelIntel:  o.NodeLabelIntel,
		// §11: the pools create files with the same UMASK this
		// Deployment was given.
		Umask: os.Getenv(pool.UmaskEnv),
		// The pool pods pull their tasks from, and report on, the
		// controller's own bus.
		NATSURL: o.NATSURL,
		// The workers log and trace as this controller does: without
		// these their spans -- the ffmpeg run's among them -- would be
		// recorded into a TracerProvider exporting nowhere.
		ExtraArgs: workerObservabilityArgs(o.Logging, o.Tracing),
	}
}

// workerObservabilityArgs renders the root command's --log-* and
// --tracing-* flags (pkg/obs/obsflags.Bind, which both cmd/clustarr's
// bindObservabilityFlags and cmd/squasharr-worker call) for a pool's
// workers, so a pool pod logs in the controller's format and level and
// exports its spans -- the squasharr.worker.process and transcode.run
// (ffmpeg) spans -- to the same collector. Only what differs from the flags'
// defaults is rendered. TestWorkerObservabilityArgsParse holds the names to
// the flags.
func workerObservabilityArgs(lo logging.Options, to tracing.Options) []string {
	var args []string
	if lo.Level != 0 {
		args = append(args, "--log-level="+lo.Level.String())
	}
	if lo.Format != "" {
		args = append(args, "--log-format="+lo.Format)
	}
	if lo.AddSource {
		args = append(args, "--log-add-source")
	}
	if to.Enabled {
		args = append(args, "--tracing-enabled")
		if to.Endpoint != "" {
			args = append(args, "--tracing-endpoint="+to.Endpoint)
		}
		if to.Insecure {
			args = append(args, "--tracing-insecure")
		}
	}
	// The sampler is parent-based, so a worker whose task carries a sampled
	// trace is sampled whatever this says; it decides only a trace the
	// worker starts itself.
	args = append(args, "--tracing-sample-ratio="+strconv.FormatFloat(to.SampleRatio, 'g', -1, 64))
	return args
}
