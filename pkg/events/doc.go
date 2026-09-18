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

// Package events defines the Clustarr message bus contract: the envelope and
// headers every message carries, the publisher, subscriber, work-queue,
// key/value and request/reply interfaces, the stream and subject vocabulary,
// and the declarative broker topology.
//
// The package is deliberately free of broker-specific types except for
// EnsureTopology, which applies a Topology to a live JetStream connection.
// Two implementations satisfy Bus:
//
//   - natsbus, backed by NATS JetStream, used in production.
//   - membus, an in-process implementation with the same observable
//     semantics, used by unit tests that must not depend on a broker.
//
// contracttest holds the shared conformance suite both implementations pass.
package events
