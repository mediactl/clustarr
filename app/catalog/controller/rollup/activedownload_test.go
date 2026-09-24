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

package rollup_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

func TestDownloadNonTerminal(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name string
		dl   *downloadv1alpha1.Download
		want bool
	}{
		{"nil", nil, false},
		{"no phase yet: just created, grabarr has not seen it", dl(""), true},
		{"pending", dl(downloadv1alpha1.DownloadPhasePending), true},
		{"assigned", dl(downloadv1alpha1.DownloadPhaseAssigned), true},
		{"queued", dl(downloadv1alpha1.DownloadPhaseQueued), true},
		{"downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), true},
		{"paused", dl(downloadv1alpha1.DownloadPhasePaused), true},
		{"completed is waiting for import, not done", dl(downloadv1alpha1.DownloadPhaseCompleted), true},
		{"seeding is not imported yet (Imported is sticky over it)", dl(downloadv1alpha1.DownloadPhaseSeeding), true},
		{"imported", dl(downloadv1alpha1.DownloadPhaseImported), false},
		{"failed", dl(downloadv1alpha1.DownloadPhaseFailed), false},
		{"blocklisted", dl(downloadv1alpha1.DownloadPhaseBlocklisted), false},
		{"removing", dl(downloadv1alpha1.DownloadPhaseRemoving), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, rollup.DownloadNonTerminal(c.dl))
		})
	}

	t.Run("a deletion timestamp is terminal whatever the phase", func(t *testing.T) {
		d := dl(downloadv1alpha1.DownloadPhaseDownloading)
		now := metav1.Now()
		d.DeletionTimestamp = &now
		assert.False(t, rollup.DownloadNonTerminal(d))
	})
}

func TestActiveDownload(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	mk := func(name string, uid types.UID, age time.Duration, p downloadv1alpha1.DownloadPhase) downloadv1alpha1.Download {
		return downloadv1alpha1.Download{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				CreationTimestamp: metav1.NewTime(base.Add(-age)),
				OwnerReferences:   []metav1.OwnerReference{{UID: uid}},
			},
			Status: downloadv1alpha1.DownloadStatus{Phase: p},
		}
	}
	ownedBy := func(uid types.UID) func(*downloadv1alpha1.Download) bool {
		return func(d *downloadv1alpha1.Download) bool {
			for _, r := range d.OwnerReferences {
				if r.UID == uid {
					return true
				}
			}
			return false
		}
	}

	t.Run("none", func(t *testing.T) {
		assert.Nil(t, rollup.ActiveDownload(nil, ownedBy("me")))
	})

	t.Run("terminal downloads never count", func(t *testing.T) {
		items := []downloadv1alpha1.Download{
			mk("a", "me", time.Hour, downloadv1alpha1.DownloadPhaseImported),
			mk("b", "me", time.Minute, downloadv1alpha1.DownloadPhaseFailed),
		}
		assert.Nil(t, rollup.ActiveDownload(items, ownedBy("me")))
	})

	t.Run("a download owned by someone else is never adopted", func(t *testing.T) {
		items := []downloadv1alpha1.Download{
			mk("stale", "deleted-predecessor", time.Hour, downloadv1alpha1.DownloadPhaseDownloading),
		}
		assert.Nil(t, rollup.ActiveDownload(items, ownedBy("me")))
	})

	t.Run("the oldest live one wins, whatever the order", func(t *testing.T) {
		items := []downloadv1alpha1.Download{
			mk("newer", "me", time.Minute, downloadv1alpha1.DownloadPhaseAssigned),
			mk("finished", "me", 2*time.Hour, downloadv1alpha1.DownloadPhaseImported),
			mk("older", "me", time.Hour, downloadv1alpha1.DownloadPhaseCompleted),
		}
		got := rollup.ActiveDownload(items, ownedBy("me"))
		require.NotNil(t, got)
		assert.Equal(t, "older", got.Name)
	})

	t.Run("a creation-time tie breaks on the name", func(t *testing.T) {
		items := []downloadv1alpha1.Download{
			mk("b", "me", time.Hour, downloadv1alpha1.DownloadPhaseQueued),
			mk("a", "me", time.Hour, downloadv1alpha1.DownloadPhaseQueued),
		}
		got := rollup.ActiveDownload(items, ownedBy("me"))
		require.NotNil(t, got)
		assert.Equal(t, "a", got.Name)
	})
}
