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

// Package k8s is the plumbing every Clustarr service's run.go shares: the
// single place allowed to write to a status subresource, plus the scheme, the
// manager options behind the common flags, the NATS connection and readiness
// gate, and the condition, finalizer, owner-reference, predicate and naming
// helpers.
//
// # Single-writer rule
//
// Section 3 of the design spec gives every custom resource exactly one
// controller owner that writes status.phase and status.conditions, and lets a
// worker of the same service apply a disjoint, enumerated set of status fields
// under its own field manager. That only works if every status write is a
// server-side apply carrying a deliberate field-manager name, so
// Status().Update and Status().Patch are banned by golangci's forbidigo
// outside this package and all status writes go through [PatchStatus].
package k8s
