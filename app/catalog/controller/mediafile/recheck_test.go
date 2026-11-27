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
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// An episode's file was deleted by hand and its record went on saying "file
// present" (talos, 2026-11-27): a settled file that was never transcoded
// had no timer, so nothing made the loop stat it again, and only that stat
// says FileMissing. Every settled file is now looked at again a day on,
// transcoded or not, and the pass that finds it gone marks it and looks
// again in a minute.
func TestASettledFileIsLookedAtAgainADayOn(t *testing.T) {
	const size = 1507013031
	path := "/data/media/anime/Gosick (2011)/Season 01/Gosick (2011) - S01E22.mkv"
	mod := time.Date(2026, 11, 27, 3, 29, 0, 0, time.UTC)
	now := metav1.NewTime(time.Date(2026, 11, 27, 6, 33, 0, 0, time.UTC))
	for name, original := range map[string]*bool{"never transcoded": new(true), "transcoded": new(false), "unset": nil} {
		t.Run(name, func(t *testing.T) {
			mf := &catalogv1alpha1.MediaFile{
				ObjectMeta: metav1.ObjectMeta{Name: "gosick-s01e22", Namespace: "clustarr-system", UID: "uid-1"},
				Spec:       catalogv1alpha1.MediaFileSpec{Path: path, SizeBytes: size, ModTime: metav1.NewTime(mod), Original: original},
				Status: catalogv1alpha1.MediaFileStatus{
					ProbeHash: evaluateProbe(path, size, mod, "").Hash, ProbeVersion: mediainfo.ProbeVersion,
					ProbedAt: &metav1.Time{Time: mod}, MediaInfo: &commonv1.MediaInfo{},
				},
			}
			k8s.MarkTrue(mf, &mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady, "Ready", "file present and probed")

			out := mf.Status.DeepCopy()
			o := PlanProbe(ProbeInput{File: mf, Prev: &mf.Status, Path: path, Info: statInfo{size, mod}, Now: now}, out)
			require.Nil(t, o.Request, "setup: a settled file is asked nothing")
			assert.Equal(t, now.Add(RecheckInterval), o.Due)

			out = mf.Status.DeepCopy()
			o = PlanProbe(ProbeInput{File: mf, Prev: &mf.Status, Path: path, Missing: fs.ErrNotExist, Now: now}, out)
			ready := k8s.FindCondition(out.Conditions, catalogv1alpha1.MediaFileConditionReady)
			require.NotNil(t, ready)
			assert.Equal(t, metav1.ConditionFalse, ready.Status)
			assert.Equal(t, "FileMissing", ready.Reason)
			assert.Equal(t, now.Add(time.Minute), o.Due)
		})
	}
}
