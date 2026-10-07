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

// Package binpath names where each clustarr binary lives in its image (spec
// §3.1, §10.1.1). The clustarr image holds manager and ui; the native image
// holds agent, markers and transcode. No image sets an ENTRYPOINT, because one
// image holds several binaries, so every manifest and every pod the manager
// renders names one of these as its command.
package binpath

const (
	// Manager is cmd/manager, in the clustarr image.
	Manager = "/usr/bin/manager"
	// UI is cmd/ui, in the clustarr image.
	UI = "/usr/bin/ui"
	// Agent is cmd/agent, in the native image.
	Agent = "/usr/bin/agent"
	// Markers is cmd/markers, in the native image.
	Markers = "/usr/bin/markers"
	// Transcode is cmd/transcode, in the native image.
	Transcode = "/usr/bin/transcode"
)
