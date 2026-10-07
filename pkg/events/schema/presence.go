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

package schema

import "time"

// AgentPresence is one agent pod's liveness report in clustarr-progress at
// events.PresenceKey(domain, pod), written at start and every 30 s and
// retired by the bucket's TTL (ADR-0019 §5.3). It is telemetry, not a
// records bucket: the manager's admission reads it through a 5 s cache.
type AgentPresence struct {
	Domain  string `json:"domain"`
	Pod     string `json:"pod"`
	Node    string `json:"node,omitempty"`
	Version string `json:"version,omitempty"`
	// Slots is the handler slots per bound durable.
	Slots map[string]int `json:"slots,omitempty"`
	// Durables are the durables the pod binds.
	Durables []string `json:"durables,omitempty"`
	// Capabilities are what the pod proved it can do (ffgo and its FFmpeg
	// version, configured providers, /data mounted).
	Capabilities map[string]string `json:"capabilities,omitempty"`
	At           time.Time         `json:"at"`
}

// Schema implements Payload.
func (AgentPresence) Schema() string { return "agent.Presence.v1" }
