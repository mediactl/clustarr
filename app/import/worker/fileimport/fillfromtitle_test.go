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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// A file that names no quality takes the one its download's release title
// names, even when no release pattern reads that title. The release and the
// file are the ones talos held on 2026-11-27, "quality Unknown is not
// allowed by the quality profile", after a person picked the batch: its
// title names no episode, so it parses as no release, and its tags were
// not read at all.
func TestAFileTakesTheQualityOfAReleaseTitleNoPatternReads(t *testing.T) {
	const title = "[GERMini] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]"
	_, err := release.Parse(title, release.Options{Kind: commonv1.MediaKindEpisode})
	require.Error(t, err, "the case this is for: the title is no release to the parser")

	path := "/data/torrents/tv/rozen-maiden-0000000000/Rozen Maiden - Zurückspulen [1080p;H265] (2013)/Rozen Maiden - Zurückspulen - 01.mkv"
	p, err := parseMediaFile(path, "", commonv1.MediaKindEpisode)
	require.NoError(t, err)
	require.Equal(t, commonv1.SourceUnknown, p.Quality.Source, "the file's own name names no source")

	fillFromRelease(p, title, commonv1.MediaKindEpisode)
	assert.Equal(t, "Bluray-1080p", p.Quality.Name)
	assert.Equal(t, commonv1.SourceBluray, p.Quality.Source)
	assert.EqualValues(t, 1080, p.Quality.Resolution)
	assert.Equal(t, "GERMini", p.Group)

	// A title that names no quality either gives none, and a file that
	// names its own keeps it.
	bare, err := parseMediaFile(path, "", commonv1.MediaKindEpisode)
	require.NoError(t, err)
	fillFromRelease(bare, "Rozen Maiden (2013)", commonv1.MediaKindEpisode)
	assert.Equal(t, commonv1.SourceUnknown, bare.Quality.Source)

	own, err := parseMediaFile("/data/x/[Group] Show - 01 [WEB-DL 720p].mkv", "", commonv1.MediaKindEpisode)
	require.NoError(t, err)
	fillFromRelease(own, title, commonv1.MediaKindEpisode)
	assert.Equal(t, commonv1.SourceWebDL, own.Quality.Source, "a name that names its own source keeps it")
}
