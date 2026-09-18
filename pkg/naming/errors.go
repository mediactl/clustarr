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

package naming

import "errors"

// Sentinel errors returned by this package. Callers should use errors.Is,
// not string comparison, since every error returned by a naming.Engine
// method wraps one of these with additional context via fmt.Errorf's %w.
var (
	// ErrUnknownToken is returned by Render when a template references a
	// token this package does not recognise.
	ErrUnknownToken = errors.New("naming: unknown token")

	// ErrNoFolder is returned by BuildFolder for a MediaKind that has no
	// folder of its own (for example episode and issue, which live inside
	// their parent's folder, or an unrecognised kind).
	ErrNoFolder = errors.New("naming: media kind has no folder of its own")

	// ErrNoFile is returned by BuildFile for a MediaKind that is a
	// container with no single-file path of its own (series, artist), or
	// an unrecognised kind.
	ErrNoFile = errors.New("naming: media kind has no single-file path")

	// ErrNotUnderRoot is reserved for callers computing a path relative to
	// a root folder (for example pkg/fsops's recycle-bin destination
	// calculation) who want a single sentinel to test against with
	// errors.Is when an item path does not live under the expected root.
	ErrNotUnderRoot = errors.New("naming: path is not under the root folder")
)
