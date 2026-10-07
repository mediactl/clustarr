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

// Package manager is captionarr's manager-side registration (spec §4.2.1):
// the SubtitleProfile controller and its Bootstrap, the SubtitleProvider
// controller (the only writer of provider status, ruling R2) and the
// SubtitleRequest controller.
package manager

import (
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/caption/controller/subtitleprofile"
	"github.com/mediactl/clustarr/app/caption/controller/subtitleprovider"
	"github.com/mediactl/clustarr/app/caption/controller/subtitlerequest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Options is what the caption manager registration takes.
type Options struct {
	k8s.Options
	// DataDir is where the RWX media volume is mounted; SubtitleRequest
	// reads a media file's directory through it (app/caption/datapath).
	DataDir string
}

// Register registers captionarr's three reconcilers (§6.5, §16 M5):
//
//   - subtitleprofile validates each SubtitleProfile and ensures one
//     SubtitleRequest per video-kind MediaFile the profile wins, woken by
//     MediaFile.status.probeHash (task F-3);
//   - subtitleprovider validates each SubtitleProvider through
//     app/caption/providerset.Validate and projects the shared
//     clustarr-provider-throttle KV state into its status -- ruling R2 makes
//     it that status's only writer (task F-3);
//   - subtitlerequest plans each request, publishes a fetch task for every
//     language that is due, and runs the adaptive and upgrade cadences
//     (task F-4). It needs the bus -- a nil Bus fails every reconcile that
//     has a task to send -- and o.DataDir, through which it reads the media
//     file's directory exactly as the fetch worker does.
//
// Secrets are read through the API reader, never the cache: a cached Get
// would start a cluster-wide Secret informer.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("caption manager: Register needs the bus")
	}
	c := mgr.GetClient()
	if err := subtitleprofile.NewReconciler(
		c, mgr.GetScheme(), mgr.GetEventRecorder("subtitleprofile"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("captionarr: subtitleprofile: %w", err)
	}
	// An English default profile on a cluster that has none, so subtitles
	// work without setup. Create-only; leader-elected.
	if err := mgr.Add(&subtitleprofile.Bootstrap{Client: c, Namespace: o.Namespace}); err != nil {
		return fmt.Errorf("captionarr: subtitleprofile bootstrap: %w", err)
	}

	provider := subtitleprovider.NewReconciler(c, bus.KV(events.BucketProviderThrottle), mgr.GetEventRecorder("subtitleprovider"))
	provider.Secrets = mgr.GetAPIReader()
	if err := provider.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("captionarr: subtitleprovider: %w", err)
	}

	if err := (&subtitlerequest.Reconciler{
		Client:  c,
		Bus:     bus,
		DataDir: o.DataDir,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("captionarr: subtitlerequest: %w", err)
	}
	return nil
}
