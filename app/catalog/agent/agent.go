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

// Package agent is the leaf every agent domain's registration shares (spec
// §4.2.1): the Registration a domain's Register returns, and the one
// registrar and startup assertion for the field indexes a domain declares.
// It imports no worker package, so importing it links no domain.
package agent

import "github.com/mediactl/clustarr/pkg/k8s"

// Registration is what an agent domain's Register hands back to the process
// that runs it (spec §3.5.2 step 9).
type Registration struct {
	// Ready and Live are the domain's own checks, named <domain>.<check>
	// (spec §3.3). Nil is none.
	Ready, Live *k8s.Checks
	// Indexes are the cache field indexes the domain's consumers read. The
	// domain declares them and never registers them: the process registers
	// each once (internal/cli/agent) and proves it reached the cache
	// (AssertIndexes).
	Indexes []k8s.FieldIndex
	// Close releases what the domain holds open -- the release index, an
	// engine's download client -- once the manager has stopped. Nil is
	// nothing to close.
	Close func() error
}
