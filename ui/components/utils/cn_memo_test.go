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

package utils

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A page of thousands of like cells calls CN with the same classes for each
// (the TV Guide's 9,000 programme buttons, talos 2026-11-16: 309 MB of a
// profile's allocations), so a repeated call must not merge again. If this
// fails after `shadcn-templ add --overwrite` rewrote the file, put the memo
// back in CN.
func TestCNRemembersARepeatedMerge(t *testing.T) {
	const base = "inline-flex shrink-0 items-center rounded-md border px-4 py-2 text-sm hover:bg-muted"
	want := cnMerge([]string{base, "absolute px-2 py-1"})
	assert.Equal(t, want, CN(base, "absolute px-2 py-1"))
	allocs := testing.AllocsPerRun(200, func() { _ = CN(base, "absolute px-2 py-1") })
	assert.LessOrEqual(t, allocs, 3.0, "a repeated call is a lookup")
}

func TestCNMemoKeepsDifferentInputsApart(t *testing.T) {
	assert.Equal(t, "p-4", CN("p-2", "p-4"))
	assert.Equal(t, "p-2", CN("p-4", "p-2"))
	// One string and the same text split in two are the same classes, but a
	// separator inside a class must never join two inputs into another key.
	assert.Equal(t, CN("p-2 m-1"), CN("p-2", "m-1"))
	assert.Equal(t, "", CN())
	assert.Equal(t, "p-2", CN(map[string]bool{"p-2": true, "p-4": false}))
}

func TestCNMemoIsBounded(t *testing.T) {
	for i := range cnMemoMax + 500 {
		_ = CN("w-[" + strconv.Itoa(i) + "px]")
	}
	assert.LessOrEqual(t, cnMemoLen(), cnMemoMax)
	assert.Equal(t, "w-[7px]", CN("w-[7px]"))
}
