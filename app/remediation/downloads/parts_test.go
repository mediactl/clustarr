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
package downloads

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The first part's import is given the set's other parts in part order,
// each with its transfer record, and nothing of another grab.
func TestTheFirstPartIsGivenItsSetsOtherPartsInOrder(t *testing.T) {
	part := func(uid string, n int32) catalogv1alpha1.DownloadEntry {
		return catalogv1alpha1.DownloadEntry{ID: "e-" + uid, UID: uid, Set: &catalogv1alpha1.EntrySet{ID: "parts:x", Part: n, Of: 3}}
	}
	entries := []catalogv1alpha1.DownloadEntry{part("c", 3), {ID: "other", UID: "o"}, part("a", 1), part("b", 2)}
	recs := map[string]*schema.TransferRecord{"b": {ContentRoot: "/b"}, "c": {ContentRoot: "/c"}}
	got := setParts(entries, &entries[2], func(e *catalogv1alpha1.DownloadEntry) *schema.TransferRecord { return recs[e.UID] })
	if assert.Len(t, got, 2) {
		assert.Equal(t, "b", got[0].Entry.UID)
		assert.Equal(t, "/b", got[0].Transfer.ContentRoot)
		assert.Equal(t, "c", got[1].Entry.UID)
	}
}
