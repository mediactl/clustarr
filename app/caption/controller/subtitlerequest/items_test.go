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

package subtitlerequest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// A language the plan no longer wants -- audioExclude now sees the file's
// audio, or the item's original language -- is withdrawn when it never
// downloaded a subtitle, so the request stops reading Searching for a
// search that will never be sent. One that did download is kept: its file
// is on disk and upgrades still judge it.
func TestPlanItemsWithdrawsAnUnwantedLanguageThatNeverDownloaded(t *testing.T) {
	now := metav1.NewTime(time.Now())
	profile := map[string]bool{"en": true, "fr": true, "de": true}
	items := []subtitlev1alpha1.SubtitleItem{
		{LangKey: "en", NextSearchAt: &now, Attempts: commonv1alpha1.Attempts{Count: 1}},                            // searched once, now unwanted
		{LangKey: "fr", State: subtitlev1alpha1.SubtitleItemDownloaded, DownloadedAt: &now, Path: "/data/x.fr.srt"}, // unwanted but on disk
		{LangKey: "de", NextSearchAt: &now}, // still wanted
	}
	kept, dropped := planItems(items, profile, []subtitles.LangKey{"de"})

	var keys []string
	for _, it := range kept {
		keys = append(keys, it.LangKey)
	}
	assert.ElementsMatch(t, []string{"fr", "de"}, keys)
	assert.Equal(t, []string{"en"}, dropped)
}
