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

// Package subtitles implements subtitle-provider search, Bazarr-equivalent
// scoring, and post-processing (charset, format conversion, hearing-impaired
// stripping). It has no Kubernetes dependency: Query/Candidate are plain Go
// mirrors of the corresponding api/subtitle/v1alpha1 shapes (never imported —
// see the task's Scope note) except for Query.Release, which is the real
// *release.ParsedRelease (pkg/release, wave 1). The missing-subtitle planner
// and sidecar-path naming are out of scope here; they belong to the
// captionarr controller and pkg/naming respectively, and are deferred to
// Phase F.
package subtitles
