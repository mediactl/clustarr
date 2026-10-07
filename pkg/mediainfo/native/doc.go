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

// Package native is the in-process probe: what ffprobe printed for every
// field clustarr stores, read through ffgo (spec 2026-10-06 §6). Only
// app/import/agent and app/squash/worker/inprocess import it, so only
// cmd/agent and cmd/transcode link it; it touches no FFmpeg log callback
// (the binaries route it through pkg/ffruntime).
package native
