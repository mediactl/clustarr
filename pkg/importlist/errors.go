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

package importlist

import "errors"

// ErrNotImplemented is the sentinel every deferred provider's
// NotImplementedError wraps, so callers can test for "not implemented yet"
// generically with errors.Is regardless of which provider raised it.
var ErrNotImplemented = errors.New("importlist: provider not implemented")

// NotImplementedError is returned by a deferred provider's Fetch (tmdb,
// custom, arr — see spec §17 "Deferred").
type NotImplementedError struct {
	// Provider names the provider package, e.g. "tmdb", "custom", "arr".
	Provider string

	// TODO names the deferred list item and where it is written up, so the
	// error message points a reader at the follow-up work rather than just
	// saying "no".
	TODO string
}

// Error implements the error interface.
func (e *NotImplementedError) Error() string {
	return "importlist: " + e.Provider + " not implemented yet: " + e.TODO
}

// Unwrap returns ErrNotImplemented, so errors.Is(err, ErrNotImplemented)
// works on any NotImplementedError regardless of provider.
func (e *NotImplementedError) Unwrap() error { return ErrNotImplemented }
