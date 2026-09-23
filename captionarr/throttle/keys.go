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

package throttle

import "github.com/mediactl/clustarr/pkg/events"

// ProviderKey is the KV key holding one SubtitleProvider's [State]: throttle
// window, error count, quota and cached auth. providerUID is the
// SubtitleProvider object's UID, matching spec §5's documented
// "<provider-uid>" key shape.
//
// events.KVKeyToken is not optional -- see the package doc comment and
// CLAUDE.md's "A NATS KV key must match ... and nothing in the Go types
// enforces it" gotcha. providerUID reaches this package from a Kubernetes
// object UID, which is always a well-formed UUID in practice, but nothing in
// the type system guarantees that, and the escape is free.
func ProviderKey(providerUID string) string { return events.KVKeyToken(providerUID) }

// TokenBucketKey is the KV key holding one provider's shared rate-limit
// token bucket (bucketState). It is [ProviderKey] plus a literal ".bucket"
// suffix, which can never collide with another provider's [ProviderKey]:
// KVKeyToken's output alphabet is [0-9A-Za-z-] and never contains ".", so no
// escaped UID can forge this suffix and no bare ProviderKey can equal one
// that carries it.
func TokenBucketKey(providerUID string) string { return events.KVKeyToken(providerUID) + ".bucket" }
