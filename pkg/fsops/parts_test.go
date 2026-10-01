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

package fsops_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/fsops"
)

// ParseTranscodePart reads back exactly the per-attempt form IsPart
// recognizes, and nothing else: the anacrolix .part suffix, the generic
// <stem>.part.<ext> and a release name shaped like a part are not a
// transcode attempt's file, so neither sweep ever reads one as such.
func TestParseTranscodePartReadsOnlyThePerAttemptForm(t *testing.T) {
	for name, want := range map[string]*fsops.TranscodePart{
		"/lib/Heat (1995).part-a1b2c3d4-1.mkv":   {Stem: "/lib/Heat (1995)", JobUID8: "a1b2c3d4", Attempt: 1, Ext: ".mkv"},
		"/lib/Heat (1995).part-0123abcd-12.mp4":  {Stem: "/lib/Heat (1995)", JobUID8: "0123abcd", Attempt: 12, Ext: ".mp4"},
		"/lib/Heat.part.2.part-a1b2c3d4-3.mkv":   {Stem: "/lib/Heat.part.2", JobUID8: "a1b2c3d4", Attempt: 3, Ext: ".mkv"},
		"/lib/Heat (1995).mkv":                   nil,
		"/lib/Heat (1995).mkv.part":              nil,
		"/lib/Heat (1995).part.mkv":              nil,
		"/lib/Movie.part-two.mkv":                nil,
		"/lib/Film.part-1.mkv":                   nil,
		"/lib/Heat (1995).part-A1B2C3D4-1.mkv":   nil,
		"/lib/Heat (1995).part-a1b2c3d4-.mkv":    nil,
		"/lib/Heat (1995).part-a1b2c3d4-1":       nil,
		"/lib/Heat (1995).part-a1b2c3d4-1.mkv.x": nil,
	} {
		got, ok := fsops.ParseTranscodePart(name)
		if want == nil {
			assert.Falsef(t, ok, "%s", name)
			continue
		}
		if assert.Truef(t, ok, "%s", name) {
			assert.Equalf(t, *want, got, "%s", name)
			assert.Truef(t, fsops.IsPart(name), "%s: every transcode part is a part", name)
		}
	}
}
