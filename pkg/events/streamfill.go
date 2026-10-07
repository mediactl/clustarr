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

package events

import "context"

// StreamFill is a stream's size against its limit (STREAM.INFO). MaxBytes is
// zero for a stream without a byte limit.
type StreamFill struct{ Bytes, MaxBytes, Messages uint64 }

// StreamStater is implemented by both buses. It is optional, so fakes of
// events.StreamAdmin need not grow a method. QueueGauge exports
// Bytes/MaxBytes as clustarr_stream_fill_ratio: lag cannot show a single-node
// memory stream discarding its oldest messages, as 12,161 were on 2026-10-01
// (split §9.2 and §9.4 as amended 2026-10-07, S10).
type StreamStater interface {
	// StreamFill reads stream's size. A missing stream is ErrStreamNotFound.
	StreamFill(ctx context.Context, stream string) (StreamFill, error)
}
