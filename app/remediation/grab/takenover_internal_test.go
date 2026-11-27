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

package grab

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
)

// The grab's log line names what the planner read of each item the entry
// covers and of no other: a final file, a file that is not, and an item
// that read as having none.
func TestTakenOverNamesWhatThePlannerReadOfEachCoveredItem(t *testing.T) {
	ep := func(n int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("show-s01e%02d", n)},
			Spec:       catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: n},
		}
	}
	q := commonv1.Quality{Name: "WEBDL-1080p"}
	g := &gathered{
		objects: []client.Object{ep(1), ep(2), ep(3), ep(4)},
		v: grabplan.View{Covered: []grabplan.Covered{
			{Current: &decision.Current{Quality: q, Transcoded: true}},
			{Current: &decision.Current{Quality: q}},
			{},
			{Current: &decision.Current{Quality: q, Transcoded: true}},
		}},
	}
	e := &catalogv1alpha1.DownloadEntry{Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 1}, {Season: 1, Number: 2}, {Season: 1, Number: 3}}}
	require.Equal(t, []string{
		"show-s01e01=WEBDL-1080p/transcoded", "show-s01e02=WEBDL-1080p", "show-s01e03=none",
	}, takenOver(g, e))
}
