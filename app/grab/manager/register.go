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
// DownloadClient controller (which renders every engine workload) with its
// blocklist sweeper, and the Download controller.
package manager

import (
	"errors"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/grab/controller/download"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
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

// Register registers the DownloadClient and Download reconcilers, plus the
// blocklist sweeper (§6.3, §16 M3; plan tasks D2-3, D2-4, D2-8a).
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
	if err := dcReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: downloadclient: %w", err)
	}
	if err := downloadclient.NewBlocklistSweeper(
		mgr.GetClient(), mgr.GetEventRecorder("downloadclient-blocklist"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: downloadclient blocklist sweeper: %w", err)
	}

	dlReconciler := download.NewReconciler(mgr.GetClient(), mgr.GetEventRecorder("download"), o.DataDir)
	// download.Reconciler.Bus is events.Publisher, not the full events.Bus:
	// see its doc comment -- NewReconciler leaves it nil for callers that
	// exercise only Phase=Assigned, but a real deployment must wire a real
	// bus or a completed Download is never imported.
	dlReconciler.Bus = bus
	if err := dlReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: download: %w", err)
	}
	return nil
}

// engineRuntime is what the DownloadClient controller stamps onto every
// engine pod from this process (downloadclient.EngineRuntime): the engine
// ServiceAccount, this controller's own bus address and single-node
// setting -- the engine joins the same JetStream the controller does -- and
// its $UMASK (design §11), the same pass-through squasharr gives its Jobs.
func engineRuntime(o Options) downloadclient.EngineRuntime {
	return downloadclient.EngineRuntime{
		ServiceAccountName: o.EngineServiceAccount,
		NATSURL:            o.NATSURL,
		BusSingleNode:      o.BusSingleNode,
		Umask:              os.Getenv("UMASK"),
	}
}
