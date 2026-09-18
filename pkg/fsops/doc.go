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

// Package fsops is Clustarr's filesystem primitives for moving media into
// and out of the library: hardlink-else-copy import, atomic single-file
// and directory writes, free-space checks, permission and recycle-bin
// handling, a safe-delete guard, and the sample/extra/part classification
// the scanner uses under amendment §A1.5's never-guess rule.
//
// Every function here operates on paths under the single RWX volume spec
// §11 mounts at /data in every media-touching pod; hardlinks and
// rename(2) only work within one filesystem, which every EXDEV fallback
// in this package exists to paper over.
package fsops
