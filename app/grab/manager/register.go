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

// Package manager is grabarr's manager-side registration (spec §4.2.1): the
// DownloadClient controller, which renders every engine workload, ensures
// the per-engine durables, reads the engine records and resyncs and prunes
// the engines (ADR-0019 §6.7). The Download controller and the blocklist
// sweeper are gone: grabs are entries on their owners, run by the
// remediation loop's downloads stage, and the blocklist is the release
// index's.
package manager

import (
	"errors"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// Options is what the grab manager registration takes.
type Options struct {
	k8s.Options

	// DataDir is the /data mount, stamped onto engines; the Download
	// finalizer removes under it.
	DataDir string

	// ScratchDir is the scratch mount path stamped onto engines.
	ScratchDir string

	// EngineImage is the image every engine workload runs.
	EngineImage string

	// DataClaimName is the RWX claim every engine mounts at DataDir.
	DataClaimName string

	// EngineServiceAccount is the account every engine pod runs as.
	EngineServiceAccount string
}

// Cache contributions: none beyond today's Secret DisableFor, which the
// shim's ManagerOptions keeps (Wave 5's merged cache owns it, §5.6).

// Register registers the DownloadClient reconciler (§6.3, §16 M3; ADR-0019
// A3.7).
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("grab manager: Register needs the bus")
	}
	dcReconciler := downloadclient.NewReconciler(
		mgr.GetClient(), mgr.GetEventRecorder("downloadclient"), o.DataDir, o.ScratchDir, o.EngineImage,
	)
	// NewReconciler defaults the claim to config/'s "clustarr-data"; the
	// chart's is "<release fullname>-data", so the flag must win or every
	// engine under any other release name mounts a claim that does not exist.
	dcReconciler.DataClaimName = o.DataClaimName
	dcReconciler.Engine = engineRuntime(o)
	// By name and uncached, so reading a provider Secret needs only get.
	dcReconciler.SecretReader = mgr.GetAPIReader()
	// The engine records, the per-engine durables and the commands
	// (ADR-0019 §5.1, §6.7). The ProxyUDPUnavailable and
	// UnidentifiedTransferRemoved Events are recorded as grabarr-engine
	// (§7.7), though the manager records them.
	dcReconciler.Bus = bus
	if admin, ok := bus.(events.StreamAdmin); ok {
		dcReconciler.Admin = admin
	}
	dcReconciler.Engines = records.NewReader(bus.KV(events.BucketEngines), agentrecords.Engines())
	dcReconciler.EngineRecorder = mgr.GetEventRecorder("grabarr-engine")
	if err := dcReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: downloadclient: %w", err)
	}
	return nil
}

// engineRuntime is what the DownloadClient controller stamps onto every
// engine pod from this process (downloadclient.EngineRuntime): the engine
// ServiceAccount, this controller's own bus address -- the engine joins the
// same JetStream the manager does, and waits for the topology the manager
// ensures (spec §3.5.5) -- and its $UMASK (design §11), the same
// pass-through the transcode pools get.
func engineRuntime(o Options) downloadclient.EngineRuntime {
	return downloadclient.EngineRuntime{
		ServiceAccountName: o.EngineServiceAccount,
		NATSURL:            o.NATSURL,
		Umask:              os.Getenv("UMASK"),
	}
}
