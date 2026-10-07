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

package manager

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	squashmanager "github.com/mediactl/clustarr/app/squash/manager"
	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/internal/cli/ctrlflags"
)

// RunFunc starts the manager with parsed options. NewCommandWith takes one so
// cmd/manager's tests can record the options a command line produced without
// starting anything: an unexported variable here could not be stubbed from
// another package.
type RunFunc func(context.Context, Options) error

// NewCommand is `manager`, running Run.
func NewCommand() *cobra.Command { return NewCommandWith(Run) }

// NewCommandWith is `manager` with run in place of Run.
func NewCommandWith(run RunFunc) *cobra.Command {
	root, lo, to := cli.NewRoot("manager",
		"Every Clustarr reconciler in one leader-elected process",
		"manager runs every Clustarr reconciler -- catalog, import, index, grab, squash and\n"+
			"caption -- plus the autoscaler, under one lease, manager.clustarr.io. It is the\n"+
			"only process that creates the JetStream topology; the domain agents wait for it.\n\n"+
			"It renders the download engines and the transcode pools, both from --native-image.")
	root.Args = cobra.NoArgs
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := cli.ApplyUmask(cmd, args); err != nil {
			return err
		}
		if err := ctrlflags.RegisterMetrics(); err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
		return nil
	}

	fs := root.Flags()
	common := ctrlflags.BindManager(fs)
	d := DefaultOptions()
	var slots, renderGroups string

	fs.StringVar(&d.DataDir, "data-dir", d.DataDir,
		"RWX media volume, mounted read-write: renames, library deletes, Download data removal, "+
			"DownloadClient free-space checks and the MediaFile stat run here. Also the mount path "+
			"stamped onto the engines and the transcode pools.")
	fs.StringVar(&d.EngineScratchDir, "engine-scratch-dir", d.EngineScratchDir,
		"Scratch mount path the DownloadClient controller stamps onto the engines: the usenet engine's "+
			"working area for yEnc assembly, PAR2 repair and extraction.")
	fs.StringVar(&d.NativeImage, "native-image", cli.EnvOr(cli.EnvNativeImage, d.NativeImage),
		"Image the manager stamps onto the download engines and every transcode pool, nvidia's included "+
			"(the NVIDIA runtime injects the driver). Required. Defaults to $"+cli.EnvNativeImage+".")
	fs.StringVar(&d.DataClaim, "data-claim", cli.EnvOr(cli.EnvDataClaim, d.DataClaim),
		"RWX PersistentVolumeClaim the engines and the transcode pools mount at --data-dir. "+
			"Defaults to $"+cli.EnvDataClaim+".")
	fs.StringVar(&d.EngineServiceAccount, "engine-service-account",
		cli.EnvOr(cli.EnvEngineServiceAccount, d.EngineServiceAccount),
		"ServiceAccount the engine pods run as; it must hold the engine's RBAC "+
			"(config/rbac/grabarr_engine_role.yaml). Defaults to $"+cli.EnvEngineServiceAccount+", then "+
			d.EngineServiceAccount+".")

	fs.StringVar(&slots, "slots", squashmanager.FormatSlots(d.Slots),
		"Concurrent transcode budget per hardware class, e.g. cpu=2,nvidia=1,intel=1. "+
			"A budget of 0 means that class is never admitted.")
	fs.StringVar(&renderGroups, "intel-render-groups", cli.EnvOr(cli.EnvIntelRenderGroups, ""),
		"Comma-separated GIDs every Intel transcode pool's pod gets as supplementalGroups: the host group "+
			"owning /dev/dri/renderD* on the Intel GPU nodes, e.g. 109 or 44,109. It varies per host install, "+
			"so there is no default; empty relies on the container runtime's "+
			"device_ownership_from_security_context. Defaults to $"+cli.EnvIntelRenderGroups+".")
	fs.StringVar(&d.NodeLabelNVIDIA, "gpu-node-label-nvidia", cli.EnvOr(cli.EnvGPUNodeLabelNVIDIA, d.NodeLabelNVIDIA),
		"Node label that, set to \"true\", marks an NVIDIA GPU node; the NVIDIA GPU Operator's GPU Feature "+
			"Discovery sets the default. hardware: auto sends work to a profile's nvidia pool only while a Ready node "+
			"carries it with allocatable nvidia.com/gpu, and nvidia pools are held to it. Defaults to $"+
			cli.EnvGPUNodeLabelNVIDIA+".")
	fs.StringVar(&d.NodeLabelIntel, "gpu-node-label-intel", cli.EnvOr(cli.EnvGPUNodeLabelIntel, d.NodeLabelIntel),
		"Node label that, set to \"true\", marks an Intel GPU node; Node Feature Discovery's rules from the "+
			"Intel Device Plugins Operator set the default. hardware: auto sends work to a profile's intel pool only "+
			"while a Ready node carries it with allocatable gpu.intel.com/i915, and intel pools are held to it. "+
			"Defaults to $"+cli.EnvGPUNodeLabelIntel+".")
	fs.IntVar(&d.JobWindow, "job-window", d.JobWindow,
		"Most TranscodeJobs a profile keeps that have not finished: it holds the next files, not one job per "+
			"matching file. 0 is no limit.")
	fs.DurationVar(&d.JobRetention, "job-retention", d.JobRetention,
		"How long a Succeeded TranscodeJob is kept once its MediaFile has been re-probed. 0 keeps it for good.")
	fs.IntVar(&d.GraftConcurrency, "graft-concurrency", d.GraftConcurrency,
		"Most audio grafts (AudioGraft Jobs, each a CPU pod of a cpu pool's size) running at once.")

	fs.StringVar(&d.CardigannDefinitionsDir, "cardigann-definitions-dir",
		cli.EnvOr(cli.EnvCardigannDefinitionsDir, d.CardigannDefinitionsDir),
		"Directory of Cardigann definition files (what hack/sync-cardigann writes) to load as IndexerDefinitions "+
			"at startup, so an Indexer's spec.definition can name any of them. When set it replaces the embedded "+
			"corpus. Defaults to $"+cli.EnvCardigannDefinitionsDir+".")
	fs.BoolVar(&d.CardigannBundled, "cardigann-bundled", cli.EnvBoolOr(cli.EnvCardigannBundled, d.CardigannBundled),
		"Load the Cardigann definitions compiled into the binary as IndexerDefinitions when "+
			"--cardigann-definitions-dir is empty. Defaults to $"+cli.EnvCardigannBundled+", else true.")
	fs.StringVar(&d.TraktBaseURL, "trakt-base-url", cli.EnvOr(cli.EnvTraktBaseURL, d.TraktBaseURL),
		"Trakt API the ImportList controller's device-code flow reaches; the import agent's syncs must name "+
			"the same host. Empty is https://api.trakt.tv. Defaults to $"+cli.EnvTraktBaseURL+".")

	fs.BoolVar(&d.Autoscale, "autoscale", d.Autoscale,
		"Scale the autoscaled agent domains with HPAs the manager renders, fed by its own External Metrics API. "+
			"Needs --external-metrics-bind-address. With --autoscale=false the manager deletes the HPAs it labelled.")
	fs.StringVar(&d.ExternalMetricsBindAddress, "external-metrics-bind-address", d.ExternalMetricsBindAddress,
		`Address the External Metrics API (external.metrics.k8s.io/v1beta1) binds to. "0" disables it, and `+
			"--autoscale must then be false.")
	fs.StringVar(&d.ExternalMetricsService, "external-metrics-service",
		cli.EnvOr(cli.EnvExternalMetricsService, d.ExternalMetricsService),
		"Service fronting the External Metrics API: named in the serving certificate and in the APIService's "+
			"spec.service.name. Defaults to $"+cli.EnvExternalMetricsService+".")
	fs.StringVar(&d.ExternalMetricsSecret, "external-metrics-secret",
		cli.EnvOr(cli.EnvExternalMetricsSecret, d.ExternalMetricsSecret),
		"Secret holding the External Metrics API's self-generated CA and serving certificate. "+
			"Defaults to $"+cli.EnvExternalMetricsSecret+".")
	fs.BoolVar(&d.LegacyLeaseCheck, "legacy-lease-check", d.LegacyLeaseCheck,
		"Hold the manager.clustarr.io election shut while any of the six legacy <service>.clustarr.io leases "+
			"is held, so a cutover never runs two writers of one object. For the cutover release only.")

	root.RunE = func(cmd *cobra.Command, _ []string) error {
		o := d
		o.Options = *common
		o.Logging, o.Tracing = *lo, *to
		var err error
		if o.Slots, err = squashmanager.ParseSlots(slots); err != nil {
			return fmt.Errorf("--slots: %w", err)
		}
		if o.IntelRenderGroups, err = ctrlflags.ParseGIDs(renderGroups); err != nil {
			return fmt.Errorf("--intel-render-groups: %w", err)
		}
		if err := o.Validate(); err != nil {
			return err
		}
		return run(cmd.Context(), o)
	}
	return root
}
