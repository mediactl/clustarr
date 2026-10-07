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

// Package manager is caption's manager-side registration (spec §4.2.1):
// the slim SubtitleProfile reconciler, its Bootstrap and the loop's Wake that
// feeds it, and the SubtitleProvider controller (the only writer of provider
// status, ruling R2). A file's subtitles are the remediation loop's (loop
// spec §6).
package manager

import (
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/caption/controller/subtitleprofile"
	"github.com/mediactl/clustarr/app/caption/controller/subtitleprovider"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Options is what the caption manager registration takes.
type Options struct {
	k8s.Options
	// ProfileWake is the loop's subtitleProfileWake; internal/cli/manager
	// hands the same Wake to the remediation loop.
	ProfileWake *subtitleprofile.Wake
}

// Register registers caption's two reconcilers:
//
//   - subtitleprofile validates each SubtitleProfile and counts the files
//     the loop planned with it, woken by its own generation and by
//     o.ProfileWake (loop spec §2.13); never by MediaFile;
//   - subtitleprovider validates each SubtitleProvider through
//     app/caption/providerset.Validate and projects the shared
//     clustarr-provider-throttle KV state into its status -- ruling R2 makes
//     it that status's only writer.
//
// Secrets are read through the API reader, never the cache: a cached Get
// would start a cluster-wide Secret informer.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("caption manager: Register needs the bus")
	}
	if o.ProfileWake == nil {
		return errors.New("caption manager: Register needs the SubtitleProfile wake the loop signals")
	}
	c := mgr.GetClient()
	if err := mgr.Add(o.ProfileWake); err != nil {
		return fmt.Errorf("caption: subtitleprofile wake: %w", err)
	}
	if err := subtitleprofile.NewReconciler(c, mgr.GetEventRecorder("subtitleprofile")).SetupWithManager(mgr, o.ProfileWake); err != nil {
		return fmt.Errorf("caption: subtitleprofile: %w", err)
	}
	// An English default profile on a cluster that has none, so subtitles
	// work without setup. Create-only; leader-elected.
	if err := mgr.Add(&subtitleprofile.Bootstrap{Client: c, Namespace: o.Namespace}); err != nil {
		return fmt.Errorf("caption: subtitleprofile bootstrap: %w", err)
	}
	provider := subtitleprovider.NewReconciler(c, bus.KV(events.BucketProviderThrottle), mgr.GetEventRecorder("subtitleprovider"))
	provider.Secrets = mgr.GetAPIReader()
	if err := provider.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("caption: subtitleprovider: %w", err)
	}
	return nil
}
