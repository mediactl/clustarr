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

package importlist

import "time"

// Provider refresh-interval floors, from ImportListSpec.RefreshInterval's
// own doc comment ("clamped up to the provider minimum") and the base
// design spec's ImportList table, which names the same six: "trakt/tmdb/
// mdblist 12h, plex 6h, arr 15m, stevenlu 24h, custom 6h". Neither source
// states a floor for imdbCSV, so it is grouped with plex and custom's 6h:
// like them, it is a poll of a resource the user is expected to update by
// hand (a ConfigMap edit, rather than an upstream API refreshing its own
// data), so there is no provider-side rate limit forcing a longer floor,
// and 6h is a reasonable default poll cadence for a resource nothing
// notifies this controller about changing.
const (
	minRefreshTrakt    = 12 * time.Hour
	minRefreshTmdb     = 12 * time.Hour
	minRefreshMdblist  = 12 * time.Hour
	minRefreshPlex     = 6 * time.Hour
	minRefreshCustom   = 6 * time.Hour
	minRefreshImdbCSV  = 6 * time.Hour
	minRefreshArr      = 15 * time.Minute
	minRefreshStevenLu = 24 * time.Hour

	// defaultMinRefresh applies if somehow no provider field is set (the
	// CRD's CEL rule forbids this state from ever being admitted, so this
	// is a defensive floor, not a reachable path).
	defaultMinRefresh = 6 * time.Hour

	// maxRequeue caps how far ahead the controller will sleep in one
	// reconcile, mirroring rootfolderschedule's own cap: a sleep longer
	// than this is not something a human troubleshooting a stuck sync can
	// reason about, and periodically re-checking Enabled and the Trakt auth
	// state is cheap.
	maxRequeue = 12 * time.Hour

	// idleRecheck is how often a disabled ImportList is looked at again, so
	// re-enabling it is noticed without a spec edit forcing the issue.
	idleRecheck = time.Hour
)

// providerMinRefresh returns spec's provider-specific floor for
// spec.refreshInterval. Exactly one provider field is set (the CRD's CEL
// rule and pkg/importlist.Config.Validate both enforce this upstream); the
// switch checks every field defensively rather than assuming that always
// holds, falling back to defaultMinRefresh if somehow none do.
func providerMinRefresh(spec catalogImportListSpec) time.Duration {
	switch {
	case spec.Trakt:
		return minRefreshTrakt
	case spec.Tmdb:
		return minRefreshTmdb
	case spec.Mdblist:
		return minRefreshMdblist
	case spec.Plex:
		return minRefreshPlex
	case spec.Custom:
		return minRefreshCustom
	case spec.ImdbCSV:
		return minRefreshImdbCSV
	case spec.Arr:
		return minRefreshArr
	case spec.StevenLu:
		return minRefreshStevenLu
	default:
		return defaultMinRefresh
	}
}

// catalogImportListSpec is the subset of ImportListSpec providerMinRefresh
// needs: which provider field is set, and the requested interval. It exists
// so this file -- and its test -- has no dependency on api/catalog/v1alpha1
// or its applyconfigurations, making the clamp and due-time arithmetic
// testable as plain Go.
type catalogImportListSpec struct {
	Trakt, Plex, Tmdb, Mdblist, StevenLu, ImdbCSV, Custom, Arr bool
	Requested                                                  time.Duration
}

// effectiveRefreshInterval clamps spec.Requested up to its provider's
// floor. A zero Requested (the field is optional, unset) means "use the
// floor", not "sync continuously" -- there is no such thing as an
// unthrottled import-list poll.
func effectiveRefreshInterval(spec catalogImportListSpec) time.Duration {
	floor := providerMinRefresh(spec)
	if spec.Requested < floor {
		return floor
	}
	return spec.Requested
}

// requeueFor clamps a sleep to something a human can reason about and keeps
// it strictly positive, mirroring rootfolderschedule's own helper.
func requeueFor(d time.Duration) time.Duration {
	switch {
	case d > maxRequeue:
		return maxRequeue
	case d < time.Second:
		return time.Second
	default:
		return d
	}
}
