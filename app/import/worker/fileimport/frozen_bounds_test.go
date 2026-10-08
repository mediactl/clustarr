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

package fileimport

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestFrozenReleaseFitsTheMediaFileBounds (F2.2): a grab's release fields
// are unbounded, and the MediaFile spec's are not (loop spec §2.11.2). The
// inspect clamps them where it freezes them (frozenFields, which the plan
// carries into the MediaFile spec) and where it names the file (frozenSpec,
// which the naming context reads), or the apiserver refuses the MediaFile
// and the import never lands. Since A3.8 the manager renders importedFrom
// (importplan's importSource, held by its own rules test); the inspect's
// release title is the naming context's.
func TestFrozenReleaseFitsTheMediaFileBounds(t *testing.T) {
	parse := func(title string) *release.ParsedRelease {
		t.Helper()
		p, err := release.Parse(title, release.Options{Kind: commonv1.MediaKindMovie})
		require.NoError(t, err)
		return p
	}

	// A release whose group and edition run past the spec's bounds: the
	// parse is the real one, the over-long fields an indexer's.
	long := parse("Heat.1995.Directors.Cut.1080p.BluRay.x264-NTb")
	long.Group = strings.Repeat("g", 300)
	long.Edition = strings.Repeat("é", 200) // 400 bytes of two-byte runes
	f := frozenFields(videoFacts{parsed: long}, quality.Profile{})
	assert.LessOrEqual(t, len(f.ReleaseGroup), catalogv1alpha1.MaxReleaseGroupLength)
	require.NotNil(t, f.Edition)
	assert.LessOrEqual(t, len(*f.Edition), catalogv1alpha1.MaxEditionLength)
	assert.True(t, utf8.ValidString(*f.Edition), "the edition is cut on a rune boundary")

	title := "\u0096" + strings.Repeat("t", 600)
	spec := frozenSpec(f, title)
	require.NotNil(t, spec.ImportedFrom)
	assert.LessOrEqual(t, len(spec.ImportedFrom.ReleaseTitle), catalogv1alpha1.MaxReleaseTitleLength)
	assert.False(t, k8s.HasUnapplyable(spec.ImportedFrom.ReleaseTitle), "a rune server-side apply cannot carry is replaced")
	assert.Equal(t, f.ReleaseGroup, spec.ReleaseGroup, "the naming context reads the clamped group")
	assert.Equal(t, *f.Edition, spec.Edition, "and the clamped edition")

	// A release inside the bounds freezes verbatim.
	plain := parse("Heat.1995.Directors.Cut.1080p.BluRay.x264-NTb")
	pf := frozenFields(videoFacts{parsed: plain}, quality.Profile{})
	assert.Equal(t, "NTb", pf.ReleaseGroup)
	require.NotNil(t, pf.Edition)
	assert.Contains(t, *pf.Edition, "Cut")
	ps := frozenSpec(pf, "Heat.1995.Directors.Cut.1080p.BluRay.x264-NTb")
	assert.Equal(t, "Heat.1995.Directors.Cut.1080p.BluRay.x264-NTb", ps.ImportedFrom.ReleaseTitle)

	assert.Len(t, importText(strings.Repeat("n", 300), catalogv1alpha1.MaxIndexerNameLength), catalogv1alpha1.MaxIndexerNameLength)
	assert.Equal(t, "nzbgeek", importText("nzbgeek", catalogv1alpha1.MaxIndexerNameLength))
}
