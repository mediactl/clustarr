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
	"strings"
	"sync"
)

// cnMemoMax bounds the remembered merges. A page's classes are a few
// hundred distinct strings; a class built from data (a width in pixels) may
// be any number of them, so a full memo is emptied and starts over.
const cnMemoMax = 4096

var cnMemoized = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{}}

// cnMemo is cnMerge of flat, remembered by the classes as written: CN is a
// pure function of them, and merging re-parses every class of a string a
// component passes unchanged for each cell of a table or a grid (owner,
// 2026-11-16: one load of the TV Guide spent 230 MB there, and the ui pods
// sat at their memory limit).
func cnMemo(flat []string) string {
	if len(flat) == 0 {
		return ""
	}
	// Inputs are joined by a space before the merge, so the joined text is
	// the whole input.
	key := strings.Join(flat, " ")
	cnMemoized.RLock()
	out, ok := cnMemoized.m[key]
	cnMemoized.RUnlock()
	if ok {
		return out
	}
	out = cnMerge(flat)
	cnMemoized.Lock()
	if len(cnMemoized.m) >= cnMemoMax {
		clear(cnMemoized.m)
	}
	cnMemoized.m[key] = out
	cnMemoized.Unlock()
	return out
}

// cnMemoLen is how many merges are remembered.
func cnMemoLen() int {
	cnMemoized.RLock()
	defer cnMemoized.RUnlock()
	return len(cnMemoized.m)
}
