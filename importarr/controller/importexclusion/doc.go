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

// Package importexclusion implements the ImportExclusion controller: it
// projects each exclusion's external IDs into the clustarr-import-exclusions
// KV bucket, so an import-list sync or a search can ask "is this id blocked?"
// with one Get instead of listing every ImportExclusion per candidate
// (amendment §A1.3; §16 M1).
//
// ImportExclusion belongs to importarr, not catalogarr: amendment §A1.3's
// ownership table, k8s.ManagerImportarr's own doc comment and catalogarr's
// setupControllers comment all agree. This controller is the single writer of
// ImportExclusion.status.
//
// # The lookup contract
//
// For each recognised (provider, id) pair the controller puts an
// events.ExclusionEntry at events.ExclusionKey(provider, id). A consumer does:
//
//	entry, err := bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("tmdb", "949"))
//	switch {
//	case errors.Is(err, events.ErrKeyNotFound): // not excluded
//	case err != nil:                            // bus problem
//	default:                                    // excluded; DecodeExclusionEntry gives the reason
//	}
//
// # Why a finalizer
//
// The bucket has no TTL: an exclusion is a standing block, not ephemeral
// progress. Skipping cleanup on delete would leave the block silently in
// force forever after a user removed the resource, which is a correctness
// bug rather than a cosmetic leak. The keys the controller last wrote are
// recorded in the [AnnotationKeys] annotation -- metadata, not status, so
// writing it does not touch the single-writer-restricted subresource -- and
// the finalizer deletes exactly those.
//
// # Registration
//
// Nothing here registers itself. Task C12 wires it into importarr's
// setupControllers with exactly:
//
//	if err := (&importexclusion.Reconciler{
//	        Client: mgr.GetClient(),
//	        Bus:    bus,
//	        Clock:  time.Now,
//	}).SetupWithManager(mgr); err != nil {
//	        return fmt.Errorf("importarr: importexclusion: %w", err)
//	}
package importexclusion
