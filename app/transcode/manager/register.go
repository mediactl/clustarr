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

// Package manager is transcode's manager-side registration (spec §4.2.1;
// loop spec §3.1 "Registration changes"): transcode-admission, the pools and
// the slim TranscodeProfile reconciler, on the admission ledger the
// remediation loop shares, plus (until F7.3) the AudioGraft controller, which
// runs graft and donor-reduce Jobs on the cpu pool's pod template. A file's
// transcode is its MediaFile's status.transcode, which the loop alone writes
// (ADR-0016); nothing here reads or writes a TranscodeJob. The pool pods run
// cmd/transcode.
package manager

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/audiograft"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/controller/transcodeprofile"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Options is what the transcode manager registration takes.
type Options struct {
	k8s.Options
	Slots             map[string]int32  // the per-hardware admission budget (--slots)
	DataDir           string            // the logical /data root the pools mount
	WorkerImage       string            // the image every pool and graft Job runs
	DataClaimName     string            // the RWX claim the pools mount at DataDir
	IntelRenderGroups []int64           // supplementalGroups for Intel pools
	NodeLabelNVIDIA   string            // the label marking an NVIDIA node
	NodeLabelIntel    string            // the label marking an Intel GPU node
	JobWindow         int               // most files per profile in a window phase (--job-window, loop spec §5.11)
	GraftConcurrency  int               // most audio grafts running at once (--graft-concurrency)
	Ledger            *admission.Ledger // shared with the remediation loop (the manager command builds it once)
	Logging           logging.Options   // rendered onto the pool workers' args
	Tracing           tracing.Options   // likewise
}

// namespacesOf is the namespaces admission rebuilds from: the manager's
// own when it is namespaced, else all (nil).
func namespacesOf(o k8s.Options) []string {
	if o.Namespace == "" {
		return nil
	}
	return []string{o.Namespace}
}

// Register adds transcode-admission, the slim TranscodeProfile reconciler
// (pools and status) and, until F7.3, the AudioGraft controller (loop spec
// §5.6, §5.12, §5.14).
//
// The ledger is the one the remediation loop takes grants from: admission
// rebuilds and resyncs it, passes over it and wakes the loop; the slim
// reconciler renders each profile's pools from its confirmed dispatches and
// counts the profile's files. The budget (o.Slots) and the window
// (o.JobWindow) are the ledger's own, set where it is built.
//
// bus carries clustarr-progress (health, limits, tier), clustarr-transcodes
// and clustarr-transcode-leases, which admission's task sweep withdraws
// through. Register refuses a nil bus or ledger, and a bus that cannot
// administer streams, before it touches mgr.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("transcode manager: Register needs the bus")
	}
	if o.Ledger == nil {
		return errors.New("transcode manager: Register needs the admission ledger the loop shares")
	}
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return fmt.Errorf("transcode manager: %T cannot administer streams", bus)
	}
	cfg := poolConfig(o)
	adm := &admission.Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Ledger: o.Ledger, Bus: bus, Admin: admin,
		Namespaces: namespacesOf(o.Options), Pool: cfg,
		Withdrawal: task.Withdrawal{
			Records: task.Requests(bus.KV(events.BucketTranscodes)),
			Leases:  bus.KV(events.BucketTranscodeLeases), Admin: admin,
		},
	}
	if err := adm.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("transcode: %s: %w", admission.ControllerName, err)
	}
	profiles := &transcodeprofile.Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Ledger: o.Ledger, Pool: cfg,
		Recorder: mgr.GetEventRecorder("transcodeprofile"), Progress: bus.KV(events.BucketProgress),
	}
	if err := profiles.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("transcode: transcodeprofile: %w", err)
	}
	grafts := &audiograft.Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Pool: cfg, Concurrency: o.GraftConcurrency,
	}
	if err := grafts.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("transcode: audiograft: %w", err)
	}
	return nil
}

// PoolConfig is what admission and the slim TranscodeProfile reconciler
// render every pool Job with, and the remediation loop's graft planner its
// standalone graft Jobs (remediationmanager.Options.GraftPool): the one
// pool configuration, so a graft pod is a cpu pool pod's twin.
func PoolConfig(o Options) pool.Config { return poolConfig(o) }

// poolConfig is PoolConfig, from this manager's own options and
// environment. It is a function of o alone so a test can hold each option
// to the pool it reaches (TestPoolConfigCarriesTheControllerOptions):
// several of them -- the render groups, the claim -- have legal empty
// values that only fail on a real node.
func poolConfig(o Options) pool.Config {
	return pool.Config{
		Namespace:         o.Namespace,
		Image:             o.WorkerImage,
		DataClaimName:     o.DataClaimName,
		DataDir:           o.DataDir,
		IntelRenderGroups: o.IntelRenderGroups,
		// §18.5: the labels a GPU class's nodes carry, which admission
		// reads to choose an auto file's class and the class's pools are
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
// --tracing-* flags (pkg/obs/obsflags.Bind, which both internal/cli's
// NewRoot and cmd/transcode call) for a pool's
// workers, so a pool pod logs in the controller's format and level and
// exports its spans -- the transcode.worker.process and transcode.run
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
