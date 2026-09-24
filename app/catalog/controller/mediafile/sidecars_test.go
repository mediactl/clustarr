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

package mediafile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

func TestSidecarsFromSubtitleRequest(t *testing.T) {
	items := []subtitlev1alpha1.SubtitleItem{
		{LangKey: "en", State: subtitlev1alpha1.SubtitleItemDownloaded, Path: "Inception (2010).en.srt"},
		{LangKey: "en:forced", State: subtitlev1alpha1.SubtitleItemDownloaded, Path: "Inception (2010).en.forced.srt"},
		{LangKey: "pt-BR:hi", State: subtitlev1alpha1.SubtitleItemUpgradable, Path: "Inception (2010).pt-BR.sdh.srt"},
		{LangKey: "fr", State: subtitlev1alpha1.SubtitleItemSearching}, // no path yet -- excluded
	}
	dir := "/data/media/movies/Inception (2010)"

	got, err := sidecarsFromSubtitleRequest(dir, items)
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, "/data/media/movies/Inception (2010)/Inception (2010).en.srt", got[0].Path)
	assert.Equal(t, "en", got[0].Language)
	assert.False(t, got[0].Forced)
	assert.False(t, got[0].HI)

	assert.True(t, got[1].Forced)

	assert.Equal(t, "pt-BR", got[2].Language)
	assert.True(t, got[2].HI)
}
