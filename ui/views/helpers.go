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

package views

import "github.com/mediactl/clustarr/pkg/pipeline"

// clampPercent keeps a progress bar's width inside [0, 100] even if a caller
// passes Entry's "no percentage" sentinel (-1) or an out-of-range value.
func clampPercent(p int32) int32 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return p
	}
}

// stageBadgeClass returns the Tailwind utility classes for a stage's badge,
// grouped by what the stage means (failed, blocked, complete, or still
// moving) rather than by its exact name, so a stage this switch does not
// know about still renders with the "in progress" look instead of unstyled
// text.
func stageBadgeClass(stage pipeline.Stage) string {
	return "rounded-full px-2 py-0.5 text-xs font-medium " + stageTone(stage)
}

// stageTone is the colour alone, for a badge component that brings its own
// shape: the pipeline row's stage badge keeps these semantics over the
// component's neutral secondary colours.
func stageTone(stage pipeline.Stage) string {
	switch stage {
	case pipeline.StageFailed:
		return "bg-red-500/20 text-red-300"
	case pipeline.StageBlocked:
		return "bg-amber-500/20 text-amber-300"
	case pipeline.StageComplete:
		return "bg-emerald-500/20 text-emerald-300"
	default:
		return "bg-sky-500/20 text-sky-300"
	}
}
