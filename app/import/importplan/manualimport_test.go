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

// A manual import does not take an H.264 10-bit, an HEVC 12-bit or an XviD
// file (owner, 2026-11-27, asked whether an override should still take an
// H.264 10-bit release: "Never - nor HEVC 12-bit", then "close XviD"): Plex
// cannot transcode it, so the file errors on the server where a missing one
// would not. Override is exempt from every other content gate, the
// profile's allowed qualities included
// (TestAnOverrideImportTakesAQualityTheProfileDoesNotAllow).
func TestNoManualImportTakesWhatPlexCannotPlay(t *testing.T) {
	const file = "Heat.1995.1080p.BluRay.x264-GRP.mkv"
	for name, mi := range map[string]*commonv1.MediaInfo{
		"H.264 10-bit":  {VideoCodec: "h264", VideoBitDepth: 10, Width: 1920, Height: 1080},
		"HEVC 12-bit":   {VideoCodec: "hevc", VideoBitDepth: 12, Width: 1920, Height: 1080},
		"MPEG-4 Part 2": {VideoCodec: "mpeg4", VideoBitDepth: 8, Width: 1920, Height: 1080},
	} {
		t.Run(name, func(t *testing.T) {
			wd := newWorld(t)
			wd.probes[file] = mi
			g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", wd.payload(map[string]int64{file: big}))
			g.in.Intent = &lifecycle.ImportIntent{ID: g.in.Entry.ID, Override: true}
			g.decide()
			g.in.Intent = nil
			g.deliver()
			dec := g.decide()
			assert.Nil(t, dec.Dispatch, "nothing is placed")
			assert.Equal(t, catalogv1alpha1.ImportPhaseHeld, dec.Summary.Phase, dec.Summary.Message)
			assert.Contains(t, dec.Summary.Message, name)
			assert.Contains(t, dec.Summary.Message, "by a manual import either")
		})
	}
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

// A person's pick of a release at a quality the profile does not allow is
// imported at that quality: they overrode the grab's refusal of it, and the
// import does not ask again. On talos (2026-11-26) each Blu-ray remux picked
// for Panty & Stocking with Garterbelt's first season was downloaded and
// then held "quality Remux-1080p is not allowed by the quality profile". An
// automatic grab of the same file is the release's fault as before, and so
// is a pick whose file is another quality than its release advertised
// (TestAManualImportIsNeverBlocklisted).
func TestAPickIsImportedAtTheQualityItWasPickedAt(t *testing.T) {
	const title = "Heat.1995.1080p.BluRay.REMUX.AVC.FLAC.2.0-GRP"
	wd := newWorld(t)
	root := wd.payload(map[string]int64{title + ".mkv": big})

	auto := wd.grab(commonv1.MediaKindMovie, "heat", title, root)
	concluded(t, auto.inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry, "Remux-1080p is not allowed")

	g := wd.grab(commonv1.MediaKindMovie, "heat", title, root)
	g.in.Entry.Manual, g.in.Entry.GrabbedBy = true, commonv1.GrabSourceInteractive
	// What the grab records of the release it was made from.
	g.in.Entry.Release.Quality = commonv1.Quality{Name: "Remux-1080p", Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierRemux}
	g.decide()
	g.deliver()
	g.decide()
	task := g.plan()
	require.Len(t, task.Plan.Moves, 1, "the pick's file is taken")
	require.NotNil(t, task.Plan.Moves[0].Frozen.Quality)
	assert.Equal(t, "Remux-1080p", task.Plan.Moves[0].Frozen.Quality.Name)

	dec := g.executed()
	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, dec.Summary.Message)
}

// A download whose names say no quality is judged by what its file is
// (owner, 2026-11-27, of releases taken as a last resort though their
// titles name no quality: "we should find out the quality after download to
// determine if we'll keep it"): the probe's frame size makes it HDTV at
// that resolution, which the profile takes or refuses as it would any
// release named so. Until then every such file was "quality Unknown is not
// allowed by the quality profile", whatever it held.
func TestAFileNamingNoQualityIsJudgedByItsProbe(t *testing.T) {
	const file = "Heat (1995).mkv"
	inspected := func(t *testing.T, mi *commonv1.MediaInfo) *grab {
		t.Helper()
		wd := newWorld(t)
		wd.probes[file] = mi
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat 1995", wd.payload(map[string]int64{file: big}))
		return g
	}
	t.Run("a 720p file is HDTV-720p, which this profile takes", func(t *testing.T) {
		g := inspected(t, &commonv1.MediaInfo{VideoCodec: "hevc", Width: 1280, Height: 720})
		g.inspected()
		task := g.plan()
		require.Len(t, task.Plan.Moves, 1)
		require.NotNil(t, task.Plan.Moves[0].Frozen.Quality)
		assert.Equal(t, "HDTV-720p", task.Plan.Moves[0].Frozen.Quality.Name)
	})
	t.Run("a 1080p file is HDTV-1080p, which it does not", func(t *testing.T) {
		g := inspected(t, &commonv1.MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080})
		concluded(t, g.inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry, "HDTV-1080p is not allowed")
	})
}
