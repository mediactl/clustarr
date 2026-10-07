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

// Package iptvchannel reconciles IPTVChannel status (Live TV spec
// 2026-10-07 §3.0, §6), its sole writer under k8s.ManagerLiveTVChannel: the
// channel's place in its provider's lineup, read from the tuner's snapshot.
// It writes only a status that differs, so a refresh that changes nothing
// writes nothing across thousands of channels.
//
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvchannels,verbs=get;list;watch
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvchannels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvproviders,verbs=get;list;watch
package iptvchannel
