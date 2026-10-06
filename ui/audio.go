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

package ui

import (
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// graftLabels say what each status.audio.graft state is doing.
var graftLabels = map[string]string{
	"searching": "looking for a donor",
	"grabbed":   "donor downloading",
	"pending":   "graft queued",
	"aligned":   "grafting",
	"failed":    "graft failed",
}

// audioNote is an item's status.audio as one short note for its page and
// its episode row: the dubs it lacks and what the graft is doing about
// them, or that a dub was grafted in; nothing when there is nothing to say.
// A failure's reason is the note's tooltip.
func audioNote(a *catalogv1.AudioState) views.AudioNote {
	if a == nil {
		return views.AudioNote{}
	}
	if len(a.Missing) == 0 {
		if a.Graft == "done" {
			// status.audio does not say which wanted language is the
			// original, so the note does not guess which is the dub.
			return views.AudioNote{Text: "dub grafted"}
		}
		return views.AudioNote{}
	}
	langs := strings.Join(a.Missing, ", ")
	label, grafts := graftLabels[a.Graft]
	if !grafts {
		return views.AudioNote{Text: "no " + langs + " audio", Warn: true}
	}
	n := views.AudioNote{Text: "no " + langs + " dub · " + label, Warn: a.Graft == "failed"}
	if a.Graft == "failed" {
		n.Title = a.Reason
	}
	return n
}
