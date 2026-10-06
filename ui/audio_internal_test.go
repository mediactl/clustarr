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
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestAudioNoteSaysWhatTheGraftIsDoing: an item's page and episode row show
// its status.audio as one short note (anime dual-audio spec §5.3, §9: a
// missing dub is visible and never blocks the video).
func TestAudioNoteSaysWhatTheGraftIsDoing(t *testing.T) {
	missing := func(graft, reason string) *catalogv1.AudioState {
		return &catalogv1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"ja"}, Missing: []string{"en"}, Graft: graft, Reason: reason}
	}
	cases := []struct {
		a          *catalogv1.AudioState
		text, hint string
	}{
		{nil, "", ""},
		{&catalogv1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"en", "ja"}, Graft: "none"}, "", ""},
		{&catalogv1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"en", "ja"}, Graft: "done"}, "dub grafted", ""},
		{missing("searching", ""), "no en dub · looking for a donor", ""},
		{missing("grabbed", ""), "no en dub · donor downloading", ""},
		{missing("pending", ""), "no en dub · graft queued", ""},
		{missing("aligned", ""), "no en dub · grafting", ""},
		{missing("failed", "AlignmentRejected: coverage 12%"), "no en dub · graft failed", "AlignmentRejected: coverage 12%"},
		{missing("", ""), "no en audio", ""},
	}
	for _, c := range cases {
		n := audioNote(c.a)
		assert.Equal(t, c.text, n.Text, "%+v", c.a)
		assert.Equal(t, c.hint, n.Title, "%+v", c.a)
	}
}
