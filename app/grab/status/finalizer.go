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

package status

// EngineFinalizer is the finalizer every engine adds to a Download it owns.
// It is distinct from the Download controller's own finalizer
// (k8s.FinalizerFor(Download) = "download.clustarr.io/download") because the
// two guard different things: this one the transfer, that one the data. See
// app/grab/engine's package doc for the ordering between them.
//
// It lives here, beside the field-manager sets both sides already share, so
// the Download controller can wait on it without linking the engine runtime
// (spec §4.3 G1).
const EngineFinalizer = "download.clustarr.io/engine"
