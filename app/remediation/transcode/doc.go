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

// Package transcode is the loop's transcode planner (loop spec §5.8-§5.10):
// it gathers what app/transcode/transcodeplan.Plan reads from the manager
// cache, the admission ledger and clustarr-transcodes, renders
// status.transcode, turns the decision into effects -- the requested
// record, then the v2 task, then the ledger confirmation and the history --
// and owns the transcode half of status.handledNonces (§3.5). The manager
// imports it as the PlannerTranscode planner.
package transcode
