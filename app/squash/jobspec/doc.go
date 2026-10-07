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

// Package jobspec is what the squasharr reconcilers and the transcode
// worker must agree on about a job without either linking the other:
// the task a TranscodeJob dispatches (BuildTask), the output and data
// paths, the profile's hash, hardware and policy accessors, the
// standard's inputs and tier, the CPU-limit environment variable, and the
// worker's process exit codes. It links no ffgo and runs nothing (spec
// §4.3 S1); the AudioGraft controller takes its root-folder
// helpers (RootFolderFor, RecycleBinOf) from here too; the graft run maps
// its paths with app/squash/grafttask's own LocalPath and Within.
package jobspec
