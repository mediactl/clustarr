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

package importplan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestAnOverrideImportTakesAQualityTheProfileDoesNotAllow is the smoke
// test's T2 finding (P57): a person who imports by hand with override was
// still refused a file whose probe-corrected quality the profile does not
// allow, as the release's fault -- and blocklisted. Override is the person
// saying "this file, for this item": the import takes it, frozen at the
// quality it is. An automatic grab of the same file is still the release's
// fault (TestImportplanCarriesEveryRuleOfFileimport's P57).
func TestAnOverrideImportTakesAQualityTheProfileDoesNotAllow(t *testing.T) {
	wd := newWorld(t)
	wd.probes["Heat.1995.1080p.BluRay.x264-GRP.mkv"] = &commonv1.MediaInfo{VideoCodec: "h264", Width: 720, Height: 480}
	root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
	g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
	concluded(t, g.inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry, "Bluray-480p is not allowed")

	g.in.Intent = &lifecycle.ImportIntent{ID: g.in.Entry.ID, Override: true}
	dec := g.decide()
	require.NotNil(t, dec.Dispatch)
	require.Equal(t, schema.ImportSubInspect, dec.Dispatch.Phase)
	g.in.Intent = nil
	g.deliver()
	g.decide()
	task := g.plan()
	require.Len(t, task.Plan.Moves, 1, "the person's override takes the file")
	require.NotNil(t, task.Plan.Moves[0].Frozen.Quality)
	assert.Equal(t, "Bluray-480p", task.Plan.Moves[0].Frozen.Quality.Name, "frozen at the quality it is")

	dec = g.executed()
	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, dec.Summary.Message)
	assert.Equal(t, catalogv1alpha1.ImportPhaseImported, dec.Summary.Phase)
}

// TestAManualImportIsNeverBlocklisted is the other half of the T2 finding:
// an import a person made -- a pick (the entry's manual), or the import
// intent with override or a target -- that imports nothing is that
// person's to settle. It is held for them (needsPerson) at once, never
// inspected again "before the release is blocklisted" and never
// blocklisted (the remediation classes: itemState and needsPerson hold).
// An automatic grab of the same payload is still the release's fault.
func TestAManualImportIsNeverBlocklisted(t *testing.T) {
	notAllowed := func(wd *world) string {
		wd.probes["Heat.1995.1080p.BluRay.x264-GRP.mkv"] = &commonv1.MediaInfo{VideoCodec: "h264", Width: 720, Height: 480}
		return wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
	}
	noMedia := func(wd *world) string {
		return wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.nfo": 4 << 10})
	}
	held := func(t *testing.T, dec lifecycle.ImportDecision) {
		t.Helper()
		concluded(t, dec, commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld)
		assert.Equal(t, catalogv1alpha1.ImportPhaseHeld, dec.Summary.Phase)
		assert.NotEqual(t, lifecycle.ImportReleaseFault, dec.Verdict, "a person's import is never blocklisted")
		assert.NotContains(t, dec.Summary.Message, "before the release is blocklisted")
	}

	for _, tc := range []struct {
		name    string
		payload func(*world) string
		person  func(*grab)
	}{
		{"a pick whose file the profile does not allow", notAllowed, func(g *grab) { g.in.Entry.Manual = true }},
		{"a pick of a payload with no media", noMedia, func(g *grab) { g.in.Entry.Manual = true }},
		{"an override import of a payload with no media", noMedia, func(g *grab) {
			g.in.Intent = &lifecycle.ImportIntent{ID: g.in.Entry.ID, Override: true}
		}},
		{"an import naming its target of a payload with no media", noMedia, func(g *grab) {
			g.in.Intent = &lifecycle.ImportIntent{ID: g.in.Entry.ID, Target: &schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: ns, Name: "heat"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wd := newWorld(t)
			root := tc.payload(wd)

			auto := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
			concluded(t, auto.inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry, "before the release is blocklisted")

			g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
			tc.person(g)
			g.decide()
			g.in.Intent = nil
			g.deliver()
			dec := g.decide()
			held(t, dec)

			// Held, it stays held -- and expires, never blocklisted.
			wd.now = wd.now.Add(lifecycle.ImportHoldRetention)
			dec = g.decide()
			assert.Equal(t, lifecycle.ImportExpired, dec.Verdict)
		})
	}
}
