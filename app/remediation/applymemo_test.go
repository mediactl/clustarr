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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

// An item is known unchanged only at the resourceVersion the same
// declaration last left it at: another declaration, or a write by anyone
// since (a newer resourceVersion), and the apply is sent.
func TestApplyMemoKnowsAnItemOnlyAsItsOwnApplyLeftIt(t *testing.T) {
	a := NewApplyMemo()
	d1, d2 := sha256.Sum256([]byte("one")), sha256.Sum256([]byte("two"))
	assert.False(t, a.Unchanged("u1", "10", d1), "nothing applied yet")

	a.Applied("u1", "10", d1)
	cases := []struct {
		name   string
		uid    types.UID
		rv     string
		digest [sha256.Size]byte
		want   bool
	}{
		{"the same declaration, the item as it left it", "u1", "10", d1, true},
		{"another declaration", "u1", "10", d2, false},
		{"something wrote the item since", "u1", "11", d1, false},
		{"another item", "u2", "10", d1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, a.Unchanged(c.uid, c.rv, c.digest))
		})
	}

	a.Applied("u1", "12", d2)
	assert.False(t, a.Unchanged("u1", "10", d1), "the newer apply replaces the older")
	assert.True(t, a.Unchanged("u1", "12", d2))
}

// The memory is bounded: past applyMemoMax items it forgets them all, which
// costs each one apply.
func TestApplyMemoForgetsEverythingWhenFull(t *testing.T) {
	a := NewApplyMemo()
	d := sha256.Sum256([]byte("d"))
	for i := range applyMemoMax {
		a.Applied(types.UID(fmt.Sprintf("u%d", i)), "1", d)
	}
	assert.True(t, a.Unchanged("u0", "1", d))
	a.Applied("u0", "2", d)
	assert.True(t, a.Unchanged("u1", "1", d), "an item it already knows does not empty it")
	a.Applied("one-more", "1", d)
	assert.False(t, a.Unchanged("u1", "1", d), "full: everything before is forgotten")
	assert.True(t, a.Unchanged("one-more", "1", d))
}
