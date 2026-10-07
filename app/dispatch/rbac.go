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

package dispatch

// The Ledger records one Warning DispatchUnattended Event per durable per
// transition on the manager's own Pod (ADR-0019 §5.5, §7.7), through the
// "clustarr-dispatch" events.k8s.io recorder; pods get is the narrowest verb
// for the core/v1 Pod it names (cmd/clustarr's syntactic built-in-kind
// guard), and nothing here reads one.
//
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get
