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

package metadata

import (
	"context"
	"time"
)

// Cache is deliberately generic (Get/Set with a per-call TTL) so the L1
// LRUCache in this package and a future shared L2 (Redis or NATS JetStream
// KV, built by the gateway, ADR-0007) are interchangeable to every caller.
type Cache interface {
	Get(ctx context.Context, key string, out any) (bool, error)
	Set(ctx context.Context, key string, v any, ttl time.Duration) error
}
