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

// Package lineup is the lineup controller (Live TV custom guides design
// 2026-11-10 §5.2): it turns an IPTVProvider's spec.lineup and the
// playlist entries its filters admit into each channel's number and
// category. It plans through pkg/iptv/lineup, stores the plan as the
// provider's lineup-plan object (which the provider controller reads for
// its blocks and conditions), and -- only while spec.lineup.enabled --
// creates, numbers, activates, deactivates and, after seven days absent,
// deletes the provider's IPTVChannels.
//
// It writes under k8s.ManagerLiveTVLineup alone, and only spec.providerRef,
// spec.key, spec.number, spec.active, the clustarr.io/origin label of the
// channels it created, and status.lineup: a person's spec.name, group,
// logo, epg, language and category are never in its applies, so they stay
// theirs (co-ownership by field). A dry run (enabled false) writes the plan
// object and status.lineup, never a spec.
//
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvchannels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvchannels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package lineup
