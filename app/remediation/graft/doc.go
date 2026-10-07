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

// Package graft is the remediation loop's graft planner adapter (loop spec
// §4.12, §5.13; ADR-0016): it gathers what app/transcode/graftplan.Plan
// reads -- the file's item and its audio policy, the RootFolder, the
// clustarr-grafts record, the joined transcode's record, the standalone
// graft Job -- and maps its effects onto the loop's: the record request and
// withdrawal (RecordWrite), the graft Job's creation and deletion, and the
// ledger's graft-slot confirmation. The Job runs `transcode --graft-task` on
// the cpu pool's pod template with no ServiceAccount token and answers in
// clustarr-grafts; the manager creates it, create-only, under its own
// identity (§3.14).
//
// It reads the file's Movie or Episode (and the Series), the QualityProfile
// and the RootFolders through the manager's cache, and creates and deletes
// the graft Jobs it watches (S15). The blank line keeps the markers
// package-level.
//
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;series;rootfolders;qualityprofiles,verbs=get;list;watch
package graft
