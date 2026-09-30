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
package rescan

import (
	"context"
	"errors"

	"github.com/mediactl/clustarr/pkg/events"
)

// CodeExcluded is a folder whose id an ImportExclusion names: the scan
// creates no item for it (a delete that kept the files and asked to stay
// out).
const CodeExcluded = "excluded"

// excluded looks provider/id up in the clustarr-import-exclusions bucket,
// the lookup the import-list sync makes. With no bus nothing is excluded.
func (w *Worker) excluded(ctx context.Context, provider, id string) (events.ExclusionEntry, bool, error) {
	if w.Bus == nil || id == "" {
		return events.ExclusionEntry{}, false, nil
	}
	entry, err := w.Bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey(provider, id))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return events.ExclusionEntry{}, false, nil
	case err != nil:
		return events.ExclusionEntry{}, false, err
	}
	decoded, err := events.DecodeExclusionEntry(entry.Value)
	return decoded, true, err
}
