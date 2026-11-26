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

package remediation

import (
	"crypto/sha256"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
)

// applyMemoMax is how many items an ApplyMemo remembers before it forgets
// them all: a deleted item's entry is never claimed again, and forgetting
// costs one apply an item.
const applyMemoMax = 1 << 18

// ApplyMemo is the item path's itempass.Applies: per item uid, the digest
// of the catalog declaration last applied and the resourceVersion that
// apply left. It lives as long as the process; a new manager applies every
// item once and learns them again.
type ApplyMemo struct {
	mu sync.Mutex
	m  map[types.UID]appliedItem
}

type appliedItem struct {
	rv     string
	digest [sha256.Size]byte
}

var _ itempass.Applies = (*ApplyMemo)(nil)

// NewApplyMemo is an empty memory.
func NewApplyMemo() *ApplyMemo {
	return &ApplyMemo{m: map[types.UID]appliedItem{}}
}

// Unchanged reports that uid at rv is what the declaration with this digest
// last left: nothing wrote the item since, so the same declaration changes
// nothing.
func (a *ApplyMemo) Unchanged(uid types.UID, rv string, digest [sha256.Size]byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	last, ok := a.m[uid]
	return ok && last.rv == rv && last.digest == digest
}

// Applied records that applying the declaration left uid at rv.
func (a *ApplyMemo) Applied(uid types.UID, rv string, digest [sha256.Size]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.m[uid]; !ok && len(a.m) >= applyMemoMax {
		clear(a.m)
	}
	a.m[uid] = appliedItem{rv: rv, digest: digest}
}
