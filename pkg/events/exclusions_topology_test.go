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

package events_test

import (
	"testing"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestImportExclusionsSurviveASingleNodeRestart: an ImportExclusion is the
// owner's decision, consulted by the list sync and the search paths, and
// the bucket's own description has always said "durable". On talos
// (2026-10-16, single-node NATS) it was nevertheless on memory storage,
// because ForSingleNode maps every bucket not marked Durable to memory, so
// every NATS restart forgot the exclusions until their controller re-synced
// them. Durable keeps it on file storage on a single node, like the
// metadata-extended and plex-extras buckets.
func TestImportExclusionsSurviveASingleNodeRestart(t *testing.T) {
	for _, b := range events.Default().ForSingleNode().Buckets {
		if b.Name != events.BucketImportExclusions {
			continue
		}
		if !b.Durable {
			t.Errorf("%s: Durable = false, want true", b.Name)
		}
		if b.Storage != events.StorageFile {
			t.Errorf("%s: single-node Storage = %q, want %q", b.Name, b.Storage, events.StorageFile)
		}
		return
	}
	t.Fatalf("bucket %s is not in the default topology", events.BucketImportExclusions)
}
