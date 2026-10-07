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

// Package ffruntime is the one runtime gate every binary that links ffgo
// passes through (spec 2026-10-06 §7.4). Load maps FFmpeg 9 and the ffgo
// shim once and checks the shim's API; Require checks the demuxers, muxers,
// decoders, encoders and filters a caller needs; one process-wide log
// trampoline routes FFmpeg's log to slog (RouteLog), to a run's own sink
// (Capture) or nowhere (Mute); and Do bounds every in-process FFmpeg call by
// its context plus Grace, counting calls it had to abandon until they
// return, and reports the process wedged at MaxAbandoned outstanding calls.
//
// ffgo dlopens FFmpeg at package init (avutil/avutil.go's init), not at
// Load, so any binary linking this package has mapped FFmpeg before main.
// It imports the standard library and ffgo only (TestFFRuntimeImportsOnlyFFgo).
package ffruntime
