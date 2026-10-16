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
		"/lib/Heat (1995).part-a1b2c3d4-1.mkv":  {Stem: "/lib/Heat (1995)", FileUID8: "a1b2c3d4", Seq: 1, Ext: ".mkv"},
		"/lib/Heat (1995).part-0123abcd-12.mp4": {Stem: "/lib/Heat (1995)", FileUID8: "0123abcd", Seq: 12, Ext: ".mp4"},
		"/lib/Heat.part.2.part-a1b2c3d4-3.mkv":  {Stem: "/lib/Heat.part.2", FileUID8: "a1b2c3d4", Seq: 3, Ext: ".mkv"},
		// A dispatch sequence is an int64 (F6.4), so its digit run is long.
		"/lib/Heat (1995).part-a1b2c3d4-1759881600123.mkv": {Stem: "/lib/Heat (1995)", FileUID8: "a1b2c3d4", Seq: 1759881600123, Ext: ".mkv"},
		"/lib/Heat (1995).mkv":                             nil,
		"/lib/Heat (1995).mkv.part":                        nil,
		"/lib/Heat (1995).part.mkv":                        nil,
		"/lib/Movie.part-two.mkv":                          nil,
		"/lib/Film.part-1.mkv":                             nil,
		"/lib/Heat (1995).part-A1B2C3D4-1.mkv":             nil,
		"/lib/Heat (1995).part-a1b2c3d4-.mkv":              nil,
		"/lib/Heat (1995).part-a1b2c3d4-1":                 nil,
		"/lib/Heat (1995).part-a1b2c3d4-1.mkv.x":           nil,
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

// A part's copy still in flight -- its name with ".partial" after it, which a
// pod killed mid copy-back leaves beside the output -- is that part's
// leftover too (Beetlejuice Beetlejuice, 2026-10-16: two, 8.7 GB, that
// neither sweep recognised). Anything else under ".partial" is not.
func TestAPartsCopyInFlightReadsAsThePart(t *testing.T) {
	want := fsops.TranscodePart{Stem: "/lib/Heat (1995)", FileUID8: "dcf11b8a", Seq: 1792098225000, Ext: ".mp4"}
	for _, name := range []string{
		"/lib/Heat (1995).part-dcf11b8a-1792098225000.mp4",
		"/lib/Heat (1995).part-dcf11b8a-1792098225000.mp4.partial",
		// MoveAtomic copied through a second .partial before 2026-10-16.
		"/lib/Heat (1995).part-dcf11b8a-1792098225000.mp4.partial.partial",
	} {
		got, ok := fsops.ParseTranscodePartCopy(name)
		if assert.Truef(t, ok, "%s", name) {
			assert.Equalf(t, want, got, "%s", name)
		}
		assert.Truef(t, fsops.IsPart(name), "%s is a part to the scan", name)
	}
	for _, name := range []string{
		"/lib/Heat (1995).mp4.partial",
		"/lib/Heat (1995).en.srt.partial",
		"/lib/Movie.part-two.mkv.partial",
		"/lib/Heat (1995).partial",
	} {
		_, ok := fsops.ParseTranscodePartCopy(name)
		assert.Falsef(t, ok, "%s", name)
		assert.Falsef(t, fsops.IsPart(name), "%s", name)
	}
	_, ok := fsops.ParseTranscodePart("/lib/Heat (1995).part-dcf11b8a-1.mp4.partial")
	assert.False(t, ok, "ParseTranscodePart itself still reads only the exact form")
}
